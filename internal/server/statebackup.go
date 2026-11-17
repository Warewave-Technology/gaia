package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/dirtar"
	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// StateArchiveKind is the diskstore archive kind of Gaia's state backups.
const StateArchiveKind = "gaia-state"

// EvidenceArchiveKind is the archive kind of one sealed evidence bundle: stored once, never pruned.
const EvidenceArchiveKind = "gaia-evidence"

// After jobs, Gaia's state is backed up at most once per stateBackupGap; changes in between go into the next one.
var stateBackupGap = 5 * time.Minute

// State archives kept: every one of the last two days, then the newest of each day for 30 days.
const (
	stateKeepAll  = 48 * time.Hour
	stateKeepDays = 30
)

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
	o, err := s.repoOpts(r)
	if err != nil {
		return nil, err
	}
	return diskstore.Open(ctx, o.ConfigFile, o.Password)
}

// BackupState stores a consistent copy of Gaia's state in its repository: the database (VACUUM INTO), the
// evidence ledger, the public evidence key and the config, and each evidence bundle not stored yet. Never secrets:
// those are in the recovery kit (ADR-007 B5). The returned record says how it ended; err is set only if no record
// could be made. It refuses to run beside another state backup.
func (s *Server) BackupState(ctx context.Context, actor, reason string) (store.StateBackup, error) {
	if !s.backupMu.TryLock() {
		return store.StateBackup{}, fmt.Errorf("%w: a state backup is already running", store.ErrConflict)
	}
	defer s.backupMu.Unlock()
	return s.backupStateLocked(ctx, actor, reason)
}

func (s *Server) backupStateLocked(ctx context.Context, actor, reason string) (store.StateBackup, error) {
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
	return s.db.FinishStateBackup(context.WithoutCancel(ctx), actor, b.ID, snap, n, sum, errText)
}

// stateChanged says that a job ended, so Gaia's state should be backed up soon (backupStateAfterChanges).
func (s *Server) stateChanged() {
	select {
	case s.stateDirty <- struct{}{}:
	default:
	}
}

// backupStateAfterChanges backs Gaia's state up after jobs end, at most once per stateBackupGap, so a lost server
// is rebuilt with every point and test but those of the last few minutes (ADR-007 B5).
func (s *Server) backupStateAfterChanges(ctx context.Context) {
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stateDirty:
		}
		if wait := stateBackupGap - time.Since(last); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		select { // what changed while waiting goes into this backup
		case <-s.stateDirty:
		default:
		}
		last = time.Now()
		s.backupMu.Lock()
		b, err := s.backupStateLocked(ctx, "system", "after jobs")
		s.backupMu.Unlock()
		if err != nil {
			s.log.Error("state backup after jobs did not run", "error", err)
		} else if b.State != "ok" {
			s.log.Error("state backup after jobs failed", "id", b.ID, "error", b.Error)
		}
	}
}

// archiveBundles stores every evidence bundle of the catalog that the repository does not hold yet, each as its
// own archive. A bundle whose directory is gone is named, not fatal.
func (s *Server) archiveBundles(ctx context.Context, st *diskstore.Store) (stored int, missing []string, err error) {
	have := map[string]bool{}
	as, err := st.Archives(ctx, EvidenceArchiveKind)
	if err != nil {
		return 0, nil, err
	}
	for _, a := range as {
		have[a.Labels["bundle_id"]] = true
	}
	evs, err := s.db.EvidenceBundles(ctx, 0, math.MaxInt32)
	if err != nil {
		return 0, nil, err
	}
	missing = []string{}
	for _, e := range evs {
		if have[e.BundleID] {
			continue
		}
		dir := filepath.Join(s.cfg.bundlesDir(), e.BundleID)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, e.BundleID)
			continue
		}
		var buf bytes.Buffer
		if err := dirtar.Write(&buf, dir); err != nil {
			return stored, missing, fmt.Errorf("evidence bundle %s: %w", e.BundleID, err)
		}
		if _, err := st.PutArchive(ctx, EvidenceArchiveKind, &buf, map[string]string{"bundle_id": e.BundleID, "ledger_seq": fmt.Sprint(e.LedgerSeq)}); err != nil {
			return stored, missing, err
		}
		stored++
	}
	if len(missing) > 0 {
		s.log.Error("evidence bundles in the catalog are not on disk and cannot be stored", "bundles", missing)
	}
	return stored, missing, nil
}

// pruneStateArchives removes the state archives stateArchivesToPrune names.
func (s *Server) pruneStateArchives(ctx context.Context, st *diskstore.Store, keep string, now time.Time) (int, error) {
	as, err := st.Archives(ctx, StateArchiveKind)
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, id := range stateArchivesToPrune(as, keep, now) {
		if err := st.DeleteArchive(ctx, id); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}

// stateArchivesToPrune keeps every state archive of the last two days and the newest of each day before, for 30
// days; keep is never pruned. as is oldest first.
func stateArchivesToPrune(as []diskstore.Archive, keep string, now time.Time) []string {
	newestOfDay := map[string]string{}
	for _, a := range as {
		newestOfDay[a.Created.UTC().Format(time.DateOnly)] = a.ID
	}
	var out []string
	for _, a := range as {
		age := now.Sub(a.Created)
		if a.ID == keep || age < stateKeepAll || (age < stateKeepDays*24*time.Hour && newestOfDay[a.Created.UTC().Format(time.DateOnly)] == a.ID) {
			continue
		}
		out = append(out, a.ID)
	}
	return out
}

func (s *Server) writeStateArchive(ctx context.Context, repo store.Repository, id int64) (string, int64, string, error) {
	st, err := s.OpenRepository(ctx, repo)
	if err != nil {
		return "", 0, "", err
	}
	defer st.Close(ctx)
	bundlesStored, bundlesMissing, err := s.archiveBundles(ctx, st)
	if err != nil {
		return "", 0, "", fmt.Errorf("evidence bundles: %w", err)
	}
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
	var entries []FileEntry
	for _, m := range members {
		b, err := os.ReadFile(m.path)
		if err != nil {
			return "", 0, "", fmt.Errorf("%s: %w", m.name, err)
		}
		h := sha256.Sum256(b)
		entries = append(entries, FileEntry{m.name, int64(len(b)), hex.EncodeToString(h[:])})
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now().UTC()}); err != nil {
			return "", 0, "", err
		}
		if _, err := tw.Write(b); err != nil {
			return "", 0, "", err
		}
	}
	idx, err := json.MarshalIndent(StateManifest{Format: "gaia-state-backup/1", BackupID: id, Created: time.Now().UTC().Format(time.RFC3339Nano),
		GaiaVersion: s.version, SchemaVersion: store.Latest(), Files: entries, BundlesStoredNow: bundlesStored, BundlesMissing: bundlesMissing}, "", "  ")
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
	host, _ := os.Hostname()
	a, err := st.PutArchive(ctx, StateArchiveKind, f, map[string]string{"backup_id": fmt.Sprint(id), "hostname": host})
	if err != nil {
		return "", 0, "", err
	}
	if n, err := s.pruneStateArchives(ctx, st, a.ID, time.Now()); err != nil {
		s.log.Error("pruning old state archives", "repository", repo.Name, "error", err)
	} else if n > 0 {
		s.log.Info("pruned old state archives", "repository", repo.Name, "count", n)
	}
	return a.ID, a.Bytes, a.SHA256, nil
}

// StateManifest is MANIFEST.json, the last member of a state archive.
type StateManifest struct {
	Format           string      `json:"format"` // gaia-state-backup/1
	BackupID         int64       `json:"backup_id"`
	Created          string      `json:"created"`
	GaiaVersion      string      `json:"gaia_version"`
	SchemaVersion    int         `json:"schema_version"`
	Files            []FileEntry `json:"files"`
	BundlesStoredNow int         `json:"evidence_bundles_stored_now"`
	BundlesMissing   []string    `json:"evidence_bundles_missing"`
}

// FileEntry is one member of a state archive.
type FileEntry struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
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
		s.backupMu.Lock() // after one that runs now, if any
		b, err := s.backupStateLocked(ctx, "system", "schedule")
		s.backupMu.Unlock()
		if err != nil {
			s.log.Error("scheduled state backup did not run", "error", err)
		} else if b.State != "ok" {
			s.log.Error("scheduled state backup failed", "id", b.ID, "error", b.Error)
		}
	}
}
