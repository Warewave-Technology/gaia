package status

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

var updateDoc = flag.Bool("update", false, "rewrite docs/reason-codes.md")

// causes widens a combination with what the codes tell apart: why backups stopped, which check failed, why a test
// could not run.
type causes struct {
	backup  string // none, unreachable, untrusted (the host key was never trusted), changed (another key than the pinned one), failed
	check   string // boot, stable, ssh
	sandbox bool
	full    bool // the newest checked point is a full backup for a reason
}

func (k causes) apply(in Input) Input {
	in.Host = &Scope{Kind: ScopeHost, ID: 4, Name: "kvm-07"}
	in.Repository = &Scope{Kind: ScopeRepository, ID: 1, Name: "main"}
	switch k.backup {
	case "unreachable":
		in.FailedBackup = &Backup{JobID: 40, FinishedAt: ago(30), Error: "SSH to gaia@kvm-07: dial tcp: i/o timeout", Unreachable: true}
	case "untrusted":
		in.FailedBackup = &Backup{JobID: 42, FinishedAt: ago(30), Error: "the host's SSH key is not trusted yet", HostKeyUntrusted: true}
	case "changed":
		pinned := ago(86400 * 30)
		in.FailedBackup = &Backup{JobID: 43, FinishedAt: ago(30), Error: "host key SHA256:new is not the pinned SHA256:old: refusing to connect", HostKeyChanged: true}
		in.HostKeyChanged = &HostKey{PinnedAt: &pinned, PresentedFingerprint: "SHA256:new"}
	case "failed":
		in.FailedBackup = &Backup{JobID: 41, FinishedAt: ago(30), Error: "libvirt: domain is not running"}
	}
	if lt := in.LatestTest; lt != nil && lt.Verdict == Critical {
		c := *lt
		c.FailedCheck = k.check
		in.LatestTest = &c
		if in.LatestAttempt != nil && in.LatestAttempt.ID == lt.ID {
			in.LatestAttempt = &c
		}
	}
	if a := in.LatestAttempt; a != nil && a.Verdict == Unknown {
		c := *a
		c.SandboxDown = k.sandbox
		in.LatestAttempt = &c
	}
	if p := in.NewestChecked; p != nil && k.full {
		c := *p
		c.FullReason = "checkpoint gaia-1-9 of point 9 is not on the host"
		in.NewestChecked = &c
	}
	return in
}

func allCauses() []causes {
	var out []causes
	for _, b := range []string{"none", "unreachable", "untrusted", "changed", "failed"} {
		for _, c := range []string{"boot", "stable", "ssh"} {
			for _, s := range []bool{false, true} {
				for _, full := range []bool{false, true} {
					out = append(out, causes{b, c, s, full})
				}
			}
		}
	}
	return out
}

// Every reason and note Compute gives has a code from the closed list, with that code's status, row and scope, and
// the text its sentence makes; every code is reachable. An unmapped reason fails here.
func TestReasonCodes(t *testing.T) {
	seen := map[Code]int{}
	check := func(c combo, k causes, x Reason, note bool) {
		d, ok := Lookup(x.Code)
		if !ok {
			t.Fatalf("%v %+v: reason without a code from the list: %+v", c, k, x)
		}
		if note != (d.Status == "") || !d.Allows(x.Status, x.Rule) {
			t.Fatalf("%v %+v: %s has status %q rule %q, its definition %q %q", c, k, x.Code, x.Status, x.Rule, d.Status, d.Rule)
		}
		if x.Scope != nil && x.Scope.Kind != d.Scope {
			t.Fatalf("%v %+v: %s with scope %+v, its definition allows %q", c, k, x.Code, x.Scope, d.Scope)
		}
		if got := Render(x, now); got != x.Text {
			t.Fatalf("%v %+v: %s renders %q, Compute wrote %q", c, k, x.Code, got, x.Text)
		}
		seen[x.Code]++
	}
	for _, c := range allCombos() {
		for _, k := range allCauses() {
			r := Compute(k.apply(c.input()))
			for _, x := range r.Reasons {
				check(c, k, x, false)
			}
			if len(r.Notes) != len(r.NoteReasons) {
				t.Fatalf("%v %+v: notes %v and note reasons %+v differ", c, k, r.Notes, r.NoteReasons)
			}
			for i, x := range r.NoteReasons {
				check(c, k, x, true)
				if r.Notes[i] != x.Text {
					t.Fatalf("%v %+v: note %q is not its reason's text %q", c, k, r.Notes[i], x.Text)
				}
			}
			// The scope is the shared cause: a host only for an unreachable or untrusted host, the sandbox only when it
			// was down.
			for _, x := range append(slices.Clone(r.Reasons), r.NoteReasons...) {
				if (x.Code == HostUnreachable || x.Code == HostKeyUntrusted || x.Code == HostKeyChanged) != (x.Scope != nil && x.Scope.Kind == ScopeHost) ||
					(x.Code == TestNotRun && (x.Scope != nil) != k.sandbox) {
					t.Fatalf("%v %+v: %s with scope %+v", c, k, x.Code, x.Scope)
				}
			}
		}
	}
	for _, d := range Defs() {
		if seen[d.Code] == 0 && d.Code != NoRule && d.Subject == "" {
			t.Errorf("code %s is never given", d.Code)
		}
	}
}

// A stored reason reads right later: its sentence is written for the time it is read.
func TestRenderLater(t *testing.T) {
	targets := &Targets{RPOS: 900, RTOS: 600, MaxProofAgeS: 3600}
	good := &Test{ID: 1, PointID: 2, Verdict: OK, PointTakenAt: ago(300), FinishedAt: ago(200), ProvenRTOS: f(120)}
	r := Compute(Input{Now: now, Targets: targets, NewestChecked: &Point{ID: 3, TakenAt: ago(60)}, LatestTest: good, LatestPassingTest: good})
	later := now.Add(10 * time.Minute)
	if got := Render(r.Reasons[0], later); !strings.Contains(got, "proven RPO 15m0s") || !strings.Contains(got, "proof age 13m20s") {
		t.Fatalf("rendered later: %q", got)
	}
}

// NextChange is exactly when time alone changes the outcome: nothing changes before it, something right after.
func TestNextChange(t *testing.T) {
	sig := signature
	n := 0
	// The combinations alone, and with a host whose key changed (ADR-007 C18: WARNING backups_paused turns CRITICAL
	// when the RPO target passes).
	var ins []Input
	var names []string
	for _, c := range allCombos() {
		for _, k := range []causes{{backup: "none"}, {backup: "changed"}} {
			ins, names = append(ins, k.apply(c.input())), append(names, fmt.Sprintf("%v %+v", c, k))
		}
	}
	for i, in := range ins {
		c := names[i]
		next := NextChange(in)
		base := sig(Compute(in))
		if next == nil {
			later := in
			later.Now = in.Now.Add(1000 * time.Hour)
			if got := sig(Compute(later)); got != base {
				t.Fatalf("%v: no next change, but %q becomes %q", c, base, got)
			}
			continue
		}
		n++
		before, after := in, in
		before.Now, after.Now = next.Add(-time.Millisecond), next.Add(time.Millisecond)
		if got := sig(Compute(before)); got != base {
			t.Fatalf("%v: changed before %v: %q -> %q", c, *next, base, got)
		}
		if got := sig(Compute(after)); got == base {
			t.Fatalf("%v: nothing changed at %v (%q)", c, *next, base)
		}
	}
	if n == 0 {
		t.Fatal("no combination has a next change")
	}
}

// docs/reason-codes.md is this list (go test ./internal/status -run TestReasonCodesDoc -update rewrites it).
func TestReasonCodesDoc(t *testing.T) {
	var b bytes.Buffer
	fmt.Fprintf(&b, `# Reason codes (version %d)

Generated from `+"`internal/status/codes.go`"+` by `+"`go test ./internal/status -run TestReasonCodesDoc -update`"+`; do not edit.

Every reason that makes a VM's status what it is, and every note, carries one of these codes (ADR-007 C3). The API,
the command line's JSON, the notifications and the daily report carry the code, its scope and its parameters next to
the sentence; the sentence is written from them when it is read, so ages are current. A reason's **rule** is the row
of the status table (ADR-007 "The status model") it belongs to; several codes can share a row when the cause differs.
The **scope** is the cause a group of VMs shares (the overview's "Needs attention" groups by code and scope); a code
without a scope is a cause of the VM itself. Codes are part of the API contract: a change is a new version of this list.

| Code | Status | Rule | Scope | Parameters | Cause | Meaning |
|---|---|---|---|---|---|---|
`, CodesVersion)
	for _, d := range Defs() {
		st, rule, scope := d.Status, "`"+d.Rule+"`", d.Scope
		for _, v := range d.Also {
			st, rule = st+", "+v.Status, rule+", `"+v.Rule+"`"
		}
		if st == "" {
			st = "note"
		}
		if d.Rule == "" {
			rule = "`-`"
		}
		if d.Subject != "" {
			rule = "`-` (" + strings.ReplaceAll(d.Subject, "_", " ") + ")"
		}
		if scope == "" {
			scope = "-"
		}
		params := "-"
		if len(d.Params) > 0 {
			params = "`" + strings.Join(d.Params, "`, `") + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s |\n", d.Code, st, rule, scope, params, d.Cause, d.Means)
	}
	const path = "../../docs/reason-codes.md"
	if *updateDoc {
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, b.Bytes()) {
		t.Fatalf("docs/reason-codes.md is not what codes.go says (go test ./internal/status -run TestReasonCodesDoc -update): %v", err)
	}
}
