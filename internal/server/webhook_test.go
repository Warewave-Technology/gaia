package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubWebhookFetchesNow(t *testing.T) {
	cat := &CatalogSource{}
	srv := &Server{cfg: Config{WebhookSecret: []byte("s3cret"), Catalog: cat}, mux: http.NewServeMux()}
	srv.cfg.Log = discard()
	srv.routes()
	hs := httptest.NewServer(srv.mux)
	defer hs.Close()

	post := func(event, sig string, body []byte) int {
		req, _ := http.NewRequest("POST", hs.URL+"/api/v1/webhooks/github", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	sign := func(secret string, body []byte) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(body)
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	body := []byte(`{"ref":"refs/heads/main"}`)
	if code := post("push", "", body); code != 401 {
		t.Errorf("unsigned: %d", code)
	}
	if code := post("push", sign("other", body), body); code != 401 {
		t.Errorf("another secret: %d", code)
	}
	if code := post("push", sign("s3cret", body), []byte(`{"ref":"refs/heads/evil"}`)); code != 401 {
		t.Errorf("another body: %d", code)
	}
	if code := post("ping", sign("s3cret", body), body); code != 200 {
		t.Errorf("ping: %d", code)
	}
	select {
	case <-cat.kicked():
		t.Fatal("a ping fetched")
	default:
	}
	if code := post("push", sign("s3cret", body), body); code != 202 {
		t.Errorf("push: %d", code)
	}
	select {
	case <-cat.kicked():
	case <-time.After(time.Second):
		t.Error("a push did not fetch")
	}
	if code := post("issues", sign("s3cret", body), body); code != 204 {
		t.Errorf("another event: %d", code)
	}

	// Without a secret there is no webhook.
	off := &Server{cfg: Config{Catalog: cat, Log: discard()}, mux: http.NewServeMux()}
	off.routes()
	rec := httptest.NewRecorder()
	off.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/webhooks/github", bytes.NewReader(body)))
	if rec.Code != 404 {
		t.Errorf("no secret: %d", rec.Code)
	}
}

// kicked is the channel Kick fills, for tests.
func (c *CatalogSource) kicked() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kick == nil {
		c.kick = make(chan struct{}, 1)
	}
	return c.kick
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
