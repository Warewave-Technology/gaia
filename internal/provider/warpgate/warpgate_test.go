package warpgate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/itest"
)

func hostKey(t *testing.T) ssh.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWarpgate(t *testing.T) {
	itest.Require(t)
	wg := itest.StartWarpgate(t)
	ctx := context.Background()
	c, err := New(wg.URL, wg.Token, true, nil)
	if err != nil {
		t.Fatal(err)
	}

	keys, err := c.OwnKeys(ctx)
	if err != nil || len(keys) == 0 || !strings.HasPrefix(keys[0], "ssh-") {
		t.Fatalf("own keys = %v %v", keys, err)
	}

	ssh1 := Target{Name: "labtpoc01", Description: "Managed by Gaia. gaia.uid=u1", Kind: "ssh", Host: "10.50.0.100", Port: 22, Username: "gaia-wg"}
	res, err := c.EnsureTarget(ctx, ssh1, "", "gaia.uid=u1")
	if err != nil || res.Action != "create" || res.ID == "" {
		t.Fatalf("create = %+v %v", res, err)
	}
	id := res.ID
	if res, err = c.EnsureTarget(ctx, ssh1, id, "gaia.uid=u1"); err != nil || res != (Result{ID: id, Action: "same"}) {
		t.Errorf("again = %+v %v", res, err)
	}
	// Without the recorded ID, the mark finds it.
	if res, err = c.EnsureTarget(ctx, ssh1, "", "gaia.uid=u1"); err != nil || res.ID != id || res.Action != "same" {
		t.Errorf("by mark = %+v %v", res, err)
	}
	if _, err = c.EnsureTarget(ctx, ssh1, "", "gaia.uid=u2"); !errors.Is(err, ErrTaken) {
		t.Errorf("another server's name: %v", err)
	}
	moved := ssh1
	moved.Host = "10.50.0.101"
	if res, err = c.EnsureTarget(ctx, moved, id, "gaia.uid=u1"); err != nil || res.Action != "update" {
		t.Errorf("update = %+v %v", res, err)
	}
	rdp := Target{Name: "labwin01", Description: "gaia.uid=w1", Kind: "rdp", Host: "10.50.0.110", Port: 3389}
	rres, err := c.EnsureTarget(ctx, rdp, "", "gaia.uid=w1")
	if err != nil || rres.Action != "create" {
		t.Fatalf("rdp = %+v %v", rres, err)
	}
	if again, err := c.EnsureTarget(ctx, rdp, rres.ID, "gaia.uid=w1"); err != nil || again.Action != "same" {
		t.Errorf("rdp again = %+v %v", again, err)
	}

	role, err := c.EnsureRole(ctx, "gaia/poc-web")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := c.EnsureRole(ctx, "gaia/poc-web"); err != nil || again != role {
		t.Errorf("role again = %s %v", again, err)
	}
	other, _ := c.EnsureRole(ctx, "operators") // not Gaia's
	if changed, err := c.SetTargetRoles(ctx, id, []string{role, other}); err != nil || len(changed) != 2 {
		t.Errorf("target roles = %v %v", changed, err)
	}
	if changed, err := c.SetTargetRoles(ctx, id, nil); err != nil || !slices.Equal(changed, []string{"-gaia/poc-web"}) {
		t.Errorf("roles taken back = %v %v (operators stays)", changed, err)
	}
	if roles, err := c.Roles(ctx); err != nil || len(roles) != 1 || roles[0].Name != "gaia/poc-web" {
		t.Errorf("Gaia's roles = %+v %v", roles, err)
	}

	users, err := c.Users(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "admin" {
		t.Fatalf("users = %+v %v", users, err)
	}
	if added, removed, err := c.SetRoleUsers(ctx, role, users); err != nil || !slices.Equal(added, []string{"admin"}) || removed != nil {
		t.Errorf("add = %v %v %v", added, removed, err)
	}
	if added, removed, err := c.SetRoleUsers(ctx, role, nil); err != nil || added != nil || !slices.Equal(removed, []string{"admin"}) {
		t.Errorf("remove = %v %v %v", added, removed, err)
	}

	k1, k2 := hostKey(t), hostKey(t)
	for i, want := range []bool{true, false} {
		if changed, err := c.SetHostKeys(ctx, "10.50.0.100", 22, []ssh.PublicKey{k1}); err != nil || changed != want {
			t.Errorf("host keys #%d = %v %v", i, changed, err)
		}
	}
	if changed, err := c.SetHostKeys(ctx, "10.50.0.100", 22, []ssh.PublicKey{k2}); err != nil || !changed {
		t.Errorf("another server on the address = %v %v", changed, err)
	}
	if changed, _ := c.SetHostKeys(ctx, "10.50.0.100", 22, nil); !changed {
		t.Error("keys not removed")
	}

	if gone, err := c.DeleteTarget(ctx, id); err != nil || !gone {
		t.Errorf("delete = %v %v", gone, err)
	}
	if gone, err := c.DeleteTarget(ctx, id); err != nil || gone {
		t.Errorf("delete again = %v %v", gone, err)
	}
}
