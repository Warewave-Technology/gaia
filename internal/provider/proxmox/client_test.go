package proxmox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUsedAndFreeVMIDs(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=gaia@pve!poc=s3cret" || r.URL.Path != "/api2/json/cluster/resources" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"vmid":9300},{"vmid":9400},{"vmid":9402}]}`))
	}))
	defer srv.Close()

	c := New(Credentials{Endpoint: srv.URL, TokenID: "gaia@pve!poc", TokenSecret: "s3cret", Insecure: true})
	used, err := c.UsedVMIDs(context.Background())
	if err != nil || len(used) != 3 || !used[9402] {
		t.Fatalf("UsedVMIDs = %v, %v", used, err)
	}
	if id, _ := FreeVMID(used, nil, 9400, 9499); id != 9401 {
		t.Errorf("FreeVMID = %d, want 9401", id)
	}
	if id, _ := FreeVMID(used, map[int]bool{9401: true}, 9400, 9499); id != 9403 {
		t.Errorf("FreeVMID with taken = %d, want 9403", id)
	}
	if _, err := FreeVMID(used, nil, 9400, 9400); err == nil {
		t.Error("full range: no error")
	}

	bad := New(Credentials{Endpoint: srv.URL, TokenID: "gaia@pve!poc", TokenSecret: "wrong", Insecure: true})
	if _, err := bad.UsedVMIDs(context.Background()); err == nil {
		t.Error("wrong token: no error")
	}
}

func TestSnapshotWaitsForItsTask(t *testing.T) {
	var posted url.Values
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[{"vmid":9401,"node":"pve2"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve2/qemu/9401/snapshot":
			_ = r.ParseForm()
			posted = r.PostForm
			_, _ = w.Write([]byte(`{"data":"UPID:pve2:1:2:3:qmsnapshot:9401:root@pam:"}`))
		case strings.HasPrefix(r.URL.Path, "/api2/json/nodes/pve2/tasks/"):
			polls++
			if polls < 2 {
				_, _ = w.Write([]byte(`{"data":{"status":"running"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(Credentials{Endpoint: srv.URL, TokenID: "gaia@pve!t", TokenSecret: "s"})
	if err := c.Snapshot(context.Background(), 9401, SnapshotName("gaia-patch-2026-11"), "Gaia"); err != nil {
		t.Fatal(err)
	}
	if posted.Get("snapname") != "gaia_patch_2026_11" || posted.Get("vmstate") != "0" || polls != 2 {
		t.Errorf("posted %v after %d polls", posted, polls)
	}
	if err := c.Snapshot(context.Background(), 1, "x", ""); err == nil {
		t.Error("a VM that is not there")
	}
	if got := SnapshotName("1-a"); got != "g1_a" {
		t.Errorf("SnapshotName = %s", got)
	}
}
