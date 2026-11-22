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
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/kopia/kopia/repo/maintenance"

	"github.com/Warewave-Technology/gaia/internal/buildinfo"
	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Config is /etc/gaia/server.json.
type Config struct {
	Listen  string `json:"listen"`   // e.g. ":8443"
	DataDir string `json:"data_dir"` // e.g. /var/lib/gaia
	TLSCert string `json:"tls_cert"` // PEM; default <data_dir>/tls/cert.pem
	TLSKey  string `json:"tls_key"`  // PEM; default <data_dir>/tls/key.pem
	// PublicURL is how browsers and the CLI reach this server, https://NAME[:PORT]: the WebAuthn relying party (its host
	// name; WebAuthn refuses IP addresses) and the OIDC redirect. Default https://<host name>:<listen port>.
	PublicURL             string `json:"public_url"`
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
	// Restore tests (T16).
	SandboxSocket    string `json:"sandbox_socket"`     // the sandbox helper (gaia sandbox serve); default /run/gaia-sandbox/sandbox.sock
	SandboxImagesDir string `json:"sandbox_images_dir"` // where restore tests restore disks for the helper; default /var/lib/gaia-sandbox/images
	// Replaced by ADR-008 (0.2.0): sessions and failed sign-ins have fixed limits and settings (signin.go). A config
	// written by an earlier release still has them; they are read and ignored, with a warning at start.
	SessionTTL           string `json:"session_ttl,omitempty"`
	LoginMaxFailures     int    `json:"login_max_failures,omitempty"`
	LoginLockout         string `json:"login_lockout,omitempty"`
	LoginAddressFailures int    `json:"login_address_failures,omitempty"`
	LoginAddressWindow   string `json:"login_address_window,omitempty"`
	// Notifications (T19, notify.go, report.go).
	DailyReportAt string `json:"daily_report_at"` // HH:MM local time of the daily report; default 07:00
	// Migration checks (T21, migration.go): the converter service (gaia convert serve) and its job directories.
	ConvertSocket  string `json:"convert_socket"`   // default /run/gaia-convert/convert.sock
	ConvertJobsDir string `json:"convert_jobs_dir"` // default /var/lib/gaia-convert/jobs
	// Stopping (T22): how long a stop waits for running jobs before it ends them (gaia.service's TimeoutStopSec must
	// be longer). Go duration, default 10m; 0s ends them at once.
	StopDrain string `json:"stop_drain"`
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
	if c.StateBackupAt == "" {
		c.StateBackupAt = "02:30"
	}
	switch {
	case c.DataDir == "" || !filepath.IsAbs(c.DataDir):
		return fmt.Errorf("data_dir %q: an absolute path is required", c.DataDir)
	case !hhmm.MatchString(c.StateBackupAt):
		return fmt.Errorf("state_backup_at %q: want HH:MM", c.StateBackupAt)
	}
	if c.DailyReportAt == "" {
		c.DailyReportAt = "07:00"
	}
	if !hhmm.MatchString(c.DailyReportAt) {
		return fmt.Errorf("daily_report_at %q: want HH:MM", c.DailyReportAt)
	}
	if err := c.resolvePublicURL(); err != nil {
		return err
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
	if c.SandboxSocket == "" {
		c.SandboxSocket = "/run/gaia-sandbox/sandbox.sock"
	}
	if c.SandboxImagesDir == "" {
		c.SandboxImagesDir = "/var/lib/gaia-sandbox/images"
	}
	if c.ConvertSocket == "" {
		c.ConvertSocket = "/run/gaia-convert/convert.sock"
	}
	if c.ConvertJobsDir == "" {
		c.ConvertJobsDir = "/var/lib/gaia-convert/jobs"
	}
	if c.StopDrain == "" {
		c.StopDrain = "10m"
	}
	if d, err := time.ParseDuration(c.StopDrain); err != nil || d < 0 || d > time.Hour {
		return fmt.Errorf("stop_drain %q: want a duration from 0s to 1h", c.StopDrain)
	}
	switch {
	case !filepath.IsAbs(c.HostSocketDir) || !filepath.IsAbs(c.HostScratchDir):
		return fmt.Errorf("host_socket_dir and host_scratch_dir must be absolute paths")
	case !filepath.IsAbs(c.SandboxSocket) || !filepath.IsAbs(c.SandboxImagesDir):
		return fmt.Errorf("sandbox_socket and sandbox_images_dir must be absolute paths")
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
func (c Config) dbPath() string           { return filepath.Join(c.DataDir, "gaia.db") }
func (c Config) keyPath() string          { return filepath.Join(c.DataDir, "keys", "evidence-key.pem") }
func (c Config) pubPath() string          { return filepath.Join(c.DataDir, "keys", "evidence-key.pub") }
func (c Config) ledgerPath() string       { return filepath.Join(c.DataDir, "evidence", "ledger.jsonl") }
func (c Config) bundlesDir() string       { return filepath.Join(c.DataDir, "evidence", "bundles") }
func (c Config) secretsDir() string       { return filepath.Join(c.DataDir, "secrets") }
func (c Config) kopiaDir() string         { return filepath.Join(c.DataDir, "kopia") }
func (c Config) cacheDir() string         { return filepath.Join(c.DataDir, "cache") }
func (c Config) tmpDir() string           { return filepath.Join(c.DataDir, "tmp") }
func (c Config) sshKeyPath() string       { return filepath.Join(c.DataDir, "keys", "ssh-key") }
func (c Config) stopDrain() time.Duration { d, _ := time.ParseDuration(c.StopDrain); return d }
func (c Config) DBPath() string           { return c.dbPath() }
func (c Config) EvidenceKey() string      { return c.keyPath() }
func (c Config) EvidencePub() string      { return c.pubPath() }
func (c Config) EvidenceLedger() string   { return c.ledgerPath() }

// Server is a running gaia server.
type Server struct {
	cfg        Config
	cfgPath    string
	db         *store.DB
	log        *slog.Logger
	routes     []route
	sandboxCap sandboxState                     // the sandbox helper's slots (testqueue.go)
	images     imagesReservation                // the running tests' restored disks (testqueue.go)
	capCache   capacityCache                    // the proof capacity's inputs (capacityview.go)
	whatIfRate rateLimiter                      // what-if calls per session (ADR-007 C13)
	capWatch   capacityWatch                    // capacity_short (capacitywatch.go)
	testRun    func(context.Context, store.Job) // runs a restore test job; nil: runTest (tests set it)
	recomputed recomputeState                   // the status table's newest full recompute (statuscache.go)
	handler    http.Handler
	started    time.Time
	backupMu   sync.Mutex
	version    string
	openapi    []byte
	sshKey     hostconn.Key
	repoLocks  repoLocks
	// Kopia's full safety (24 h before an unreferenced content may go); tests use none.
	maintenanceSafety maintenance.SafetyParameters
	// Sign-in (ADR-008): the steps of sign-ins under way, step-ups, enrolments on one's own account, CLI sign-ins
	// through a browser, the OIDC client and the WebAuthn relying party.
	signins     signins
	stepups     stepUps
	enrolling   ownEnrolments
	approvals   cliApprovals
	oidc        *oidcClient
	webauthn    *webauthn.WebAuthn
	webauthnErr error // why webauthn is nil
	instance    string
	jobsWG      sync.WaitGroup
	// The context of jobs (backups, restore tests, verifications, migration checks). It is not the server's: when the
	// server stops, running jobs may end first (stop_drain); stopJobs ends them when the drain is over.
	jobs       context.Context
	stopJobs   context.CancelFunc
	running    sync.Map // VM ID -> ID of its running backup job
	testing    sync.Map // VM ID -> ID of its running restore test job
	migrating  sync.Map // job ID of the running migration check (one at a time)
	sandbox    *sandbox.Client
	now        func() time.Time // the clock of the status model
	kitMu      sync.Mutex       // one recovery kit write at a time
	stateDirty chan struct{}    // a job ended: back Gaia's state up soon
	testFreed  chan struct{}    // a restore test ended: its slot goes to the next queued test now (testqueue.go)
	ledgerMu   sync.Mutex       // one ledger check at a time
	reportMu   sync.Mutex       // one daily report at a time
	// Set by gaia server restore-state: the lost server may have made checkpoints after its last state backup with
	// the job IDs this one issues again, so names made on hosts after a rebuild carry the rebuild's tag.
	restoreTag string
}

// loadSSHKey is the key Gaia logs in to hosts with (created on first use).
func loadSSHKey(cfg Config) (hostconn.Key, error) {
	host, _ := os.Hostname()
	return hostconn.LoadOrCreateKey(cfg.sshKeyPath(), "gaia@"+host)
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
		maintenanceSafety: maintenance.SafetyFull, sandbox: sandbox.NewClient(cfg.SandboxSocket), now: time.Now,
		stateDirty: make(chan struct{}, 1), testFreed: make(chan struct{}, 1)}
	s.jobs, s.stopJobs = context.WithCancel(context.WithoutCancel(ctx))
	s.instance = instanceName()
	db.SetInstance(s.instance)
	s.signins.m, s.stepups.m, s.enrolling.m, s.approvals.m = map[string]*pendingSignin{}, map[string]*stepUp{}, map[int64]*pendingSignin{}, map[string]*cliApproval{}
	s.oidc = newOIDCClient(s)
	// A public_url WebAuthn cannot use (an upgraded server whose host name has no domain) must not stop backups and
	// restore tests: the server starts, security keys are refused with the reason, and gaia system shows it.
	if s.webauthn, err = newWebAuthn(cfg, s.instance); err != nil {
		s.webauthnErr = fmt.Errorf("%w: public_url %s cannot be WebAuthn's relying party (%v): set public_url in %s to https://NAME:PORT with a DNS name and restart gaia (install.md step 12)",
			errWebAuthnOff, cfg.PublicURL, err, cfgPath)
		s.webauthn = nil
		log.Error("security keys are not available", "error", s.webauthnErr)
	}
	if cfg.SessionTTL != "" || cfg.LoginMaxFailures != 0 || cfg.LoginLockout != "" || cfg.LoginAddressFailures != 0 || cfg.LoginAddressWindow != "" {
		log.Warn("server.json: session_ttl and login_* are ignored since 0.2.0: sessions and failed sign-ins follow ADR-008 (gaia settings)", "config", cfgPath)
	}
	if s.sshKey, err = loadSSHKey(cfg); err != nil {
		db.Close()
		return nil, fmt.Errorf("SSH key: %w", err)
	}
	if _, err := db.FailInterruptedStateBackups(ctx, "system", "the server stopped while it ran"); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.finishInterrupted(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if n, err := db.FinishInterruptedMigrationChecks(ctx, "not checked: the server stopped while the check ran"); err != nil {
		db.Close()
		return nil, err
	} else if n > 0 {
		log.Warn("migration checks interrupted by the last stop are UNKNOWN", "count", n)
	}
	if s.restoreTag, err = db.RestoreTag(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if sum, err := kitSummary(ctx, cfg, db); err != nil {
		db.Close()
		return nil, err
	} else if sum.State != "confirmed" {
		log.Warn("recovery kit: "+sum.Text, "state", sum.State)
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
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "no such endpoint: " + r.Method + " " + r.URL.Path, Code: "not_found"})
	})
	mux.Handle("/", uiHandler()) // the web UI (web/dist, T20)
	// A request a browser sends from another site (Sec-Fetch-Site / Origin) cannot change anything: the session
	// cookie is SameSite=Strict as well, and the CLI sends neither header.
	s.handler = http.NewCrossOriginProtection().Handler(mux)
	return s, nil
}

// DB is the state database (for `gaia server` subcommands and tests).
func (s *Server) DB() *store.DB { return s.db }

// Handler is the HTTP API.
func (s *Server) Handler() http.Handler { return s.handler }

// Close waits for background work (backups, verifications) and closes the state database.
func (s *Server) Close() error {
	s.stopJobs()
	s.jobsWG.Wait()
	return s.db.Close()
}

func buildVersion() string { return buildinfo.String() }

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
	s.jobsWG.Add(1)
	go func() {
		defer s.jobsWG.Done()
		s.backupStateAfterChanges(ctx)
	}()
	go s.schedule(ctx)
	go s.notifyLoop(ctx)
	go s.sessionSweep(ctx)
	s.log.Info("gaia server is listening", "address", ln.Addr().String(), "version", s.version, "schema", store.Latest())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.log.Info("shutting down")
		err := hs.Shutdown(sctx)
		s.drain(s.cfg.stopDrain())
		return err
	}
}

// drain waits up to d for running jobs to end on their own (the scheduler starts none any more), then ends the rest:
// a job ended so is failed, and the next start queues it again (finishInterrupted). Running backups end their
// libvirt jobs before the process goes.
func (s *Server) drain(d time.Duration) {
	done := make(chan struct{})
	go func() {
		s.jobsWG.Wait()
		close(done)
	}()
	running := s.runningJobs()
	if running > 0 {
		s.log.Info("waiting for running jobs to end", "jobs", running, "stop_drain", d.String())
	}
	select {
	case <-done:
	case <-time.After(d):
		s.log.Warn("stop_drain is over: ending the running jobs", "jobs", s.runningJobs())
	}
	s.stopJobs()
	<-done
}

// runningJobs counts the jobs this process runs now.
func (s *Server) runningJobs() int {
	n := 0
	for _, m := range []*sync.Map{&s.running, &s.testing, &s.migrating} {
		m.Range(func(_, _ any) bool { n++; return true })
	}
	return n
}

// retryReason starts the reason of a job queued again after a stop; such a job is not queued again a second time,
// so a job that stops the server cannot restart it for ever.
const retryReason = "retry of job "

// retryIfStopped queues a backup or restore test again that failed because a stop ended it (the drain was over), so
// the next start runs it; finishInterrupted does the same for a job the stop did not even let record its end.
func (s *Server) retryIfStopped(j store.Job) {
	if s.jobs.Err() == nil || j.VMID == nil || strings.HasPrefix(j.Reason, retryReason) {
		return
	}
	ctx := context.Background()
	got, err := s.db.Job(ctx, j.ID)
	if err != nil {
		s.log.Error("retry after the stop", "job", j.ID, "error", err)
		return
	}
	if got.State != "failed" {
		return
	}
	if _, open, err := s.db.OpenJob(ctx, *j.VMID, j.Kind); err != nil {
		s.log.Error("retry after the stop", "job", j.ID, "error", err)
		return
	} else if open {
		return
	}
	r, err := s.db.CreateJob(ctx, "system", j.Kind, j.VMID, fmt.Sprintf("%s%d: the server stopped while it ran", retryReason, j.ID))
	if err != nil {
		s.log.Error("retry after the stop", "job", j.ID, "error", err)
		return
	}
	s.log.Info("queued again for the next start", "job", j.ID, "kind", j.Kind, "as", r.ID)
}

// finishInterrupted ends what the last stop left running: jobs fail with the reason, restore tests become UNKNOWN,
// repository verifications fail (one left running would block every later verification of its repository), migration
// checks become UNKNOWN. A backup or restore test that was ended this way is queued again at once, unless it was
// itself such a retry or its VM has one of that kind queued already.
func (s *Server) finishInterrupted(ctx context.Context) error {
	const why = "the server stopped while it ran"
	js, err := s.db.FailInterruptedJobs(ctx, why)
	if err != nil {
		return err
	}
	if len(js) > 0 {
		s.log.Warn("jobs interrupted by the last stop were marked failed", "count", len(js))
	}
	if n, err := s.db.FinishInterruptedTests(ctx, why); err != nil {
		return err
	} else if n > 0 {
		s.log.Warn("restore tests interrupted by the last stop are UNKNOWN", "count", n)
	}
	if n, err := s.db.FailInterruptedVerifications(ctx, why); err != nil {
		return err
	} else if n > 0 {
		s.log.Warn("repository verifications interrupted by the last stop were marked failed", "count", n)
	}
	for _, j := range js {
		if (j.Kind != "backup" && j.Kind != "restore_test") || j.VMID == nil || strings.HasPrefix(j.Reason, retryReason) {
			continue
		}
		if _, open, err := s.db.OpenJob(ctx, *j.VMID, j.Kind); err != nil {
			return err
		} else if open { // one is queued already
			continue
		}
		r, err := s.db.CreateJob(ctx, "system", j.Kind, j.VMID, fmt.Sprintf("%s%d: %s", retryReason, j.ID, why))
		if err != nil {
			return err
		}
		s.log.Info("queued again", "job", j.ID, "kind", j.Kind, "as", r.ID)
	}
	return nil
}

// --- requests ---

// apiError is the body of every error response.
type apiError struct {
	Error string `json:"error"`
	// unauthorized, session_ended, forbidden, step_up_required, step_up_failed, idp_unreachable, signin_refused,
	// password_change_required, not_found, conflict, invalid, not_implemented, internal, unavailable, too_many_requests
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"` // session_ended: why (idle, absolute, ...); password_change_required: why; too_many_requests: account_paused, delay or rate_limited
}

var errNotImplemented = errors.New("not implemented in this version")

// call is one request being served.
type call struct {
	w         http.ResponseWriter
	r         *http.Request
	user      store.User
	session   store.Session
	token     *store.APIToken // the request came with an API token, not a session
	steppedUp bool            // a step-up passed for this request (stepup.go)
	target    string          // for the audit log: what the action was about
	detail    string          // for the audit log: what happened (never a secret)
	raw       bool            // the handler wrote the response itself
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
	case errors.Is(err, errTooManyLogins), errors.Is(err, errRateLimited):
		return http.StatusTooManyRequests, "too_many_requests"
	case errors.Is(err, errStepUpRequired):
		return http.StatusForbidden, "step_up_required"
	case errors.Is(err, errStepUpFailed):
		return http.StatusForbidden, "step_up_failed"
	case errors.Is(err, errIdPUnreachable):
		return http.StatusForbidden, "idp_unreachable"
	case errors.Is(err, errSignin):
		return http.StatusForbidden, "signin_refused"
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, errWebAuthnOff):
		return http.StatusServiceUnavailable, "webauthn_unavailable"
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

// serve wraps a route: authentication (a session or an API token, ADR-008 §5, §9), the session's refresh at the IdP,
// the password that must change first, the role check, the step-up (§4), the audit log, and the response.
func (s *Server) serve(rt *route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c := &call{w: w, r: r, target: r.URL.Path}
		if rt.roles != nil {
			if code, e := s.authenticate(c); code != 0 {
				writeJSON(w, code, e)
				return
			}
			if !allowed(rt.roles, c.user.Role) {
				s.audit(ctx, c, rt, http.StatusForbidden, "role "+c.user.Role+" is not allowed")
				writeJSON(w, http.StatusForbidden, apiError{Error: fmt.Sprintf("role %s may not %s %s", c.user.Role, rt.method, rt.path), Code: "forbidden"})
				return
			}
			if e := s.passwordGate(c, rt); e != nil {
				writeJSON(w, http.StatusForbidden, e)
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
		var status int
		var body any
		err := s.passStepUp(c, rt)
		if err == nil && rt.stepUp == stepAlways && !c.steppedUp {
			err = c.requireStepUp("a destructive or security action (ADR-008 §4)")
		}
		if err == nil {
			status, body, err = rt.h(c)
		}
		switch {
		case err != nil:
			code, name := statusOf(err)
			if code == http.StatusInternalServerError {
				s.log.Error("request failed", "route", rt.method+" "+rt.path, "user", c.user.Username, "error", err)
			}
			msg := err.Error()
			for _, e := range []error{store.ErrInvalid, store.ErrConflict, store.ErrNotFound, errUnauthorized, errNotImplemented, errHost, errTooManyLogins, errRateLimited,
				errStepUpRequired, errStepUpFailed, errIdPUnreachable, errSignin, errForbidden, errWebAuthnOff} {
				msg = strings.TrimPrefix(msg, e.Error()+": ") // the code says it already
			}
			if code == http.StatusInternalServerError {
				msg = "internal error (details in the server log)"
			}
			c.detail = strings.TrimSpace(c.detail + " " + err.Error())
			ae := apiError{Error: msg, Code: name}
			if we := (*waitError)(nil); errors.As(err, &we) {
				ae.Reason = we.reason()
			}
			if re := (*rateError)(nil); errors.As(err, &re) {
				ae.Reason = "rate_limited"
			}
			writeJSON(rec, code, ae)
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

// authenticate finds the request's user: an API token (Authorization: Bearer gaiat_...) or a session (the browser's
// cookie, or Authorization: Bearer gaia_... for the CLI). A non-zero status refuses the request.
func (s *Server) authenticate(c *call) (int, apiError) {
	ctx := c.r.Context()
	token, ok := strings.CutPrefix(c.r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		// The web UI's session cookie (T20); cross-origin requests never reach here (CrossOriginProtection).
		if ck, err := c.r.Cookie(sessionCookie); err == nil && ck.Value != "" {
			token, ok = ck.Value, true
		}
	}
	if !ok || token == "" {
		return http.StatusUnauthorized, apiError{Error: "sign in first (Authorization: Bearer <token>)", Code: "unauthorized"}
	}
	if strings.HasPrefix(token, "gaiat_") {
		t, use, err := s.db.UseAPIToken(ctx, token, remote(c.r))
		if errors.Is(err, store.ErrNoSession) || errors.Is(err, store.ErrTokenRefused) {
			msg := "the API token is not valid"
			if errors.Is(err, store.ErrTokenRefused) {
				msg = strings.TrimPrefix(err.Error(), store.ErrTokenRefused.Error()+": ")
			}
			return http.StatusUnauthorized, apiError{Error: msg, Code: "unauthorized"}
		}
		if err != nil {
			s.log.Error("API token lookup", "error", err)
			return http.StatusInternalServerError, apiError{Error: "API token lookup failed", Code: "internal"}
		}
		c.token, c.user = &t, store.User{Username: "token:" + t.Name, Role: t.Role, Source: "api_token", Factors: []string{}}
		if use.First || use.ClosedDay != "" {
			e := store.AuditEntry{Username: c.user.Username, Role: t.Role, Action: "api_token_use", Target: fmt.Sprintf("api_token:%d", t.ID),
				Outcome: 200, RemoteAddr: remote(c.r)}
			if use.First {
				e.Detail = "first use"
			} else {
				e.Detail = fmt.Sprintf("used %d times on %s", use.ClosedCount, use.ClosedDay)
			}
			if _, err := s.db.AppendAudit(ctx, e); err != nil {
				return http.StatusServiceUnavailable, apiError{Error: "the audit log cannot be written", Code: "unavailable"}
			}
		}
		return 0, apiError{}
	}
	st, err := s.settings(ctx)
	if err != nil {
		s.log.Error("settings", "error", err)
		return http.StatusInternalServerError, apiError{Error: "settings could not be read", Code: "internal"}
	}
	background := c.r.Header.Get("X-Gaia-Background") == "1"
	u, sess, err := s.db.UseSession(ctx, token, store.SessionLimits{Idle: st.idle()}, background)
	var ended *store.SessionEnded
	if errors.As(err, &ended) {
		return http.StatusUnauthorized, apiError{Error: "the session ended: " + endText(ended.Reason, st), Code: "session_ended", Reason: ended.Reason}
	}
	if errors.Is(err, store.ErrNoSession) {
		return http.StatusUnauthorized, apiError{Error: "the session is not valid", Code: "unauthorized"}
	}
	if err != nil {
		s.log.Error("session lookup", "error", err)
		return http.StatusInternalServerError, apiError{Error: "session lookup failed", Code: "internal"}
	}
	// An OIDC session's user is read again at the IdP every 10 minutes (§2); the sweep does it too, this is for a
	// request that comes first.
	if sess.Method == "oidc" && s.now().Sub(tsTime(sess.IdPRefreshed)) >= oidcRefreshEvery {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.refreshOIDC(rctx, sess)
		cancel()
		if err != nil {
			s.log.Error("OIDC session refresh", "session", sess.ID, "error", err)
			return http.StatusInternalServerError, apiError{Error: "session refresh failed", Code: "internal"}
		}
		if u, sess, err = s.db.UseSession(ctx, token, store.SessionLimits{Idle: st.idle()}, true); err != nil {
			if errors.As(err, &ended) {
				return http.StatusUnauthorized, apiError{Error: "the session ended: " + endText(ended.Reason, st), Code: "session_ended", Reason: ended.Reason}
			}
			return http.StatusUnauthorized, apiError{Error: "the session is not valid", Code: "unauthorized"}
		}
	}
	c.user, c.session = u, sess
	return 0, apiError{}
}

// endText says why a session ended, for the screen that says so.
func endText(reason string, st SigninSettings) string {
	switch reason {
	case store.EndIdle:
		return fmt.Sprintf("no activity for %d minutes", st.IdleTimeoutMin)
	case store.EndAbsolute:
		return fmt.Sprintf("sessions last at most %d hours", st.AbsoluteTimeoutH)
	case store.EndReplaced:
		return fmt.Sprintf("a newer sign-in replaced it (at most %d sessions per user)", store.MaxSessionsPerUser)
	case store.EndRevoked:
		return "it was signed out"
	case store.EndIdPRemoved:
		return "the identity provider no longer gives access"
	}
	return strings.ReplaceAll(reason, "_", " ")
}

// passwordGate: a user whose password must change first (expired, reset by an admin, on the blocklist) may only
// see itself, sign out, and change it (§6).
func (s *Server) passwordGate(c *call, rt *route) *apiError {
	if c.token != nil || c.user.Source != store.SourceLocal {
		return nil
	}
	why := c.user.PasswordChange
	if why == "" {
		st, err := s.settings(c.r.Context())
		if err != nil {
			return &apiError{Error: "settings could not be read", Code: "internal"}
		}
		if exp := st.passwordExpiresAt(c.user); !exp.IsZero() && !s.now().Before(exp) {
			why = "expired"
		}
	}
	if why == "" {
		return nil
	}
	switch rt.method + " " + rt.path {
	case "GET /api/v1/me", "POST /api/v1/logout", "PUT /api/v1/me/password", "POST /api/v1/step-up":
		return nil
	}
	return &apiError{Error: "set a new password first (" + why + ")", Code: "password_change_required", Reason: why}
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
