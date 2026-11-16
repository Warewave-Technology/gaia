package diskstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"time"

	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/maintenancestats"
	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/repo/object"
)

// DeletePoint removes a point's manifest. Its blocks stay until garbage collection finds them unreferenced.
func (s *Store) DeletePoint(ctx context.Context, id string) error {
	return repo.WriteSession(ctx, s.r, repo.WriteSessionOptions{Purpose: "gaia-retention"}, func(wctx context.Context, w repo.RepositoryWriter) error {
		return w.DeleteManifest(wctx, manifest.ID(id))
	})
}

// PointCheck is the post-backup check of one point (core matrix #1): its manifest and block index read back,
// every block's contents present in the repository's index, and every block of the size the index implies.
type PointCheck struct {
	Point      string   `json:"point"`
	Blocks     int      `json:"blocks"`
	ZeroBlocks int      `json:"zero_blocks"`
	Contents   int      `json:"contents"`
	Problems   []string `json:"problems"` // empty = OK
}

// CheckPoint checks a point without reading its data.
func (s *Store) CheckPoint(ctx context.Context, id string) (PointCheck, error) {
	r := PointCheck{Point: id, Problems: []string{}}
	p, err := s.Point(ctx, id)
	if err != nil {
		r.Problems = append(r.Problems, "manifest: "+err.Error())
		return r, nil
	}
	idx, err := s.loadIndex(ctx, p)
	if err != nil {
		r.Problems = append(r.Problems, "block index: "+err.Error())
		return r, nil
	}
	if idx.Size != p.Size || idx.BlockSize != p.BlockSize {
		r.Problems = append(r.Problems, fmt.Sprintf("block index says %d bytes in %d-byte blocks, the manifest %d in %d", idx.Size, idx.BlockSize, p.Size, p.BlockSize))
	}
	r.Blocks = len(idx.Blocks)
	for i, b := range idx.Blocks {
		if b == "" {
			r.ZeroBlocks++
			continue
		}
		want := int64(min(idx.BlockSize, idx.Size-uint64(i)*idx.BlockSize))
		oid, err := object.ParseID(b)
		if err != nil {
			r.Problems = append(r.Problems, fmt.Sprintf("block %d: object ID %q: %v", i, b, err))
			continue
		}
		cids, err := s.r.VerifyObject(ctx, oid)
		if err != nil {
			r.Problems = append(r.Problems, fmt.Sprintf("block %d: %v", i, err))
			continue
		}
		var length int64
		for _, cid := range cids {
			ci, err := s.r.ContentInfo(ctx, cid)
			if err != nil {
				r.Problems = append(r.Problems, fmt.Sprintf("block %d: content %v: %v", i, cid, err))
				continue
			}
			if ci.Deleted {
				r.Problems = append(r.Problems, fmt.Sprintf("block %d: content %v is marked deleted", i, cid))
			}
			r.Contents++
			if cid.Prefix() != content.IDPrefix("x") { // an indirect object's own index is not data
				length += int64(ci.OriginalLength)
			}
		}
		if length != want {
			r.Problems = append(r.Problems, fmt.Sprintf("block %d is %d bytes, expected %d", i, length, want))
		}
	}
	return r, nil
}

// PointVerify is the result of reading a point's blocks back (core matrix #2).
type PointVerify struct {
	Point     string   `json:"point"`
	Blocks    int      `json:"blocks"`     // non-zero blocks in the point
	Read      int      `json:"read"`       // blocks read
	BytesRead int64    `json:"bytes_read"` // their size
	Problems  []string `json:"problems"`   // unreadable or wrong-size blocks; empty = OK
}

// VerifyPoint reads a fraction of a point's non-zero blocks (1 = all) through the repository, which checks every
// content's authenticated encryption on the way. Open the store with OpenUncached, or a cached content hides a
// corrupted one.
func (s *Store) VerifyPoint(ctx context.Context, id string, fraction float64, rng *rand.Rand) (PointVerify, error) {
	r := PointVerify{Point: id, Problems: []string{}}
	p, err := s.Point(ctx, id)
	if err != nil {
		r.Problems = append(r.Problems, "manifest: "+err.Error())
		return r, nil
	}
	idx, err := s.loadIndex(ctx, p)
	if err != nil {
		r.Problems = append(r.Problems, "block index: "+err.Error())
		return r, nil
	}
	var nonzero []int
	for i, b := range idx.Blocks {
		if b != "" {
			nonzero = append(nonzero, i)
		}
	}
	r.Blocks = len(nonzero)
	pick := nonzero
	if fraction < 1 && len(nonzero) > 0 {
		n := max(1, int(float64(len(nonzero))*fraction+0.5))
		rng.Shuffle(len(nonzero), func(a, b int) { nonzero[a], nonzero[b] = nonzero[b], nonzero[a] })
		pick = nonzero[:n]
	}
	for _, i := range pick {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		want := int64(min(idx.BlockSize, idx.Size-uint64(i)*idx.BlockSize))
		n, err := s.readAll(ctx, idx.Blocks[i])
		r.Read++
		r.BytesRead += n
		switch {
		case err != nil:
			r.Problems = append(r.Problems, fmt.Sprintf("block %d (%s): %v", i, idx.Blocks[i], err))
		case n != want:
			r.Problems = append(r.Problems, fmt.Sprintf("block %d is %d bytes, expected %d", i, n, want))
		}
	}
	return r, nil
}

func (s *Store) readAll(ctx context.Context, id string) (int64, error) {
	oid, err := object.ParseID(id)
	if err != nil {
		return 0, err
	}
	or, err := s.r.OpenObject(ctx, oid)
	if err != nil {
		return 0, err
	}
	defer or.Close()
	return io.Copy(io.Discard, or)
}

// MaintenanceResult says what a maintenance run did.
type MaintenanceResult struct {
	Mode       string                            `json:"mode"` // quick, full, or "" when none was due
	GC         *maintenancestats.SnapshotGCStats `json:"gc"`   // Gaia's garbage collection (full mode only)
	Referenced int                               `json:"referenced_contents"`
	Seconds    float64                           `json:"seconds"`
}

// Maintain runs Kopia's maintenance as it is due (quick hourly, full daily), with Gaia's own garbage collection in
// place of Kopia's snapshot GC: Gaia's points and archives are not Kopia snapshots, so snapshot GC would find every
// block unreferenced and delete it. Gaia's GC follows snapshot GC's rules exactly (in-use contents from Gaia's
// manifests, manifests themselves kept, contents younger than the safety margin kept, a referenced content that was
// marked deleted restored) and records itself under the same task, so Kopia's later steps keep their safety.
// It must not run while a backup writes to the repository.
func (s *Store) Maintain(ctx context.Context, force bool, safety maintenance.SafetyParameters) (MaintenanceResult, error) {
	start := time.Now()
	res := MaintenanceResult{}
	dr, ok := s.r.(repo.DirectRepository)
	if !ok {
		return res, errors.New("maintenance needs a direct repository")
	}
	ctx, w, err := dr.NewDirectWriter(ctx, repo.WriteSessionOptions{Purpose: "gaia-maintenance"})
	if err != nil {
		return res, err
	}
	defer w.Close(ctx)
	// Maintenance runs only for its owner: a repository Gaia created is owned by Gaia's client identity.
	p, err := maintenance.GetParams(ctx, w)
	if err != nil {
		return res, err
	}
	if p.Owner == "" {
		def := maintenance.DefaultParams()
		def.Owner = w.ClientOptions().UsernameAtHost()
		if err := maintenance.SetParams(ctx, w, &def); err != nil {
			return res, fmt.Errorf("set maintenance owner: %w", err)
		}
		if err := w.Flush(ctx); err != nil {
			return res, err
		}
	}
	mode := maintenance.ModeAuto
	if force {
		mode = maintenance.ModeFull
	}
	err = maintenance.RunExclusive(ctx, w, mode, force, func(ctx context.Context, rp maintenance.RunParameters) error {
		res.Mode = string(rp.Mode)
		if rp.Mode == maintenance.ModeFull {
			if err := maintenance.ReportRun(ctx, w, maintenance.TaskSnapshotGarbageCollection, nil, func() (maintenancestats.Kind, error) {
				st, n, err := s.gc(ctx, w, safety, rp.MaintenanceStartTime)
				res.GC, res.Referenced = st, n
				return st, err
			}); err != nil {
				return fmt.Errorf("Gaia garbage collection: %w", err)
			}
		}
		return maintenance.Run(ctx, rp, safety)
	})
	if err == nil {
		err = w.Flush(ctx)
	}
	res.Seconds = time.Since(start).Seconds()
	return res, err
}

// referenced collects the contents reachable from Gaia's manifests: every point's block index and blocks, and every
// archive's stream.
func (s *Store) referenced(ctx context.Context, r repo.Repository) (map[content.ID]struct{}, error) {
	used := map[content.ID]struct{}{}
	seen := map[string]bool{}
	add := func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		seen[id] = true
		oid, err := object.ParseID(id)
		if err != nil {
			return err
		}
		cids, err := r.VerifyObject(ctx, oid)
		if err != nil {
			return fmt.Errorf("object %s: %w", id, err)
		}
		for _, c := range cids {
			used[c] = struct{}{}
		}
		return nil
	}
	ms, err := r.FindManifests(ctx, map[string]string{manifest.TypeLabelKey: pointType})
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		p, err := s.Point(ctx, string(m.ID))
		if err != nil {
			return nil, err
		}
		if err := add(p.Index); err != nil {
			return nil, fmt.Errorf("point %s: %w", m.ID, err)
		}
		idx, err := s.loadIndex(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, b := range idx.Blocks {
			if err := add(b); err != nil {
				return nil, fmt.Errorf("point %s: %w", m.ID, err)
			}
		}
	}
	as, err := r.FindManifests(ctx, map[string]string{manifest.TypeLabelKey: archiveType})
	if err != nil {
		return nil, err
	}
	for _, m := range as {
		var a Archive
		if _, err := r.GetManifest(ctx, m.ID, &a); err != nil {
			return nil, err
		}
		if err := add(a.ObjectID); err != nil {
			return nil, fmt.Errorf("archive %s: %w", m.ID, err)
		}
	}
	return used, nil
}

func (s *Store) gc(ctx context.Context, w repo.DirectRepositoryWriter, safety maintenance.SafetyParameters, start time.Time) (*maintenancestats.SnapshotGCStats, int, error) {
	st := &maintenancestats.SnapshotGCStats{}
	used, err := s.referenced(ctx, w)
	if err != nil {
		return st, 0, err
	}
	err = w.ContentReader().IterateContents(ctx, content.IterateOptions{IncludeDeleted: true}, func(ci content.Info) error {
		size := uint64(ci.PackedLength)
		if ci.ContentID.Prefix() == manifest.ContentPrefix {
			st.InUseSystemContentCount++
			st.InUseSystemContentSize += size
			return nil
		}
		if _, ok := used[ci.ContentID]; ok {
			if ci.Deleted {
				if err := w.ContentManager().UndeleteContent(ctx, ci.ContentID); err != nil {
					return fmt.Errorf("undelete referenced content %v: %w", ci.ContentID, err)
				}
				st.RecoveredContentCount++
				st.RecoveredContentSize += size
			}
			st.InUseContentCount++
			st.InUseContentSize += size
			return nil
		}
		if start.Sub(ci.Timestamp()) < safety.MinContentAgeSubjectToGC {
			st.UnreferencedRecentContentCount++
			st.UnreferencedRecentContentSize += size
			return nil
		}
		st.UnreferencedContentCount++
		st.UnreferencedContentSize += size
		if !ci.Deleted {
			if err := w.ContentManager().DeleteContent(ctx, ci.ContentID); err != nil {
				return fmt.Errorf("delete content %v: %w", ci.ContentID, err)
			}
			st.DeletedContentCount++
			st.DeletedContentSize += size
		}
		return nil
	})
	return st, len(used), err
}

// OpenUncached opens the repository through a client config of its own whose content and metadata caches are off,
// so every read comes from the repository's storage (for verification).
func OpenUncached(ctx context.Context, o RepoOptions) (*Store, error) {
	o.ConfigFile += ".uncached"
	o.CacheDir += ".uncached"
	o.ContentCacheMB, o.ContentCacheLimitMB, o.MetadataCacheMB, o.MetadataCacheLimitMB = 0, 0, 0, 0
	if _, err := os.Stat(o.ConfigFile); errors.Is(err, os.ErrNotExist) {
		if err := Connect(ctx, o); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return Open(ctx, o.ConfigFile, o.Password)
}
