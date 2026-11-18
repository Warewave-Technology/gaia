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
