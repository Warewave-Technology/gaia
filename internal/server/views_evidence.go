package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// The Evidence screen's entries (T27 step 1b, docs/design/v3 rule 1): the sealed bundles newest first, one page at a
// time with a cursor, each with the name of what it proves (the VM, or the VM of a migration check) and a restore
// test's measured RTO, so the screen asks once per page. Read-only.

// EvidenceRow is one ledger entry of the Evidence screen.
type EvidenceRow struct {
	ID               int64    `json:"id"`
	LedgerSeq        int      `json:"ledger_seq"`
	CreatedAt        string   `json:"created_at"`
	BundleID         string   `json:"bundle_id"`
	BundleSHA256     string   `json:"bundle_sha256"`
	Verdict          string   `json:"verdict"`
	TestID           *int64   `json:"test_id"`
	MigrationCheckID *int64   `json:"migration_check_id"`
	VMID             *int64   `json:"vm_id"`
	VM               string   `json:"vm"`           // the protected VM's name, or the source VM's of a migration check
	ProvenRTOS       *float64 `json:"proven_rto_s"` // a restore test's measured RTO
}

// EvidenceView is one page of the entries.
type EvidenceView struct {
	Clock ServerClock   `json:"clock"`
	Total int           `json:"total"` // entries that match the filter
	Rows  []EvidenceRow `json:"rows"`
	Next  string        `json:"next"` // the cursor of the next page (before=), "" at the end
}

func (s *Server) evidenceViewRoute() route {
	return route{method: "GET", path: "/api/v1/views/evidence", summary: "The Evidence screen's ledger entries, newest first, in pages (T27)",
		roles: anyRole, out: EvidenceView{}, query: []param{
			{"before", "integer", "a previous answer's next"},
			{"limit", "integer", "at most this many (default 25, at most 200)"},
			{"kind", "string", "test or migration: only restore tests' or migration checks' entries"},
			{"verdict", "string", "OK, WARNING, CRITICAL or UNKNOWN: only entries with this verdict"},
		}, h: s.evidenceView}
}

func (s *Server) evidenceView(c *call) (int, any, error) {
	ctx := c.r.Context()
	before, err := c.intQuery("before", 0, 0)
	if err != nil {
		return 0, nil, err
	}
	n, err := c.intQuery("limit", 25, 200)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		n = 25
	}
	var where []string
	var args []any
	switch k := c.r.URL.Query().Get("kind"); k {
	case "":
	case "test":
		where = append(where, "e.migration_check_id IS NULL")
	case "migration":
		where = append(where, "e.migration_check_id IS NOT NULL")
	default:
		return 0, nil, fmt.Errorf("%w: kind %q: test or migration", store.ErrInvalid, k)
	}
	switch v := c.r.URL.Query().Get("verdict"); v {
	case "":
	case "OK", "WARNING", "CRITICAL", "UNKNOWN":
		where = append(where, "e.verdict = ?")
		args = append(args, v)
	default:
		return 0, nil, fmt.Errorf("%w: verdict %q: OK, WARNING, CRITICAL or UNKNOWN", store.ErrInvalid, v)
	}
	filter := ""
	if len(where) > 0 {
		filter = " WHERE " + strings.Join(where, " AND ")
	}
	db := s.db.SQL()
	out := EvidenceView{Clock: serverClock(s.now()), Rows: []EvidenceRow{}}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_bundles e`+filter, args...).Scan(&out.Total); err != nil {
		return 0, nil, err
	}
	page := filter
	pageArgs := append([]any{}, args...)
	if before > 0 {
		if page == "" {
			page = " WHERE e.id < ?"
		} else {
			page += " AND e.id < ?"
		}
		pageArgs = append(pageArgs, before)
	}
	pageArgs = append(pageArgs, n+1) // one more than the page: whether a next page exists
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.ledger_seq, e.created_at, e.bundle_id, e.bundle_sha256, e.verdict, e.test_id,
		e.migration_check_id, e.vm_id, COALESCE(v.name, m.vm, ''), t.proven_rto_s
		FROM evidence_bundles e
		LEFT JOIN vms v ON v.id = e.vm_id
		LEFT JOIN migration_checks m ON m.id = e.migration_check_id
		LEFT JOIN restore_tests t ON t.id = e.test_id`+page+` ORDER BY e.id DESC LIMIT ?`, pageArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r EvidenceRow
		var test, check, vm sql.NullInt64
		var rto sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.LedgerSeq, &r.CreatedAt, &r.BundleID, &r.BundleSHA256, &r.Verdict, &test, &check, &vm, &r.VM, &rto); err != nil {
			return 0, nil, err
		}
		if test.Valid {
			r.TestID = &test.Int64
		}
		if check.Valid {
			r.MigrationCheckID = &check.Int64
		}
		if vm.Valid {
			r.VMID = &vm.Int64
		}
		if rto.Valid {
			r.ProvenRTOS = &rto.Float64
		}
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(out.Rows) > int(n) {
		out.Rows = out.Rows[:n]
		out.Next = strconv.FormatInt(out.Rows[n-1].ID, 10)
	}
	return http.StatusOK, out, nil
}
