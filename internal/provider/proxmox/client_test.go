package proxmox

import (
	"context"
	"net/http"
	"net/http/httptest"
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
