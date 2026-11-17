package diskstore

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/object"
)

func newStoreOpts(t *testing.T) (*Store, RepoOptions) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	o := RepoOptions{Path: filepath.Join(dir, "repo"), ConfigFile: filepath.Join(dir, "kopia.config"),
		Password: "test-only", CacheDir: filepath.Join(dir, "cache"), ContentCacheMB: 64, ContentCacheLimitMB: 64}
	if err := Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, o.ConfigFile, o.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	return s, o
}

func full(t *testing.T, s *Store, domain string, seed uint64) (*Point, []byte) {
	t.Helper()
	d, alloc := testDisk()
	fill(d.data[:BlockSize], seed)
	p, err := s.Backup(context.Background(), BackupRequest{Domain: domain, Disk: "vda", Checkpoint: "c", Size: uint64(len(d.data)),
		Changed: alloc, Sources: []Source{d}})
	if err != nil {
		t.Fatal(err)
	}
	return p, d.data
}

func TestCheckPoint(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreOpts(t)
	p, _ := full(t, s, "vm1", 1)
	c, err := s.CheckPoint(ctx, p.ID)
	if err != nil || len(c.Problems) != 0 || c.Blocks != 6 || c.ZeroBlocks != 3 || c.Contents != 3 {
		t.Fatalf("fresh point: %+v %v", c, err)
	}
	// A block whose content went missing from the repository's index.
	idx, _ := s.loadIndex(ctx, p)
	oid, _ := object.ParseID(idx.Blocks[0])
	cids, _ := s.r.VerifyObject(ctx, oid)
	dr := s.r.(repo.DirectRepository)
	wctx, w, err := dr.NewDirectWriter(ctx, repo.WriteSessionOptions{Purpose: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.ContentManager().DeleteContent(wctx, cids[0]); err != nil {
		t.Fatal(err)
	}
	w.Flush(wctx)
	w.Close(wctx)
	if c, _ := s.CheckPoint(ctx, p.ID); len(c.Problems) == 0 || !strings.Contains(c.Problems[0], "block 0") {
		t.Fatalf("a deleted block content: %+v", c)
	}
	if c, _ := s.CheckPoint(ctx, "nope"); len(c.Problems) == 0 {
		t.Fatal("a missing manifest passed the check")
	}
}

// One byte changed in a pack file on disk: the uncached verification names the block, the cached path may not.
func TestVerifyPointFindsCorruption(t *testing.T) {
	ctx := context.Background()
	s, o := newStoreOpts(t)
	p, _ := full(t, s, "vm1", 1)
	u, err := OpenUncached(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close(ctx)
	rng := rand.New(rand.NewPCG(1, 2))
	if v, err := u.VerifyPoint(ctx, p.ID, 1, rng); err != nil || len(v.Problems) != 0 || v.Read != 3 || v.BytesRead == 0 {
		t.Fatalf("intact: %+v %v", v, err)
	}
	if v, _ := u.VerifyPoint(ctx, p.ID, 0.01, rng); v.Read != 1 {
		t.Fatalf("a 1%% sample reads at least one block: %+v", v)
	}
	var packs []string
	filepath.Walk(o.Path, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(o.Path, p) // pack blobs are sharded under directories named p...
		if err == nil && !fi.IsDir() && strings.HasPrefix(rel, "p") {
			packs = append(packs, p)
		}
		return nil
	})
	if len(packs) == 0 {
		t.Fatal("no pack files")
	}
	for _, pk := range packs {
		b, _ := os.ReadFile(pk)
		b[len(b)/2] ^= 0xff
		os.WriteFile(pk, b, 0o600)
	}
	v, err := u.VerifyPoint(ctx, p.ID, 1, rng)
	if err != nil || len(v.Problems) == 0 {
		t.Fatalf("corruption not found: %+v %v", v, err)
	}
	t.Logf("found: %v", v.Problems)
}

// Gaia's GC deletes only what no point or archive references: the kept point restores byte for byte.
func TestMaintainKeepsReferencedData(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreOpts(t)
	keep, keepData := full(t, s, "vm1", 1)
	drop, _ := full(t, s, "vm2", 2)
	if _, err := s.PutArchive(ctx, "test", bytes.NewReader([]byte("state archive")), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePoint(ctx, drop.ID); err != nil {
		t.Fatal(err)
	}
	res, err := s.Maintain(ctx, true, maintenance.SafetyNone)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "full" || res.GC == nil || res.GC.DeletedContentCount == 0 || res.GC.InUseContentCount == 0 {
		t.Fatalf("maintenance %+v gc %+v", res, res.GC)
	}
	// Exactly the dropped point's own blocks went: vm2 had a different first block; the rest of the test disk is
	// shared by both points (dedup) and must survive.
	if res.GC.DeletedContentCount != 2 { // vm2's block 0 and its block index
		t.Fatalf("deleted %d contents, want 2: %+v", res.GC.DeletedContentCount, res.GC)
	}
	restoreAndCompare(t, s, keep, keepData)
	if c, _ := s.CheckPoint(ctx, keep.ID); len(c.Problems) != 0 {
		t.Fatalf("kept point after GC: %+v", c.Problems)
	}
	as, err := s.Archives(ctx, "test")
	if err != nil || len(as) != 1 {
		t.Fatalf("archives %v %v", as, err)
	}
	var buf bytes.Buffer
	if _, err := s.ReadArchive(ctx, as[0].ID, &buf); err != nil || buf.String() != "state archive" {
		t.Fatalf("archive after GC: %q %v", buf.String(), err)
	}
	if err := s.DeleteArchive(ctx, as[0].ID); err != nil {
		t.Fatal(err)
	}
	if as, err := s.Archives(ctx, "test"); err != nil || len(as) != 0 {
		t.Fatalf("archives after delete %v %v", as, err)
	}
	_ = content.ID{}
}

func corruptPacks(t *testing.T, repoPath string) []string {
	t.Helper()
	var packs []string
	filepath.Walk(repoPath, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(repoPath, p) // pack blobs are sharded under directories named p...
		if err == nil && !fi.IsDir() && strings.HasPrefix(rel, "p") {
			packs = append(packs, p)
		}
		return nil
	})
	if len(packs) == 0 {
		t.Fatal("no pack files")
	}
	for _, pk := range packs {
		b, _ := os.ReadFile(pk)
		b[len(b)/2] ^= 0xff
		os.WriteFile(pk, b, 0o600)
	}
	return packs
}

// corruptBlock flips one byte inside the stored copy of block i of point p, and returns that content's ID.
func corruptBlock(t *testing.T, s *Store, o RepoOptions, p *Point, i int) string {
	t.Helper()
	idx, err := s.loadIndex(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return corruptObject(t, s, o, idx.Blocks[i])
}

// corruptObject flips one byte inside the stored copy of a direct object.
func corruptObject(t *testing.T, s *Store, o RepoOptions, id string) string {
	t.Helper()
	ctx := context.Background()
	oid, _ := object.ParseID(id)
	cid, _, ok := oid.ContentID()
	if !ok {
		t.Fatalf("%s is not a direct object", id)
	}
	ci, err := s.r.ContentInfo(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	var file string
	filepath.Walk(o.Path, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(o.Path, p) // the filesystem storage shards a blob ID into directories
		if err == nil && !fi.IsDir() && strings.TrimSuffix(strings.ReplaceAll(rel, string(filepath.Separator), ""), ".f") == string(ci.PackBlobID) {
			file = p
		}
		return nil
	})
	if file == "" {
		t.Fatalf("pack %s not found", ci.PackBlobID)
	}
	b, _ := os.ReadFile(file)
	b[int(ci.PackOffset)+int(ci.PackedLength)/2] ^= 0xff
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return cid.String()
}

func reopen(t *testing.T, o RepoOptions) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, o.ConfigFile, o.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	return s
}

func uncached(t *testing.T, o RepoOptions) *Store {
	t.Helper()
	ctx := context.Background()
	u, err := OpenUncached(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close(ctx) })
	return u
}

// ADR-007 B3, extending T15's corruption test: a verification names the damaged contents, the quarantine takes them out
// of deduplication, and a new point of the same data stores them again, so it restores byte for byte. Before the
// quarantine (T15, TestDamagedContentIsReusedByDedup) the new point silently shared the damaged content.
func TestQuarantinedContentIsNotReused(t *testing.T) {
	ctx := context.Background()
	s, o := newStoreOpts(t)
	p1, data := full(t, s, "vm1", 1)
	corruptPacks(t, o.Path)
	rng := rand.New(rand.NewPCG(1, 2))
	v1, err := uncached(t, o).VerifyPoint(ctx, p1.ID, 1, rng)
	if err != nil || len(v1.Problems) == 0 || len(v1.Damaged) == 0 {
		t.Fatalf("verification of the damaged point: %+v %v", v1, err)
	}
	var ids []string
	for _, d := range v1.Damaged {
		if d.Pack == "" || !strings.Contains(d.Reason, "do not verify") {
			t.Fatalf("damage %+v", d)
		}
		ids = append(ids, d.Content)
	}
	users, err := s.PointsUsing(ctx, ids)
	if err != nil || len(users[p1.ID]) == 0 {
		t.Fatalf("points using the damaged contents: %v %v", users, err)
	}
	q, err := uncached(t, o).Quarantine(ctx, v1.Damaged, "test verification")
	if err != nil || q.ID == "" {
		t.Fatalf("quarantine %+v %v", q, err)
	}
	if qs, err := reopen(t, o).Quarantines(ctx); err != nil || len(qs) != 1 || len(qs[0].Contents) != len(v1.Damaged) {
		t.Fatalf("quarantines %+v %v", qs, err)
	}
	// The next backup opens the repository afresh (as every Gaia job does) and writes the same data.
	s2 := reopen(t, o)
	p2, _ := full(t, s2, "vm1", 1)
	if c, err := s2.CheckPoint(ctx, p2.ID); err != nil || len(c.Problems) != 0 {
		t.Fatalf("post-backup check of the new point: %+v %v", c, err)
	}
	v2, err := uncached(t, o).VerifyPoint(ctx, p2.ID, 1, rng)
	if err != nil || len(v2.Problems) != 0 {
		t.Fatalf("the new point shares damaged contents: %+v %v", v2, err)
	}
	restoreAndCompare(t, reopen(t, o), p2, data)
	// Maintenance with the quarantine record in place: the rewritten contents are ordinary again, nothing fails.
	if res, err := reopen(t, o).Maintain(ctx, true, maintenance.SafetyNone); err != nil || res.Quarantined != 0 {
		t.Fatalf("maintenance after the rewrite: %+v %v", res, err)
	}
	restoreAndCompare(t, reopen(t, o), p2, data)
}

// A quarantined content no new point rewrites stays deleted through GC (which would otherwise restore a referenced
// deleted content), and maintenance keeps working while the damaged pack and the failed point are still there: Kopia's
// rewrite skips the unreadable deleted content, and once Kopia drops its index entry, GC still resolves the point.
func TestQuarantineSurvivesMaintenance(t *testing.T) {
	ctx := context.Background()
	s, o := newStoreOpts(t)
	p1, _ := full(t, s, "vm1", 1)
	keep, keepData := full(t, s, "vm2", 7) // shares every block but block 0 with p1
	damaged := corruptBlock(t, s, o, p1, 0)
	v, err := uncached(t, o).VerifyPoint(ctx, p1.ID, 1, rand.New(rand.NewPCG(1, 2)))
	if err != nil || len(v.Damaged) != 1 || v.Damaged[0].Content != damaged {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := uncached(t, o).Quarantine(ctx, v.Damaged, "test"); err != nil {
		t.Fatal(err)
	}
	for run := range 3 {
		res, err := reopen(t, o).Maintain(ctx, true, maintenance.SafetyNone)
		if err != nil {
			t.Fatalf("maintenance run %d: %v", run, err)
		}
		t.Logf("run %d: quarantined %d, unresolved %v, gc %+v", run, res.Quarantined, res.Unresolved, res.GC)
		if run == 0 && res.Quarantined == 0 {
			t.Fatal("GC did not keep the quarantined contents deleted")
		}
		r := reopen(t, o)
		for _, d := range v.Damaged {
			cid, _ := content.ParseID(d.Content)
			if ci, err := r.r.ContentInfo(ctx, cid); err == nil && !ci.Deleted {
				t.Fatalf("run %d: quarantined content %s is live again", run, d.Content)
			}
		}
		if users, err := r.PointsUsing(ctx, []string{v.Damaged[0].Content}); err != nil || len(users[p1.ID]) == 0 {
			t.Fatalf("run %d: points using: %v %v", run, users, err)
		}
	}
	restoreAndCompare(t, reopen(t, o), keep, keepData)
	v2, err := uncached(t, o).VerifyPoint(ctx, keep.ID, 1, rand.New(rand.NewPCG(1, 2)))
	if err != nil || len(v2.Problems) != 0 {
		t.Fatalf("the other point after maintenance: %+v %v", v2, err)
	}
}

// A point whose block index is damaged hides which blocks it uses: GC lists it and goes on, keeping the blocks other
// points use.
func TestDamagedBlockIndexDoesNotStopGC(t *testing.T) {
	ctx := context.Background()
	s, o := newStoreOpts(t)
	p1, _ := full(t, s, "vm1", 1)
	keep, keepData := full(t, s, "vm2", 7)
	corruptObject(t, s, o, p1.Index)
	v, err := uncached(t, o).VerifyPoint(ctx, p1.ID, 1, rand.New(rand.NewPCG(1, 2)))
	if err != nil || len(v.Damaged) != 1 || !strings.HasPrefix(v.Problems[0], "block index") {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := uncached(t, o).Quarantine(ctx, v.Damaged, "test"); err != nil {
		t.Fatal(err)
	}
	res, err := uncached(t, o).Maintain(ctx, true, maintenance.SafetyNone)
	if err != nil || len(res.Unresolved) != 1 || res.Unresolved[0] != p1.ID {
		t.Fatalf("maintenance %+v %v", res, err)
	}
	restoreAndCompare(t, reopen(t, o), keep, keepData)
}

// A storage that fails is not damage: the verification stops with ErrStorage and names no contents.
func TestVerifyStorageErrorIsNotDamage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	ctx := context.Background()
	s, o := newStoreOpts(t)
	p, _ := full(t, s, "vm1", 1)
	u := uncached(t, o)
	var packs []string
	filepath.Walk(o.Path, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(o.Path, p)
		if err == nil && !fi.IsDir() && strings.HasPrefix(rel, "p") {
			packs = append(packs, p)
		}
		return nil
	})
	for _, pk := range packs {
		os.Chmod(pk, 0)
		t.Cleanup(func() { os.Chmod(pk, 0o600) })
	}
	v, err := u.VerifyPoint(ctx, p.ID, 1, rand.New(rand.NewPCG(1, 2)))
	if !errors.Is(err, ErrStorage) || len(v.Damaged) != 0 {
		t.Fatalf("unreadable packs: %+v %v", v, err)
	}
}
