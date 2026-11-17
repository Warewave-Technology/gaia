package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errHost         = errors.New("host")
)

func (s *Server) login(c *call) (int, any, error) {
	var in LoginRequest
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	c.target = "user:" + in.Username
	u, err := s.checkPassword(ctx, c, in.Username, in.Password)
	if errors.Is(err, store.ErrBadLogin) {
		c.detail = "login failed: " + c.detail
		return 0, nil, fmt.Errorf("%w: %v", errUnauthorized, err)
	}
	if err != nil {
		return 0, nil, err
	}
	c.user = u
	token, sess, err := s.db.CreateSession(ctx, u, remote(c.r), s.cfg.ttl())
	if err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("logged in (session %d)", sess.ID)
	if in.Browser {
		exp, err := time.Parse(time.RFC3339Nano, sess.ExpiresAt)
		if err != nil {
			return 0, nil, err
		}
		http.SetCookie(c.w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: exp, HttpOnly: true, Secure: true,
			SameSite: http.SameSiteStrictMode})
		c.detail += " in a browser"
		return http.StatusOK, LoginResponse{ExpiresAt: sess.ExpiresAt, User: u}, nil
	}
	return http.StatusOK, LoginResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: u}, nil
}

func (s *Server) changePassword(c *call) (int, any, error) {
	var in PasswordChange
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	if _, err := s.checkPassword(ctx, c, c.user.Username, in.CurrentPassword); errors.Is(err, store.ErrBadLogin) {
		c.detail = "wrong current password: " + c.detail
		return 0, nil, fmt.Errorf("%w: the current password is wrong", store.ErrInvalid)
	} else if err != nil {
		return 0, nil, err
	}
	if _, err := s.db.UpdateUser(ctx, c.user.Username, c.user.ID, store.UserPatch{Password: &in.NewPassword}); err != nil {
		return 0, nil, err
	}
	c.detail = "password changed; sessions ended"
	return http.StatusNoContent, nil, nil
}

func (s *Server) systemInfo(c *call) (int, any, error) {
	ctx := c.r.Context()
	applied, err := s.db.Applied(ctx)
	if err != nil {
		return 0, nil, err
	}
	pub, err := evidence.LoadPublicKey(s.cfg.pubPath())
	if err != nil {
		return 0, nil, fmt.Errorf("the evidence key: %w", err)
	}
	kit, err := kitSummary(ctx, s.cfg, s.db)
	if err != nil {
		return 0, nil, err
	}
	ledger, err := s.ledgerSummary(ctx)
	if err != nil {
		return 0, nil, err
	}
	notify, err := s.notifySummary(ctx)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, SystemInfo{Version: s.version, SchemaVersion: len(applied), Migrations: applied, EvidenceKeyID: evidence.KeyID(pub),
		SSHPublicKey: s.sshKey.AuthorizedKey, SSHAuthorizedKey: `restrict,port-forwarding,command="/usr/bin/false" ` + s.sshKey.AuthorizedKey,
		StartedAt: s.started.Format(time.RFC3339), StateBackupAt: s.cfg.StateBackupAt, StateBackupRepo: s.cfg.StateBackupRepository, RecoveryKit: kit,
		Ledger: ledger, Notifications: notify, DailyReportAt: s.cfg.DailyReportAt, Hostname: hostname(), Clock: serverClock(s.now())}, nil
}

// repoOptions are where Gaia keeps a repository's client config, password and cache (A2: 1 GiB hard limit).
func (s *Server) repoOptions(name, location, password string) diskstore.RepoOptions {
	return s.cfg.repoOptions(name, location, password)
}

func (c Config) repoOptions(name, location, password string) diskstore.RepoOptions {
	return diskstore.RepoOptions{Path: location, ConfigFile: filepath.Join(c.kopiaDir(), name+".config"), Password: password,
		CacheDir: filepath.Join(c.cacheDir(), name), ContentCacheMB: 512, ContentCacheLimitMB: 1024, MetadataCacheMB: 256,
		MetadataCacheLimitMB: 512, Splitter: diskstore.Splitter}
}

func (s *Server) repoPasswordFile(name string) string { return s.cfg.repoPasswordFile(name) }

func (c Config) repoPasswordFile(name string) string {
	return filepath.Join(c.secretsDir(), "repository-"+name+".password")
}

func (s *Server) createRepository(c *call) (int, any, error) {
	var in RepositoryCreate
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	if in.Kind == "" {
		in.Kind = "filesystem"
	}
	var s3key *recoverykit.S3
	switch in.Kind {
	case "filesystem":
		if !filepath.IsAbs(in.Location) || filepath.Clean(in.Location) != in.Location {
			return 0, nil, fmt.Errorf("%w: location %q: a clean absolute path", store.ErrInvalid, in.Location)
		}
		if in.S3AccessKeyID != "" || in.S3SecretAccessKey != "" || in.S3CAPEM != "" {
			return 0, nil, fmt.Errorf("%w: S3 fields for a filesystem repository", store.ErrInvalid)
		}
	case "s3":
		canon, _, err := parseS3Location(in.Location)
		if err != nil {
			return 0, nil, err
		}
		in.Location = canon
		if in.S3AccessKeyID == "" || in.S3SecretAccessKey == "" {
			return 0, nil, fmt.Errorf("%w: an S3 repository needs s3_access_key_id and s3_secret_access_key", store.ErrInvalid)
		}
		s3key = &recoverykit.S3{AccessKeyID: in.S3AccessKeyID, SecretAccessKey: in.S3SecretAccessKey, CAPEM: in.S3CAPEM}
	default:
		return 0, nil, fmt.Errorf("%w: kind %q (filesystem or s3)", store.ErrInvalid, in.Kind)
	}
	if !store.ValidName(in.Name) {
		return 0, nil, fmt.Errorf("%w: a repository name is 1-128 of A-Z a-z 0-9 . _ - and starts with a letter or digit", store.ErrInvalid)
	}
	if _, err := s.db.RepositoryByName(ctx, in.Name); err == nil {
		return 0, nil, fmt.Errorf("%w: a repository named %s exists", store.ErrConflict, in.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, nil, err
	}
	// The new kit must open the new repository: check its passphrase before anything is made.
	s.kitMu.Lock()
	defer s.kitMu.Unlock()
	if err := checkKitPassphrase(s.cfg, in.KitPassphrase); err != nil {
		return 0, nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return 0, nil, err
	}
	password := base64.RawURLEncoding.EncodeToString(b)
	o, err := s.cfg.repoOptionsFor(in.Name, in.Kind, in.Location, password, s3key)
	if err != nil {
		return 0, nil, err
	}
	if in.Kind == "filesystem" {
		if des, err := os.ReadDir(in.Location); err == nil && len(des) > 0 {
			return 0, nil, fmt.Errorf("%w: %s is not empty", store.ErrConflict, in.Location)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, nil, fmt.Errorf("%w: %s: %v", store.ErrInvalid, in.Location, err)
		}
	} else if empty, err := diskstore.Empty(ctx, o); err != nil {
		return 0, nil, fmt.Errorf("%w: %v", store.ErrInvalid, err)
	} else if !empty {
		return 0, nil, fmt.Errorf("%w: %s is not empty", store.ErrConflict, in.Location)
	}
	if err := os.MkdirAll(s.cfg.secretsDir(), 0o700); err != nil {
		return 0, nil, err
	}
	var written []string
	undo := func() {
		for _, f := range written {
			if rerr := os.Remove(f); rerr != nil {
				s.log.Error("removing a secret of a repository that was not created", "file", f, "error", rerr)
			}
		}
	}
	pwFile := s.repoPasswordFile(in.Name)
	if err := writeExclusive(pwFile, []byte(password+"\n"), 0o600); err != nil {
		return 0, nil, err
	}
	written = append(written, pwFile)
	if s3key != nil {
		if err := writeS3Secret(s.cfg.repoS3File(in.Name), s3key); err != nil {
			undo()
			return 0, nil, err
		}
		written = append(written, s.cfg.repoS3File(in.Name))
	}
	if err := os.MkdirAll(filepath.Dir(o.ConfigFile), 0o700); err != nil {
		undo()
		return 0, nil, err
	}
	if err := diskstore.Create(ctx, o); err != nil {
		undo()
		return 0, nil, fmt.Errorf("creating the repository at %s: %w", in.Location, err)
	}
	r, err := s.db.CreateRepository(ctx, store.Repository{Name: in.Name, Kind: in.Kind, Location: in.Location, Splitter: diskstore.Splitter,
		Hash: "BLAKE2B-256-128"}) // Kopia's default content hash, kept by ADR-004 A8
	if err != nil {
		return 0, nil, fmt.Errorf("the repository at %s was created but not recorded: %w", in.Location, err)
	}
	kit, err := writeKit(ctx, s.cfg, s.cfgPath, s.db, c.user.Username, "repository "+in.Name+" created", in.KitPassphrase, s.version)
	if err != nil {
		return 0, nil, fmt.Errorf("repository %s was created, but the recovery kit could not be written (gaia kit regenerate): %w", in.Name, err)
	}
	return created(c, fmt.Sprintf("repository %s (%s) at %s; recovery kit %d", in.Name, in.Kind, in.Location, kit.Generation), r, r.ID, nil)
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// exportEvidence answers a tar.gz with the bundle, the ledger and the ledger's genesis key, ready for
// `gaia evidence verify` (or any verifier of the specification).
func (s *Server) exportEvidence(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	e, err := s.db.Evidence(c.r.Context(), id)
	if err != nil {
		return 0, nil, err
	}
	c.target = "evidence:" + e.BundleID
	anchor, err := evidence.GenesisKey(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	root := "gaia-evidence-" + e.BundleID
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Now().UTC()}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	ledger, err := os.ReadFile(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	keyID := evidence.KeyID(anchor)
	readme := fmt.Sprintf(`Evidence bundle %s, exported %s.

Verify it (Restore Evidence Format v1, docs/spec/evidence-v1.md) with:
  gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles

anchor.pub is this installation's trust anchor, key_id %s. A verifier must not take the trust anchor from the
data it verifies: compare that key_id with the one you recorded when Gaia was installed.
The ledger lists every bundle; this export holds only %s, so the other bundles show up as bundle_missing.
`, e.BundleID, time.Now().UTC().Format(time.RFC3339), keyID, e.BundleID)
	if err := add("README.txt", []byte(readme)); err != nil {
		return 0, nil, err
	}
	if err := add("anchor.pub", []byte(base64.StdEncoding.EncodeToString(anchor)+"\n")); err != nil {
		return 0, nil, err
	}
	if err := add("ledger.jsonl", ledger); err != nil {
		return 0, nil, err
	}
	dir := filepath.Join(s.cfg.bundlesDir(), e.BundleID)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.cfg.bundlesDir(), p)
		return add("bundles/"+filepath.ToSlash(rel), b)
	})
	if err != nil {
		return 0, nil, fmt.Errorf("bundle %s: %w", e.BundleID, err)
	}
	if err := tw.Close(); err != nil {
		return 0, nil, err
	}
	if err := gz.Close(); err != nil {
		return 0, nil, err
	}
	c.raw = true
	c.detail = fmt.Sprintf("exported %s (%d bytes)", e.BundleID, buf.Len())
	h := c.w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+root+`.tar.gz"`)
	c.w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(c.w, &buf); err != nil {
		s.log.Error("sending an evidence export", "bundle", e.BundleID, "error", err)
	}
	return http.StatusOK, nil, nil
}
