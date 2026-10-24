package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// signer is a person with a registered SSH signing key.
type signer struct {
	name string
	git  signature.SSHSigner
}

func (e *env) signer(name string, teams ...string) signer {
	e.t.Helper()
	ctx := context.Background()
	id := store.NewUserID()
	q := e.store.Q()
	if err := q.InsertUser(ctx, db.InsertUserParams{ID: id, Username: name, Provider: "local"}); err != nil {
		e.t.Fatal(err)
	}
	for _, t := range teams {
		if err := q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: t, Source: "local"}); err != nil {
			e.t.Fatal(err)
		}
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewSignerFromKey(priv)
	if err := q.InsertSigningKey(ctx, db.InsertSigningKeyParams{
		Fingerprint: ssh.FingerprintSHA256(k.PublicKey()), UserID: id, Kind: "ssh", Name: "key",
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.PublicKey()))),
	}); err != nil {
		e.t.Fatal(err)
	}
	return signer{name: name, git: signature.SSHSigner{Key: k}}
}

func (e *env) engine() *change.Engine {
	return &change.Engine{Store: e.store, Repos: e.repos, Queue: e.queue, Keys: func(ctx context.Context) ([]signature.Key, error) {
		rows, err := e.store.Q().AllSigningKeys(ctx)
		var keys []signature.Key
		for _, k := range rows {
			keys = append(keys, signature.Key{UserID: k.UserID, Username: k.Username, Kind: k.Kind, Fingerprint: k.Fingerprint, PublicKey: k.PublicKey})
		}
		return keys, err
	}}
}

// evaluate runs the engine at the repository's head.
func (e *env) evaluate(en *change.Engine) {
	e.t.Helper()
	ctx := context.Background()
	snap, err := e.repos.Head(ctx, repo.Rev{Name: "platform", URL: e.repo})
	if err != nil {
		e.t.Fatal(err)
	}
	g, err := e.repos.Graph(ctx, snap)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := en.Evaluate(ctx, snap, g); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) open(contract string) (db.Change, bool) {
	c, err := e.store.Q().OpenChange(context.Background(), contract)
	return c, err == nil
}

func (e *env) waitChange(id string, statuses ...string) db.Change {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := e.store.Q().Change(context.Background(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, st := range statuses {
			if c.Status == st {
				return c
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("change %s is %s, want %v", id, c.Status, statuses)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func gates(t *testing.T, e *env, id string) map[string]string {
	t.Helper()
	rows, err := e.store.Q().Gates(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, g := range rows {
		out[g.Gate] = g.Status
	}
	return out
}

// edit changes a file of the repository in place.
func (e *env) edit(path, from, to string) {
	e.t.Helper()
	data, err := readFile(e.repo, path)
	if err != nil {
		e.t.Fatal(err)
	}
	if !strings.Contains(data, from) {
		e.t.Fatalf("%s has no %q", path, from)
	}
	e.git.Write(path, strings.Replace(data, from, to, 1))
}

func TestASignedCommitIsAppliedWithoutApprovalInTest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	mehmet := e.signer("mehmet", "team-web")
	e.git.CommitSigned("poc-web: label", mehmet.git)
	e.start()

	e.evaluate(en)
	c, ok := e.open("poc-web")
	if !ok {
		t.Fatal("no change")
	}
	if strings.Join(c.RequestedBy, ",") != "mehmet" || c.Source != "commit" {
		t.Errorf("change = %+v", c)
	}
	if g := gates(t, e, c.ID); g["signature"] != "ok" || g["approval"] != "ok" {
		t.Errorf("gates = %v", g)
	}
	c = e.waitChange(c.ID, change.Applied, change.Failed)
	if c.Status != change.Applied || !c.RunID.Valid {
		t.Fatalf("change = %+v", c)
	}
	st, err := e.store.Q().ContractState(ctx, "poc-web")
	if err != nil || st.Sync != "synced" || !st.AppliedPlanHash.Valid {
		t.Fatalf("state = %+v, %v", st, err)
	}
	// Nothing new in Git: nothing to do.
	e.evaluate(en)
	if _, ok := e.open("poc-web"); ok {
		t.Error("a change for an applied state")
	}
}

func TestUnsignedCommitsAreNotApplied(t *testing.T) {
	e := setup(t)
	en := e.engine()
	e.evaluate(en)
	c, ok := e.open("poc-web")
	if !ok {
		t.Fatal("no change")
	}
	g := gates(t, e, c.ID)
	if g["signature"] != "failed" || c.Status != change.Pending || c.RunID.Valid {
		t.Errorf("gates %v, change %+v", g, c)
	}
	var commits []change.Commit
	_ = json.Unmarshal(c.Commits, &commits)
	if len(commits) != 1 || !strings.Contains(commits[0].Problem, "not signed") {
		t.Errorf("commits = %+v", commits)
	}
}

func TestApprovalFollowsThePlan(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	mehmet := e.signer("mehmet", "team-platform") // a signer may not approve, even an owner
	e.signer("ayse", "team-platform")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.git.CommitSigned("test needs approval", mehmet.git)
	e.start()

	e.evaluate(en)
	c, _ := e.open("poc-web")
	g := gates(t, e, c.ID)
	if g["approval"] != "waiting" || c.Status != change.Pending {
		t.Fatalf("gates %v, change %+v", g, c)
	}
	rows, _ := e.store.Q().Gates(ctx, c.ID)
	var need change.Need
	for _, r := range rows {
		if r.Gate == "approval" {
			_ = json.Unmarshal(r.Detail, &need)
		}
	}
	if len(need.Groups) != 1 || need.Groups[0].Name != "owner" || strings.Join(need.Groups[0].Candidates, ",") != "ayse" {
		t.Fatalf("need = %+v", need)
	}

	// A newer commit that leaves the plan alone: same change, new label.
	e.git.Write("README.md", "notes")
	head := e.git.CommitSigned("notes", mehmet.git)
	e.evaluate(en)
	c2, _ := e.open("poc-web")
	if c2.ID != c.ID || !strings.Contains(c2.Label, head) {
		t.Errorf("after an unrelated commit: %+v", c2)
	}

	// mehmet's approval does not count; ayse's does.
	approve := func(who string, hash string) {
		u, _ := e.store.Q().UserByName(ctx, who)
		if err := e.store.Q().InsertApproval(ctx, db.InsertApprovalParams{ChangeID: c.ID, PlanHash: hash, UserID: u.ID, Username: who, Decision: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	approve("mehmet", c.PlanHash)
	approve("ayse", "sha256:an-older-plan")
	e.evaluate(en)
	if g := gates(t, e, c.ID); g["approval"] != "waiting" {
		t.Fatalf("approved by the signer or for another plan: %v", g)
	}
	approve("ayse", c.PlanHash)
	e.evaluate(en)
	if c := e.waitChange(c.ID, change.Applied, change.Failed); c.Status != change.Applied {
		t.Fatalf("change = %+v", c)
	}
}

func TestANewPlanSupersedesTheOpenChange(t *testing.T) {
	e := setup(t)
	en := e.engine()
	mehmet := e.signer("mehmet", "team-web")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.git.CommitSigned("approval", mehmet.git)
	e.evaluate(en)
	first, _ := e.open("poc-web")
	e.edit("contracts/poc-web.yaml", "app: poc", "app: poc2")
	e.git.CommitSigned("rename app", mehmet.git)
	e.evaluate(en)
	second, _ := e.open("poc-web")
	if second.ID == first.ID || second.PlanHash == first.PlanHash {
		t.Fatalf("no new change: %+v", second)
	}
	if c, _ := e.store.Q().Change(context.Background(), first.ID); c.Status != change.Superseded {
		t.Errorf("first change = %s", c.Status)
	}
}

func TestAFailedChangeWaitsForARetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	e.failing = true
	mehmet := e.signer("mehmet", "team-web")
	e.git.CommitSigned("go", mehmet.git)
	e.start()
	e.evaluate(en)
	c, _ := e.open("poc-web")
	if c := e.waitChange(c.ID, change.Applied, change.Failed); c.Status != change.Failed {
		t.Fatalf("change = %+v", c)
	}
	e.evaluate(en)
	if _, ok := e.open("poc-web"); ok {
		t.Fatal("a failed change was tried again by itself")
	}
	e.failing = false
	snap, _ := e.repos.Head(ctx, repo.Rev{Name: "platform", URL: e.repo})
	g, _ := e.repos.Graph(ctx, snap)
	id, err := en.Retry(ctx, c.ID, snap, g)
	if err != nil {
		t.Fatal(err)
	}
	if r := e.waitChange(id, change.Applied, change.Failed); r.Status != change.Applied || r.Source != "retry" {
		t.Errorf("retry = %+v", r)
	}
	if _, err := en.Retry(ctx, id, snap, g); err == nil {
		t.Error("an applied change was retried")
	}
}

// A run cancelled before any worker took it never reaches the worker's
// hook; the next evaluation closes its change.
func TestAChangeWhoseRunWasCancelledInTheQueueIsClosed(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	mehmet := e.signer("mehmet", "team-web")
	e.git.CommitSigned("go", mehmet.git)
	e.evaluate(en) // no worker: the run stays queued
	c, _ := e.open("poc-web")
	if c.Status != change.Running || !c.RunID.Valid {
		t.Fatalf("change = %+v", c)
	}
	if err := queue.Cancel(ctx, e.store, e.queue, c.RunID.String, "test"); err != nil {
		t.Fatal(err)
	}
	e.evaluate(en)
	if got, _ := e.store.Q().Change(ctx, c.ID); got.Status != change.Cancelled {
		t.Fatalf("change = %s", got.Status)
	}
	if _, ok := e.open("poc-web"); ok {
		t.Error("the cancelled change was opened again by itself")
	}
}

// The engine tells subscribers what happens: pending, approved, applied
// and rejected, each once.
func TestTheEngineNotifies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	en.Notify = &notify.Notifier{Store: e.store, Queue: e.queue}
	e.finished = en.Finished
	mehmet := e.signer("mehmet", "team-platform")
	e.signer("ayse", "team-platform")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.git.Write("platform/notify.yaml", `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: hook }
spec: { type: webhook, endpoint: "http://127.0.0.1:9/hook" }
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-platform }
spec:
  notifications:
    - { provider: hook, triggers: [change.pending, change.approved, change.applied, change.failed, change.rejected] }
`)
	e.git.CommitSigned("notify the platform team", mehmet.git)
	e.start()

	triggers := func(change string) string {
		rows, err := e.store.Q().Notifications(ctx, db.NotificationsParams{ChangeID: change, MaxRows: 50})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(rows) - 1; i >= 0; i-- {
			out = append(out, rows[i].Trigger)
		}
		return strings.Join(out, ",")
	}
	e.evaluate(en)
	e.evaluate(en)
	c, _ := e.open("poc-web")
	if got := triggers(c.ID); got != "change.pending" {
		t.Fatalf("after evaluating twice: %s", got)
	}
	u, _ := e.store.Q().UserByName(ctx, "ayse")
	if err := en.Decide(ctx, change.Decision{Change: c.ID, PlanHash: c.PlanHash, UserID: u.ID, Username: "ayse", Approve: true}); err != nil {
		t.Fatal(err)
	}
	e.waitChange(c.ID, change.Applied, change.Failed)
	if got := triggers(c.ID); got != "change.pending,change.approved,change.applied" {
		t.Errorf("triggers = %s", got)
	}
	var m notify.Message
	rows, _ := e.store.Q().Notifications(ctx, db.NotificationsParams{ChangeID: c.ID, MaxRows: 1})
	_ = json.Unmarshal(rows[0].Message, &m)
	if strings.Join(m.Event.ApprovedBy, ",") != "ayse" || m.Event.Run == "" || m.Channel.Endpoint != "http://127.0.0.1:9/hook" {
		t.Errorf("applied message = %+v", m)
	}

	e.edit("contracts/poc-web.yaml", "app: poc", "app: poc2")
	e.git.CommitSigned("rename", mehmet.git)
	e.evaluate(en)
	c2, _ := e.open("poc-web")
	if err := en.Decide(ctx, change.Decision{Change: c2.ID, PlanHash: c2.PlanHash, UserID: u.ID, Username: "ayse", Comment: "no"}); err != nil {
		t.Fatal(err)
	}
	if got := triggers(c2.ID); got != "change.pending,change.rejected" {
		t.Errorf("rejected: %s", got)
	}
}

// A team with an approvers group: its people approve the owner's part,
// not every member.
func TestOwnersApproveThroughTheApproversGroup(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	can := e.signer("can", "team-web")
	e.signer("mehmet")
	e.signer("ayse")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.git.Write("platform/teams.yaml", `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-platform }
spec:
  approversGroup: { dn: "CN=SG-Team-Platform-Approvers,OU=Teams,DC=gaia,DC=dev" }
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec: {}
`)
	e.git.CommitSigned("approvers", can.git)
	q := e.store.Q()
	id := func(name string) string { u, _ := q.UserByName(ctx, name); return u.ID }
	_ = q.SetMemberships(ctx, db.SetMembershipsParams{Team: "team-platform", Source: "ad:corp", Role: "member", Users: []string{id("mehmet"), id("ayse")}})
	_ = q.SetMemberships(ctx, db.SetMembershipsParams{Team: "team-platform", Source: "ad:corp", Role: "approver", Users: []string{id("ayse")}})
	e.evaluate(en)
	c, _ := e.open("poc-web")
	rows, _ := q.Gates(ctx, c.ID)
	var need change.Need
	for _, r := range rows {
		if r.Gate == "approval" {
			_ = json.Unmarshal(r.Detail, &need)
		}
	}
	if len(need.Groups) != 1 || need.Groups[0].Role != "approver" || strings.Join(need.Groups[0].Candidates, ",") != "ayse" {
		t.Errorf("need = %+v", need)
	}
}

// Granting access through a privileged group needs a platform admin and
// the group's owner, and nobody in the team gaining access approves it;
// once applied, the same access needs nothing more.
func TestAccessGrantsNeedTheirApprovers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	can := e.signer("can", "team-web")
	e.signer("ayse", "team-platform") // platform-admin
	e.signer("deniz", "team-dba", "team-platform")
	e.signer("selin", "team-sec")
	e.git.Write("platform/access.yaml", `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-platform }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-dba }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-sec }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Group
metadata: { name: dbas }
spec: { teams: [team-dba], owner: team-sec, privileged: true }
`)
	e.edit("contracts/poc-web.yaml", "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { group: dbas, sudo: true }\n")
	e.git.CommitSigned("dbas get sudo on poc-web", can.git)
	e.start()

	e.evaluate(en)
	c, _ := e.open("poc-web")
	need := func(id string) change.Need {
		rows, _ := e.store.Q().Gates(ctx, id)
		var n change.Need
		for _, r := range rows {
			if r.Gate == "approval" {
				_ = json.Unmarshal(r.Detail, &n)
			}
		}
		return n
	}
	n := need(c.ID)
	got := map[string]string{}
	for _, gr := range n.Groups {
		got[gr.Name] = strings.Join(gr.Candidates, ",")
	}
	// deniz is a platform admin too, but gains the access: ayse approves.
	if len(n.Groups) != 2 || got["platform-admin"] != "ayse" || got["group-owner/dbas"] != "selin" || !slices.Contains(n.Excluded, "deniz") {
		t.Fatalf("need = %+v", n)
	}
	for _, who := range []string{"ayse", "selin"} {
		u, _ := e.store.Q().UserByName(ctx, who)
		if err := en.Decide(ctx, change.Decision{Change: c.ID, PlanHash: c.PlanHash, UserID: u.ID, Username: who, Approve: true}); err != nil {
			t.Fatalf("%s: %v", who, err)
		}
	}
	if c := e.waitChange(c.ID, change.Applied, change.Failed); c.Status != change.Applied {
		t.Fatalf("change = %+v", c)
	}

	// The access is applied: another change to the contract needs no
	// approval for it (test needs none otherwise).
	e.edit("contracts/poc-web.yaml", "app: poc", "app: poc2")
	e.git.CommitSigned("rename", can.git)
	e.evaluate(en)
	c2, _ := e.open("poc-web")
	if n := need(c2.ID); len(n.Groups) != 0 {
		t.Errorf("after the access was applied: %+v", n)
	}
}
