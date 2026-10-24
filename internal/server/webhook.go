package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// githubWebhook takes GitHub's push events (design 7.6): a valid one
// fetches the repositories now instead of at the next refresh. The event's
// content is not trusted beyond that: Gaia reads the commits from Git.
func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.WebhookSecret) == 0 || s.cfg.Catalog == nil {
		http.NotFound(w, r)
		return
	}
	// GitHub sends at most 25 MB.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25<<20))
	if err != nil {
		badRequest(w, err)
		return
	}
	if !validSignature(s.cfg.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeJSON(w, http.StatusUnauthorized, Problem{Error: "the signature does not match the webhook secret"})
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	s.cfg.Log.Info("webhook", "event", event, "delivery", r.Header.Get("X-GitHub-Delivery"))
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "push":
		s.cfg.Catalog.Kick()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "fetching"})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// validSignature checks X-Hub-Signature-256: sha256=HMAC(secret, body).
func validSignature(secret, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(sig, m.Sum(nil))
}
