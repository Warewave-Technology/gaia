package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The recovery kit (ADR-007 B5). The server keeps its newest kit, encrypted, in <data_dir>/recovery: to hand it
// out, and to check that a passphrase given for the next kit is the one that opens this one. The passphrase itself
// is never stored.

func (c Config) kitPath() string {
	return filepath.Join(c.DataDir, "recovery", "gaia-recovery-kit.age")
}

// KitPassphrase is a request that writes a new kit.
type KitPassphrase struct {
	Passphrase    string `json:"passphrase"`               // opens the current kit (or sets the first one)
	NewPassphrase string `json:"new_passphrase,omitempty"` // to change it
}

// KitConfirm says which kit a copy outside the server is, after the copy was opened with the passphrase.
type KitConfirm struct {
	Generation int64  `json:"generation"`
	SHA256     string `json:"sha256"`
}

// KitSummary is the state of the recovery kit, as the operator must see it.
type KitSummary struct {
	State      string `json:"state"` // confirmed, not confirmed, stale, missing
	Generation int64  `json:"generation"`
	Text       string `json:"text"`
	// When the kit was confirmed as stored off the server, and by whom (state "confirmed" only), for screens that
	// write the time themselves.
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	ConfirmedBy string `json:"confirmed_by,omitempty"`
}

// KitInfo is the newest kit and the ones before it.
type KitInfo struct {
	Summary KitSummary          `json:"summary"`
	Kits    []store.RecoveryKit `json:"kits"`
}

// checkKitPassphrase checks a passphrase against the current kit; with no kit yet, it must only be acceptable.
func checkKitPassphrase(cfg Config, passphrase string) error {
	if passphrase == "" {
		return fmt.Errorf("%w: kit_passphrase is required: this writes a new recovery kit, encrypted with the passphrase of the current one", store.ErrInvalid)
	}
	b, err := os.ReadFile(cfg.kitPath())
	if errors.Is(err, os.ErrNotExist) {
		if err := recoverykit.CheckPassphrase(passphrase); err != nil {
			return fmt.Errorf("%w: %v", store.ErrInvalid, err)
		}
		return nil
	} else if err != nil {
		return err
	}
	if _, err := recoverykit.Open(b, passphrase); errors.Is(err, recoverykit.ErrPassphrase) {
		return fmt.Errorf("%w: the passphrase does not open the current recovery kit", store.ErrInvalid)
	} else if err != nil {
		return fmt.Errorf("the current recovery kit %s: %w", cfg.kitPath(), err)
	}
	return nil
}

// stateRepositoryName is the repository state backups go to: the configured one, or the first.
func stateRepositoryName(cfg Config, repos []store.Repository) string {
	if cfg.StateBackupRepository != "" {
		return cfg.StateBackupRepository
	}
	if len(repos) > 0 {
		return repos[0].Name
	}
	return ""
}

// buildKit collects the secrets of this installation.
func buildKit(ctx context.Context, cfg Config, cfgPath string, db *store.DB, gen int64, reason, version string) (recoverykit.Kit, error) {
	read := func(path string) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("recovery kit: %w", err)
		}
		return string(b), nil
	}
	host, _ := os.Hostname()
	k := recoverykit.Kit{Generation: gen, CreatedAt: time.Now().UTC().Format(time.RFC3339), Reason: reason, Hostname: host, GaiaVersion: version,
		Repositories: []recoverykit.Repository{}}
	var err error
	for _, f := range []struct {
		dst  *string
		path string
	}{{&k.EvidenceKey, cfg.keyPath()}, {&k.EvidencePub, cfg.pubPath()}, {&k.SSHKey, cfg.sshKeyPath()}, {&k.TLSCert, cfg.TLSCert}, {&k.TLSKey, cfg.TLSKey}} {
		if *f.dst, err = read(f.path); err != nil {
			return k, err
		}
	}
	pub, err := evidence.LoadPublicKey(cfg.pubPath())
	if err != nil {
		return k, err
	}
	k.EvidenceKeyID = evidence.KeyID(pub)
	sshKey, err := loadSSHKey(cfg)
	if err != nil {
		return k, err
	}
	k.SSHPub = sshKey.AuthorizedKey
	conf, err := read(cfgPath)
	if err != nil {
		return k, err
	}
	k.ServerConfig = json.RawMessage(conf)
	repos, err := db.Repositories(ctx)
	if err != nil {
		return k, err
	}
	for _, r := range repos {
		pw, err := read(cfg.repoPasswordFile(r.Name))
		if err != nil {
			return k, err
		}
		kr := recoverykit.Repository{ID: r.ID, Name: r.Name, Kind: r.Kind, Location: r.Location, Password: strings.TrimSpace(pw)}
		if r.Kind == "s3" {
			if kr.S3, err = cfg.readS3Secret(r.Name); err != nil {
				return k, err
			}
		}
		k.Repositories = append(k.Repositories, kr)
	}
	k.StateRepository = stateRepositoryName(cfg, repos)
	return k, nil
}

// writeKit writes the next generation of the kit, encrypted with passphrase, and records it. The caller has checked
// the passphrase (checkKitPassphrase) and holds no other kit write.
func writeKit(ctx context.Context, cfg Config, cfgPath string, db *store.DB, actor, reason, passphrase, version string) (store.RecoveryKit, error) {
	gen, err := db.NextKitGeneration(ctx)
	if err != nil {
		return store.RecoveryKit{}, err
	}
	k, err := buildKit(ctx, cfg, cfgPath, db, gen, reason, version)
	if err != nil {
		return store.RecoveryKit{}, err
	}
	b, err := recoverykit.Seal(k, passphrase)
	if err != nil {
		return store.RecoveryKit{}, fmt.Errorf("%w: %v", store.ErrInvalid, err)
	}
	dir := filepath.Dir(cfg.kitPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.RecoveryKit{}, err
	}
	tmp := cfg.kitPath() + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return store.RecoveryKit{}, err
	}
	names := []string{}
	for _, r := range k.Repositories {
		names = append(names, r.Name)
	}
	rec, err := db.AddRecoveryKit(ctx, actor, store.RecoveryKit{Generation: gen, Reason: reason, SHA256: recoverykit.SHA256(b), Repositories: names})
	if err != nil {
		os.Remove(tmp)
		return rec, err
	}
	if err := os.Rename(tmp, cfg.kitPath()); err != nil {
		return rec, fmt.Errorf("recovery kit %d was recorded but not put in place: %w", gen, err)
	}
	return rec, nil
}

// kitSummary says whether the newest kit can rebuild this server today.
func kitSummary(ctx context.Context, cfg Config, db *store.DB) (KitSummary, error) {
	ks, err := db.RecoveryKits(ctx)
	if err != nil {
		return KitSummary{}, err
	}
	if len(ks) == 0 {
		return KitSummary{State: "missing", Text: "there is no recovery kit: without one, a lost server cannot be rebuilt (gaia kit regenerate)"}, nil
	}
	k := ks[0]
	b, err := os.ReadFile(cfg.kitPath())
	if err != nil {
		return KitSummary{State: "missing", Generation: k.Generation, Text: fmt.Sprintf("recovery kit %d is not on this server (%v): write a new one (gaia kit regenerate)", k.Generation, err)}, nil
	}
	if recoverykit.SHA256(b) != k.SHA256 {
		return KitSummary{State: "stale", Generation: k.Generation, Text: fmt.Sprintf("the kit file is not recovery kit %d as recorded: write a new one (gaia kit regenerate)", k.Generation)}, nil
	}
	repos, err := db.Repositories(ctx)
	if err != nil {
		return KitSummary{}, err
	}
	var missing []string
	for _, r := range repos {
		if !slices.Contains(k.Repositories, r.Name) {
			missing = append(missing, r.Name)
		}
	}
	if len(missing) > 0 {
		return KitSummary{State: "stale", Generation: k.Generation, Text: fmt.Sprintf("recovery kit %d cannot open repository %s: write a new one (gaia kit regenerate), then save it",
			k.Generation, strings.Join(missing, ", "))}, nil
	}
	if k.ConfirmedAt == "" {
		return KitSummary{State: "not confirmed", Generation: k.Generation, Text: fmt.Sprintf("recovery kit %d (%s) is not confirmed as stored off this server: run gaia kit save on another machine",
			k.Generation, k.Reason)}, nil
	}
	return KitSummary{State: "confirmed", Generation: k.Generation, Text: fmt.Sprintf("recovery kit %d was saved off the server on %s by %s", k.Generation, k.ConfirmedAt, k.ConfirmedBy),
		ConfirmedAt: k.ConfirmedAt, ConfirmedBy: k.ConfirmedBy}, nil
}

// --- handlers ---

func (s *Server) kitInfo(c *call) (int, any, error) {
	ctx := c.r.Context()
	sum, err := kitSummary(ctx, s.cfg, s.db)
	if err != nil {
		return 0, nil, err
	}
	ks, err := s.db.RecoveryKits(ctx)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, KitInfo{Summary: sum, Kits: ks}, nil
}

func (s *Server) kitFile(c *call) (int, any, error) {
	ks, err := s.db.RecoveryKits(c.r.Context())
	if err != nil {
		return 0, nil, err
	}
	if len(ks) == 0 {
		return 0, nil, fmt.Errorf("%w: there is no recovery kit yet", store.ErrNotFound)
	}
	b, err := os.ReadFile(s.cfg.kitPath())
	if err != nil {
		return 0, nil, err
	}
	if recoverykit.SHA256(b) != ks[0].SHA256 {
		return 0, nil, fmt.Errorf("%w: the kit file is not recovery kit %d as recorded: write a new one", store.ErrConflict, ks[0].Generation)
	}
	c.target = fmt.Sprintf("recovery-kit:%d", ks[0].Generation)
	c.detail = "sha256 " + ks[0].SHA256
	c.raw = true
	c.w.Header().Set("Content-Type", "application/octet-stream")
	c.w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gaia-recovery-kit-%d.age"`, ks[0].Generation))
	c.w.Header().Set("X-Gaia-Kit-Generation", fmt.Sprint(ks[0].Generation))
	c.w.Header().Set("Cache-Control", "no-store")
	c.w.WriteHeader(http.StatusOK)
	_, err = c.w.Write(b)
	return http.StatusOK, nil, err
}

func (s *Server) regenerateKit(c *call) (int, any, error) {
	var in KitPassphrase
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	s.kitMu.Lock()
	defer s.kitMu.Unlock()
	if err := checkKitPassphrase(s.cfg, in.Passphrase); err != nil {
		return 0, nil, err
	}
	pass, reason := in.Passphrase, "written again by "+c.user.Username
	if in.NewPassphrase != "" {
		pass, reason = in.NewPassphrase, "new passphrase, set by "+c.user.Username
	}
	k, err := writeKit(ctx, s.cfg, s.cfgPath, s.db, c.user.Username, reason, pass, s.version)
	if err != nil {
		return 0, nil, err
	}
	c.target, c.detail = fmt.Sprintf("recovery-kit:%d", k.Generation), reason
	return http.StatusCreated, k, nil
}

func (s *Server) confirmKit(c *call) (int, any, error) {
	var in KitConfirm
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	k, err := s.db.ConfirmRecoveryKit(c.r.Context(), c.user.Username, in.Generation, in.SHA256)
	if err != nil {
		return 0, nil, err
	}
	c.target, c.detail = fmt.Sprintf("recovery-kit:%d", k.Generation), "sha256 "+k.SHA256
	return http.StatusOK, k, nil
}
