// Package catalog is a fixture (T24, ADR-007 C1): it fills a Gaia database with a synthetic fleet, so the control
// plane is measured at the design target (5,000 VMs, 100 hosts, 3 million points) instead of assumed. Not part of the
// product: nothing in cmd/ or internal/ imports it except tests.
//
// The fleet is a history, not a snapshot: every VM's backups at its policy's interval back from now (each a job and a
// point, pruned by the policy's retention), its restore tests at the test interval, and the state changes those
// record. Failures are seeded in known numbers (Seeded), so the status the server computes can be checked against
// them. The status table is left empty: the server computes it at its start, which is the full recompute T24 times.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// Options size the fleet.
type Options struct {
	VMs    int
	Hosts  int
	Points int       // in all, spread over the VMs by their policies' intervals
	Now    time.Time // the history ends here
	Seed   uint64
}

// Summary is what was written.
type Summary struct {
	Now          string         `json:"now"`
	VMs          int            `json:"vms"`
	Hosts        int            `json:"hosts"`
	Policies     int            `json:"policies"`
	Repositories int            `json:"repositories"`
	Points       int            `json:"points"`
	Present      int            `json:"present_points"`
	Jobs         int            `json:"jobs"`
	Tests        int            `json:"tests"`
	StateChanges int            `json:"state_changes"`
	Tags         int            `json:"tags"`
	Seeded       map[string]int `json:"seeded"` // failure -> VMs
	Seconds      float64        `json:"seconds"`
}

// Policy is one of the synthetic policies.
type Policy struct {
	Name                     string
	Repo                     int // index into the repositories
	BackupS, TestS           int64
	KeepLast, KeepDaily      int
	RPOS, RTOS, MaxProofAgeS int64
	Template                 string
	Weight                   float64 // share of the VMs
	Windows                  bool
}

// Policies are the fleet's policies, by share of VMs.
var Policies = []Policy{
	{"gold-db", 0, 3600, 6 * 3600, 24, 7, 3600, 900, 12 * 3600, "linux-boot", 0.13, false},
	{"std-linux", 0, 4 * 3600, 12 * 3600, 12, 7, 4 * 3600, 900, 24 * 3600, "linux-boot", 0.62, false},
	{"std-windows", 1, 4 * 3600, 12 * 3600, 12, 7, 4 * 3600, 900, 24 * 3600, "windows-basic", 0.12, true},
	{"crit-windows", 1, 3600, 6 * 3600, 24, 7, 3600, 1200, 12 * 3600, "windows-basic", 0.04, true},
	{"dev", 0, 24 * 3600, 7 * 24 * 3600, 7, 0, 24 * 3600, 3600, 8 * 24 * 3600, "linux-boot", 0.09, false},
}

// Seeded failures: how many VMs of 5,000 get each (in proportion for another size).
var seeds = []struct {
	name string
	n    int
}{
	{"backup_failing", 10}, {"test_failed_boot", 40}, {"guest_rebooted", 8}, {"test_failed_check", 6},
	{"repo_content_damaged", 25}, {"never_proven", 120}, {"proof_too_old", 250}, {"rto_over_target", 40},
	{"test_not_run", 30}, {"no_policy", 5}, {"test_warning", 15}, {"full_backup", 20},
}

// UnreachableHosts are the hosts (by index) that stopped answering 26 hours before Now: longer than every policy's RPO
// target, so each of their VMs is CRITICAL with host_unreachable.
var UnreachableHosts = []int{7, 14}

const unreachableFor = 26 * time.Hour

type writer struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt // by query text
	n     map[string]int
}

// exec runs q (prepared once per text) and counts it under name.
func (w *writer) exec(name, q string, args ...any) (int64, error) {
	st, ok := w.stmts[q]
	if !ok {
		var err error
		if st, err = w.tx.Prepare(q); err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		w.stmts[q] = st
	}
	res, err := st.Exec(args...)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	w.n[name]++
	return res.LastInsertId()
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Fill writes the fleet into db, which must be a migrated, otherwise empty Gaia database (gaia server init made it).
func Fill(ctx context.Context, db *sql.DB, o Options) (Summary, error) {
	t0 := time.Now()
	if o.VMs <= 0 || o.Hosts <= 0 || o.Points < o.VMs {
		return Summary{}, fmt.Errorf("catalog: VMs %d, hosts %d, points %d", o.VMs, o.Hosts, o.Points)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM vms) + (SELECT COUNT(*) FROM hosts)`).Scan(&n); err != nil {
		return Summary{}, err
	}
	if n > 0 {
		return Summary{}, fmt.Errorf("catalog: the database already has hosts or VMs")
	}
	rng := rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15))
	now := o.Now.UTC()
	sum := Summary{Now: ts(now), Seeded: map[string]int{}}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback()
	w := &writer{tx: tx, stmts: map[string]*sql.Stmt{}, n: map[string]int{}}
	created := ts(now.Add(-200 * 24 * time.Hour))

	repos := []int64{}
	for _, r := range []string{"main", "s3-archive"} {
		id, err := w.exec("repo", `INSERT INTO repositories (name, kind, location, splitter, hash, created_at) VALUES (?, ?, ?, 'FIXED-1M', 'BLAKE2B-256-128', ?)`,
			r, map[bool]string{true: "s3", false: "filesystem"}[r != "main"], "/srv/gaia/synthetic/"+r, created)
		if err != nil {
			return sum, err
		}
		repos = append(repos, id)
	}
	pols := make([]int64, len(Policies))
	for i, p := range Policies {
		id, err := w.exec("policy", `INSERT INTO policies (name, repository_id, backup_interval_s, test_interval_s, keep_last, keep_daily, keep_weekly,
			rpo_target_s, rto_target_s, max_proof_age_s, check_template, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
			p.Name, repos[p.Repo], p.BackupS, p.TestS, p.KeepLast, p.KeepDaily, p.RPOS, p.RTOS, p.MaxProofAgeS, p.Template, created, created)
		if err != nil {
			return sum, err
		}
		pols[i] = id
	}
	hosts := make([]int64, o.Hosts)
	hostNames := make([]string, o.Hosts)
	for i := range hosts {
		hostNames[i] = fmt.Sprintf("kvm-%03d", i+1)
		id, err := w.exec("host", `INSERT INTO hosts (name, address, ssh_port, ssh_user, created_at, updated_at) VALUES (?, ?, 22, 'gaia-backup', ?, ?)`,
			hostNames[i], fmt.Sprintf("10.20.%d.%d", i/250, i%250+1), created, created)
		if err != nil {
			return sum, err
		}
		hosts[i] = id
	}

	// Which VM gets which seeded failure: disjoint sets, chosen by the seed.
	perm := rng.Perm(o.VMs)
	seededOf := map[int]string{}
	k := 0
	for _, s := range seeds {
		cnt := s.n * o.VMs / 5000
		if cnt == 0 && o.VMs >= 50 {
			cnt = 1
		}
		for j := 0; j < cnt && k < len(perm); j, k = j+1, k+1 {
			seededOf[perm[k]] = s.name
		}
	}
	unreachable := map[int]bool{}
	for _, h := range UnreachableHosts {
		if h < o.Hosts {
			unreachable[h] = true
		}
	}

	// Points per VM: the share of the total its policy's interval earns, so a busy policy has more history.
	pick := func() int {
		x, acc := rng.Float64(), 0.0
		for i, p := range Policies {
			if acc += p.Weight; x < acc {
				return i
			}
		}
		return len(Policies) - 1
	}
	vmPol := make([]int, o.VMs)
	for i := range vmPol {
		vmPol[i] = pick()
	}
	per := float64(o.Points) / float64(o.VMs)
	prefixes := []string{"db", "app", "web", "erp", "mail", "file", "ci", "mq", "cache", "auth"}
	for i := 0; i < o.VMs; i++ {
		pi := vmPol[i]
		p := Policies[pi]
		hi := i % o.Hosts
		seed := seededOf[i]
		if unreachable[hi] && seed != "" {
			seed = "" // a VM on a host that does not answer has that one cause
		}
		name := fmt.Sprintf("%s-%s-%04d", prefixes[i%len(prefixes)], strings.TrimPrefix(p.Name, "std-"), i+1)
		var polID any = pols[pi]
		if seed == "no_policy" {
			polID = nil
		}
		vmID, err := w.exec("vm", `INSERT INTO vms (host_id, name, uuid, policy_id, created_at, updated_at) VALUES (?, ?, '', ?, ?, ?)`,
			hosts[hi], name, polID, created, created)
		if err != nil {
			return sum, err
		}
		if seed != "" {
			sum.Seeded[seed]++
		}
		if unreachable[hi] {
			sum.Seeded["host_unreachable"]++
		}
		for _, t := range tagsOf(i, p) {
			key, val, _ := strings.Cut(t, ":")
			if _, err := w.exec("tag", `INSERT INTO vm_tags (vm_id, key, value) VALUES (?, ?, ?)`, vmID, key, val); err != nil {
				return sum, err
			}
		}
		if err := fillVM(w, rng, vmID, repos[p.Repo], p, seed, unreachable[hi], hostNames[hi], now, int(math.Round(per)), &sum); err != nil {
			return sum, fmt.Errorf("VM %s: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return sum, err
	}
	sum.VMs, sum.Hosts, sum.Policies, sum.Repositories = o.VMs, o.Hosts, len(Policies), len(repos)
	sum.Points, sum.Jobs, sum.Tests, sum.StateChanges, sum.Tags = w.n["point"], w.n["job"], w.n["test"], w.n["change"], w.n["tag"]
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM points WHERE state = 'present'`).Scan(&sum.Present); err != nil {
		return sum, err
	}
	if _, err := db.ExecContext(ctx, `ANALYZE`); err != nil {
		return sum, err
	}
	sum.Seconds = time.Since(t0).Seconds()
	return sum, nil
}

func tagsOf(i int, p Policy) []string {
	env := []string{"env:prod", "env:prod", "env:prod", "env:staging", "env:dev"}[i%5]
	if p.Name == "dev" {
		env = "env:dev"
	}
	tags := []string{env, "os:" + map[bool]string{true: "windows", false: "linux"}[p.Windows]}
	if i%7 == 0 {
		tags = append(tags, "pci")
	}
	if i%11 == 0 {
		tags = append(tags, "team:"+[]string{"payments", "core", "data", "web"}[i%4])
	}
	return tags
}

// fillVM writes one VM's history: count backups at the policy's interval ending near now, its tests, and the seed.
func fillVM(w *writer, rng *rand.Rand, vmID, repoID int64, p Policy, seed string, unreachable bool, host string, now time.Time, count int, sum *Summary) error {
	interval := time.Duration(p.BackupS) * time.Second
	if seed == "never_proven" {
		count = max(2, int(24*time.Hour/interval)) // a VM added a day ago
	}
	// The newest backup is some time inside the last interval (the schedules do not line up).
	newest := now.Add(-time.Duration(rng.Int64N(int64(interval))))
	lastOK := newest
	failedAfter := 0 // backups at the end that failed instead
	failErr, failDetail := "", "{}"
	switch {
	case unreachable:
		failedAfter = int(unreachableFor/interval) + 1
		failErr = fmt.Sprintf("SSH to gaia-backup@%s: dial tcp: i/o timeout", host)
		failDetail = `{"host_unreachable":true}`
		if failedAfter >= count {
			failedAfter = count - 1
		}
	case seed == "backup_failing":
		failedAfter = int(p.RPOS/p.BackupS) + 1
		failErr = "libvirt: Domain not found: no domain with matching name"
	}
	times := make([]time.Time, count)
	for j := range times {
		times[j] = newest.Add(-time.Duration(count-1-j)*interval + time.Duration(rng.Int64N(int64(time.Second)))) // jitter: no two equal
	}
	pointIDs := make([]int64, 0, count)
	pointAt := make([]time.Time, 0, count)
	var parent any
	keep := keepSet(times[:count-failedAfter], p.KeepLast, p.KeepDaily)
	for j, at := range times {
		if j >= count-failedAfter {
			jid, err := w.exec("job", `INSERT INTO jobs (kind, vm_id, state, reason, error, created_at, started_at, finished_at, detail)
				VALUES ('backup', ?, 'failed', ?, ?, ?, ?, ?, ?)`, vmID, fmt.Sprintf("schedule: policy %s, every %d s", p.Name, p.BackupS), failErr,
				ts(at), ts(at.Add(time.Second)), ts(at.Add(15*time.Second)), failDetail)
			if err != nil {
				return err
			}
			if _, err := w.exec("change", `INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor) VALUES (?, 'job', ?, 'running', 'failed', ?, 'system')`,
				ts(at.Add(15*time.Second)), jid, failErr); err != nil {
				return err
			}
			continue
		}
		dur := time.Duration(20+rng.IntN(400)) * time.Second
		jid, err := w.exec("job", `INSERT INTO jobs (kind, vm_id, state, reason, created_at, started_at, finished_at, checkpoint, detail)
			VALUES ('backup', ?, 'ok', ?, ?, ?, ?, ?, '{}')`, vmID, fmt.Sprintf("schedule: policy %s, every %d s", p.Name, p.BackupS),
			ts(at.Add(-dur)), ts(at.Add(-dur)), ts(at.Add(5*time.Second)), fmt.Sprintf("gaia-%d-%d", vmID, j))
		if err != nil {
			return err
		}
		kind, reason := "incremental", "incremental from the previous point"
		if j == 0 {
			kind, reason = "full", "first backup of the VM"
		} else if j == count-failedAfter-1 && seed == "full_backup" {
			kind, reason = "full", fmt.Sprintf("full: checkpoint gaia-%d-%d of point %d is not on the host", vmID, j-1, pointIDs[len(pointIDs)-1])
		}
		state := "pruned"
		if keep[j] {
			state = "present"
		}
		pid, err := w.exec("point", `INSERT INTO points (vm_id, job_id, repository_id, kind, taken_at, check_state, bytes, state, checkpoint, parent_id, reason)
			VALUES (?, ?, ?, ?, ?, 'ok', ?, ?, ?, ?, ?)`, vmID, jid, repoID, kind, ts(at), 50_000_000+rng.Int64N(4_000_000_000), state,
			fmt.Sprintf("gaia-%d-%d", vmID, j), parent, reason)
		if err != nil {
			return err
		}
		parent = pid
		if _, err := w.exec("change", `INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor) VALUES (?, 'point', ?, 'unchecked', 'ok', 'post-backup check passed', 'system')`,
			ts(at.Add(5*time.Second)), pid); err != nil {
			return err
		}
		pointIDs, pointAt = append(pointIDs, pid), append(pointAt, at)
		lastOK = at
	}
	_ = lastOK
	if seed == "repo_content_damaged" && len(pointIDs) > 3 {
		// An older present point that a verification found damaged.
		for j := len(pointIDs) - 3; j >= 0; j-- {
			if keep[j] {
				if _, err := w.exec("damage", `UPDATE points SET check_state = 'failed' WHERE id = ?`, pointIDs[j]); err != nil {
					return err
				}
				break
			}
		}
	}
	if seed == "never_proven" {
		return nil
	}
	return fillTests(w, rng, vmID, p, seed, pointIDs, pointAt, now)
}

// keepSet is the retention: the newest keepLast points and one a day for keepDaily days.
func keepSet(times []time.Time, keepLast, keepDaily int) map[int]bool {
	keep := map[int]bool{}
	for j := len(times) - 1; j >= 0 && j >= len(times)-keepLast; j-- {
		keep[j] = true
	}
	days := map[string]bool{}
	for j := len(times) - 1; j >= 0 && len(days) < keepDaily; j-- {
		d := times[j].Format("2006-01-02")
		if !days[d] {
			days[d] = true
			keep[j] = true
		}
	}
	return keep
}

// fillTests writes the VM's restore tests: one per test interval, of the newest point then, and the seed's last one.
func fillTests(w *writer, rng *rand.Rand, vmID int64, p Policy, seed string, pointIDs []int64, pointAt []time.Time, now time.Time) error {
	if len(pointIDs) == 0 {
		return nil
	}
	every := time.Duration(p.TestS) * time.Second
	end := now.Add(-time.Duration(rng.Int64N(int64(every / 2))))
	if seed == "proof_too_old" {
		end = now.Add(-time.Duration(p.MaxProofAgeS)*time.Second - time.Duration(1+rng.IntN(6))*time.Hour)
	}
	start := pointAt[0].Add(time.Hour)
	var ats []time.Time
	for at := end; !at.Before(start); at = at.Add(-every) {
		ats = append([]time.Time{at}, ats...)
	}
	baseRTO := 30.0 + rng.Float64()*40
	if p.Windows {
		baseRTO = 280 + rng.Float64()*120
	}
	for ti, at := range ats {
		// The newest point taken before the test started.
		j := len(pointAt) - 1
		for j > 0 && pointAt[j].After(at.Add(-time.Minute)) {
			j--
		}
		last := ti == len(ats)-1
		verdict, reason, rto := "OK", "every check passed", baseRTO+rng.NormFloat64()*5
		checks := `[{"name":"boot","verdict":"OK"}]`
		detail := map[string]any{}
		if last {
			switch seed {
			case "test_failed_boot":
				verdict, reason = "CRITICAL", `boot CRITICAL: no match for "(?m)login: *$" on the serial console within 300 s`
				checks = `[{"name":"boot","verdict":"CRITICAL"}]`
			case "guest_rebooted":
				verdict, reason = "CRITICAL", "stable CRITICAL: the guest stopped answering the boot probe 25 s into the 60 s window"
				checks = `[{"name":"boot","verdict":"OK"},{"name":"stable","verdict":"CRITICAL"}]`
			case "test_failed_check":
				verdict, reason = "CRITICAL", "ssh CRITICAL: no SSH banner on port 22 within 60 s"
				checks = `[{"name":"boot","verdict":"OK"},{"name":"ssh","verdict":"CRITICAL"}]`
			case "test_warning":
				verdict, reason = "WARNING", "http WARNING: /health answered 503 within 30 s"
				checks = `[{"name":"boot","verdict":"OK"},{"name":"http","verdict":"WARNING"}]`
			case "test_not_run":
				verdict, reason = "UNKNOWN", `not tested: sandbox helper at /run/gaia-sandbox/sandbox.sock: Post "http://sandbox/v1/verify": dial unix /run/gaia-sandbox/sandbox.sock: connect: connection refused`
				checks = `[]`
				detail["sandbox_down"] = true
			case "rto_over_target":
				rto = float64(p.RTOS) + 60 + rng.Float64()*300
			}
		}
		var rtoArg any = math.Max(rto, 5)
		if verdict != "OK" {
			rtoArg = nil
		}
		dur := 40*time.Second + time.Duration(rng.IntN(60))*time.Second
		if p.Windows {
			dur = 6*time.Minute + time.Duration(rng.IntN(120))*time.Second
		}
		jstate := "ok"
		if verdict == "UNKNOWN" {
			jstate = "failed"
		}
		jid, err := w.exec("job", `INSERT INTO jobs (kind, vm_id, state, reason, error, created_at, started_at, finished_at) VALUES ('restore_test', ?, ?, ?, ?, ?, ?, ?)`,
			vmID, jstate, fmt.Sprintf("schedule: policy %s, every %d s", p.Name, p.TestS), map[bool]string{true: reason, false: ""}[jstate == "failed"],
			ts(at), ts(at), ts(at.Add(dur)))
		if err != nil {
			return err
		}
		var cs []any
		if err := json.Unmarshal([]byte(checks), &cs); err != nil {
			return err
		}
		detail["point"], detail["checks"], detail["verdict"] = pointIDs[j], cs, verdict
		db, _ := json.Marshal(detail)
		tid, err := w.exec("test", `INSERT INTO restore_tests (vm_id, point_id, job_id, verdict, proven_rto_s, started_at, finished_at, reason, bundle_id, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, vmID, pointIDs[j], jid, verdict, rtoArg, ts(at), ts(at.Add(dur)), reason,
			fmt.Sprintf("synthetic-%d-%d", vmID, ti), string(db))
		if err != nil {
			return err
		}
		if _, err := w.exec("change", `INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor) VALUES (?, 'restore_test', ?, 'UNKNOWN', ?, ?, 'system')`,
			ts(at.Add(dur)), tid, verdict, reason); err != nil {
			return err
		}
	}
	return nil
}
