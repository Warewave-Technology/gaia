package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// keyServer is an SSH server that only shows its host key: enough for a client to see that it is not the pinned one.
func keyServer(t *testing.T) (addr string, port int, key ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				ssh.NewServerConn(c, cfg) // the client refuses the key: the handshake ends here
			}()
		}
	}()
	host, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	return host, port, signer.PublicKey()
}

// A host that presents another SSH key than the pinned one (ADR-007 C17): the backup fails with the structured
// host_key_changed flag (hostconn.ErrHostKeyChanged), the presented key is kept for an admin, never pinned; the VMs are
// host_key_changed (scoped to the host, with the pinned date and the new fingerprint; not backup_failing) and one
// "Needs attention" group. Re-trusting through /trust is refused; accepting is admin-only, a step-up action, an audit
// entry and a state change; afterwards the code is gone.
func TestHostKeyChanged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	pol := f.ids["policies"]
	addr, port, presented := keyServer(t)
	h, err := f.s.db.CreateHost(ctx, store.HostInput{Name: "kvm-07", Address: addr, SSHPort: port, SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	op, _ := ssh.NewSignerFromKey(other)
	pinnedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(op.PublicKey())))
	if _, err := f.s.db.PinHostKey(ctx, "admin", h.ID, pinnedKey, hostconn.Fingerprint(op.PublicKey())); err != nil {
		t.Fatal(err)
	}
	var vms []int64
	for i := range 3 {
		v, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: fmt.Sprintf("kvm07-%d", i), PolicyID: &pol})
		if err != nil {
			t.Fatal(err)
		}
		vms = append(vms, v.ID)
	}
	j, err := f.s.db.CreateJob(ctx, "system", "backup", &vms[0], "test")
	if err != nil {
		t.Fatal(err)
	}
	f.s.runBackup(ctx, j)
	ji, err := f.s.db.JobInfo(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var d BackupDetail
	if err := json.Unmarshal(ji.Detail, &d); err != nil || !d.HostKeyChanged || d.HostKeyUntrusted || d.Unreachable {
		t.Fatalf("the backup's detail: %+v %v", d, err)
	}
	newFP := hostconn.Fingerprint(presented)
	hk, _ := f.s.db.Host(ctx, h.ID)
	if hk.SSHHostKey != pinnedKey || hk.PresentedFingerprint != newFP || hk.PresentedHostKey == "" || hk.PresentedAt == "" {
		t.Fatalf("the host after the backup: %+v", hk)
	}
	pinnedAt, _ := time.Parse(time.RFC3339Nano, hk.SSHHostKeyPinnedAt)
	for _, id := range vms {
		r, err := f.s.refreshStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		x := r.Reasons[0]
		if r.Status != status.Unknown || x.Code != status.HostKeyChanged || x.Scope == nil || x.Scope.ID != h.ID || x.Scope.Kind != status.ScopeHost ||
			x.Params.Fingerprint != newFP || x.Params.PinnedAt == nil || !x.Params.PinnedAt.Equal(pinnedAt) {
			t.Fatalf("VM %d: %s %+v", id, r.Status, r.Reasons)
		}
	}
	var av AttentionView
	if code := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/attention", "", &av); code != 200 {
		t.Fatalf("attention: %d", code)
	}
	n := 0
	for _, g := range av.Groups {
		if g.Code == string(status.HostKeyChanged) {
			n++
			if g.Count != 3 || g.Scope == nil || g.Scope.ID != h.ID || g.Params == nil || g.Params.Fingerprint != newFP || g.Params.PinnedAt == nil {
				t.Fatalf("group %+v", g)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d host_key_changed groups: %+v", n, av.Groups)
	}

	accept := fmt.Sprintf("/api/v1/hosts/%d/accept-key", h.ID)
	body := fmt.Sprintf(`{"fingerprint":%q}`, newFP)
	// The initial trust cannot replace a pinned key (it has no step-up).
	var ae apiError
	if code := f.do(f.tokens[store.RoleAdmin], "POST", fmt.Sprintf("/api/v1/hosts/%d/trust", h.ID), body, &ae); code != http.StatusConflict || !strings.Contains(ae.Error, "accept-key") {
		t.Fatalf("trust over a pinned key: %d %+v", code, ae)
	}
	// Admin only; a step-up every time.
	if code := f.do(f.tokens[store.RoleOperator], "POST", accept, body, &ae); code != http.StatusForbidden {
		t.Fatalf("an operator: %d %+v", code, ae)
	}
	if code, e := f.call(f.tokens[store.RoleAdmin], "POST", accept, body, nil, nil); code != http.StatusForbidden || e.Code != "step_up_required" {
		t.Fatalf("without a step-up: %d %+v", code, e)
	}
	if code := f.do(f.tokens[store.RoleAdmin], "POST", accept, `{"fingerprint":"SHA256:something-else"}`, &ae); code != http.StatusConflict {
		t.Fatalf("another fingerprint: %d %+v", code, ae)
	}
	if hk, _ := f.s.db.Host(ctx, h.ID); hk.SSHHostKey != pinnedKey {
		t.Fatalf("pinned without a valid accept: %+v", hk)
	}
	var got store.Host
	if code := f.do(f.tokens[store.RoleAdmin], "POST", accept, body, &got); code != http.StatusOK || got.SSHHostKey != hk.PresentedHostKey ||
		got.PresentedHostKey != "" || got.SSHHostKeyPinnedAt == hk.SSHHostKeyPinnedAt {
		t.Fatalf("accept: %d %+v", code, got)
	}
	if !hasAudit(f.audits(t), "POST /api/v1/hosts/{id}/accept-key", "accepted "+newFP+" in place of "+hostconn.Fingerprint(op.PublicKey())) {
		t.Fatalf("no audit entry for the accepted key: %+v", f.audits(t)[len(f.audits(t))-3:])
	}
	cs := f.stateChanges(t, "host_key")
	if last := cs[len(cs)-1]; last.EntityID != h.ID || last.From != "changed" || last.To != "trusted" || last.Actor != "admin" || !strings.Contains(last.Reason, newFP) {
		t.Fatalf("state change %+v", last)
	}
	for _, id := range vms {
		r, err := f.s.refreshStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range r.Reasons {
			if x.Code == status.HostKeyChanged {
				t.Fatalf("VM %d after the key was accepted: %+v", id, r.Reasons)
			}
		}
	}
	// The next connection reaches the host with the accepted key.
	c, err := hostconn.Dial(ctx, hostTarget(got), f.s.sshKey)
	if err != nil {
		t.Fatalf("after accepting: %v", err)
	}
	c.Close()
}

// While the sandbox helper does not answer, the capacity is modelled from the slot count it last reported, and says
// so (ADR-007 C17): not 0, not 1, not the setting. The count survives a restart (it is in the database).
func TestCapacityLastKnownSlots(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	aud := f.tokens[store.RoleAuditor]
	var v CapacityView
	if code := f.do(aud, "GET", "/api/v1/views/capacity", "", &v); code != 200 || v.SlotsSource != slotsNone || v.Slots != 0 || v.SlotsAt != nil {
		t.Fatalf("never reported: %d %s %d %v", code, v.SlotsSource, v.Slots, v.SlotsAt)
	}
	// The helper answers with 2 slots: stored as the last known count.
	dir, err := os.MkdirTemp("", "sb") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "sandbox.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	helper := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sandbox.Capacity{Slots: 2, Running: []sandbox.Running{}, Waiting: []sandbox.Waiting{}})
	})}
	go helper.Serve(ln)
	f.s.sandbox = sandbox.NewClient(sock)
	f.s.sandboxCap.mu.Lock()
	f.s.sandboxCap.at = time.Time{} // the next read asks the helper
	f.s.sandboxCap.mu.Unlock()
	if code := f.do(aud, "GET", "/api/v1/views/capacity", "", &v); code != 200 || v.SlotsSource != slotsSandbox || v.Slots != 2 || v.SlotsAt == nil {
		t.Fatalf("answering: %d %s %d", code, v.SlotsSource, v.Slots)
	}
	last, ok, err := f.s.db.LastSandboxSlots(ctx)
	if err != nil || !ok || last.Slots != 2 {
		t.Fatalf("stored %+v %v %v", last, ok, err)
	}
	// It stops answering: the view models from 2 slots, says last_known and when.
	helper.Close()
	os.Remove(sock)
	f.s.sandboxCap.mu.Lock()
	f.s.sandboxCap.at = time.Time{} // the next read asks the helper
	f.s.sandboxCap.mu.Unlock()
	if code := f.do(aud, "GET", "/api/v1/views/capacity", "", &v); code != 200 || v.Sandbox != nil || v.SandboxError == "" ||
		v.SlotsSource != slotsLastKnown || v.Slots != 2 || v.SlotsAt == nil || !v.SlotsAt.Equal(mustTime(t, last.ReportedAt)) {
		t.Fatalf("down: %d %+v", code, v)
	}
	if v.CapacityS <= 0 {
		t.Fatalf("down: modelled from no slot: %+v", v.Result)
	}
	// The what-if starts from the same count when it changes only a policy, and says when the slots are its own.
	var w CapacityView
	if code := f.do(aud, "POST", "/api/v1/capacity/what-if", fmt.Sprintf(`{"policies":[{"policy_id":%d,"test_interval_s":3600}]}`, f.ids["policies"]), &w); code != 200 ||
		w.SlotsSource != slotsLastKnown || w.Slots != 2 {
		t.Fatalf("what-if while down: %d %s %d", code, w.SlotsSource, w.Slots)
	}
	if code := f.do(aud, "POST", "/api/v1/capacity/what-if", `{"slots":4}`, &w); code != 200 || w.SlotsSource != slotsWhatIf || w.Slots != 4 || w.SlotsAt != nil {
		t.Fatalf("what-if slots: %d %s %d", code, w.SlotsSource, w.Slots)
	}
	// A restarted server (nothing in memory) still knows the count.
	f.s.sandboxCap.mu.Lock()
	f.s.sandboxCap.cap, f.s.sandboxCap.err, f.s.sandboxCap.stored = nil, fmt.Errorf("not answering"), time.Time{}
	f.s.sandboxCap.mu.Unlock()
	sc, err := f.s.slotsNow(ctx)
	if err != nil || sc.source != slotsLastKnown || sc.n != 2 {
		t.Fatalf("after a restart: %+v %v", sc, err)
	}
	var q TestQueueView
	if code := f.do(aud, "GET", "/api/v1/views/test-queue", "", &q); code != 200 || q.SlotsSource != slotsLastKnown || q.Slots != 2 {
		t.Fatalf("test queue: %d %s %d", code, q.SlotsSource, q.Slots)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	x, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}
