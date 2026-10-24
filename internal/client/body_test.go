package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostAndPutSendTheBody(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "no body", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"method": r.Method, "repo": in["repo"]})
	}))
	defer hs.Close()
	c := New(hs.URL)
	for _, m := range []string{"POST", "PUT"} {
		var out map[string]string
		send := c.Post
		if m == "PUT" {
			send = c.Put
		}
		if err := send(t.Context(), "/x", map[string]string{"repo": "platform"}, &out); err != nil || out["repo"] != "platform" || out["method"] != m {
			t.Errorf("%s: %v %v", m, out, err)
		}
	}
}
