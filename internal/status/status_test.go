package status

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func ago(s int) time.Time { return now.Add(-time.Duration(s) * time.Second) }

func f(v float64) *float64 { return &v }

// The input space, one dimension per thing the status depends on. With targets RPO 900 s, RTO 600 s and maximum
// proof age 3600 s, each value is clearly on one side of its target.
type combo struct {
	targets bool
	point   string // none, within, over
	latest  string // none, OK, WARNING, CRITICAL: the newest test with a verdict
	attempt bool   // a newer test that could not run (UNKNOWN)
	passing bool
	provRPO string // within, over (only with passing)
	rto     string // nil, within, over (only with passing)
	age     string // within, over (only with passing)
	damaged bool
}

func (c combo) String() string {
	return fmt.Sprintf("targets=%v point=%s latest=%s attempt=%v passing=%v provRPO=%s rto=%s age=%s damaged=%v",
		c.targets, c.point, c.latest, c.attempt, c.passing, c.provRPO, c.rto, c.age, c.damaged)
}

func (c combo) input() Input {
	in := Input{Now: now}
	if c.targets {
		in.Targets = &Targets{RPOS: 900, RTOS: 600, MaxProofAgeS: 3600}
	}
	switch c.point {
	case "within":
		in.NewestChecked = &Point{ID: 10, TakenAt: ago(100)}
	case "over":
		in.NewestChecked = &Point{ID: 10, TakenAt: ago(1000)}
	}
	if c.passing {
		p := &Test{ID: 5, PointID: 8, Verdict: OK, PointTakenAt: ago(map[string]int{"within": 200, "over": 1200}[c.provRPO]),
			FinishedAt: ago(map[string]int{"within": 100, "over": 4000}[c.age])}
		switch c.rto {
		case "within":
			p.ProvenRTOS = f(300)
		case "over":
			p.ProvenRTOS = f(700)
		}
		in.LatestPassingTest = p
	}
	switch c.latest {
	case "none":
	case OK:
		in.LatestTest = in.LatestPassingTest
	default:
		in.LatestTest = &Test{ID: 6, PointID: 9, Verdict: c.latest, PointTakenAt: ago(150), FinishedAt: ago(50), Detail: "boot: no login prompt"}
	}
	in.LatestAttempt = in.LatestTest
	if c.attempt {
		in.LatestAttempt = &Test{ID: 7, PointID: 10, Verdict: Unknown, PointTakenAt: ago(100), FinishedAt: ago(10), Detail: "sandbox helper unreachable"}
	}
	if c.damaged {
		in.DamagedPoints = []int64{3}
	}
	return in
}

// consistent drops combinations that cannot exist: an OK latest test is the newest passing test; without any test
// there is no passing test; proof figures exist only with a passing test.
func (c combo) consistent() bool {
	if c.latest == OK && !c.passing || c.latest == "none" && c.passing {
		return false
	}
	if !c.passing && (c.provRPO != "" || c.rto != "" || c.age != "") || c.passing && (c.provRPO == "" || c.rto == "" || c.age == "") {
		return false
	}
	return true
}

// expect is ADR-007's table written out row by row, independently of Compute: which rows apply, and the worst.
func expect(c combo) (string, []string) {
	var rules []string
	critical, unknown, warning, ok := false, false, false, false
	if c.targets && c.point == "over" {
		critical, rules = true, append(rules, "theoretical_rpo_over_target")
	}
	if c.latest == Critical {
		critical, rules = true, append(rules, "latest_test_critical")
	}
	if c.damaged {
		critical, rules = true, append(rules, "integrity_failed")
	}
	if !c.targets {
		unknown, rules = true, append(rules, "no_policy")
	}
	if c.point == "none" {
		unknown, rules = true, append(rules, "no_checked_point")
	}
	if !c.passing {
		unknown, rules = true, append(rules, "no_passing_test")
	} else if c.rto == "nil" {
		unknown, rules = true, append(rules, "no_proven_rto")
	}
	if c.targets && c.point == "within" && c.passing && c.rto != "nil" {
		if c.provRPO == "over" {
			warning, rules = true, append(rules, "proven_rpo_over_target")
		}
		if c.rto == "over" {
			warning, rules = true, append(rules, "proven_rto_over_target")
		}
		if c.age == "over" {
			warning, rules = true, append(rules, "proof_too_old")
		}
		if !warning {
			ok, rules = true, append(rules, "proven")
		}
	}
	switch {
	case critical:
		return Critical, rules
	case unknown:
		return Unknown, rules
	case warning:
		return Warning, rules
	case ok:
		return OK, rules
	}
	return Unknown, rules
}

func allCombos() []combo {
	var out []combo
	for _, targets := range []bool{false, true} {
		for _, point := range []string{"none", "within", "over"} {
			for _, latest := range []string{"none", OK, Warning, Critical} {
				for _, attempt := range []bool{false, true} {
					for _, damaged := range []bool{false, true} {
						out = append(out, combo{targets: targets, point: point, latest: latest, attempt: attempt, damaged: damaged})
						for _, provRPO := range []string{"within", "over"} {
							for _, rto := range []string{"nil", "within", "over"} {
								for _, age := range []string{"within", "over"} {
									out = append(out, combo{targets, point, latest, attempt, true, provRPO, rto, age, damaged})
								}
							}
						}
					}
				}
			}
		}
	}
	return slices.DeleteFunc(out, func(c combo) bool { return !c.consistent() })
}

// Every consistent combination of every input: the status and the set of rows that applied match the table.
func TestTruthTableExhaustive(t *testing.T) {
	seen := map[string]int{}
	ruleSeen := map[string]int{}
	cs := allCombos()
	for _, c := range cs {
		r := Compute(c.input())
		want, wantRules := expect(c)
		var got []string
		for _, x := range r.Reasons {
			got = append(got, x.Rule)
			ruleSeen[x.Rule]++
		}
		slices.Sort(got)
		slices.Sort(wantRules)
		if r.Status != want || !slices.Equal(got, wantRules) {
			t.Fatalf("%v:\n  status %s, want %s\n  rules %v, want %v\n  reasons %+v", c, r.Status, want, got, wantRules, r.Reasons)
		}
		// The first reason explains the status; UNKNOWN is never healthy; OK has no missing input.
		if r.Reasons[0].Status != r.Status {
			t.Fatalf("%v: first reason %+v does not carry the status %s", c, r.Reasons[0], r.Status)
		}
		if r.Status == OK && (r.ProvenRPO == nil || r.ProvenRTO == nil || r.ProofAge == nil || r.TheoreticalRPO == nil) {
			t.Fatalf("%v: OK with a missing figure %+v", c, r)
		}
		// A test that could not run is a note, never a status change.
		wantNotes := 0
		if c.latest == Warning {
			wantNotes++
		}
		if c.attempt {
			wantNotes++
		}
		if len(r.Notes) != wantNotes {
			t.Fatalf("%v: notes %v", c, r.Notes)
		}
		seen[r.Status]++
	}
	for _, s := range []string{OK, Warning, Unknown, Critical} {
		if seen[s] == 0 {
			t.Errorf("no combination gives %s", s)
		}
	}
	for _, rule := range []string{"theoretical_rpo_over_target", "latest_test_critical", "integrity_failed", "no_policy",
		"no_checked_point", "no_passing_test", "no_proven_rto", "proven_rpo_over_target", "proven_rto_over_target",
		"proof_too_old", "proven"} {
		if ruleSeen[rule] == 0 {
			t.Errorf("rule %s never applied", rule)
		}
	}
	t.Logf("%d combinations: %v; rules applied: %v", len(cs), seen, ruleSeen)
}

// The ADR's sentences, one case each, as an operator would read them.
func TestTruthTableExamples(t *testing.T) {
	targets := &Targets{RPOS: 900, RTOS: 600, MaxProofAgeS: 3600}
	good := &Test{ID: 1, PointID: 2, Verdict: OK, PointTakenAt: ago(300), FinishedAt: ago(200), ProvenRTOS: f(120)}
	pt := &Point{ID: 3, TakenAt: ago(60)}
	for _, c := range []struct {
		name string
		in   Input
		want string
		text string // in a reason with the status
	}{
		{"everything proven", Input{Now: now, Targets: targets, NewestChecked: pt, LatestTest: good, LatestPassingTest: good}, OK, "proven by test 1"},
		{"a new VM with backups but no test yet", Input{Now: now, Targets: targets, NewestChecked: pt}, Unknown, "has ever passed"},
		{"a new VM with nothing", Input{Now: now, Targets: targets}, Unknown, ""},
		{"the latest test failed to boot", Input{Now: now, Targets: targets, NewestChecked: pt, LatestPassingTest: good,
			LatestTest: &Test{ID: 4, PointID: 3, Verdict: Critical, Detail: "boot: no login prompt within 300 s"}}, Critical, "no login prompt"},
		{"proof older than the maximum", Input{Now: now, Targets: targets, NewestChecked: pt, LatestTest: good,
			LatestPassingTest: &Test{ID: 1, PointID: 2, Verdict: OK, PointTakenAt: ago(5000), FinishedAt: ago(4000), ProvenRTOS: f(120)}}, Warning, "over the maximum"},
		{"backups stopped", Input{Now: now, Targets: targets, NewestChecked: &Point{ID: 3, TakenAt: ago(2000)}, LatestTest: good, LatestPassingTest: good}, Critical, "over the RPO target"},
		{"a damaged point", Input{Now: now, Targets: targets, NewestChecked: pt, LatestTest: good, LatestPassingTest: good, DamagedPoints: []int64{2}}, Critical, "integrity"},
		{"no policy", Input{Now: now, NewestChecked: pt, LatestTest: good, LatestPassingTest: good}, Unknown, "no policy"},
		{"a test that could not run after a pass keeps the pass", Input{Now: now, Targets: targets, NewestChecked: pt, LatestPassingTest: good,
			LatestTest: good, LatestAttempt: &Test{ID: 5, PointID: 3, Verdict: Unknown, Detail: "sandbox: libvirt"}}, OK, ""},
		{"a test that could not run after a CRITICAL keeps the CRITICAL", Input{Now: now, Targets: targets, NewestChecked: pt, LatestPassingTest: good,
			LatestTest:    &Test{ID: 4, PointID: 3, Verdict: Critical, Detail: "boot: no login prompt"},
			LatestAttempt: &Test{ID: 5, PointID: 3, Verdict: Unknown, Detail: "sandbox: libvirt"}}, Critical, "no login prompt"},
	} {
		r := Compute(c.in)
		if r.Status != c.want || !slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Status == c.want && strings.Contains(x.Text, c.text) }) {
			t.Errorf("%s: %s %+v, want %s with %q", c.name, r.Status, r.Reasons, c.want, c.text)
		}
	}
	r := Compute(Input{Now: now, Targets: targets, NewestChecked: pt, LatestPassingTest: good, LatestTest: good, LatestAttempt: &Test{ID: 5, Verdict: Unknown}})
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "could not run") {
		t.Errorf("a test that could not run is noted: %+v", r)
	}
	// A full backup for a reason (a checkpoint gone from the host) is noted and changes nothing else.
	full := &Point{ID: 3, TakenAt: ago(60), FullReason: "checkpoint gaia-7 of point 2 is not on the host"}
	r = Compute(Input{Now: now, Targets: targets, NewestChecked: full, LatestPassingTest: good, LatestTest: good})
	if r.Status != OK || len(r.Notes) != 1 || r.Notes[0] != "the newest backup (point 3) is a full backup: checkpoint gaia-7 of point 2 is not on the host" {
		t.Errorf("a full backup is noted: %+v", r)
	}
}

// A host that presents another SSH key than the pinned one is its own cause (ADR-007 C17): backups that stopped
// because of it are host_key_changed (CRITICAL, scoped to the host, not backup_failing), with the date the trusted key
// was pinned and the new key's fingerprint; a VM never backed up on it is UNKNOWN with the same code. Once an admin
// accepted the key, the job's failure is a plain failure (the server clears the flag) and the host's facts are gone.
func TestHostKeyChanged(t *testing.T) {
	host := &Scope{Kind: ScopeHost, ID: 7, Name: "kvm-07"}
	targets := &Targets{RPOS: 900, RTOS: 600, MaxProofAgeS: 3600}
	pinned := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	key := &HostKey{PinnedAt: &pinned, PresentedFingerprint: "SHA256:presented"}
	failed := &Backup{JobID: 12, FinishedAt: ago(60), Error: "host key SHA256:presented is not the pinned SHA256:pinned: refusing to connect", HostKeyChanged: true}
	r := Compute(Input{Now: now, Targets: targets, Host: host, NewestChecked: &Point{ID: 1, TakenAt: ago(5000)}, FailedBackup: failed, HostKeyChanged: key})
	x := r.Reasons[0]
	if r.Status != Critical || x.Code != HostKeyChanged || x.Rule != "theoretical_rpo_over_target" || x.Scope != host || x.Params.Job != 12 ||
		x.Params.PinnedAt == nil || !x.Params.PinnedAt.Equal(pinned) || x.Params.Fingerprint != "SHA256:presented" {
		t.Fatalf("stopped backups: %s %+v", r.Status, x)
	}
	if !strings.Contains(x.Text, "host kvm-07 presents a different SSH host key (SHA256:presented) than the one pinned on 2026-11-02T09:30:00Z") ||
		!strings.Contains(x.Text, "until an admin checks and accepts the new key") {
		t.Fatalf("sentence %q", x.Text)
	}
	if slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Code == BackupFailing || x.Code == HostKeyUntrusted }) {
		t.Fatalf("backup_failing or host_key_untrusted as well: %+v", r.Reasons)
	}
	// The host says so but no backup job failed on it yet: the same CRITICAL once the RPO target passed.
	r = Compute(Input{Now: now, Targets: targets, Host: host, NewestChecked: &Point{ID: 1, TakenAt: ago(5000)}, HostKeyChanged: key})
	if x := r.Reasons[0]; r.Status != Critical || x.Code != HostKeyChanged || x.Rule != "theoretical_rpo_over_target" || x.Params.Job != 0 {
		t.Fatalf("stopped backups, no job yet: %s %+v", r.Status, r.Reasons)
	}
	for _, in := range []Input{
		{Now: now, Targets: targets, Host: host, FailedBackup: failed, HostKeyChanged: key}, // the job said so
		{Now: now, Targets: targets, Host: host, HostKeyChanged: key},                       // the host says so, no job yet
	} {
		r := Compute(in)
		i := slices.IndexFunc(r.Reasons, func(x Reason) bool { return x.Rule == "no_checked_point" })
		if r.Status != Unknown || i < 0 || r.Reasons[i].Code != HostKeyChanged || r.Reasons[i].Status != Unknown || r.Reasons[i].Scope != host {
			t.Fatalf("never backed up: %s %+v", r.Status, r.Reasons)
		}
		if slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Code == NeverBackedUp || x.Code == BackupFailing }) {
			t.Fatalf("never_backed_up or backup_failing as well: %+v", r.Reasons)
		}
		if got := Render(r.Reasons[i], now); got != r.Reasons[i].Text {
			t.Fatalf("sentence %q / %q", got, r.Reasons[i].Text)
		}
	}
	// Accepted: the server clears the job's flag and the host's facts; the failure stays a failure.
	accepted := *failed
	accepted.HostKeyChanged = false
	r = Compute(Input{Now: now, Targets: targets, Host: host, NewestChecked: &Point{ID: 1, TakenAt: ago(5000)}, FailedBackup: &accepted})
	if r.Reasons[0].Code != BackupFailing {
		t.Fatalf("after the key was accepted: %+v", r.Reasons)
	}
}

// A changed host key acts at once (ADR-007 C18): from the moment the change is seen, a VM on the host whose newest
// checked backup is still within the RPO target is at least WARNING (host_key_changed, rule backups_paused: Gaia reads
// nothing from the host), even when it was OK; it turns CRITICAL when its RPO target passes, and NextChange says when.
// A worse status for another reason stays (worst wins); with no checked point it is UNKNOWN (no_checked_point).
func TestHostKeyChangedPausesBackups(t *testing.T) {
	host := &Scope{Kind: ScopeHost, ID: 7, Name: "kvm-07"}
	targets := &Targets{RPOS: 900, RTOS: 600, MaxProofAgeS: 3600}
	pinned := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	key := &HostKey{PinnedAt: &pinned, PresentedFingerprint: "SHA256:presented"}
	good := &Test{ID: 3, PointID: 1, Verdict: OK, PointTakenAt: ago(300), FinishedAt: ago(200), ProvenRTOS: f(120)}
	point := &Point{ID: 1, TakenAt: ago(300)}
	ok := Input{Now: now, Targets: targets, Host: host, NewestChecked: point, LatestTest: good, LatestPassingTest: good}
	if r := Compute(ok); r.Status != OK {
		t.Fatalf("before the change: %s %+v", r.Status, r.Reasons)
	}
	paused := ok
	paused.HostKeyChanged = key // seen by a host check: no backup job failed yet
	r := Compute(paused)
	x := r.Reasons[0]
	if r.Status != Warning || x.Code != HostKeyChanged || x.Status != Warning || x.Rule != RuleBackupsPaused || x.Scope != host ||
		x.Params.Point != 1 || x.Params.TargetS != 900 || x.Params.Fingerprint != "SHA256:presented" || x.Params.PinnedAt == nil || x.Params.Job != 0 {
		t.Fatalf("paused: %s %+v", r.Status, r.Reasons)
	}
	want := "host kvm-07 presents a different SSH host key (SHA256:presented) than the one pinned on 2026-11-02T09:30:00Z; Gaia reads nothing from it until an admin checks and accepts the new key: backups are paused (the newest checked backup, point 1, is 5m0s old, within the RPO target 15m0s)"
	if x.Text != want || Render(x, now) != want {
		t.Fatalf("sentence %q", x.Text)
	}
	d, _ := Lookup(HostKeyChanged)
	if !d.Allows(Warning, RuleBackupsPaused) || d.GroupStatus != Critical {
		t.Fatalf("definition %+v", d)
	}
	// The WARNING comes first among the warnings: it is the VM's deciding reason, so its host is one group.
	late := paused
	late.LatestPassingTest = &Test{ID: 2, PointID: 1, Verdict: OK, PointTakenAt: ago(300), FinishedAt: ago(4000), ProvenRTOS: f(120)}
	late.LatestTest = late.LatestPassingTest
	if r := Compute(late); r.Status != Warning || r.Reasons[0].Code != HostKeyChanged || !slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Code == ProofTooOld }) {
		t.Fatalf("paused and proof too old: %+v", r.Reasons)
	}
	// The job that failed on it says the same, with the job.
	withJob := paused
	withJob.FailedBackup = &Backup{JobID: 12, FinishedAt: ago(60), Error: "host key SHA256:presented is not the pinned SHA256:pinned: refusing to connect", HostKeyChanged: true}
	if r := Compute(withJob); r.Status != Warning || r.Reasons[0].Rule != RuleBackupsPaused || r.Reasons[0].Params.Job != 12 ||
		slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Code == BackupFailing }) {
		t.Fatalf("paused, the job failed: %+v", r.Reasons)
	}
	// The WARNING turns CRITICAL exactly when the RPO target passes: 900 s after the point.
	next := NextChange(paused)
	if at := point.TakenAt.Add(900 * time.Second); next == nil || !next.Equal(at) {
		t.Fatalf("next change %v, want %v", next, at)
	}
	after := paused
	after.Now = next.Add(time.Second)
	if r := Compute(after); r.Status != Critical || r.Reasons[0].Code != HostKeyChanged || r.Reasons[0].Rule != "theoretical_rpo_over_target" ||
		slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Rule == RuleBackupsPaused }) {
		t.Fatalf("after the RPO target: %s %+v", r.Status, r.Reasons)
	}
	// A worse status for another reason stays; the paused backups are still one of its reasons.
	failing := paused
	failing.LatestTest = &Test{ID: 4, PointID: 1, Verdict: Critical, FinishedAt: ago(30), FailedCheck: "boot", Detail: "no login prompt"}
	if r := Compute(failing); r.Status != Critical || r.Reasons[0].Code != TestFailedBoot ||
		!slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Rule == RuleBackupsPaused && x.Status == Warning }) {
		t.Fatalf("paused and a failed test: %+v", r.Reasons)
	}
	// Without a policy there is no target to be over: paused, and UNKNOWN for the policy.
	noPolicy := paused
	noPolicy.Targets = nil
	if r := Compute(noPolicy); r.Status != Unknown || !slices.ContainsFunc(r.Reasons, func(x Reason) bool {
		return x.Rule == RuleBackupsPaused && x.Params.TargetS == 0 && strings.HasSuffix(x.Text, "backups are paused (the newest checked backup, point 1, is 5m0s old)")
	}) {
		t.Fatalf("paused without a policy: %+v", r.Reasons)
	}
	// Accepted: the host's facts are gone and the VM is OK again.
	if r := Compute(ok); r.Status != OK {
		t.Fatalf("after the key was accepted: %+v", r.Reasons)
	}
}

// A host whose SSH key was never trusted is the host's cause (ADR-007 C11): backups that stopped because of it are
// host_key_untrusted (CRITICAL, not backup_failing), and a VM never backed up on it is UNKNOWN with the same code
// (not never_backed_up), scoped to the host either way.
func TestHostKeyUntrusted(t *testing.T) {
	host := &Scope{Kind: ScopeHost, ID: 3, Name: "kvm-03"}
	targets := &Targets{RPOS: 900, RTOS: 600, MaxProofAgeS: 3600}
	failed := &Backup{JobID: 9, FinishedAt: ago(60), Error: "the host's SSH key is not trusted yet", HostKeyUntrusted: true}
	r := Compute(Input{Now: now, Targets: targets, Host: host, NewestChecked: &Point{ID: 1, TakenAt: ago(5000)}, FailedBackup: failed})
	if x := r.Reasons[0]; r.Status != Critical || x.Code != HostKeyUntrusted || x.Rule != "theoretical_rpo_over_target" || x.Scope != host || x.Params.Job != 9 {
		t.Fatalf("stopped backups: %s %+v", r.Status, x)
	}
	for _, in := range []Input{
		{Now: now, Targets: targets, Host: host, FailedBackup: failed},   // the job said so
		{Now: now, Targets: targets, Host: host, HostKeyUntrusted: true}, // the host says so, no job yet
	} {
		r := Compute(in)
		i := slices.IndexFunc(r.Reasons, func(x Reason) bool { return x.Rule == "no_checked_point" })
		if r.Status != Unknown || i < 0 || r.Reasons[i].Code != HostKeyUntrusted || r.Reasons[i].Status != Unknown || r.Reasons[i].Scope != host {
			t.Fatalf("never backed up: %s %+v", r.Status, r.Reasons)
		}
		if slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Code == NeverBackedUp || x.Code == BackupFailing }) {
			t.Fatalf("never_backed_up or backup_failing as well: %+v", r.Reasons)
		}
		if got := Render(r.Reasons[i], now); got != r.Reasons[i].Text || !strings.Contains(got, "kvm-03's SSH key is not trusted yet") {
			t.Fatalf("sentence %q / %q", got, r.Reasons[i].Text)
		}
	}
	// A failure for another reason stays backup_failing.
	other := &Backup{JobID: 9, FinishedAt: ago(60), Error: "libvirt: no such domain"}
	if r := Compute(Input{Now: now, Targets: targets, Host: host, NewestChecked: &Point{ID: 1, TakenAt: ago(5000)}, FailedBackup: other}); r.Reasons[0].Code != BackupFailing {
		t.Fatalf("another failure: %+v", r.Reasons[0])
	}
}
