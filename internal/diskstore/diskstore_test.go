package diskstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const miB = 1 << 20

// memDisk is a Source that fails any read outside the ranges it was told may be read,
// so a test proves the backup never touches unchanged bytes of the source.
type memDisk struct {
	data    []byte
	mu      sync.Mutex
	allowed []Range
	read    uint64
	fail    bool
}

func (d *memDisk) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return 0, errors.New("source broke")
	}
	ok := false
	for _, r := range d.allowed {
		if uint64(off) >= r.Offset && uint64(off)+uint64(len(p)) <= r.Offset+r.Length {
			ok = true
		}
	}
	if !ok {
		return 0, fmt.Errorf("read [%d, +%d) outside the changed ranges", off, len(p))
	}
	d.read += uint64(len(p))
	return copy(p, d.data[off:]), nil
}

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	o := RepoOptions{Path: filepath.Join(dir, "repo"), ConfigFile: filepath.Join(dir, "kopia.config"),
		Password: "test-only", CacheDir: filepath.Join(dir, "cache"), ContentCacheLimitMB: 64}
	if err := Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, o.ConfigFile, o.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	return s
}

func fill(b []byte, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 1))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
}

func restoreAndCompare(t *testing.T, s *Store, p *Point, want []byte) RestoreStats {
	t.Helper()
	out := filepath.Join(t.TempDir(), "restored.raw")
	st, err := s.Restore(context.Background(), p, out, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored %s point differs from the source", p.Kind)
	}
	return st
}

// A disk of 5.5 blocks: data in blocks 0, 2 (partly) and the short last block, zeroes elsewhere.
func testDisk() (*memDisk, []Range) {
	d := &memDisk{data: make([]byte, 5*BlockSize+BlockSize/2)}
	fill(d.data[:BlockSize], 1)
	fill(d.data[2*BlockSize+100:2*BlockSize+300_000], 2)
	fill(d.data[5*BlockSize:], 3)
	alloc := []Range{{0, BlockSize}, {2 * BlockSize, BlockSize}, {5 * BlockSize, BlockSize / 2}}
	d.allowed = alloc
	return d, alloc
}

func TestFullIncrementalRestore(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	d, alloc := testDisk()
	size := uint64(len(d.data))

	full, err := s.Backup(ctx, BackupRequest{Domain: "vm1", Disk: "vda", Checkpoint: "c1", Size: size,
		Changed: alloc, Sources: []Source{d, d}})
	if err != nil {
		t.Fatal(err)
	}
	st := full.Stats
	if st.Blocks != 6 || st.BlocksWritten != 3 || st.BlocksZero != 3 || st.BlocksReused != 0 ||
		st.SourceReadBytes != st.ChangedBytes || st.ChangedBytes != 2*BlockSize+BlockSize/2 {
		t.Fatalf("full stats %+v", st)
	}
	fullImage := bytes.Clone(d.data)
	rs := restoreAndCompare(t, s, full, fullImage)
	if rs.BlocksHole != 3 || rs.BlocksWritten != 3 {
		t.Fatalf("restore stats %+v", rs)
	}

	// Incremental: a small change inside block 0, a whole block 1 (was zero), a range across blocks 3-4.
	dirty := []Range{{4096, 65536}, {BlockSize, BlockSize}, {4*BlockSize - 8192, 16384}}
	for _, r := range dirty {
		fill(d.data[r.Offset:r.Offset+r.Length], r.Offset)
	}
	d.allowed, d.read = dirty, 0
	inc, err := s.Backup(ctx, BackupRequest{Domain: "vm1", Disk: "vda", Checkpoint: "c2", Size: size,
		Changed: dirty, Parent: full, Sources: []Source{d}})
	if err != nil {
		t.Fatal(err)
	}
	st = inc.Stats
	want := uint64(65536 + BlockSize + 16384)
	if st.ChangedBytes != want || st.SourceReadBytes != want || d.read != want {
		t.Fatalf("incremental read %d (source counted %d), changed %d, want %d", st.SourceReadBytes, d.read, st.ChangedBytes, want)
	}
	// Blocks 0, 1, 3, 4 touched; 2 and 5 reused. Block 0 completed from the parent; 3 and 4 were zero in the parent.
	if st.BlocksReused != 2 || st.BlocksWritten != 4 || st.RepoReadBytes != BlockSize {
		t.Fatalf("incremental stats %+v", st)
	}
	if inc.Kind != "incremental" || inc.ParentID != full.ID || inc.ParentCheckpoint != "c1" {
		t.Fatalf("incremental point %+v", inc)
	}
	incImage := bytes.Clone(d.data)
	restoreAndCompare(t, s, inc, incImage)

	// A second incremental on top of the first, then every point still restores to its own state.
	dirty2 := []Range{{2*BlockSize + 200, 1000}}
	fill(d.data[2*BlockSize+200:2*BlockSize+1200], 99)
	d.allowed = dirty2
	inc2, err := s.Backup(ctx, BackupRequest{Domain: "vm1", Disk: "vda", Checkpoint: "c3", Size: size,
		Changed: dirty2, Parent: inc, Sources: []Source{d}})
	if err != nil {
		t.Fatal(err)
	}
	restoreAndCompare(t, s, inc2, d.data)
	restoreAndCompare(t, s, inc, incImage)
	restoreAndCompare(t, s, full, fullImage)

	pts, err := s.Points(ctx, "vm1", "vda")
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 || pts[0].ID != full.ID || pts[2].ID != inc2.ID {
		t.Fatalf("points %v", pts)
	}
	if other, _ := s.Points(ctx, "vm2", ""); len(other) != 0 {
		t.Fatalf("points for another domain: %v", other)
	}
}

func TestRestoreIsSparse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hole accounting checked on Linux only (APFS allocates differently)")
	}
	s := newStore(t)
	d, alloc := testDisk()
	p, err := s.Backup(context.Background(), BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c", Size: uint64(len(d.data)),
		Changed: alloc, Sources: []Source{d}})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "r.raw")
	if _, err := s.Restore(context.Background(), p, out, 2); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	sys := fi.Sys().(*syscall.Stat_t)
	if alloc := sys.Blocks * 512; alloc > 3*BlockSize+miB { // 2.5 blocks of data, 3 zero blocks left as holes
		t.Fatalf("restored file allocates %d bytes", alloc)
	}
}

func TestBackupErrors(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	d, alloc := testDisk()
	size := uint64(len(d.data))
	full, err := s.Backup(ctx, BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c1", Size: size, Changed: alloc, Sources: []Source{d}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		req  BackupRequest
		want string
	}{
		"size changed": {BackupRequest{Size: size + BlockSize, Changed: nil, Parent: full, Sources: []Source{d}}, "full backup is needed"},
		"outside":      {BackupRequest{Size: size, Changed: []Range{{size - 10, 20}}, Parent: full, Sources: []Source{d}}, "outside disk"},
		"no source":    {BackupRequest{Size: size}, "no source"},
		"read error":   {BackupRequest{Size: size, Changed: alloc, Sources: []Source{&memDisk{data: d.data, fail: true}}}, "source broke"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.req.Domain, tc.req.Disk, tc.req.Checkpoint = "vm", "vda", "x"
			if _, err := s.Backup(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
	// A failed backup writes no point.
	if pts, _ := s.Points(ctx, "vm", "vda"); len(pts) != 1 {
		t.Fatalf("%d points after failed backups", len(pts))
	}
}

func TestNormalise(t *testing.T) {
	got, err := normalise([]Range{{100, 10}, {0, 50}, {40, 20}, {200, 0}, {105, 3}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[{0 60} {100 10}]" {
		t.Fatalf("normalise %v", got)
	}
	split := splitByBlock([]Range{{BlockSize - 10, 20}, {3 * BlockSize, 2 * BlockSize}}, BlockSize)
	if fmt.Sprint(split[0], split[1], split[3], split[4]) != fmt.Sprint([]Range{{BlockSize - 10, 10}}, []Range{{BlockSize, 10}},
		[]Range{{3 * BlockSize, BlockSize}}, []Range{{4 * BlockSize, BlockSize}}) || len(split) != 4 {
		t.Fatalf("split %v", split)
	}
}

// The pipeline with more writers than readers, uncompressed blocks, and the reference reader.
func TestPipelineWritersCompressionAndReadRanges(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	d, alloc := testDisk()
	size := uint64(len(d.data))
	p, err := s.Backup(ctx, BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c", Size: size, Changed: alloc,
		Sources: []Source{d}, Writers: 3, Compressor: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Stats.Readers != 1 || p.Stats.Writers != 3 || p.Stats.Compressor != "none" || p.Stats.WriteBusySeconds <= 0 || p.Stats.ReadBusySeconds <= 0 {
		t.Fatalf("stats %+v", p.Stats)
	}
	restoreAndCompare(t, s, p, d.data)

	d.read = 0
	rs, err := ReadRanges(ctx, alloc, size, 0, []Source{d, d, d})
	if err != nil {
		t.Fatal(err)
	}
	if rs.Bytes != p.Stats.SourceReadBytes || d.read != rs.Bytes || rs.Readers != 3 || rs.Blocks != 3 {
		t.Fatalf("read %+v (source counted %d), backup read %d", rs, d.read, p.Stats.SourceReadBytes)
	}
	if _, err := ReadRanges(ctx, []Range{{size - 1, 2}}, size, 0, []Source{d}); err == nil {
		t.Fatal("range outside the disk accepted")
	}
}

// A read error with many writers and readers ends the backup with that error, without a deadlock.
func TestPipelineErrorDoesNotHang(t *testing.T) {
	s := newStore(t)
	d, alloc := testDisk()
	bad := &memDisk{data: d.data, fail: true}
	done := make(chan error, 1)
	go func() {
		_, err := s.Backup(context.Background(), BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c", Size: uint64(len(d.data)),
			Changed: alloc, Sources: []Source{bad, bad, bad, bad}, Writers: 8})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "source broke") {
			t.Fatalf("error %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("backup hangs after a read error")
	}
}

// Smaller blocks on repositories with another splitter and hash: full, incremental and restore,
// and an incremental refuses a parent with another block size.
func TestBlockSizesSplittersAndHashes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		splitter, hash string
		bs             uint64
	}{{"FIXED-1M", "BLAKE3-256-128", 1 << 20}, {"FIXED-256K", "HMAC-SHA256-128", 256 << 10}} {
		t.Run(tc.splitter, func(t *testing.T) {
			dir := t.TempDir()
			o := RepoOptions{Path: filepath.Join(dir, "repo"), ConfigFile: filepath.Join(dir, "k.config"), Password: "test-only",
				CacheDir: filepath.Join(dir, "cache"), Splitter: tc.splitter, Hash: tc.hash}
			if err := Create(ctx, o); err != nil {
				t.Fatal(err)
			}
			s, err := Open(ctx, o.ConfigFile, o.Password)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(ctx)
			d, alloc := testDisk()
			size := uint64(len(d.data))
			full, err := s.Backup(ctx, BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c1", Size: size, Changed: alloc,
				Sources: []Source{d}, BlockSize: tc.bs})
			if err != nil {
				t.Fatal(err)
			}
			if full.BlockSize != tc.bs || full.Stats.Blocks != int((size+tc.bs-1)/tc.bs) {
				t.Fatalf("point %+v", full)
			}
			dirty := []Range{{4096, 65536}}
			fill(d.data[4096:4096+65536], 7)
			d.allowed = dirty
			inc, err := s.Backup(ctx, BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c2", Size: size, Changed: dirty,
				Parent: full, Sources: []Source{d}, BlockSize: tc.bs})
			if err != nil {
				t.Fatal(err)
			}
			// One block touched: the parent's copy read back is one block, not 4 MiB.
			if inc.Stats.BlocksWritten != 1 || inc.Stats.RepoReadBytes != tc.bs || inc.Stats.SourceReadBytes != 65536 {
				t.Fatalf("incremental stats %+v", inc.Stats)
			}
			restoreAndCompare(t, s, inc, d.data)
			// No block size given: an incremental keeps its parent's (points made before ADR-004 A8 stay 4 MiB).
			inc2, err := s.Backup(ctx, BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c3", Size: size, Changed: dirty,
				Parent: inc, Sources: []Source{d}})
			if err != nil || inc2.BlockSize != tc.bs {
				t.Fatalf("incremental without a block size: %+v %v", inc2, err)
			}
			if _, err := s.Backup(ctx, BackupRequest{Domain: "vm", Disk: "vda", Checkpoint: "c4", Size: size, Changed: dirty,
				Parent: inc2, Sources: []Source{d}, BlockSize: 4 << 20}); err == nil || !strings.Contains(err.Error(), "full backup is needed") {
				t.Fatalf("block size change accepted: %v", err)
			}
			if _, err := s.Backup(ctx, BackupRequest{Size: size, Sources: []Source{d}, BlockSize: 1000}); err == nil {
				t.Fatal("block size 1000 accepted")
			}
		})
	}
}
