package server

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// StateArchiveKind is the diskstore archive kind of Gaia's state backups.
const StateArchiveKind = "gaia-state"

// stateRepository is the repository state backups go to: the configured one, or the first.
func (s *Server) stateRepository(ctx context.Context) (store.Repository, error) {
	if s.cfg.StateBackupRepository != "" {
		return s.db.RepositoryByName(ctx, s.cfg.StateBackupRepository)
	}
	rs, err := s.db.Repositories(ctx)
	if err != nil {
		return store.Repository{}, err
	}
	if len(rs) == 0 {
		return store.Repository{}, fmt.Errorf("%w: there is no repository to back Gaia's state up into (create one first)", store.ErrConflict)
	}
	return rs[0], nil
}

// OpenRepository opens a repository Gaia created, with its stored password.
func (s *Server) OpenRepository(ctx context.Context, r store.Repository) (*diskstore.Store, error) {
	pw, err := os.ReadFile(s.repoPasswordFile(r.Name))
	if err != nil {
		return nil, fmt.Errorf("repository %s: password: %w", r.Name, err)
	}
	o := s.repoOptions(r.Name, r.Location, strings.TrimSpace(string(pw)))
	return diskstore.Open(ctx, o.ConfigFile, o.Password)
}

// BackupState stores a consistent copy of Gaia's state in its repository: the database (VACUUM INTO), the
// evidence ledger, the public evidence key and the config. Never secrets: the private key and repository
// passwords stay on this host. The returned record says how it ended; err is set only if no record could be made.
func (s *Server) BackupState(ctx context.Context, actor, reason string) (store.StateBackup, error) {
	if !s.backupMu.TryLock() {
		return store.StateBackup{}, fmt.Errorf("%w: a state backup is already running", store.ErrConflict)
	}
	defer s.backupMu.Unlock()
	repo, err := s.stateRepository(ctx)
	if err != nil {
		return store.StateBackup{}, err
	}
	b, err := s.db.StartStateBackup(ctx, actor, repo.ID, reason)
	if err != nil {
		return b, err
	}
	snap, n, sum, berr := s.writeStateArchive(ctx, repo, b.ID)
	errText := ""
	if berr != nil {
		errText = berr.Error()
		s.log.Error("state backup failed", "id", b.ID, "repository", repo.Name, "error", berr)
	} else {
		s.log.Info("state backup stored", "id", b.ID, "repository", repo.Name, "snapshot", snap, "bytes", n)
	}
	return s.db.FinishStateBackup(ctx, actor, b.ID, snap, n, sum, errText)
}

func (s *Server) writeStateArchive(ctx context.Context, repo store.Repository, id int64) (string, int64, string, error) {
	if err := os.MkdirAll(s.cfg.tmpDir(), 0o700); err != nil {
		return "", 0, "", err
	}
	tmp, err := os.MkdirTemp(s.cfg.tmpDir(), "state-backup-")
	if err != nil {
		return "", 0, "", err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "gaia.db")
	if _, err := s.db.SQL().ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(snapshot, "'", "''")+"'"); err != nil {
		return "", 0, "", fmt.Errorf("database snapshot: %w", err)
	}
	type member struct{ name, path string }
	members := []member{{"gaia.db", snapshot}, {"server.json", s.cfgPath}, {"keys/evidence-key.pub", s.cfg.pubPath()}}
	if _, err := os.Stat(s.cfg.ledgerPath()); err == nil {
		members = append(members, member{"evidence/ledger.jsonl", s.cfg.ledgerPath()})
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", 0, "", err
	}
	archive := filepath.Join(tmp, "state.tar")
	f, err := os.Create(archive)
	if err != nil {
		return "", 0, "", err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	type entry struct {
		Name   string `json:"name"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	var entries []entry
	for _, m := range members {
		b, err := os.ReadFile(m.path)
		if err != nil {
			return "", 0, "", fmt.Errorf("%s: %w", m.name, err)
		}
		h := sha256.Sum256(b)
		entries = append(entries, entry{m.name, int64(len(b)), hex.EncodeToString(h[:])})
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now().UTC()}); err != nil {
			return "", 0, "", err
		}
		if _, err := tw.Write(b); err != nil {
			return "", 0, "", err
		}
	}
	idx, err := json.MarshalIndent(map[string]any{"format": "gaia-state-backup/1", "backup_id": id, "created": time.Now().UTC(),
		"gaia_version": s.version, "schema_version": store.Latest(), "files": entries}, "", "  ")
	if err != nil {
		return "", 0, "", err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "MANIFEST.json", Mode: 0o600, Size: int64(len(idx)), ModTime: time.Now().UTC()}); err != nil {
		return "", 0, "", err
	}
	if _, err := tw.Write(idx); err != nil {
		return "", 0, "", err
	}
	if err := tw.Close(); err != nil {
		return "", 0, "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", err
	}
	st, err := s.OpenRepository(ctx, repo)
	if err != nil {
		return "", 0, "", err
	}
	defer st.Close(ctx)
	host, _ := os.Hostname()
	a, err := st.PutArchive(ctx, StateArchiveKind, f, map[string]string{"backup_id": fmt.Sprint(id), "hostname": host})
	if err != nil {
		return "", 0, "", err
	}
	return a.ID, a.Bytes, a.SHA256, nil
}

// nextRun is the next local time at HH:MM after t.
func nextRun(t time.Time, hhmm string) time.Time {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	n := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location())
	if !n.After(t) {
		n = n.AddDate(0, 0, 1)
	}
	return n
}

// scheduleStateBackups runs BackupState every day at cfg.StateBackupAt until ctx ends.
func (s *Server) scheduleStateBackups(ctx context.Context) {
	for {
		next := nextRun(time.Now(), s.cfg.StateBackupAt)
		s.log.Info("next state backup", "at", next.Format(time.RFC3339))
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		b, err := s.BackupState(ctx, "system", "schedule")
		if err != nil {
			s.log.Error("scheduled state backup did not run", "error", err)
		} else if b.State != "ok" {
			s.log.Error("scheduled state backup failed", "id", b.ID, "error", b.Error)
		}
	}
}
