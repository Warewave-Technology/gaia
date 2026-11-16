// Package server is `gaia server` (ADR-007 §1): one process with the REST/JSON API (§6), local users and roles
// (§8), the append-only audit log of user actions, and the daily backup of Gaia's own state (§2). Workers that back
// up and test VMs (T15, T16) build on it. Every route, with the roles allowed on it and its request and response
// types, is declared once in routes.go; the HTTP mux, the authorisation check, the audit log, the OpenAPI document
// and the role × endpoint test all come from that table.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/kopia/kopia/repo/maintenance"

	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Config is /etc/gaia/server.json.
type Config struct {
	Listen                string `json:"listen"`                  // e.g. ":8443"
	DataDir               string `json:"data_dir"`                // e.g. /var/lib/gaia
	TLSCert               string `json:"tls_cert"`                // PEM; default <data_dir>/tls/cert.pem
	TLSKey                string `json:"tls_key"`                 // PEM; default <data_dir>/tls/key.pem
	SessionTTL            string `json:"session_ttl"`             // Go duration, default 12h
	StateBackupAt         string `json:"state_backup_at"`         // HH:MM local time, default 02:30
	StateBackupRepository string `json:"state_backup_repository"` // repository name; "" = the first repository
	// Backups (T15).
	HostSocketDir      string  `json:"host_socket_dir"`      // libvirt's QEMU state dir on a host (the NBD socket goes into the VM's domain-<id>-<name> there); default /var/lib/libvirt/qemu
	HostScratchDir     string  `json:"host_scratch_dir"`     // qemu's copy-before-write scratch files on a host; default /var/lib/libvirt/images
	NBDConnections     int     `json:"nbd_connections"`      // per disk, default 2
	MaxParallelBackups int     `json:"max_parallel_backups"` // default 2
	VerifyAt           string  `json:"verify_at"`            // HH:MM local time of the daily sampled verification, default 03:30
	FullVerifyDay      string  `json:"full_verify_day"`      // weekday of the weekly full verification, default sunday
	VerifySample       float64 `json:"verify_sample"`        // fraction of each point's blocks the sampled verification reads, default 0.02
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// LoadConfig reads and checks a config file; unknown fields are an error.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.resolve()
}

func (c *Config) resolve() error {
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	if c.SessionTTL == "" {
		c.SessionTTL = "12h"
	}
	if c.StateBackupAt == "" {
		c.StateBackupAt = "02:30"
	}
	switch {
	case c.DataDir == "" || !filepath.IsAbs(c.DataDir):
		return fmt.Errorf("data_dir %q: an absolute path is required", c.DataDir)
	case !hhmm.MatchString(c.StateBackupAt):
		return fmt.Errorf("state_backup_at %q: want HH:MM", c.StateBackupAt)
	}
	if ttl, err := time.ParseDuration(c.SessionTTL); err != nil || ttl < time.Minute {
		return fmt.Errorf("session_ttl %q: want a duration of at least 1m", c.SessionTTL)
	}
	if c.HostSocketDir == "" {
		c.HostSocketDir = "/var/lib/libvirt/qemu"
	}
	if c.HostScratchDir == "" {
		c.HostScratchDir = "/var/lib/libvirt/images"
	}
	if c.NBDConnections == 0 {
		c.NBDConnections = 2
	}
	if c.MaxParallelBackups == 0 {
		c.MaxParallelBackups = 2
	}
	if c.VerifyAt == "" {
		c.VerifyAt = "03:30"
	}
	if c.FullVerifyDay == "" {
		c.FullVerifyDay = "sunday"
	}
	if c.VerifySample == 0 {
		c.VerifySample = 0.02
	}
	switch {
	case !filepath.IsAbs(c.HostSocketDir) || !filepath.IsAbs(c.HostScratchDir):
		return fmt.Errorf("host_socket_dir and host_scratch_dir must be absolute paths")
	case c.NBDConnections < 1 || c.NBDConnections > 16 || c.MaxParallelBackups < 1 || c.MaxParallelBackups > 16:
		return fmt.Errorf("nbd_connections and max_parallel_backups: 1-16")
	case !hhmm.MatchString(c.VerifyAt):
		return fmt.Errorf("verify_at %q: want HH:MM", c.VerifyAt)
	case weekday(c.FullVerifyDay) < 0:
		return fmt.Errorf("full_verify_day %q: a weekday, e.g. sunday", c.FullVerifyDay)
	case c.VerifySample <= 0 || c.VerifySample > 1:
		return fmt.Errorf("verify_sample %v: a fraction in (0, 1]", c.VerifySample)
	}
	if c.TLSCert == "" {
		c.TLSCert = filepath.Join(c.DataDir, "tls", "cert.pem")
	}
	if c.TLSKey == "" {
		c.TLSKey = filepath.Join(c.DataDir, "tls", "key.pem")
	}
	return nil
}

// Paths under the data directory.
func (c Config) dbPath() string         { return filepath.Join(c.DataDir, "gaia.db") }
func (c Config) keyPath() string        { return filepath.Join(c.DataDir, "keys", "evidence-key.pem") }
func (c Config) pubPath() string        { return filepath.Join(c.DataDir, "keys", "evidence-key.pub") }
func (c Config) ledgerPath() string     { return filepath.Join(c.DataDir, "evidence", "ledger.jsonl") }
func (c Config) bundlesDir() string     { return filepath.Join(c.DataDir, "evidence", "bundles") }
func (c Config) secretsDir() string     { return filepath.Join(c.DataDir, "secrets") }
func (c Config) kopiaDir() string       { return filepath.Join(c.DataDir, "kopia") }
func (c Config) cacheDir() string       { return filepath.Join(c.DataDir, "cache") }
func (c Config) tmpDir() string         { return filepath.Join(c.DataDir, "tmp") }
func (c Config) sshKeyPath() string     { return filepath.Join(c.DataDir, "keys", "ssh-key") }
func (c Config) ttl() time.Duration     { d, _ := time.ParseDuration(c.SessionTTL); return d }
func (c Config) DBPath() string         { return c.dbPath() }
func (c Config) EvidenceKey() string    { return c.keyPath() }
func (c Config) EvidencePub() string    { return c.pubPath() }
func (c Config) EvidenceLedger() string { return c.ledgerPath() }

// Server is a running gaia server.
type Server struct {
	cfg       Config
	cfgPath   string
	db        *store.DB
	log       *slog.Logger
	routes    []route
	handler   http.Handler
	started   time.Time
	backupMu  sync.Mutex
	version   string
	openapi   []byte
	sshKey    hostconn.Key
	repoLocks repoLocks
	// Kopia's full safety (24 h before an unreferenced content may go); tests use none.
	maintenanceSafety maintenance.SafetyParameters
	jobsWG            sync.WaitGroup
	running           sync.Map // VM ID -> ID of its running backup job
}

func weekday(s string) time.Weekday {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(d.String(), s) {
			return d
		}
	}
	return -1
}

// New opens the state database, migrates it to the latest schema and builds the API.
func New(ctx context.Context, cfg Config, cfgPath string, log *slog.Logger) (*Server, error) {
	if err := cfg.resolve(); err != nil {
		return nil, err
	}
	db, err := store.Open(cfg.dbPath())
	if err != nil {
		return nil, err
	}
	done, err := db.Migrate(ctx, 0)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("state database: %w", err)
	}
	if len(done) > 0 {
		log.Info("migrated the state database", "versions", done, "schema", store.Latest())
	}
	s := &Server{cfg: cfg, cfgPath: cfgPath, db: db, log: log, started: time.Now().UTC(), version: buildVersion(),
		maintenanceSafety: maintenance.SafetyFull}
	host, _ := os.Hostname()
	if s.sshKey, err = hostconn.LoadOrCreateKey(cfg.sshKeyPath(), "gaia@"+host); err != nil {
		db.Close()
		return nil, fmt.Errorf("SSH key: %w", err)
	}
	if n, err := db.FailInterruptedJobs(ctx, "the server stopped while the job ran"); err != nil {
		db.Close()
		return nil, err
	} else if n > 0 {
		log.Warn("jobs interrupted by the last stop were marked failed", "count", n)
	}
	s.routes = s.table()
	if s.openapi, err = OpenAPI(s.routes, s.version); err != nil {
		db.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	for i := range s.routes {
		rt := &s.routes[i]
		mux.Handle(rt.method+" "+rt.path, s.serve(rt))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "no such endpoint: " + r.Method + " " + r.URL.Path, Code: "not_found"})
	})
	s.handler = mux
	return s, nil
}

// DB is the state database (for `gaia server` subcommands and tests).
func (s *Server) DB() *store.DB { return s.db }

// Handler is the HTTP API.
func (s *Server) Handler() http.Handler { return s.handler }

// Close waits for background work (backups, verifications) and closes the state database.
func (s *Server) Close() error {
	s.jobsWG.Wait()
	return s.db.Close()
}

func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, mod := "", ""
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			mod = kv.Value
		}
	}
	if rev == "" {
		return "devel"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if mod == "true" {
		rev += "+modified"
	}
	return rev
}

// Run serves the API over TLS on cfg.Listen and runs the daily state backup until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
	if err != nil {
		return fmt.Errorf("TLS certificate: %w", err)
	}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 2 * time.Minute,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}},
		ErrorLog:  slog.NewLogLogger(s.log.Handler(), slog.LevelWarn)}
	errc := make(chan error, 1)
	go func() { errc <- hs.ServeTLS(ln, "", "") }()
	go s.scheduleStateBackups(ctx)
	go s.schedule(ctx)
	s.log.Info("gaia server is listening", "address", ln.Addr().String(), "version", s.version, "schema", store.Latest())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.log.Info("shutting down")
		err := hs.Shutdown(sctx)
		s.jobsWG.Wait() // running backups end their libvirt jobs before the process goes
		return err
	}
}

// --- requests ---

// apiError is the body of every error response.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"` // unauthorized, forbidden, not_found, conflict, invalid, not_implemented, internal, unavailable
}

var errNotImplemented = errors.New("not implemented in this version")

// call is one request being served.
type call struct {
	w       http.ResponseWriter
	r       *http.Request
	user    store.User
	session store.Session
	target  string // for the audit log: what the action was about
	detail  string // for the audit log: what happened (never a secret)
	raw     bool   // the handler wrote the response itself
}

func (c *call) id(name string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(c.r.PathValue(name), "%d", &id); err != nil || id < 1 || fmt.Sprint(id) != c.r.PathValue(name) {
		return 0, fmt.Errorf("%w: %s %q is not an ID", store.ErrInvalid, name, c.r.PathValue(name))
	}
	return id, nil
}

func (c *call) intQuery(name string, def, max int64) (int64, error) {
	v := c.r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 || fmt.Sprint(n) != v || (max > 0 && n > max) {
		return 0, fmt.Errorf("%w: query %s=%q", store.ErrInvalid, name, v)
	}
	return n, nil
}

// decode reads a JSON body of at most 1 MiB into v; unknown fields and trailing data are errors.
func (c *call) decode(v any) error {
	dec := json.NewDecoder(io.LimitReader(c.r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: request body: %v", store.ErrInvalid, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: request body: data after the JSON object", store.ErrInvalid)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// statusOf maps an error to its HTTP status and code.
func statusOf(err error) (int, string) {
	switch {
	case errors.Is(err, errUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, store.ErrInvalid):
		return http.StatusBadRequest, "invalid"
	case errors.Is(err, errNotImplemented):
		return http.StatusNotImplemented, "not_implemented"
	case errors.Is(err, errHost):
		return http.StatusBadGateway, "host"
	}
	return http.StatusInternalServerError, "internal"
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func remote(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// serve wraps a route: authentication, the role check, the audit log, and the response.
func (s *Server) serve(rt *route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c := &call{w: w, r: r, target: r.URL.Path}
		if rt.roles != nil {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				writeJSON(w, http.StatusUnauthorized, apiError{Error: "log in first (Authorization: Bearer <token>)", Code: "unauthorized"})
				return
			}
			u, sess, err := s.db.SessionUser(ctx, token)
			if errors.Is(err, store.ErrNoSession) {
				writeJSON(w, http.StatusUnauthorized, apiError{Error: "the session is not valid (expired, logged out, or the user is disabled)", Code: "unauthorized"})
				return
			}
			if err != nil {
				s.log.Error("session lookup", "error", err)
				writeJSON(w, http.StatusInternalServerError, apiError{Error: "session lookup failed", Code: "internal"})
				return
			}
			c.user, c.session = u, sess
			if !allowed(rt.roles, u.Role) {
				s.audit(ctx, c, rt, http.StatusForbidden, "role "+u.Role+" is not allowed")
				writeJSON(w, http.StatusForbidden, apiError{Error: fmt.Sprintf("role %s may not %s %s", u.Role, rt.method, rt.path), Code: "forbidden"})
				return
			}
		}
		audited := rt.audited()
		// An action that cannot be recorded is not done: the attempt goes to the audit log first.
		if audited && rt.roles != nil {
			if err := s.audit(ctx, c, rt, 0, "attempt"); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "the audit log cannot be written, so the action was not done", Code: "unavailable"})
				return
			}
		}
		rec := &recorder{ResponseWriter: w}
		c.w = rec
		status, body, err := rt.h(c)
		switch {
		case err != nil:
			code, name := statusOf(err)
			if code == http.StatusInternalServerError {
				s.log.Error("request failed", "route", rt.method+" "+rt.path, "user", c.user.Username, "error", err)
			}
			msg := err.Error()
			for _, e := range []error{store.ErrInvalid, store.ErrConflict, store.ErrNotFound, errUnauthorized, errNotImplemented, errHost} {
				msg = strings.TrimPrefix(msg, e.Error()+": ") // the code says it already
			}
			if code == http.StatusInternalServerError {
				msg = "internal error (details in the server log)"
			}
			c.detail = strings.TrimSpace(c.detail + " " + err.Error())
			writeJSON(rec, code, apiError{Error: msg, Code: name})
		case c.raw:
		case body == nil:
			rec.WriteHeader(status)
		default:
			writeJSON(rec, status, body)
		}
		if audited {
			if err := s.audit(ctx, c, rt, rec.status, c.detail); err != nil {
				s.log.Error("the audit log could not record an outcome", "route", rt.method+" "+rt.path, "user", c.user.Username, "status", rec.status, "error", err)
			}
		}
	})
}

func allowed(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

func (s *Server) audit(ctx context.Context, c *call, rt *route, outcome int, detail string) error {
	e := store.AuditEntry{Username: c.user.Username, Role: c.user.Role, Action: rt.method + " " + rt.path, Target: c.target,
		Outcome: outcome, RemoteAddr: remote(c.r), Detail: detail}
	if c.user.ID != 0 {
		id := c.user.ID
		e.UserID = &id
	}
	_, err := s.db.AppendAudit(ctx, e)
	return err
}
