package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The Evidence screen's entries (T27 step 1b, docs/design/v3 rule 1): the sealed bundles newest first, one page at a
// time with a cursor, each with the name of what it proves (the VM, or the VM of a migration check) and a restore
// test's measured RTO, so the screen asks once per page. Read-only.
//
// ADR-007 C19.6: an export given while the ledger could not record it (C18.5) has no ledger entry yet, only its
// pending record. It is a row all the same, at the top of the first page (newest first), with ledger_record
// not_recorded, no sequence number, no hash and verdict UNKNOWN: nothing about evidence may look more finished than it
// is. Once the next successful ledger write appends it, the row is its ledger entry (ledger_record recorded_late).

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
	Kind             string   `json:"kind"`         // test, migration, or export (the record of an evidence export, ADR-007 C7)
	By               string   `json:"by"`           // an export's: the user who made it (from its manifest)
	// LedgerRecord says whether the row is in the ledger (ADR-007 C19.6): recorded; recorded_late (an export's record
	// appended after the export was given, C18.5); or not_recorded (an export given without a ledger record, waiting
	// for the next successful ledger write; ledger_seq 0, no hash).
	LedgerRecord string `json:"ledger_record"`
	// NotRecordedReason is why the ledger could not record a not_recorded export (raw text, for "Technical details").
	NotRecordedReason string `json:"not_recorded_reason"`
}

// The values of EvidenceRow.LedgerRecord.
const (
	ledgerRecorded     = "recorded"
	ledgerRecordedLate = "recorded_late"
	ledgerNotRecorded  = "not_recorded"
)

// isExport is the SQL condition of an export's record: no test, no migration check, and its bundle_id.
const isExport = `(e.test_id IS NULL AND e.migration_check_id IS NULL AND e.bundle_id LIKE 'export-%')`

// EvidenceView is one page of the entries.
type EvidenceView struct {
	Clock ServerClock   `json:"clock"`
	Total int           `json:"total"` // entries that match the filter
	Rows  []EvidenceRow `json:"rows"`
	Next  string        `json:"next"` // the cursor of the next page (before=), "" at the end
	// NotRecorded counts the exports given without a ledger record that wait for the next ledger write, whatever the
	// filter (ADR-007 C19.6): the screen says so next to the download. The first page lists them (at most a page).
	NotRecorded int `json:"not_recorded"`
}

func (s *Server) evidenceViewRoute() route {
	return route{method: "GET", path: "/api/v1/views/evidence", summary: "The Evidence screen's ledger entries, newest first, in pages (T27)",
		roles: anyRole, out: EvidenceView{}, query: []param{
			{"before", "integer", "a previous answer's next"},
			{"limit", "integer", "at most this many (default 25, at most 200)"},
			{"kind", "string", "test, migration or export: only restore tests', migration checks' or evidence exports' entries"},
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
	withPending := true // whether the exports not recorded match the filter
	switch k := c.r.URL.Query().Get("kind"); k {
	case "":
	case "test":
		withPending = false
		where = append(where, "e.migration_check_id IS NULL AND NOT "+isExport)
	case "migration":
		withPending = false
		where = append(where, "e.migration_check_id IS NOT NULL")
	case "export":
		where = append(where, isExport)
	default:
		return 0, nil, fmt.Errorf("%w: kind %q: test, migration or export", store.ErrInvalid, k)
	}
	switch v := c.r.URL.Query().Get("verdict"); v {
	case "":
	case "OK", "WARNING", "CRITICAL", "UNKNOWN":
		withPending = withPending && v == evidence.Unknown
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
	pending, err := s.pendingExports()
	if err != nil {
		return 0, nil, err
	}
	out.NotRecorded = len(pending)
	m := n // ledger rows on this page
	if withPending {
		out.Total += len(pending)
		if before == 0 {
			for i := len(pending) - 1; i >= 0 && len(out.Rows) < int(n); i-- { // newest first
				out.Rows = append(out.Rows, notRecordedRow(pending[i]))
			}
			m -= int64(len(out.Rows))
		}
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
	pageArgs = append(pageArgs, m+1) // one more than the page: whether a next page exists
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.ledger_seq, e.created_at, e.bundle_id, e.bundle_sha256, e.verdict, e.test_id,
		e.migration_check_id, e.vm_id, COALESCE(v.name, m.vm, ''), t.proven_rto_s, `+isExport+`
		FROM evidence_bundles e
		LEFT JOIN vms v ON v.id = e.vm_id
		LEFT JOIN migration_checks m ON m.id = e.migration_check_id
		LEFT JOIN restore_tests t ON t.id = e.test_id`+page+` ORDER BY e.id DESC LIMIT ?`, pageArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var ledgerRows []EvidenceRow
	for rows.Next() {
		var r EvidenceRow
		var test, check, vm sql.NullInt64
		var rto sql.NullFloat64
		var export bool
		if err := rows.Scan(&r.ID, &r.LedgerSeq, &r.CreatedAt, &r.BundleID, &r.BundleSHA256, &r.Verdict, &test, &check, &vm, &r.VM, &rto, &export); err != nil {
			return 0, nil, err
		}
		switch {
		case export:
			r.Kind = "export"
		case check.Valid:
			r.Kind = "migration"
		default:
			r.Kind = "test"
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
		r.LedgerRecord = ledgerRecorded
		ledgerRows = append(ledgerRows, r)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(ledgerRows) > int(m) {
		if m == 0 {
			// The exports not recorded fill this page: the ledger's entries start on the next one.
			out.Next = strconv.FormatInt(ledgerRows[0].ID+1, 10)
		} else {
			out.Next = strconv.FormatInt(ledgerRows[m-1].ID, 10)
		}
		ledgerRows = ledgerRows[:m]
	}
	for i := range ledgerRows {
		if ledgerRows[i].Kind == "export" {
			var late bool
			if ledgerRows[i].By, late, err = s.exportBy(ledgerRows[i].BundleID); err != nil {
				return 0, nil, err
			}
			if late {
				ledgerRows[i].LedgerRecord = ledgerRecordedLate
			}
		}
	}
	out.Rows = append(out.Rows, ledgerRows...)
	return http.StatusOK, out, nil
}

// notRecordedRow is the row of an export given without a ledger record (ADR-007 C19.6): no entry, no hash, UNKNOWN.
func notRecordedRow(p pendingExport) EvidenceRow {
	return EvidenceRow{CreatedAt: p.Manifest.Made, BundleID: p.BundleID, Verdict: evidence.Unknown, Kind: "export", By: p.Manifest.By.User,
		LedgerRecord: ledgerNotRecorded, NotRecordedReason: p.Manifest.LedgerRecord.Reason}
}

// exportBy is the user an export's record names (its manifest's subject), and whether the record was appended after
// the export was given (C18.5, recorded_late). A record whose bundle is not on disk has none here: the ledger check
// reports it (bundle_missing).
func (s *Server) exportBy(bundleID string) (string, bool, error) {
	b, err := os.ReadFile(filepath.Join(s.cfg.bundlesDir(), bundleID, evidence.ManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var m struct {
		Subject map[string]string `json:"subject"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", false, fmt.Errorf("evidence bundle %s: manifest: %w", bundleID, err)
	}
	return m.Subject["user"], m.Subject["recorded_late"] == "true", nil
}
