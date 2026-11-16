package diskstore

import (
	"bytes"
	"context"
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
	_ = content.ID{}
}

// A content damaged on disk stays in the index, so a later backup of the same data deduplicates onto it: the new
// point passes the post-backup check (which confirms presence, not readability) and only a verification that reads
// the data finds it. This documents the gap raised in the T15 report; when damaged contents are taken out of the
// index on detection, the second backup must write the block again and this test must change.
func TestDamagedContentIsReusedByDedup(t *testing.T) {
	ctx := context.Background()
	s, o := newStoreOpts(t)
	p1, _ := full(t, s, "vm1", 1)
	var packs []string
	filepath.Walk(o.Path, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(o.Path, p)
		if err == nil && !fi.IsDir() && strings.HasPrefix(rel, "p") {
			packs = append(packs, p)
		}
		return nil
	})
	for _, pk := range packs {
		b, _ := os.ReadFile(pk)
		b[len(b)/2] ^= 0xff
		os.WriteFile(pk, b, 0o600)
	}
	p2, _ := full(t, s, "vm1", 1) // the same data again
	if c, err := s.CheckPoint(ctx, p2.ID); err != nil || len(c.Problems) != 0 {
		t.Fatalf("post-backup check of the second point: %+v %v", c, err)
	}
	u, err := OpenUncached(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close(ctx)
	rng := rand.New(rand.NewPCG(1, 2))
	v1, _ := u.VerifyPoint(ctx, p1.ID, 1, rng)
	v2, _ := u.VerifyPoint(ctx, p2.ID, 1, rng)
	if len(v1.Problems) == 0 || len(v2.Problems) == 0 {
		t.Fatalf("both points share the damaged content: first %v, second %v", v1.Problems, v2.Problems)
	}
	t.Logf("second point, written after the damage, reads: %v", v2.Problems)
}
