package openbao

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/itest"
)

// bao calls the API as root during setup.
func bao(t *testing.T, addr, method, path, body string) {
	t.Helper()
	req, _ := http.NewRequest(method, addr+"/v1/"+path, bytes.NewBufferString(body))
	req.Header.Set("X-Vault-Token", itest.OpenBaoRootToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d", method, path, resp.StatusCode)
	}
}

func TestAppRoleSession(t *testing.T) {
	itest.Require(t)
	addr := itest.StartOpenBao(t)
	bao(t, addr, "POST", "sys/mounts/kv", `{"type":"kv","options":{"version":"2"}}`)
	bao(t, addr, "POST", "kv/data/platform/x", `{"data":{"v":"1"}}`)
	bao(t, addr, "POST", "kv/data/other/y", `{"data":{"v":"2"}}`)
	bao(t, addr, "PUT", "sys/policies/acl/gaia", `{"policy":"path \"kv/data/platform/*\" { capabilities = [\"read\"] }"}`)
	bao(t, addr, "POST", "sys/auth/approle", `{"type":"approle"}`)
	// A token that lives 3 seconds and can be renewed up to 6.
	bao(t, addr, "POST", "auth/approle/role/gaia", `{"token_policies":["gaia"],"token_ttl":"3s","token_max_ttl":"6s"}`)
	bao(t, addr, "POST", "auth/approle/role/gaia/role-id", `{"role_id":"gaia-role"}`)
	bao(t, addr, "POST", "auth/approle/role/gaia/custom-secret-id", `{"secret_id":"gaia-secret"}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Login(ctx, addr, "gaia-role", "gaia-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Client(addr)
	if v, err := c.Field(ctx, "bao://kv/platform/x", "v"); err != nil || v != "1" {
		t.Fatalf("read: %q %v", v, err)
	}
	// The policy is the limit: other paths are not readable.
	if _, err := c.Read(ctx, "bao://kv/other/y"); err == nil {
		t.Error("read outside the policy")
	}
	first := s.Token()
	go s.Keep(ctx)
	// Past the max TTL the session has logged in again and still reads.
	time.Sleep(8 * time.Second)
	if v, err := c.Field(ctx, "bao://kv/platform/x", "v"); err != nil || v != "1" {
		t.Fatalf("read after max TTL: %q %v", v, err)
	}
	if s.Token() == first {
		t.Error("the token never changed past its max TTL")
	}

	if _, err := Login(ctx, addr, "gaia-role", "wrong", nil); err == nil {
		t.Error("login with a wrong secret_id")
	}
}

func TestTransit(t *testing.T) {
	itest.Require(t)
	addr := itest.StartOpenBao(t)
	bao(t, addr, "POST", "sys/mounts/transit", `{"type":"transit"}`)
	bao(t, addr, "POST", "transit/keys/gaia-auth", `{}`)
	c := New(addr, itest.OpenBaoRootToken)
	ctx := context.Background()
	ct, err := c.Encrypt(ctx, "gaia-auth", []byte("JBSWY3DPEHPK3PXP"))
	if err != nil || !strings.HasPrefix(ct, "vault:v1:") {
		t.Fatalf("encrypt: %q %v", ct, err)
	}
	pt, err := c.Decrypt(ctx, "gaia-auth", ct)
	if err != nil || string(pt) != "JBSWY3DPEHPK3PXP" {
		t.Errorf("decrypt: %q %v", pt, err)
	}
	if _, err := c.Decrypt(ctx, "gaia-auth", "vault:v1:bm9wZQ=="); err == nil {
		t.Error("decrypted garbage")
	}
}
