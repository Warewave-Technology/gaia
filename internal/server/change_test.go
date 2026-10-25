package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/drift"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/plan"
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

// When a team's people change, the contracts that give it access get an
// access run with what was applied; one waiting is enough.
func TestPeopleChangesStartAccessRuns(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	can := e.signer("can", "team-web")
	e.git.Write("platform/teams.yaml", "apiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-platform }\nspec: {}\n---\napiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-web }\nspec: {}\n")
	e.edit("contracts/poc-web.yaml", "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { team: team-web }\n")
	e.git.CommitSigned("web gets access", can.git)
	srv := e.start()
	e.evaluate(en)
	c, _ := e.open("poc-web")
	if c := e.waitChange(c.ID, change.Applied, change.Failed); c.Status != change.Applied {
		t.Fatalf("change = %+v", c)
	}

	if runs, err := queue.Redistribute(ctx, e.store, srv.Queue(), []string{"team-ops"}); err != nil || len(runs) != 0 {
		t.Errorf("a team without access: %v %v", runs, err)
	}
	runs, err := queue.Redistribute(ctx, e.store, srv.Queue(), []string{"team-web"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("redistribute: %v %v", runs, err)
	}
	r := e.waitFor(runs[0], "succeeded", "failed")
	if r.Status != "succeeded" || r.RequestedBy.String != queue.AccessRequester {
		t.Errorf("access run = %+v", r)
	}
	if _, err := queue.EnqueueAccess(ctx, e.store, srv.Queue(), "nope"); !errors.Is(err, queue.ErrNotApplied) {
		t.Errorf("a contract never applied: %v", err)
	}
}

// An Operation is a change: approved by the owner, scheduled for its
// window, and never changed once approved.
func TestOperationsAreApprovedThenScheduled(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	mehmet := e.signer("mehmet", "team-platform")
	e.signer("ayse", "team-platform")
	q := e.store.Q()
	if err := e.store.CreateHost(ctx, store.NewHost{UID: "uid-1", Contract: "poc-web", Hostname: "labtpoc01", Site: "lab", Network: "lab-poc", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	_ = q.SetHostStatus(ctx, db.SetHostStatusParams{Uid: "uid-1", Status: db.HostStatusActive})
	start := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	op := fmt.Sprintf(`apiVersion: gaia/v1alpha1
kind: Operation
metadata: { name: reboot-poc }
spec:
  action: reboot
  selector: 'contract == "poc-web"'
  window: { start: "%s", end: "%s" }
  strategy: { batchSize: "1" }
`, start.Format(time.RFC3339), start.Add(time.Hour).Format(time.RFC3339))
	e.git.Write("contracts/reboot.yaml", op)
	e.git.Write("contracts/late.yaml", "apiVersion: gaia/v1alpha1\nkind: Operation\nmetadata: { name: late }\nspec:\n  action: reboot\n  selector: 'true'\n  notAfter: \"2020-01-01T00:00:00Z\"\n")
	e.edit("platform/policy.yaml", "  gaiaRoles:", "  operations: { requireWindow: { test: [patch] } }\n  gaiaRoles:")
	e.git.Write("contracts/nowindow.yaml", "apiVersion: gaia/v1alpha1\nkind: Operation\nmetadata: { name: nowindow }\nspec:\n  action: patch\n  selector: 'true'\n")
	e.git.CommitSigned("operations", mehmet.git)
	e.evaluate(en)

	c, ok := e.open(change.OperationKey("reboot-poc"))
	if !ok {
		t.Fatal("no change for the operation")
	}
	rows, _ := q.Gates(ctx, c.ID)
	var need change.Need
	for _, r := range rows {
		if r.Gate == "approval" {
			_ = json.Unmarshal(r.Detail, &need)
		}
	}
	if len(need.Groups) != 1 || need.Groups[0].Name != "owner" || strings.Join(need.Groups[0].Candidates, ",") != "ayse" {
		t.Fatalf("need = %+v", need)
	}
	for key, want := range map[string]string{"Operation/late": "notAfter has passed", "Operation/nowindow": "patch in test needs a window"} {
		st, err := q.ContractState(ctx, key)
		if err != nil || !strings.Contains(st.Error.String, want) {
			t.Errorf("%s: %+v %v", key, st, err)
		}
		if _, ok := e.open(key); ok {
			t.Errorf("%s has a change", key)
		}
	}

	u, _ := q.UserByName(ctx, "ayse")
	if err := en.Decide(ctx, change.Decision{Change: c.ID, PlanHash: c.PlanHash, UserID: u.ID, Username: "ayse", Approve: true}); err != nil {
		t.Fatal(err)
	}
	row, err := q.Operation(ctx, "reboot-poc")
	if err != nil || row.Status != "scheduled" {
		t.Fatalf("operation = %+v, %v", row, err)
	}
	if got, _ := q.Change(ctx, c.ID); got.Status != change.Running || !got.RunID.Valid {
		t.Errorf("change = %+v", got)
	}
	var at time.Time
	if err := e.store.Pool().QueryRow(ctx, "select scheduled_at from river_job where kind = 'gaia.operation'").Scan(&at); err != nil || !at.Equal(start) {
		t.Errorf("scheduled at %v, want %v (%v)", at, start, err)
	}

	// Approved: a commit that changes it is refused.
	e.edit("contracts/reboot.yaml", `batchSize: "1"`, `batchSize: "2"`)
	e.git.CommitSigned("change the operation", mehmet.git)
	e.evaluate(en)
	st, _ := q.ContractState(ctx, change.OperationKey("reboot-poc"))
	if !strings.Contains(st.Error.String, "it does not change") {
		t.Errorf("after changing it: %+v", st)
	}
}

// A schedule approved once approves the Operations it writes.
func TestAScheduleApprovedOnceApprovesItsOperations(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	mehmet := e.signer("mehmet", "team-platform")
	e.signer("ayse", "team-platform")
	q := e.store.Q()
	if err := e.store.CreateHost(ctx, store.NewHost{UID: "uid-1", Contract: "poc-web", Hostname: "labtpoc01", Site: "lab", Network: "lab-poc", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	_ = q.SetHostStatus(ctx, db.SetHostStatusParams{Uid: "uid-1", Status: db.HostStatusActive})
	e.git.Write("platform/schedule.yaml", `apiVersion: gaia/v1alpha1
kind: OperationSchedule
metadata: { name: nightly }
spec:
  recurrence: "FREQ=DAILY"
  start: "03:00"
  duration: 1h
  approval: once
  output: contracts/operations
  template: { action: reboot, selector: 'contract == "poc-web"' }
`)
	start := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	gen, err := catalog.Render(catalog.KindOperation, "nightly-1", nil, &catalog.OperationSpec{Action: "reboot", Selector: `contract == "poc-web"`,
		Window: &catalog.Window{Start: start.Format(time.RFC3339), End: start.Add(time.Hour).Format(time.RFC3339)}}, "OperationSchedule/nightly", "x")
	if err != nil {
		t.Fatal(err)
	}
	e.git.Write("contracts/operations/nightly-1.yaml", string(gen))
	e.git.CommitSigned("nightly reboots", mehmet.git)

	e.evaluate(en)
	sc, ok := e.open(change.ScheduleKey("nightly"))
	if !ok {
		t.Fatal("no change for the schedule")
	}
	oc, ok := e.open(change.OperationKey("nightly-1"))
	if !ok || oc.Status != change.Pending {
		t.Fatalf("the operation before the schedule is approved: %+v", oc)
	}
	u, _ := q.UserByName(ctx, "ayse")
	if err := en.Decide(ctx, change.Decision{Change: sc.ID, PlanHash: sc.PlanHash, UserID: u.ID, Username: "ayse", Approve: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Change(ctx, sc.ID); got.Status != change.Applied {
		t.Fatalf("schedule change = %s", got.Status)
	}
	e.evaluate(en)
	if row, err := q.Operation(ctx, "nightly-1"); err != nil || row.Status != "scheduled" {
		t.Fatalf("operation = %+v, %v", row, err)
	}
	rows, _ := q.Gates(ctx, oc.ID)
	for _, r := range rows {
		if r.Gate == "approval" && (r.Status != "ok" || !strings.Contains(string(r.Detail), `"via": "OperationSchedule/nightly"`)) {
			t.Errorf("approval gate = %s %s", r.Status, r.Detail)
		}
	}
}

// A server whose grace period is over is deleted by a decommission run,
// queued by the periodic sweep; the contract stays synced.
func TestGracePeriodsEndInADecommissionRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	srv := e.start()
	// A server the last applied change stopped, a day ago.
	q := e.store.Q()
	if err := e.store.CreateHost(ctx, store.NewHost{UID: "u-old", Contract: "poc-web", Hostname: "labtpoc01", Site: "lab", Network: "lab-poc", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.StopHost(ctx, db.StopHostParams{Uid: "u-old", DeleteAfter: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	snap := e.snapshot()
	g, err := e.repos.Graph(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(ctx, g, nil, apply.StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	snapJSON, _ := json.Marshal(snap)
	if err := q.SetApplied(ctx, db.SetAppliedParams{Contract: "poc-web", AppliedSnapshot: snapJSON, AppliedPlanHash: pgtype.Text{String: p.Hash, Valid: true}, AppliedPlan: data}); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.Queue().Insert(ctx, queue.EndGraceArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		h, _ := q.Host(ctx, "u-old")
		if h.Status == db.HostStatusDeleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host is %s", h.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The applied plan is the one after the run: nothing to change.
	deadline = time.Now().Add(10 * time.Second)
	for {
		st, _ := q.ContractState(ctx, "poc-web")
		var applied plan.Plan
		_ = json.Unmarshal(st.AppliedPlan, &applied)
		if len(applied.Contracts) == 1 && len(applied.Contracts[0].Servers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("applied plan = %s", st.AppliedPlan)
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.evaluate(en)
	if c, ok := e.open("poc-web"); ok {
		t.Errorf("a change opened after the decommission run: %+v", c)
	}
}

// apply records the head as applied, as a change would.
func (e *env) applied(contract string) {
	e.t.Helper()
	ctx := context.Background()
	snap := e.snapshot()
	g, err := e.repos.Graph(ctx, snap)
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := plan.Build(ctx, g, []string{contract}, apply.StoreState{S: e.store})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.store.Q().SetApplied(ctx, db.SetAppliedParams{Contract: contract, AppliedSnapshot: mustJSONT(e.t, snap), AppliedPlanHash: pgtype.Text{String: p.Hash, Valid: true}, AppliedPlan: mustJSONT(e.t, p)}); err != nil {
		e.t.Fatal(err)
	}
}

func mustJSONT(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Drift opens the change that puts it right: at once for registration,
// after the owner's approval when the Policy says so.
func TestDriftOpensAChange(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	e.start()
	e.applied("poc-web")
	now := time.Now()
	found := []apply.Finding{{Contract: "poc-web", UID: "u1", Hostname: "labtpoc01", Area: "registration", Key: "monitoring/enabled", Want: "enabled", Got: "disabled"}}
	sc := &drift.Scanner{Store: e.store, Repos: e.repos, Systems: e.systems, Open: en.OpenDrift, Now: func() time.Time { return now },
		Checks: map[string]drift.Check{"registration": func(context.Context, *catalog.Graph, *plan.Plan, apply.Deps) ([]apply.Finding, error) {
			return found, nil
		}}}
	if err := sc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	live, err := e.store.Q().LiveDrift(ctx, pgtype.Text{})
	if err != nil || len(live) != 1 || live[0].Key != "monitoring/enabled" || !live[0].ChangeID.Valid {
		t.Fatalf("drift = %+v %v", live, err)
	}
	c, err := e.store.Q().Change(ctx, live[0].ChangeID.String)
	if err != nil || c.Source != change.DriftAuto {
		t.Fatalf("change = %+v %v", c, err)
	}
	if c := e.waitChange(c.ID, change.Applied, change.Failed); c.Status != change.Applied {
		t.Fatalf("drift change = %s", c.Status)
	}
	// Not due again for fifteen minutes.
	if err := sc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.open("poc-web"); ok {
		t.Error("checked again before it was due")
	}

	// The Policy wants approval: the change waits for the owner.
	e.edit("platform/policy.yaml", "  gaiaRoles:\n", "  drift: { remediation: { registration: approval } }\n  gaiaRoles:\n")
	e.git.Commit("drift needs approval")
	e.applied("poc-web")
	now = now.Add(16 * time.Minute)
	if err := sc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	open, ok := e.open("poc-web")
	if !ok || open.Source != change.Drift || open.Status != change.Pending || gates(t, e, open.ID)["approval"] != change.Waiting {
		t.Fatalf("open = %+v %v, gates %v", open, ok, gates(t, e, open.ID))
	}
	// An evaluation does not take it away: it keeps the applied plan.
	e.evaluate(en)
	if again, ok := e.open("poc-web"); !ok || again.ID != open.ID {
		t.Errorf("after evaluation: %+v", again)
	}
	// Put right: the report resolves.
	found = nil
	now = now.Add(16 * time.Minute)
	if err := sc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if live, _ := e.store.Q().LiveDrift(ctx, pgtype.Text{}); len(live) != 0 {
		t.Errorf("drift after it went = %+v", live)
	}
	if c, _ := e.store.Q().Change(ctx, open.ID); c.Status != change.Superseded {
		t.Errorf("its change = %s", c.Status)
	}
}
