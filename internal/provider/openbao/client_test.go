package openbao

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	m, p, err := ParseRef("bao://kv/platform/proxmox/lab")
	if err != nil || m != "kv" || p != "platform/proxmox/lab" {
		t.Errorf("= %q %q %v", m, p, err)
	}
	for _, bad := range []string{"kv/x", "bao://kv", "bao:///x", "bao://kv/"} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q): no error", bad)
		}
	}
}

func TestRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/kv/data/platform/proxmox/lab":
			_, _ = w.Write([]byte(`{"data":{"data":{"endpoint":"https://pve:8006","token_id":"gaia@pve!poc"},"metadata":{}}}`))
		case "/v1/kv/data/odd":
			_, _ = w.Write([]byte(`{"data":{"data":{"n":1}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c := New(srv.URL+"/", "tok")
	f, err := c.Read(ctx, "bao://kv/platform/proxmox/lab")
	if err != nil || f["endpoint"] != "https://pve:8006" || f["token_id"] != "gaia@pve!poc" {
		t.Errorf("Read = %v, %v", f, err)
	}
	if v, err := c.Field(ctx, "bao://kv/platform/proxmox/lab", "token_id"); err != nil || v != "gaia@pve!poc" {
		t.Errorf("Field = %q, %v", v, err)
	}
	for ref, want := range map[string]string{
		"bao://kv/missing":              "no such secret",
		"bao://kv/odd":                  "field n is not a string",
		"bao://kv/platform/proxmox/lab": `no field "nope"`,
	} {
		_, err := c.Field(ctx, ref, "nope")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", ref, err, want)
		}
	}
	if _, err := New(srv.URL, "wrong").Read(ctx, "bao://kv/platform/proxmox/lab"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("wrong token: %v", err)
	}
}
