package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Session is Gaia's own login to OpenBao with AppRole (design 13.2): it
// logs in, renews its token before it expires and logs in again when the
// token cannot be renewed any more.
type Session struct {
	addr, mount      string
	roleID, secretID string
	http             *http.Client
	log              *slog.Logger
	mu               sync.RWMutex
	token            string
	expires          time.Time
	renewable        bool
}

// Login logs in with role_id and secret_id at the approle mount.
func Login(ctx context.Context, addr, roleID, secretID string, log *slog.Logger) (*Session, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Session{addr: strings.TrimRight(addr, "/"), mount: "approle", roleID: roleID, secretID: secretID,
		http: &http.Client{Timeout: 30 * time.Second}, log: log}
	return s, s.login(ctx)
}

// Token is the current token.
func (s *Session) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Client returns a KV client that always uses the session's current token.
func (s *Session) Client(addr string) *Client {
	c := New(addr, "")
	c.tokenFn = s.Token
	return c
}

type authResponse struct {
	Auth *struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int    `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
	Errors []string `json:"errors"`
}

func (s *Session) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"role_id": s.roleID, "secret_id": s.secretID})
	a, err := s.post(ctx, "/v1/auth/"+s.mount+"/login", "", body)
	if err != nil {
		return fmt.Errorf("openbao approle login: %w", err)
	}
	s.set(a)
	return nil
}

func (s *Session) renew(ctx context.Context) error {
	a, err := s.post(ctx, "/v1/auth/token/renew-self", s.Token(), []byte("{}"))
	if err != nil {
		return err
	}
	s.set(a)
	return nil
}

func (s *Session) set(a *authResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = a.Auth.ClientToken
	s.renewable = a.Auth.Renewable
	s.expires = time.Now().Add(time.Duration(a.Auth.LeaseDuration) * time.Second)
}

func (s *Session) post(ctx context.Context, path, token string, body []byte) (*authResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.addr+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var a authResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a); err != nil {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || a.Auth == nil {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.Join(a.Errors, "; "))
	}
	return &a, nil
}

// Keep renews the token at two thirds of its lease until ctx ends; when
// renewing fails (the token reached its max TTL, or was revoked) it logs
// in again, retrying every minute.
func (s *Session) Keep(ctx context.Context) {
	for {
		s.mu.RLock()
		wait := time.Until(s.expires) * 2 / 3
		renewable := s.renewable
		s.mu.RUnlock()
		if wait < time.Second {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if renewable {
			if err := s.renew(ctx); err == nil {
				continue
			} else {
				s.log.Warn("openbao: renewing the token failed; logging in again", "err", err)
			}
		}
		for {
			err := s.login(ctx)
			if err == nil {
				break
			}
			s.log.Error("openbao: login failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
		}
	}
}
