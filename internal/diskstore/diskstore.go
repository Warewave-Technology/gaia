// Package diskstore keeps disk images in a Kopia repository through the Kopia Go
// library (ADR-004 §4), as points in time that are each a complete image.
//
// A point is a list of fixed 1 MiB blocks (ADR-004 A8: FIXED-1M, s2-default), each a
// Kopia object, or "" for a block of zeroes (stored as nothing). A full backup writes
// the blocks that hold data. An incremental backup reads only the changed ranges
// from the source; blocks it does not touch reuse the parent's objects, and a partly
// changed block is completed from the parent's copy in the repository, so the source
// is never read outside the changed ranges. Restoring any point needs only that
// point: there is no chain to replay.
//
// The block index is a Kopia object of its own; the point itself is a Kopia manifest
// (labels type=gaia-disk-point, domain, disk), visible with `kopia manifest list`.
package diskstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob/filesystem"
	"github.com/kopia/kopia/repo/compression"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/format"
	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/repo/object"
)

// ADR-004 A8 defaults. BlockSize is the default block of a full backup's point; a backup may choose
// another, and an incremental uses its parent's (points made before A8 keep their 4 MiB blocks).
const (
	BlockSize   = 1 << 20
	Splitter    = "FIXED-1M"
	Compression = "s2-default"
	pointType   = "gaia-disk-point"
)

// RepoOptions locate a filesystem Kopia repository and this client's config and cache.
type RepoOptions struct {
	Path       string // repository directory
	ConfigFile string // Kopia client config written by Create/Connect
	Password   string
	// Cache: T5 measured that an unbounded cache on the repository disk slows restore down,
	// so a hard limit and a directory of its own can be set (0 = Kopia's defaults).
	CacheDir             string
	ContentCacheMB       int64
	ContentCacheLimitMB  int64
	MetadataCacheMB      int64
	MetadataCacheLimitMB int64
	// Repository format, fixed at creation: Kopia's splitter (default Splitter) and content hash
	// ("" = Kopia's default, BLAKE2B-256-128). T10 measures both.
	Splitter string
	Hash     string
}

// Create initialises a new repository with the ADR-004 §2 splitter and connects to it.
func Create(ctx context.Context, o RepoOptions) error {
	st, err := filesystem.New(ctx, &filesystem.Options{Path: o.Path}, true)
	if err != nil {
		return fmt.Errorf("repository storage %s: %w", o.Path, err)
	}
	defer st.Close(ctx)
	sp := o.Splitter
	if sp == "" {
		sp = Splitter
	}
	if err := repo.Initialize(ctx, st, &repo.NewRepositoryOptions{ObjectFormat: format.ObjectFormat{Splitter: sp},
		BlockFormat: format.ContentFormat{Hash: o.Hash}}, o.Password); err != nil {
		return fmt.Errorf("initialise repository: %w", err)
	}
	return Connect(ctx, o)
}

// Connect writes the client config for an existing repository.
func Connect(ctx context.Context, o RepoOptions) error {
	st, err := filesystem.New(ctx, &filesystem.Options{Path: o.Path}, false)
	if err != nil {
		return fmt.Errorf("repository storage %s: %w", o.Path, err)
	}
	defer st.Close(ctx)
	mb := func(n int64) int64 { return n << 20 }
	err = repo.Connect(ctx, o.ConfigFile, st, o.Password, &repo.ConnectOptions{
		ClientOptions: repo.ClientOptions{Hostname: "gaia", Username: "gaia-backup"},
		CachingOptions: content.CachingOptions{
			CacheDirectory:              o.CacheDir,
			ContentCacheSizeBytes:       mb(o.ContentCacheMB),
			ContentCacheSizeLimitBytes:  mb(o.ContentCacheLimitMB),
			MetadataCacheSizeBytes:      mb(o.MetadataCacheMB),
			MetadataCacheSizeLimitBytes: mb(o.MetadataCacheLimitMB),
		},
	})
	if err != nil {
		return fmt.Errorf("connect repository: %w", err)
	}
	return nil
}

// Store is an open repository.
type Store struct {
	r repo.Repository
}

// Open opens the repository a client config points to.
func Open(ctx context.Context, configFile, password string) (*Store, error) {
	r, err := repo.Open(ctx, configFile, password, &repo.Options{})
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	return &Store{r: r}, nil
}

// Close closes the repository.
func (s *Store) Close(ctx context.Context) error { return s.r.Close(ctx) }

// Range is a byte range of the disk.
type Range struct {
	Offset uint64 `json:"offset"`
	Length uint64 `json:"length"`
}

// Source is the point-in-time disk being backed up, e.g. an NBD export.
type Source interface {
	ReadAt(p []byte, off int64) (int, error)
}

// BackupStats is what a backup read and wrote.
type BackupStats struct {
	ChangedBytes    uint64  `json:"changed_bytes"`     // sum of the ranges given as changed (dirty bitmap, or allocated data for a full)
	SourceReadBytes uint64  `json:"source_read_bytes"` // bytes read from the source
	RepoReadBytes   uint64  `json:"repo_read_bytes"`   // parent bytes read back to complete partly changed blocks
	Blocks          int     `json:"blocks"`
	BlocksWritten   int     `json:"blocks_written"`
	BlocksReused    int     `json:"blocks_reused"` // taken unchanged from the parent
	BlocksZero      int     `json:"blocks_zero"`
	Seconds         float64 `json:"seconds"`
	// The pipeline: Readers fill blocks from the source, Writers compress, encrypt and store them.
	// Busy seconds are summed over the goroutines of a stage; divided by Seconds they say how many
	// of them were working on average, which locates the bottleneck.
	Readers           int     `json:"readers"`
	Writers           int     `json:"writers"`
	Compressor        string  `json:"compressor"`
	ReadBusySeconds   float64 `json:"read_busy_seconds"`
	WriteBusySeconds  float64 `json:"write_busy_seconds"`
	ReaderWaitSeconds float64 `json:"reader_wait_seconds"` // readers waiting for a free buffer (writers behind)
}

// Point is one disk image in time.
type Point struct {
	ID               string      `json:"id"` // manifest ID, set when loaded
	Domain           string      `json:"domain"`
	Disk             string      `json:"disk"`
	Kind             string      `json:"kind"` // "full" or "incremental"
	Checkpoint       string      `json:"checkpoint"`
	ParentID         string      `json:"parent_id,omitempty"`
	ParentCheckpoint string      `json:"parent_checkpoint,omitempty"`
	Size             uint64      `json:"size"`
	BlockSize        uint64      `json:"block_size"`
	Index            string      `json:"index"` // object ID of the block index
	Created          time.Time   `json:"created"`
	Stats            BackupStats `json:"stats"`
}

type blockIndex struct {
	Size      uint64   `json:"size"`
	BlockSize uint64   `json:"block_size"`
	Blocks    []string `json:"blocks"` // object ID per block, "" = zeroes
}

// BackupRequest is one backup of one disk.
type BackupRequest struct {
	Domain, Disk, Checkpoint string
	Size                     uint64
	// Changed lists the ranges to read from the source: the dirty bitmap for an incremental,
	// the allocated (non-zero) extents for a full. Everything else is unchanged, or zero for a full.
	Changed []Range
	Parent  *Point   // nil: full backup
	Sources []Source // one per parallel reader (an NBD connection serves one request at a time)
	Writers int      // goroutines that compress, encrypt and store blocks; 0: one per source
	// BlockSize of the new point (0: the parent's for an incremental, BlockSize for a full). An incremental
	// must use its parent's.
	BlockSize uint64
	// Compressor overrides Compression; "none" stores blocks uncompressed.
	Compressor string
}

type blockJob struct {
	i      int
	ranges []Range // changed ranges inside block i, relative to the disk
}

// Backup writes a new point and returns it.
func (s *Store) Backup(ctx context.Context, req BackupRequest) (*Point, error) {
	start := time.Now()
	if len(req.Sources) == 0 {
		return nil, errors.New("no source")
	}
	var parent *blockIndex
	if req.Parent != nil {
		var err error
		if parent, err = s.loadIndex(ctx, req.Parent); err != nil {
			return nil, err
		}
	}
	bs := req.BlockSize
	switch {
	case bs != 0:
	case parent != nil:
		bs = parent.BlockSize
	default:
		bs = BlockSize
	}
	if bs < 64<<10 || bs%4096 != 0 || bs > 64<<20 {
		return nil, fmt.Errorf("block size %d: want a multiple of 4096 between 64 KiB and 64 MiB", bs)
	}
	n := int((req.Size + bs - 1) / bs)
	if parent != nil {
		if parent.Size != req.Size || parent.BlockSize != bs {
			return nil, fmt.Errorf("disk is %d bytes, parent point %d bytes (block %d): a full backup is needed",
				req.Size, parent.Size, parent.BlockSize)
		}
	}
	changed, err := normalise(req.Changed, req.Size)
	if err != nil {
		return nil, err
	}
	idx := &blockIndex{Size: req.Size, BlockSize: bs, Blocks: make([]string, n)}
	st := BackupStats{Blocks: n}
	for _, r := range changed {
		st.ChangedBytes += r.Length
	}
	var jobs []blockJob
	perBlock := splitByBlock(changed, bs)
	for i := range n {
		rs := perBlock[i]
		switch {
		case len(rs) > 0:
			jobs = append(jobs, blockJob{i, rs})
		case parent != nil:
			idx.Blocks[i] = parent.Blocks[i]
			st.BlocksReused++
		default:
			st.BlocksZero++
		}
	}

	pt := &Point{Domain: req.Domain, Disk: req.Disk, Kind: "full", Checkpoint: req.Checkpoint,
		Size: req.Size, BlockSize: bs, Created: start.UTC()}
	if req.Parent != nil {
		pt.Kind, pt.ParentID, pt.ParentCheckpoint = "incremental", req.Parent.ID, req.Parent.Checkpoint
	}
	writers := req.Writers
	if writers <= 0 {
		writers = len(req.Sources)
	}
	comp := compression.Name(Compression)
	if req.Compressor != "" {
		comp = compression.Name(req.Compressor)
	}
	st.Readers, st.Writers, st.Compressor = len(req.Sources), writers, string(comp)
	outer := ctx
	err = repo.WriteSession(ctx, s.r, repo.WriteSessionOptions{Purpose: "gaia-backup"}, func(wctx context.Context, w repo.RepositoryWriter) error {
		ctx, cancel := context.WithCancel(wctx)
		defer cancel()
		var (
			mu, emu  sync.Mutex
			firstErr error
			rwg, wwg sync.WaitGroup
			jobsCh   = make(chan blockJob)
			full     = make(chan filledBlock, writers)
			pool     = make(chan []byte, len(req.Sources)+writers)
		)
		fail := func(err error) {
			emu.Lock()
			defer emu.Unlock()
			if firstErr == nil {
				firstErr = err
				cancel()
			}
		}
		for range cap(pool) {
			pool <- make([]byte, bs)
		}
		for _, src := range req.Sources {
			rwg.Add(1)
			go func() {
				defer rwg.Done()
				for j := range jobsCh {
					t := time.Now()
					var buf []byte
					select {
					case buf = <-pool:
					case <-ctx.Done():
						return
					}
					waited := time.Since(t)
					t = time.Now()
					b, srcBytes, repoBytes, err := fillBlock(ctx, w, src, parent, req.Size, bs, j, buf)
					busy := time.Since(t)
					mu.Lock()
					st.ReaderWaitSeconds += waited.Seconds()
					st.ReadBusySeconds += busy.Seconds()
					st.SourceReadBytes += srcBytes
					st.RepoReadBytes += repoBytes
					mu.Unlock()
					if err != nil {
						pool <- buf
						fail(err)
						return
					}
					select {
					case full <- filledBlock{i: j.i, b: b, buf: buf}:
					case <-ctx.Done():
						pool <- buf
						return
					}
				}
			}()
		}
		for range writers {
			wwg.Add(1)
			go func() {
				defer wwg.Done()
				for f := range full { // keeps draining after an error, so readers never block on a full channel
					if ctx.Err() != nil {
						pool <- f.buf
						continue
					}
					t := time.Now()
					id, err := storeBlock(ctx, w, f.b, comp)
					busy := time.Since(t)
					pool <- f.buf
					if err != nil {
						fail(fmt.Errorf("block %d: write: %w", f.i, err))
						continue
					}
					mu.Lock()
					st.WriteBusySeconds += busy.Seconds()
					idx.Blocks[f.i] = id
					if id == "" {
						st.BlocksZero++
					} else {
						st.BlocksWritten++
					}
					mu.Unlock()
				}
			}()
		}
	feed:
		for _, j := range jobs {
			select {
			case jobsCh <- j:
			case <-ctx.Done():
				break feed
			}
		}
		close(jobsCh)
		rwg.Wait()
		close(full)
		wwg.Wait()
		if err := errors.Join(firstErr, outer.Err()); err != nil {
			return err
		}
		indexID, err := writeJSON(ctx, w, "gaia block index", idx)
		if err != nil {
			return err
		}
		pt.Index = indexID
		st.Seconds = time.Since(start).Seconds()
		pt.Stats = st
		mid, err := w.PutManifest(ctx, map[string]string{
			manifest.TypeLabelKey: pointType, "domain": req.Domain, "disk": req.Disk, "checkpoint": req.Checkpoint,
		}, pt)
		if err != nil {
			return fmt.Errorf("write point manifest: %w", err)
		}
		pt.ID = string(mid)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pt, nil
}

type filledBlock struct {
	i   int
	b   []byte // the block's bytes, a prefix of buf
	buf []byte // goes back to the pool once the block is stored
}

// fillBlock assembles block j.i in buf: the parent's copy (or zeroes) with the changed ranges read from src on top.
func fillBlock(ctx context.Context, w repo.RepositoryWriter, src Source, parent *blockIndex, size, bs uint64, j blockJob, buf []byte) (b []byte, srcBytes, repoBytes uint64, err error) {
	bStart := uint64(j.i) * bs
	b = buf[:min(bs, size-bStart)]
	covered := uint64(0)
	for _, r := range j.ranges {
		covered += r.Length
	}
	if covered < uint64(len(b)) { // partly changed: start from what the parent had
		clear(b)
		if parent != nil && parent.Blocks[j.i] != "" {
			if err := readObject(ctx, w, parent.Blocks[j.i], b); err != nil {
				return nil, 0, 0, fmt.Errorf("block %d: parent copy: %w", j.i, err)
			}
			repoBytes = uint64(len(b))
		}
	}
	for _, r := range j.ranges {
		p := b[r.Offset-bStart : r.Offset-bStart+r.Length]
		if _, err := src.ReadAt(p, int64(r.Offset)); err != nil {
			return nil, srcBytes, repoBytes, fmt.Errorf("block %d: read [%d, +%d): %w", j.i, r.Offset, r.Length, err)
		}
		srcBytes += r.Length
	}
	return b, srcBytes, repoBytes, nil
}

// storeBlock writes one block as a Kopia object ("" for a block of zeroes, which is not stored).
func storeBlock(ctx context.Context, w repo.RepositoryWriter, b []byte, comp compression.Name) (string, error) {
	if isZero(b) {
		return "", nil
	}
	ow := w.NewObjectWriter(ctx, object.WriterOptions{Description: "gaia disk block", Compressor: comp})
	defer ow.Close()
	if _, err := ow.Write(b); err != nil {
		return "", err
	}
	oid, err := ow.Result()
	if err != nil {
		return "", err
	}
	return oid.String(), nil
}

// ReadStats is what ReadRanges read.
type ReadStats struct {
	Bytes   uint64  `json:"bytes"`
	Blocks  int     `json:"blocks"`
	Readers int     `json:"readers"`
	Seconds float64 `json:"seconds"`
}

// ReadRanges reads exactly what Backup would read from the sources for these changed ranges, in the
// same blocks (blockSize, 0 = BlockSize) and with one reader per source, and discards it: the
// hash-free reference of ADR-004 A1 for the backup's throughput.
func ReadRanges(ctx context.Context, changed []Range, size, blockSize uint64, sources []Source) (ReadStats, error) {
	start := time.Now()
	if blockSize == 0 {
		blockSize = BlockSize
	}
	rs, err := normalise(changed, size)
	if err != nil {
		return ReadStats{}, err
	}
	perBlock := splitByBlock(rs, blockSize)
	idx := make([]int, 0, len(perBlock))
	for i := range perBlock {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	st := ReadStats{Blocks: len(idx), Readers: len(sources)}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		next = make(chan int)
		errs = make(chan error, len(sources))
	)
	for _, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, blockSize)
			for i := range next {
				var n uint64
				for _, r := range perBlock[i] {
					if _, err := src.ReadAt(buf[:r.Length], int64(r.Offset)); err != nil {
						errs <- fmt.Errorf("read [%d, +%d): %w", r.Offset, r.Length, err)
						for range next {
						}
						return
					}
					n += r.Length
				}
				mu.Lock()
				st.Bytes += n
				mu.Unlock()
			}
		}()
	}
	for _, i := range idx {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	close(errs)
	if err := errors.Join(ctx.Err(), <-errs); err != nil {
		return st, err
	}
	st.Seconds = time.Since(start).Seconds()
	return st, nil
}

// RestoreStats is what a restore read and wrote.
type RestoreStats struct {
	RepoReadBytes uint64  `json:"repo_read_bytes"`
	WrittenBytes  uint64  `json:"written_bytes"`
	BlocksWritten int     `json:"blocks_written"`
	BlocksHole    int     `json:"blocks_hole"` // zero blocks left as holes in the output file
	Seconds       float64 `json:"seconds"`
}

// Restore writes point p as a raw image to path (sparse: zero blocks become holes), with
// workers parallel block readers (T5: one reader is bound to about one CPU core), and fsyncs it.
func (s *Store) Restore(ctx context.Context, p *Point, path string, workers int) (RestoreStats, error) {
	start := time.Now()
	idx, err := s.loadIndex(ctx, p)
	if err != nil {
		return RestoreStats{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return RestoreStats{}, err
	}
	defer f.Close()
	if err := f.Truncate(int64(idx.Size)); err != nil {
		return RestoreStats{}, err
	}
	var (
		st   RestoreStats
		mu   sync.Mutex
		wg   sync.WaitGroup
		next = make(chan int)
		errs = make(chan error, max(workers, 1))
	)
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, idx.BlockSize)
			for i := range next {
				b := buf[:min(idx.BlockSize, idx.Size-uint64(i)*idx.BlockSize)]
				if err := readObject(ctx, s.r, idx.Blocks[i], b); err != nil {
					errs <- fmt.Errorf("block %d: %w", i, err)
					for range next {
					}
					return
				}
				zero := isZero(b)
				if !zero {
					if _, err := f.WriteAt(b, int64(i)*int64(idx.BlockSize)); err != nil {
						errs <- err
						for range next {
						}
						return
					}
				}
				mu.Lock()
				st.RepoReadBytes += uint64(len(b))
				if zero {
					st.BlocksHole++
				} else {
					st.BlocksWritten++
					st.WrittenBytes += uint64(len(b))
				}
				mu.Unlock()
			}
		}()
	}
	for i, id := range idx.Blocks {
		if ctx.Err() != nil {
			break
		}
		if id == "" {
			st.BlocksHole++
			continue
		}
		next <- i
	}
	close(next)
	wg.Wait()
	close(errs)
	if err := errors.Join(ctx.Err(), <-errs); err != nil {
		return st, err
	}
	if err := f.Sync(); err != nil {
		return st, err
	}
	st.Seconds = time.Since(start).Seconds()
	return st, f.Close()
}

// Points lists the points of a domain's disk (both "" = all), oldest first.
func (s *Store) Points(ctx context.Context, domain, disk string) ([]*Point, error) {
	labels := map[string]string{manifest.TypeLabelKey: pointType}
	if domain != "" {
		labels["domain"] = domain
	}
	if disk != "" {
		labels["disk"] = disk
	}
	ms, err := s.r.FindManifests(ctx, labels)
	if err != nil {
		return nil, err
	}
	var out []*Point
	for _, m := range ms {
		p, err := s.Point(ctx, string(m.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *Point) int { return a.Created.Compare(b.Created) })
	return out, nil
}

// Point loads one point by manifest ID.
func (s *Store) Point(ctx context.Context, id string) (*Point, error) {
	var p Point
	if _, err := s.r.GetManifest(ctx, manifest.ID(id), &p); err != nil {
		return nil, fmt.Errorf("point %s: %w", id, err)
	}
	p.ID = id
	return &p, nil
}

func (s *Store) loadIndex(ctx context.Context, p *Point) (*blockIndex, error) {
	oid, err := object.ParseID(p.Index)
	if err != nil {
		return nil, fmt.Errorf("point %s: index ID: %w", p.ID, err)
	}
	r, err := s.r.OpenObject(ctx, oid)
	if err != nil {
		return nil, fmt.Errorf("point %s: index: %w", p.ID, err)
	}
	defer r.Close()
	var idx blockIndex
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, fmt.Errorf("point %s: index: %w", p.ID, err)
	}
	if idx.BlockSize == 0 || uint64(len(idx.Blocks)) != (idx.Size+idx.BlockSize-1)/idx.BlockSize {
		return nil, fmt.Errorf("point %s: index has %d blocks for %d bytes", p.ID, len(idx.Blocks), idx.Size)
	}
	return &idx, nil
}

type objectOpener interface {
	OpenObject(ctx context.Context, id object.ID) (object.Reader, error)
}

// readObject fills b with object id ("" = zeroes) and checks that the object is exactly len(b) bytes.
func readObject(ctx context.Context, r objectOpener, id string, b []byte) error {
	if id == "" {
		clear(b)
		return nil
	}
	oid, err := object.ParseID(id)
	if err != nil {
		return err
	}
	or, err := r.OpenObject(ctx, oid)
	if err != nil {
		return err
	}
	defer or.Close()
	if or.Length() != int64(len(b)) {
		return fmt.Errorf("object %s is %d bytes, expected %d", id, or.Length(), len(b))
	}
	_, err = io.ReadFull(or, b)
	return err
}

func writeJSON(ctx context.Context, w repo.RepositoryWriter, desc string, v any) (string, error) {
	ow := w.NewObjectWriter(ctx, object.WriterOptions{Description: desc, Compressor: Compression})
	defer ow.Close()
	if err := json.NewEncoder(ow).Encode(v); err != nil {
		return "", err
	}
	oid, err := ow.Result()
	if err != nil {
		return "", err
	}
	return oid.String(), nil
}

// normalise sorts and merges ranges and checks they lie inside the disk.
func normalise(in []Range, size uint64) ([]Range, error) {
	rs := slices.Clone(in)
	slices.SortFunc(rs, func(a, b Range) int { return cmpU64(a.Offset, b.Offset) })
	var out []Range
	for _, r := range rs {
		if r.Length == 0 {
			continue
		}
		if r.Offset+r.Length > size || r.Offset+r.Length < r.Offset {
			return nil, fmt.Errorf("range [%d, +%d) outside disk of %d bytes", r.Offset, r.Length, size)
		}
		if n := len(out); n > 0 && r.Offset <= out[n-1].Offset+out[n-1].Length {
			out[n-1].Length = max(out[n-1].Offset+out[n-1].Length, r.Offset+r.Length) - out[n-1].Offset
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// splitByBlock cuts sorted, merged ranges at block boundaries (blocks of bs): block index -> its ranges.
func splitByBlock(rs []Range, bs uint64) map[int][]Range {
	out := map[int][]Range{}
	for _, r := range rs {
		for off, end := r.Offset, r.Offset+r.Length; off < end; {
			i := int(off / bs)
			bEnd := min(end, uint64(i+1)*bs)
			out[i] = append(out[i], Range{off, bEnd - off})
			off = bEnd
		}
	}
	return out
}

func cmpU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func isZero(b []byte) bool {
	var zero [64 << 10]byte
	for len(b) > 0 {
		n := min(len(b), len(zero))
		if !bytes.Equal(b[:n], zero[:n]) {
			return false
		}
		b = b[n:]
	}
	return true
}
