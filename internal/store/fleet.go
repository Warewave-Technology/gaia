package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The fleet views' queries (T24, ADR-007 C1): every one reads the status table (vm_status), so a screen region is a
// few queries whatever the number of VMs.

// Status ranks, the status table's sort order.
const (
	RankCritical = 0
	RankUnknown  = 1
	RankWarning  = 2
	RankOK       = 3
)

// RankOf is a status's rank; an unknown word ranks as UNKNOWN.
func RankOf(status string) int {
	switch status {
	case "CRITICAL":
		return RankCritical
	case "WARNING":
		return RankWarning
	case "OK":
		return RankOK
	}
	return RankUnknown
}

// FleetCounts is the whole installation in numbers.
type FleetCounts struct {
	VMs        int `json:"vms"`
	Critical   int `json:"critical"`
	Unknown    int `json:"unknown"`
	Warning    int `json:"warning"`
	OK         int `json:"ok"`
	Uncomputed int `json:"uncomputed"` // VMs without a stored status yet (UNKNOWN until the next tick)
	// The VM whose newest proven point is the oldest: the fleet's worst proven RPO. Nil when nothing is proven.
	WorstProven *WorstProven `json:"worst_proven"`
}

// WorstProven names the VM with the oldest proven point.
type WorstProven struct {
	VMID          int64   `json:"vm_id"`
	VM            string  `json:"vm"`
	ProvenTakenAt float64 `json:"proven_taken_at"`
	RPOTargetS    *int64  `json:"rpo_target_s"`
}

// FleetCounts counts the VMs by stored status.
func (d *DB) FleetCounts(ctx context.Context) (FleetCounts, error) {
	var c FleetCounts
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(s.rank = 0), 0), COALESCE(SUM(s.rank = 1 OR s.vm_id IS NULL), 0), COALESCE(SUM(s.rank = 2), 0), COALESCE(SUM(s.rank = 3), 0),
		COALESCE(SUM(s.vm_id IS NULL), 0)
		FROM vms v LEFT JOIN vm_status s ON s.vm_id = v.id`).Scan(&c.VMs, &c.Critical, &c.Unknown, &c.Warning, &c.OK, &c.Uncomputed)
	if err != nil {
		return c, err
	}
	var w WorstProven
	err = d.db.QueryRowContext(ctx, `SELECT v.id, v.name, s.proven_taken_at, s.rpo_target_s FROM vm_status s JOIN vms v ON v.id = s.vm_id
		WHERE s.proven_taken_at IS NOT NULL ORDER BY s.proven_taken_at, v.id LIMIT 1`).Scan(&w.VMID, &w.VM, &w.ProvenTakenAt, &w.RPOTargetS)
	switch {
	case err == nil:
		c.WorstProven = &w
	case err != sql.ErrNoRows:
		return c, err
	}
	return c, nil
}

// NameCount is a name with how many VMs it has in a group.
type NameCount struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AttentionGroup is the VMs that are not OK for the same cause: the deciding reason's code and scope.
type AttentionGroup struct {
	Rank      int         `json:"rank"`
	Code      string      `json:"code"`
	ScopeKind string      `json:"scope_kind"`
	ScopeID   int64       `json:"scope_id"`
	ScopeName string      `json:"scope_name"`
	Count     int         `json:"count"`
	Since     *float64    `json:"since"`      // the oldest status change in the group (unix)
	ExampleVM int64       `json:"example_vm"` // the VM longest in this state, for the group's example sentence
	Hosts     []NameCount `json:"hosts"`      // where: the hosts with most of the group's VMs, at most 3
	Policies  []NameCount `json:"policies"`   // and the policies
}

// AttentionGroups groups every VM that is not OK by cause, worst status first, then the largest group.
func (d *DB) AttentionGroups(ctx context.Context) ([]AttentionGroup, error) {
	gs, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (AttentionGroup, error) {
		var g AttentionGroup
		err := s.Scan(&g.Rank, &g.Code, &g.ScopeKind, &g.ScopeID, &g.ScopeName, &g.Count, &g.Since)
		g.Hosts, g.Policies = []NameCount{}, []NameCount{}
		return g, err
	}, `SELECT MIN(rank), code, scope_kind, scope_id, MAX(scope_name), COUNT(*), MIN(since) FROM vm_status WHERE rank < 3
		GROUP BY code, scope_kind, scope_id ORDER BY MIN(rank), COUNT(*) DESC, code, scope_id`)
	if err != nil || len(gs) == 0 {
		return gs, err
	}
	key := func(code, kind string, id int64) string {
		return code + "\x00" + kind + "\x00" + strconv.FormatInt(id, 10)
	}
	at := map[string]int{}
	for i, g := range gs {
		at[key(g.Code, g.ScopeKind, g.ScopeID)] = i
	}
	type part struct {
		code, kind string
		sid, id    int64
		name       string
		n          int
	}
	scanPart := func(s interface{ Scan(...any) error }) (part, error) {
		var p part
		return p, s.Scan(&p.code, &p.kind, &p.sid, &p.id, &p.name, &p.n)
	}
	hosts, err := rowsOf(ctx, d.db, scanPart, `SELECT s.code, s.scope_kind, s.scope_id, h.id, h.name, COUNT(*) FROM vm_status s
		JOIN vms v ON v.id = s.vm_id JOIN hosts h ON h.id = v.host_id WHERE s.rank < 3
		GROUP BY s.code, s.scope_kind, s.scope_id, h.id ORDER BY COUNT(*) DESC, h.name`)
	if err != nil {
		return nil, err
	}
	pols, err := rowsOf(ctx, d.db, scanPart, `SELECT s.code, s.scope_kind, s.scope_id, COALESCE(p.id, 0), COALESCE(p.name, ''), COUNT(*) FROM vm_status s
		JOIN vms v ON v.id = s.vm_id LEFT JOIN policies p ON p.id = v.policy_id WHERE s.rank < 3
		GROUP BY s.code, s.scope_kind, s.scope_id, p.id ORDER BY COUNT(*) DESC, p.name`)
	if err != nil {
		return nil, err
	}
	for _, p := range hosts {
		if g := &gs[at[key(p.code, p.kind, p.sid)]]; len(g.Hosts) < 3 {
			g.Hosts = append(g.Hosts, NameCount{p.id, p.name, p.n})
		}
	}
	for _, p := range pols {
		if g := &gs[at[key(p.code, p.kind, p.sid)]]; len(g.Policies) < 3 {
			g.Policies = append(g.Policies, NameCount{p.id, p.name, p.n})
		}
	}
	examples, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (part, error) {
		var p part
		return p, s.Scan(&p.code, &p.kind, &p.sid, &p.id)
	}, `SELECT code, scope_kind, scope_id, vm_id FROM (SELECT code, scope_kind, scope_id, vm_id,
		ROW_NUMBER() OVER (PARTITION BY code, scope_kind, scope_id ORDER BY since, vm_id) AS n FROM vm_status WHERE rank < 3) WHERE n = 1`)
	if err != nil {
		return nil, err
	}
	for _, e := range examples {
		gs[at[key(e.code, e.kind, e.sid)]].ExampleVM = e.id
	}
	return gs, nil
}

// CoverageGroup is a host, a policy or a tag with its VMs counted by status.
type CoverageGroup struct {
	ID       int64  `json:"id"` // the host's or policy's id; 0 for a tag
	Name     string `json:"name"`
	VMs      int    `json:"vms"`
	Critical int    `json:"critical"`
	Unknown  int    `json:"unknown"`
	Warning  int    `json:"warning"`
	OK       int    `json:"ok"`
	// The group's VM with the oldest proven point, and its target; the oldest proof.
	WorstProvenVM      *int64   `json:"worst_proven_vm"`
	WorstProvenTakenAt *float64 `json:"worst_proven_taken_at"`
	WorstRPOTargetS    *int64   `json:"worst_rpo_target_s"`
	OldestProofAt      *float64 `json:"oldest_proof_at"`
}

// Coverage counts the VMs of every host, policy or tag by status: the groups with VMs that are not OK first (worst
// first), then the healthy ones by name.
func (d *DB) Coverage(ctx context.Context, by string) ([]CoverageGroup, error) {
	var from, id, name, group string
	switch by {
	case "host":
		from, id, name, group = `vms v JOIN hosts g ON g.id = v.host_id`, `g.id`, `g.name`, `g.id`
	case "policy":
		from, id, name, group = `vms v JOIN policies g ON g.id = v.policy_id`, `g.id`, `g.name`, `g.id`
	case "tag":
		from, id, name, group = `vms v JOIN vm_tags g ON g.vm_id = v.id`, `0`, `g.key || CASE WHEN g.value = '' THEN '' ELSE ':' || g.value END`, `g.key, g.value`
	default:
		return nil, fmt.Errorf("%w: coverage by %q (host, policy or tag)", ErrInvalid, by)
	}
	q := `WITH m AS (SELECT ` + id + ` AS gid, ` + name + ` AS gname, v.id AS vm_id, COALESCE(s.rank, 1) AS rank, s.proven_taken_at, s.proven_finished_at,
			s.rpo_target_s FROM ` + from + ` LEFT JOIN vm_status s ON s.vm_id = v.id),
		w AS (SELECT gname, vm_id, proven_taken_at, rpo_target_s, ROW_NUMBER() OVER (PARTITION BY gname ORDER BY proven_taken_at, vm_id) AS n
			FROM m WHERE proven_taken_at IS NOT NULL)
		SELECT MIN(m.gid), m.gname, COUNT(*), SUM(m.rank = 0), SUM(m.rank = 1), SUM(m.rank = 2), SUM(m.rank = 3),
			w.vm_id, w.proven_taken_at, w.rpo_target_s, MIN(m.proven_finished_at)
		FROM m LEFT JOIN w ON w.gname = m.gname AND w.n = 1
		GROUP BY m.gname ORDER BY SUM(m.rank = 0) DESC, SUM(m.rank = 1) DESC, SUM(m.rank = 2) DESC, m.gname`
	_ = group
	return rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (CoverageGroup, error) {
		var g CoverageGroup
		return g, s.Scan(&g.ID, &g.Name, &g.VMs, &g.Critical, &g.Unknown, &g.Warning, &g.OK, &g.WorstProvenVM, &g.WorstProvenTakenAt,
			&g.WorstRPOTargetS, &g.OldestProofAt)
	}, q)
}

// VMFilter selects and orders the VM list.
type VMFilter struct {
	Ranks     []int  // only these statuses
	Attention bool   // only VMs that are not OK
	HostID    int64  // only this host's
	PolicyID  int64  // only this policy's
	Tag       string // "key" (any value) or "key:value"
	Code      string // only VMs whose deciding reason has this code
	ScopeKind string // and this scope (with ScopeID)
	ScopeID   int64
	Q         string // text: VM, host or policy name, or a point id
	Sort      string // "" (worst first, then oldest proof) or "name"
	After     string // the cursor of the last row of the previous page
	Limit     int
}

// VMRow is one VM in the list: the VM, its host and policy names, its tags and its stored status.
type VMRow struct {
	VM
	Host   string   `json:"host"`
	Policy string   `json:"policy"`
	Tags   []string `json:"tags"`
	Status VMStatus `json:"status"`
	Cursor string   `json:"cursor"` // pass as after= for the rows that follow
}

// ParseTag splits "key" or "key:value" and checks it: a key is 1-64 of a-z A-Z 0-9 . _ - and starts with a letter or
// digit; a value is at most 128 printable characters without spaces.
func ParseTag(t string) (key, value string, err error) {
	key, value, _ = strings.Cut(t, ":")
	if !tagKey.MatchString(key) || len(value) > 128 || strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", "", fmt.Errorf("%w: tag %q: key:value, the key 1-64 of A-Z a-z 0-9 . _ - (starting with a letter or digit), the value at most 128 characters without spaces",
			ErrInvalid, t)
	}
	return key, value, nil
}

var tagKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ListVMs lists VMs with their stored status, filtered and in pages; total is how many match the filter.
func (d *DB) ListVMs(ctx context.Context, f VMFilter) (rows []VMRow, total int, err error) {
	var where []string
	var args []any
	if len(f.Ranks) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Ranks)), ",")
		where = append(where, `COALESCE(s.rank, 1) IN (`+ph+`)`)
		for _, r := range f.Ranks {
			args = append(args, r)
		}
	}
	if f.Attention {
		where = append(where, `COALESCE(s.rank, 1) < 3`)
	}
	if f.HostID > 0 {
		where, args = append(where, `v.host_id = ?`), append(args, f.HostID)
	}
	if f.PolicyID > 0 {
		where, args = append(where, `v.policy_id = ?`), append(args, f.PolicyID)
	}
	if f.Tag != "" {
		k, val, err := ParseTag(f.Tag)
		if err != nil {
			return nil, 0, err
		}
		if strings.Contains(f.Tag, ":") {
			where, args = append(where, `EXISTS (SELECT 1 FROM vm_tags t WHERE t.vm_id = v.id AND t.key = ? AND t.value = ?)`), append(args, k, val)
		} else {
			where, args = append(where, `EXISTS (SELECT 1 FROM vm_tags t WHERE t.vm_id = v.id AND t.key = ?)`), append(args, k)
		}
	}
	if f.Code != "" {
		where, args = append(where, `s.code = ?`), append(args, f.Code)
		if f.ScopeKind != "" || f.ScopeID != 0 {
			where, args = append(where, `s.scope_kind = ? AND s.scope_id = ?`), append(args, f.ScopeKind, f.ScopeID)
		}
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q)) + "%"
		cond := `(lower(v.name) LIKE ? ESCAPE '\' OR lower(h.name) LIKE ? ESCAPE '\' OR lower(COALESCE(p.name, '')) LIKE ? ESCAPE '\'`
		args = append(args, like, like, like)
		if id, err := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64); err == nil && id > 0 {
			cond += ` OR v.id = (SELECT vm_id FROM points WHERE id = ?)`
			args = append(args, id)
		}
		where = append(where, cond+`)`)
	}
	from := ` FROM vms v JOIN hosts h ON h.id = v.host_id LEFT JOIN policies p ON p.id = v.policy_id LEFT JOIN vm_status s ON s.vm_id = v.id`
	w := ""
	if len(where) > 0 {
		w = ` WHERE ` + strings.Join(where, ` AND `)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Keyset pages: the sort key and the id are the cursor.
	order, key := `COALESCE(s.rank, 1), COALESCE(s.proven_finished_at, -1), v.id`, []string{"COALESCE(s.rank, 1)", "COALESCE(s.proven_finished_at, -1)", "v.id"}
	if f.Sort == "name" {
		order, key = `v.name, v.id`, []string{"v.name", "v.id"}
	} else if f.Sort != "" {
		return nil, 0, fmt.Errorf("%w: sort %q (worst first by default, or name)", ErrInvalid, f.Sort)
	}
	pageWhere, pageArgs := w, append([]any{}, args...)
	if f.After != "" {
		vals, err := parseCursor(f.After, len(key), f.Sort == "name")
		if err != nil {
			return nil, 0, err
		}
		cond := `(` + strings.Join(key, ", ") + `) > (` + strings.TrimSuffix(strings.Repeat("?,", len(key)), ",") + `)`
		if pageWhere == "" {
			pageWhere = ` WHERE ` + cond
		} else {
			pageWhere += ` AND ` + cond
		}
		pageArgs = append(pageArgs, vals...)
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	q := `SELECT v.id, v.host_id, v.name, v.uuid, v.policy_id, v.created_at, v.updated_at, v.full_backup_reason, h.name, COALESCE(p.name, ''),
		COALESCE(s.rank, 1), COALESCE(s.proven_finished_at, -1), ` + qualifiedStatusCols + from + pageWhere + ` ORDER BY ` + order + ` LIMIT ?`
	rows, err = rowsOf(ctx, d.db, func(sc interface{ Scan(...any) error }) (VMRow, error) {
		var r VMRow
		var rank int
		var pf float64
		var st statusNulls
		var pid sql.NullInt64
		dest := []any{&r.ID, &r.HostID, &r.Name, &r.UUID, &pid, &r.CreatedAt, &r.UpdatedAt, &r.FullBackupReason, &r.Host, &r.Policy, &rank, &pf}
		err := sc.Scan(append(dest, st.dest()...)...)
		if pid.Valid {
			r.PolicyID = &pid.Int64
		}
		r.Status = st.value()
		if f.Sort == "name" {
			r.Cursor = cursorOf(r.Name, r.ID)
		} else {
			r.Cursor = cursorOf(rank, pf, r.ID)
		}
		r.Tags = []string{}
		return r, err
	}, q, append(pageArgs, f.Limit)...)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	tags, err := d.TagsOf(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		if t, ok := tags[rows[i].ID]; ok {
			rows[i].Tags = t
		}
	}
	return rows, total, nil
}

var qualifiedStatusCols = "s." + strings.Join(strings.Fields(strings.ReplaceAll(statusCols, ",", " ")), ", s.")

// statusNulls scans a vm_status row that a LEFT JOIN may have left empty.
type statusNulls struct {
	vmID                                      sql.NullInt64
	status, reason, result, changed, computed sql.NullString
	rank                                      sql.NullInt64
	v                                         VMStatus
}

func (n *statusNulls) dest() []any {
	v := &n.v
	return []any{&n.vmID, &n.status, &n.reason, &n.result, &n.changed, &n.computed, &n.rank, &v.Since, &v.Code, &v.ScopeKind, &v.ScopeID,
		&v.ScopeName, &v.NewestPointID, &v.NewestTakenAt, &v.ProvenPointID, &v.ProvenTakenAt, &v.ProvenTestID, &v.ProvenFinishedAt,
		&v.ProvenRTOS, &v.RPOTargetS, &v.RTOTargetS, &v.MaxProofAgeS, &v.NextChangeAt}
}

func (n *statusNulls) value() VMStatus {
	v := n.v
	if !n.vmID.Valid { // never computed: UNKNOWN until the scheduler's next tick
		return VMStatus{Status: "UNKNOWN", Result: []byte(`null`), StatusRow: StatusRow{Rank: RankUnknown}}
	}
	v.VMID, v.Status, v.Reason, v.Result, v.ChangedAt, v.ComputedAt, v.Rank = n.vmID.Int64, n.status.String, n.reason.String,
		[]byte(n.result.String), n.changed.String, n.computed.String, int(n.rank.Int64)
	return v
}

// cursorOf writes a sort key as a cursor; parseCursor reads it back.
func cursorOf(vals ...any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case float64:
			parts[i] = strconv.FormatFloat(x, 'g', -1, 64)
		default:
			parts[i] = fmt.Sprint(x)
		}
	}
	return strings.Join(parts, "~")
}

func parseCursor(c string, n int, byName bool) ([]any, error) {
	bad := fmt.Errorf("%w: cursor %q (use a row's cursor from the previous page)", ErrInvalid, c)
	parts := strings.Split(c, "~")
	if len(parts) < n {
		return nil, bad
	}
	if byName { // the name may hold a ~; the id is the last part
		id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil {
			return nil, bad
		}
		return []any{strings.Join(parts[:len(parts)-1], "~"), id}, nil
	}
	if len(parts) != n {
		return nil, bad
	}
	rank, err1 := strconv.Atoi(parts[0])
	pf, err2 := strconv.ParseFloat(parts[1], 64)
	id, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, bad
	}
	return []any{rank, pf, id}, nil
}

// TagsOf returns the tags of the given VMs ("key" or "key:value"), sorted.
func (d *DB) TagsOf(ctx context.Context, vmIDs []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(vmIDs) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(vmIDs)), ",")
	args := make([]any, len(vmIDs))
	for i, id := range vmIDs {
		args[i] = id
	}
	type tag struct {
		vm         int64
		key, value string
	}
	ts, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (tag, error) {
		var t tag
		return t, s.Scan(&t.vm, &t.key, &t.value)
	}, `SELECT vm_id, key, value FROM vm_tags WHERE vm_id IN (`+ph+`) ORDER BY vm_id, key, value`, args...)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		s := t.key
		if t.value != "" {
			s += ":" + t.value
		}
		out[t.vm] = append(out[t.vm], s)
	}
	return out, nil
}

// SetTags replaces a VM's tags.
func (d *DB) SetTags(ctx context.Context, vmID int64, tags []string) ([]string, error) {
	type kv struct{ k, v string }
	var parsed []kv
	seen := map[kv]bool{}
	for _, t := range tags {
		k, v, err := ParseTag(t)
		if err != nil {
			return nil, err
		}
		if !seen[kv{k, v}] {
			seen[kv{k, v}] = true
			parsed = append(parsed, kv{k, v})
		}
	}
	if len(parsed) > 64 {
		return nil, fmt.Errorf("%w: at most 64 tags per VM", ErrInvalid)
	}
	if _, err := d.VM(ctx, vmID); err != nil {
		return nil, err
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM vm_tags WHERE vm_id = ?`, vmID); err != nil {
			return err
		}
		for _, t := range parsed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO vm_tags (vm_id, key, value) VALUES (?, ?, ?)`, vmID, t.k, t.v); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE vms SET updated_at = ? WHERE id = ?`, d.ts(), vmID)
		return err
	})
	if err != nil {
		return nil, err
	}
	m, err := d.TagsOf(ctx, []int64{vmID})
	if err != nil {
		return nil, err
	}
	if m[vmID] == nil {
		return []string{}, nil
	}
	return m[vmID], nil
}

// SearchHits is what a text matches: VMs, hosts and policies by name, and a point by its id.
type SearchHits struct {
	VMs      []NameCount `json:"vms"`      // count: 0
	Hosts    []NameCount `json:"hosts"`    // count: its VMs
	Policies []NameCount `json:"policies"` // count: its VMs
	Points   []PointHit  `json:"points"`
}

// PointHit is a point found by its id.
type PointHit struct {
	ID      int64  `json:"id"`
	VMID    int64  `json:"vm_id"`
	VM      string `json:"vm"`
	TakenAt string `json:"taken_at"`
}

// Search finds q in names (case-insensitive) and as a point id; at most limit of each.
func (d *DB) Search(ctx context.Context, q string, limit int) (SearchHits, error) {
	out := SearchHits{VMs: []NameCount{}, Hosts: []NameCount{}, Policies: []NameCount{}, Points: []PointHit{}}
	q = strings.TrimSpace(q)
	if q == "" {
		return out, nil
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q)) + "%"
	scan := func(s interface{ Scan(...any) error }) (NameCount, error) {
		var n NameCount
		return n, s.Scan(&n.ID, &n.Name, &n.Count)
	}
	var err error
	// A name that starts with the text comes first, then the shorter name.
	if out.VMs, err = rowsOf(ctx, d.db, scan, `SELECT id, name, 0 FROM vms WHERE lower(name) LIKE ? ESCAPE '\'
		ORDER BY instr(lower(name), ?) , length(name), name LIMIT ?`, like, strings.ToLower(q), limit); err != nil {
		return out, err
	}
	if out.Hosts, err = rowsOf(ctx, d.db, scan, `SELECT h.id, h.name, (SELECT COUNT(*) FROM vms v WHERE v.host_id = h.id) FROM hosts h
		WHERE lower(h.name) LIKE ? ESCAPE '\' ORDER BY instr(lower(h.name), ?), length(h.name), h.name LIMIT ?`, like, strings.ToLower(q), limit); err != nil {
		return out, err
	}
	if out.Policies, err = rowsOf(ctx, d.db, scan, `SELECT p.id, p.name, (SELECT COUNT(*) FROM vms v WHERE v.policy_id = p.id) FROM policies p
		WHERE lower(p.name) LIKE ? ESCAPE '\' ORDER BY instr(lower(p.name), ?), length(p.name), p.name LIMIT ?`, like, strings.ToLower(q), limit); err != nil {
		return out, err
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64); err == nil && id > 0 {
		if out.Points, err = rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (PointHit, error) {
			var p PointHit
			return p, s.Scan(&p.ID, &p.VMID, &p.VM, &p.TakenAt)
		}, `SELECT p.id, p.vm_id, v.name, p.taken_at FROM points p JOIN vms v ON v.id = p.vm_id WHERE p.id = ?`, id); err != nil {
			return out, err
		}
	}
	return out, nil
}

// JobCount is how many jobs of a kind are in a state.
type JobCount struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Count int    `json:"count"`
}

// OpenJobCounts counts the queued and running jobs by kind.
func (d *DB) OpenJobCounts(ctx context.Context) ([]JobCount, error) {
	return rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (JobCount, error) {
		var c JobCount
		return c, s.Scan(&c.Kind, &c.State, &c.Count)
	}, `SELECT kind, state, COUNT(*) FROM jobs WHERE state IN ('queued', 'running') GROUP BY kind, state ORDER BY kind, state`)
}

// StatusPage lists stored statuses in VM id order after a VM id.
func (d *DB) StatusPage(ctx context.Context, after int64, limit int) ([]VMStatus, error) {
	return rowsOf(ctx, d.db, scanStatus, `SELECT `+statusCols+` FROM vm_status WHERE vm_id > ? ORDER BY vm_id LIMIT ?`, after, limit)
}

// MarkStatusesDue makes the scheduler's next tick recompute the VMs of a host or a policy (one of them > 0): their
// status depends on what changed (a policy's targets, a host's name).
func (d *DB) MarkStatusesDue(ctx context.Context, hostID, policyID int64) error {
	col, id := "host_id", hostID
	if policyID > 0 {
		col, id = "policy_id", policyID
	}
	_, err := d.db.ExecContext(ctx, `UPDATE vm_status SET next_change_at = 0 WHERE vm_id IN (SELECT id FROM vms WHERE `+col+` = ?)`, id)
	return err
}

// ScheduleFact is what the scheduler needs to know of a VM with a policy for one kind of job.
type ScheduleFact struct {
	VMID            int64
	Policy          string
	BackupIntervalS int64
	TestIntervalS   int64
	LastCreatedAt   string // the newest job of the kind; "" when there is none
	Open            bool   // a job of the kind is queued or running
	HasPoint        bool   // a present point passed its check
}

// ScheduleFacts reads, in one query, every VM with a policy and what queueing a job of kind depends on (T24: it was
// three to four queries per VM at every tick).
func (d *DB) ScheduleFacts(ctx context.Context, kind string) ([]ScheduleFact, error) {
	return rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (ScheduleFact, error) {
		var f ScheduleFact
		var last sql.NullString
		err := s.Scan(&f.VMID, &f.Policy, &f.BackupIntervalS, &f.TestIntervalS, &last, &f.Open, &f.HasPoint)
		f.LastCreatedAt = last.String
		return f, err
	}, `SELECT v.id, p.name, p.backup_interval_s, p.test_interval_s,
		(SELECT j.created_at FROM jobs j WHERE j.vm_id = v.id AND j.kind = ?1 ORDER BY j.id DESC LIMIT 1),
		EXISTS (SELECT 1 FROM jobs j WHERE j.vm_id = v.id AND j.kind = ?1 AND j.state IN ('queued', 'running')),
		EXISTS (SELECT 1 FROM points pt WHERE pt.vm_id = v.id AND pt.state = 'present' AND pt.check_state = 'ok')
		FROM vms v JOIN policies p ON p.id = v.policy_id ORDER BY v.id`, kind)
}
