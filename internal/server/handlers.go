package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
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
	errUnauthorized  = errors.New("unauthorized")
	errForbidden     = errors.New("forbidden")
	errHost          = errors.New("host")
	errTooManyLogins = errors.New("too many failed sign-ins")
)

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
		Ledger: ledger, Notifications: notify, DailyReportAt: s.cfg.DailyReportAt, Hostname: hostname(), Clock: serverClock(s.now()),
		PublicURL: s.cfg.PublicURL, SigninProblem: errText(s.webauthnReady())}, nil
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
		// Made here, so that a directory the server may not write (a disk mounted on /srv/gaia after the package
		// made it) is named as such, not as a missing path deep in the storage layer.
		if err := os.MkdirAll(in.Location, 0o750); err != nil {
			return 0, nil, fmt.Errorf("%w: the server cannot make %s: %v (the directory it goes in must belong to the gaia user: chown gaia:gaia <that directory>; "+
				"outside /srv/gaia the service also needs it in ReadWritePaths, install.md step 7)", store.ErrInvalid, in.Location, err)
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

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
