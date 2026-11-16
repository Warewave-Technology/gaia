package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/dirtar"
	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// RestoreOptions rebuild a lost server from its recovery kit and its repository alone (ADR-007 B5).
type RestoreOptions struct {
	ConfigPath  string            // to create, e.g. /etc/gaia/server.json; must not exist
	DataDir     string            // "" = the lost server's; must be empty or not exist
	Kit         []byte            // the recovery kit file
	Passphrase  string            // opens the kit, and encrypts the kit this rebuild writes
	Locations   map[string]string // repository name -> where it is reachable now (missing = where it was)
	StateBackup string            // a state archive ID, or a Gaia state backup ID ("" = the newest)
	Hostnames   []string          // given: a new TLS certificate for these names; none: the lost server's certificate
}

// RestoreResult says what came back and what did not.
type RestoreResult struct {
	Verdict          string               `json:"verdict"` // OK; WARNING if something could not be restored; CRITICAL if the ledger does not verify
	Problems         []string             `json:"problems"`
	ConfigPath       string               `json:"config"`
	DataDir          string               `json:"data_dir"`
	KitGenerationIn  int64                `json:"recovery_kit_generation_used"`
	Kit              string               `json:"recovery_kit"` // the new kit; it must be saved off the server again
	KitGeneration    int64                `json:"recovery_kit_generation"`
	EvidenceKeyID    string               `json:"evidence_key_id"`
	CertSHA256       string               `json:"tls_certificate_sha256"`
	CertNames        []string             `json:"tls_certificate_names,omitempty"` // only for a new certificate
	StateBackup      RestoredState        `json:"state_backup"`
	Repositories     []RestoredRepository `json:"repositories"`
	Catalog          store.CatalogCounts  `json:"catalog"`
	Evidence         RestoredEvidence     `json:"evidence"`
	Ledger           LedgerCheck          `json:"ledger"`
	InterruptedJobs  int                  `json:"interrupted_jobs"`
	SessionsEnded    int64                `json:"sessions_ended"` // logins on the lost server; everyone logs in again
	Tag              string               `json:"restore_tag"`
	MigratedVersions []int                `json:"migrated_versions"`
}

// RestoredState is the state archive the server was rebuilt from.
type RestoredState struct {
	ID            string `json:"id"`        // the archive's manifest ID
	BackupID      string `json:"backup_id"` // the lost server's state backup ID
	Created       string `json:"created"`
	Hostname      string `json:"hostname"`
	GaiaVersion   string `json:"gaia_version"`
	SchemaVersion int    `json:"schema_version"`
	Available     int    `json:"available"` // state archives in the repository
}

// RestoredRepository is one repository of the kit, as the rebuilt server reaches it.
type RestoredRepository struct {
	Name      string `json:"name"`
	Location  string `json:"location"`
	Moved     bool   `json:"moved"` // its location differs from the lost server's
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
	// Points the lost server stored after its last state backup: the catalog does not know them, so neither
	// restores nor retention use them.
	PointsNotInCatalog []string `json:"points_not_in_catalog"`
}

// RestoredEvidence is what came back of the evidence bundles.
type RestoredEvidence struct {
	Restored              int      `json:"restored"`
	MissingFromRepository []string `json:"missing_from_repository"` // in the catalog, but never stored
	NotInCatalog          []string `json:"not_in_catalog"`          // stored after the last state backup: left in the repository
}

// LedgerCheck is the verification of the restored ledger with its bundles (evidence spec v1).
type LedgerCheck struct {
	Verdict  string   `json:"verdict"`
	Entries  int      `json:"entries"`
	Keys     []string `json:"keys"`
	Problems []string `json:"problems"`
}

const restoreActor = "gaia server restore-state"

// RestoreState rebuilds a lost server: the secrets from the kit, the catalog, ledger and evidence from the
// repository. It refuses to touch an existing installation; if it fails, the data directory is emptied again.
func RestoreState(ctx context.Context, o RestoreOptions) (RestoreResult, error) {
	res := RestoreResult{Problems: []string{}, Repositories: []RestoredRepository{}, ConfigPath: o.ConfigPath}
	kit, err := recoverykit.Open(o.Kit, o.Passphrase)
	if err != nil {
		return res, err
	}
	res.KitGenerationIn, res.EvidenceKeyID = kit.Generation, kit.EvidenceKeyID
	if kit.StateRepository == "" {
		return res, fmt.Errorf("recovery kit %d has no repository (it was written before the first one was created): there is no state backup to restore", kit.Generation)
	}
	if _, err := os.Stat(o.ConfigPath); err == nil {
		return res, fmt.Errorf("%s exists: this host is already set up", o.ConfigPath)
	}
	var lost Config
	if err := json.Unmarshal(kit.ServerConfig, &lost); err != nil {
		return res, fmt.Errorf("the kit's server config: %w", err)
	}
	dataDir := o.DataDir
	if dataDir == "" {
		dataDir = lost.DataDir
	}
	cfg := Config{Listen: lost.Listen, DataDir: filepath.Clean(dataDir)}
	if err := cfg.resolve(); err != nil {
		return res, err
	}
	res.DataDir = cfg.DataDir
	if des, err := os.ReadDir(cfg.DataDir); err == nil {
		for _, de := range des {
			if de.Name() != "lost+found" {
				return res, fmt.Errorf("%s is not empty: a rebuild starts from an empty data directory", cfg.DataDir)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	done := false
	defer func() {
		if !done {
			emptyDir(cfg.DataDir)
		}
	}()
	if err := writeSecrets(cfg, kit, o.Hostnames, &res); err != nil {
		return res, err
	}

	// Every repository of the kit, where it is now.
	stores := map[string]*diskstore.Store{}
	defer func() {
		for _, st := range stores {
			st.Close(context.WithoutCancel(ctx))
		}
	}()
	for _, r := range kit.Repositories {
		rr := RestoredRepository{Name: r.Name, Location: r.Location, PointsNotInCatalog: []string{}}
		if loc, moved := o.Locations[r.Name]; moved && loc != r.Location {
			rr.Location, rr.Moved = loc, true
		}
		opts := cfg.repoOptions(r.Name, rr.Location, r.Password)
		st, err := connectRepository(ctx, opts)
		if err != nil {
			rr.Error = err.Error()
			res.Problems = append(res.Problems, fmt.Sprintf("repository %s at %s: %v", r.Name, rr.Location, err))
		} else {
			rr.Connected, stores[r.Name] = true, st
		}
		res.Repositories = append(res.Repositories, rr)
	}
	for name := range o.Locations {
		if _, ok := kit.Repository(name); !ok {
			return res, fmt.Errorf("-repository %s: recovery kit %d has no repository of that name", name, kit.Generation)
		}
	}
	st := stores[kit.StateRepository]
	if st == nil {
		return res, fmt.Errorf("the state repository %s cannot be opened, so there is nothing to restore (give its location now with -repository %s=PATH)",
			kit.StateRepository, kit.StateRepository)
	}

	// The state archive.
	files, man, err := readStateArchive(ctx, st, o.StateBackup, &res.StateBackup)
	if err != nil {
		return res, err
	}
	if string(files["keys/evidence-key.pub"]) != kit.EvidencePub {
		return res, errors.New("the state backup's evidence key is not the recovery kit's: the kit and the repository belong to different installations")
	}
	if man.SchemaVersion > store.Latest() {
		return res, fmt.Errorf("the state backup has schema version %d; this Gaia knows up to %d: install the Gaia version that wrote it (%s) or newer",
			man.SchemaVersion, store.Latest(), man.GaiaVersion)
	}
	if err := restoredConfig(files["server.json"], &cfg); err != nil {
		return res, err
	}
	if err := os.WriteFile(cfg.dbPath(), files["gaia.db"], 0o600); err != nil {
		return res, err
	}
	if ledger, ok := files["evidence/ledger.jsonl"]; ok {
		if err := os.WriteFile(cfg.ledgerPath(), ledger, 0o640); err != nil {
			return res, err
		}
	}

	db, err := store.Open(cfg.dbPath())
	if err != nil {
		return res, err
	}
	defer db.Close()
	if res.MigratedVersions, err = db.Migrate(ctx, 0); err != nil {
		return res, fmt.Errorf("the restored database: %w", err)
	}
	if res.MigratedVersions == nil {
		res.MigratedVersions = []int{}
	}
	if err := restoreBundles(ctx, cfg, db, st, &res.Evidence); err != nil {
		return res, err
	}
	for _, id := range res.Evidence.MissingFromRepository {
		res.Problems = append(res.Problems, "evidence bundle "+id+" is in the catalog but was never stored in the repository")
	}
	if n := len(res.Evidence.NotInCatalog); n > 0 {
		res.Problems = append(res.Problems, fmt.Sprintf("%d evidence bundle(s) were sealed after the state backup and are not in the restored ledger; they stay in the repository: %s",
			n, strings.Join(res.Evidence.NotInCatalog, ", ")))
	}

	// The catalog against the kit and the repositories.
	repos, err := db.Repositories(ctx)
	if err != nil {
		return res, err
	}
	for _, r := range repos {
		i := slices.IndexFunc(res.Repositories, func(rr RestoredRepository) bool { return rr.Name == r.Name })
		if i < 0 {
			res.Problems = append(res.Problems, fmt.Sprintf("repository %s is in the catalog, but recovery kit %d cannot open it (its password is not in the kit)", r.Name, kit.Generation))
			continue
		}
		rr := &res.Repositories[i]
		if rr.Location != r.Location {
			if _, err := db.SetRepositoryLocation(ctx, restoreActor, r.ID, rr.Location, "rebuilt server reaches it there"); err != nil {
				return res, err
			}
		}
		if st := stores[r.Name]; st != nil {
			known, err := db.StoreIDs(ctx, r.ID)
			if err != nil {
				return res, err
			}
			pts, err := st.Points(ctx, "", "")
			if err != nil {
				return res, fmt.Errorf("repository %s: points: %w", r.Name, err)
			}
			for _, p := range pts {
				if !known[p.ID] {
					rr.PointsNotInCatalog = append(rr.PointsNotInCatalog, p.ID)
				}
			}
			if n := len(rr.PointsNotInCatalog); n > 0 {
				res.Problems = append(res.Problems, fmt.Sprintf("repository %s holds %d disk point(s) stored after the state backup; the catalog does not know them, so they are neither restored from nor pruned",
					r.Name, n))
			}
		}
	}
	if res.InterruptedJobs, err = db.FailInterruptedJobs(ctx, "the server was lost while the job ran; rebuilt from state backup "+res.StateBackup.ID); err != nil {
		return res, err
	}
	if res.SessionsEnded, err = db.EndAllSessions(ctx); err != nil {
		return res, err
	}
	res.Ledger = checkLedger(cfg)
	if res.Ledger.Verdict != "OK" && res.Ledger.Verdict != "none" {
		res.Problems = append(res.Problems, "the restored evidence ledger does not verify: "+strings.Join(res.Ledger.Problems, "; "))
	}
	if res.Catalog, err = db.Counts(ctx); err != nil {
		return res, err
	}

	// Record the rebuild, write the config, and the next kit (the old one does not know the rebuilt server).
	tag := make([]byte, 3)
	if _, err := rand.Read(tag); err != nil {
		return res, err
	}
	res.Tag = hex.EncodeToString(tag)
	detail, err := json.Marshal(res)
	if err != nil {
		return res, err
	}
	if _, err := db.AddStateRestore(ctx, restoreActor, store.StateRestore{Tag: res.Tag, StateBackup: res.StateBackup.ID,
		StateBackupCreated: res.StateBackup.Created, KitGeneration: kit.Generation, Detail: detail}); err != nil {
		return res, err
	}
	summary := fmt.Sprintf("rebuilt from state backup %s (%s, lost server %s) with recovery kit %d; %d problem(s)",
		res.StateBackup.ID, res.StateBackup.Created, res.StateBackup.Hostname, kit.Generation, len(res.Problems))
	if _, err := db.AppendAudit(ctx, store.AuditEntry{Username: restoreActor, Action: "gaia server restore-state", Target: "state-backup:" + res.StateBackup.ID,
		Outcome: 200, RemoteAddr: "local", Detail: summary}); err != nil {
		return res, err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(o.ConfigPath), 0o750); err != nil {
		return res, err
	}
	if err := writeExclusive(o.ConfigPath, append(b, '\n'), 0o640); err != nil {
		return res, err
	}
	nk, err := writeKit(ctx, cfg, o.ConfigPath, db, restoreActor, "server rebuilt from state backup "+res.StateBackup.ID, o.Passphrase, buildVersion())
	if err != nil {
		os.Remove(o.ConfigPath)
		return res, fmt.Errorf("recovery kit: %w", err)
	}
	res.Kit, res.KitGeneration = cfg.kitPath(), nk.Generation
	res.Verdict = "OK"
	switch {
	case res.Ledger.Verdict != "OK" && res.Ledger.Verdict != "none":
		res.Verdict = "CRITICAL"
	case len(res.Problems) > 0:
		res.Verdict = "WARNING"
	}
	done = true
	return res, nil
}

// emptyDir removes what a failed rebuild put into dir (which was empty before).
func emptyDir(dir string) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range des {
		if de.Name() != "lost+found" {
			os.RemoveAll(filepath.Join(dir, de.Name()))
		}
	}
}

// writeSecrets lays out the data directory and writes the kit's keys, certificate and repository passwords.
func writeSecrets(cfg Config, kit recoverykit.Kit, hostnames []string, res *RestoreResult) error {
	for dir, mode := range map[string]os.FileMode{cfg.DataDir: 0o750, filepath.Dir(cfg.TLSCert): 0o700, filepath.Dir(cfg.keyPath()): 0o700,
		cfg.bundlesDir(): 0o750, cfg.secretsDir(): 0o700, cfg.kopiaDir(): 0o700, cfg.cacheDir(): 0o700, cfg.tmpDir(): 0o700} {
		if err := os.MkdirAll(dir, mode); err != nil {
			return err
		}
		if err := os.Chmod(dir, mode); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		path string
		data string
		mode os.FileMode
	}{{cfg.keyPath(), kit.EvidenceKey, 0o600}, {cfg.pubPath(), kit.EvidencePub, 0o644}, {cfg.sshKeyPath(), kit.SSHKey, 0o600}} {
		if err := writeExclusive(f.path, []byte(f.data), f.mode); err != nil {
			return err
		}
	}
	if pub, err := evidence.LoadPublicKey(cfg.pubPath()); err != nil {
		return fmt.Errorf("the kit's evidence key: %w", err)
	} else if evidence.KeyID(pub) != kit.EvidenceKeyID {
		return fmt.Errorf("the kit's evidence key is %s, not %s as it says", evidence.KeyID(pub), kit.EvidenceKeyID)
	}
	if _, err := evidence.LoadPrivateKey(cfg.keyPath()); err != nil {
		return fmt.Errorf("the kit's evidence private key: %w", err)
	}
	if _, err := loadSSHKey(cfg); err != nil {
		return fmt.Errorf("the kit's SSH key: %w", err)
	}
	if len(hostnames) > 0 {
		res.CertNames = certNames(hostnames)
		fp, err := generateCert(cfg.TLSCert, cfg.TLSKey, res.CertNames)
		if err != nil {
			return fmt.Errorf("TLS certificate: %w", err)
		}
		res.CertSHA256 = fp
	} else {
		if err := writeExclusive(cfg.TLSKey, []byte(kit.TLSKey), 0o600); err != nil {
			return err
		}
		if err := writeExclusive(cfg.TLSCert, []byte(kit.TLSCert), 0o644); err != nil {
			return err
		}
		fp, err := CertFileFingerprint(cfg.TLSCert)
		if err != nil {
			return fmt.Errorf("the kit's TLS certificate: %w", err)
		}
		res.CertSHA256 = fp
	}
	for _, r := range kit.Repositories {
		if err := writeExclusive(cfg.repoPasswordFile(r.Name), []byte(r.Password+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// connectRepository writes the Kopia client config for a repository and opens it.
func connectRepository(ctx context.Context, o diskstore.RepoOptions) (*diskstore.Store, error) {
	if fi, err := os.Stat(o.Path); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", o.Path)
	}
	if err := diskstore.Connect(ctx, o); err != nil {
		return nil, err
	}
	return diskstore.Open(ctx, o.ConfigFile, o.Password)
}

// readStateArchive reads the chosen (or newest) state archive and checks every member against its manifest.
func readStateArchive(ctx context.Context, st *diskstore.Store, want string, out *RestoredState) (map[string][]byte, StateManifest, error) {
	var man StateManifest
	as, err := st.Archives(ctx, StateArchiveKind)
	if err != nil {
		return nil, man, err
	}
	out.Available = len(as)
	if len(as) == 0 {
		return nil, man, errors.New("the state repository holds no state backup")
	}
	a := as[len(as)-1]
	if want != "" {
		i := slices.IndexFunc(as, func(a diskstore.Archive) bool { return a.ID == want || a.Labels["backup_id"] == want })
		if i < 0 {
			return nil, man, fmt.Errorf("no state backup %s in the repository", want)
		}
		a = as[i]
	}
	var buf bytes.Buffer
	if _, err := st.ReadArchive(ctx, a.ID, &buf); err != nil {
		return nil, man, err
	}
	files := map[string][]byte{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, man, fmt.Errorf("state backup %s: %w", a.ID, err)
		}
		b, err := io.ReadAll(io.LimitReader(tr, 4<<30))
		if err != nil {
			return nil, man, err
		}
		files[h.Name] = b
	}
	if err := json.Unmarshal(files["MANIFEST.json"], &man); err != nil {
		return nil, man, fmt.Errorf("state backup %s: MANIFEST.json: %w", a.ID, err)
	}
	if man.Format != "gaia-state-backup/1" {
		return nil, man, fmt.Errorf("state backup %s: format %q", a.ID, man.Format)
	}
	for _, f := range man.Files {
		b, ok := files[f.Name]
		h := sha256.Sum256(b)
		if !ok || int64(len(b)) != f.Bytes || hex.EncodeToString(h[:]) != f.SHA256 {
			return nil, man, fmt.Errorf("state backup %s: %s does not match its manifest", a.ID, f.Name)
		}
	}
	for _, need := range []string{"gaia.db", "server.json", "keys/evidence-key.pub"} {
		if !slices.ContainsFunc(man.Files, func(f FileEntry) bool { return f.Name == need }) {
			return nil, man, fmt.Errorf("state backup %s has no %s", a.ID, need)
		}
	}
	*out = RestoredState{ID: a.ID, BackupID: a.Labels["backup_id"], Created: a.Created.UTC().Format("2006-01-02T15:04:05Z"), Hostname: a.Labels["hostname"],
		GaiaVersion: man.GaiaVersion, SchemaVersion: man.SchemaVersion, Available: len(as)}
	return files, man, nil
}

// restoredConfig takes the lost server's settings from its backed-up config, but this server's data directory and
// the TLS files under it.
func restoredConfig(b []byte, cfg *Config) error {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("the state backup's server.json: %w", err)
	}
	c.DataDir, c.TLSCert, c.TLSKey = cfg.DataDir, cfg.TLSCert, cfg.TLSKey
	if err := c.resolve(); err != nil {
		return fmt.Errorf("the state backup's server.json: %w", err)
	}
	*cfg = c
	return nil
}

// restoreBundles brings back every evidence bundle of the restored catalog from the repository.
func restoreBundles(ctx context.Context, cfg Config, db *store.DB, st *diskstore.Store, out *RestoredEvidence) error {
	out.MissingFromRepository, out.NotInCatalog = []string{}, []string{}
	evs, err := db.EvidenceBundles(ctx, 0, math.MaxInt32)
	if err != nil {
		return err
	}
	inCatalog := map[string]bool{}
	for _, e := range evs {
		inCatalog[e.BundleID] = true
	}
	as, err := st.Archives(ctx, EvidenceArchiveKind)
	if err != nil {
		return err
	}
	stored := map[string]bool{}
	for _, a := range as {
		id := a.Labels["bundle_id"]
		if stored[id] {
			continue
		}
		stored[id] = true
		if !inCatalog[id] {
			out.NotInCatalog = append(out.NotInCatalog, id)
			continue
		}
		if !filepath.IsLocal(id) || strings.ContainsRune(id, filepath.Separator) {
			return fmt.Errorf("evidence archive %s: bundle ID %q", a.ID, id)
		}
		var buf bytes.Buffer
		if _, err := st.ReadArchive(ctx, a.ID, &buf); err != nil {
			return fmt.Errorf("evidence bundle %s: %w", id, err)
		}
		if err := dirtar.Read(&buf, filepath.Join(cfg.bundlesDir(), id)); err != nil {
			return fmt.Errorf("evidence bundle %s: %w", id, err)
		}
		out.Restored++
	}
	for _, e := range evs {
		if !stored[e.BundleID] {
			out.MissingFromRepository = append(out.MissingFromRepository, e.BundleID)
		}
	}
	return nil
}

// checkLedger verifies the restored ledger and bundles against the ledger's own anchor (verdict "none": no ledger).
func checkLedger(cfg Config) LedgerCheck {
	lc := LedgerCheck{Keys: []string{}, Problems: []string{}}
	if _, err := os.Stat(cfg.ledgerPath()); errors.Is(err, os.ErrNotExist) {
		lc.Verdict = "none"
		return lc
	}
	anchor, err := evidence.GenesisKey(cfg.ledgerPath())
	if err != nil {
		lc.Verdict, lc.Problems = "CRITICAL", []string{err.Error()}
		return lc
	}
	r, err := evidence.VerifyLedger(cfg.ledgerPath(), cfg.bundlesDir(), anchor)
	if err != nil {
		lc.Verdict, lc.Problems = "UNKNOWN", []string{err.Error()}
		return lc
	}
	lc.Verdict, lc.Entries, lc.Keys = r.Verdict, r.Entries, r.Keys
	lc.Problems = append(lc.Problems, r.Problems...)
	for _, b := range r.Bundles {
		if b.Verdict != "OK" {
			lc.Problems = append(lc.Problems, fmt.Sprintf("bundle %s: %s", b.BundleID, b.Verdict))
		}
	}
	return lc
}
