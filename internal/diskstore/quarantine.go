package diskstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/repo/object"
)

// ADR-007 B3: a damaged content stays in Kopia's index, and deduplication would let every later backup of the same data
// point at it. Quarantine marks it deleted in the index, so the next write of the same data stores a fresh copy (Kopia
// rewrites a deleted content instead of deduplicating onto it), and records it in a manifest of its own, so Gaia's
// garbage collection does not restore it as "referenced". The pack keeps the damaged bytes until Kopia's maintenance
// drops the deleted entry and the pack holds nothing else alive.
const quarantineType = "gaia-quarantine"

// Damage is a content that cannot be read back as it was written. Pack is empty when the content is not in the index at
// all.
type Damage struct {
	Content string `json:"content"`
	Pack    string `json:"pack"`
	Reason  string `json:"reason"`
}

// ErrStorage marks a verification that could not read the repository's storage, as opposed to reading damaged data:
// nothing is quarantined on a storage error, since the data may be fine.
var ErrStorage = errors.New("repository storage")

// diagnose finds out why object id could not be read. It returns the damaged contents, or an error wrapping ErrStorage
// when the storage itself failed. An empty list without an error means every content read back fine this time.
func (s *Store) diagnose(ctx context.Context, id string) ([]Damage, error) {
	oid, err := object.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("object ID %q: %w", id, err)
	}
	return s.diagnoseObject(ctx, oid)
}

func (s *Store) diagnoseObject(ctx context.Context, oid object.ID) ([]Damage, error) {
	if cid, _, ok := oid.ContentID(); ok {
		d, err := s.diagnoseContent(ctx, cid)
		if d == nil || err != nil {
			return nil, err
		}
		return []Damage{*d}, nil
	}
	// An indirect object: its index object first, since its entries are unknown while it is unreadable.
	inner, _ := oid.IndexObjectID()
	if ds, err := s.diagnoseObject(ctx, inner); err != nil || len(ds) > 0 {
		return ds, err
	}
	cids, err := s.r.VerifyObject(ctx, oid)
	if err != nil {
		return nil, fmt.Errorf("%w: listing the contents of object %v: %v", ErrStorage, oid, err)
	}
	var out []Damage
	for _, cid := range cids {
		d, err := s.diagnoseContent(ctx, cid)
		if err != nil {
			return nil, err
		}
		if d != nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

// diagnoseContent reads one content. When that fails it fetches the content's bytes from its pack without decoding
// them: if those arrive, the stored bytes are what is wrong (damage); if the pack is gone, the content is lost (damage);
// any other failure is the storage's.
func (s *Store) diagnoseContent(ctx context.Context, cid content.ID) (*Damage, error) {
	dr, ok := s.r.(repo.DirectRepository)
	if !ok {
		return nil, errors.New("diagnosis needs a direct repository")
	}
	ci, err := dr.ContentInfo(ctx, cid)
	if errors.Is(err, content.ErrContentNotFound) {
		return &Damage{Content: cid.String(), Reason: "not in the repository's index"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: content %v: %v", ErrStorage, cid, err)
	}
	_, rerr := dr.ContentReader().GetContent(ctx, cid)
	if rerr == nil {
		return nil, nil
	}
	var raw rawBuffer
	err = dr.BlobReader().GetBlob(ctx, ci.PackBlobID, int64(ci.PackOffset), int64(ci.PackedLength), &raw)
	d := &Damage{Content: cid.String(), Pack: string(ci.PackBlobID)}
	switch {
	case errors.Is(err, blob.ErrBlobNotFound):
		d.Reason = fmt.Sprintf("pack %s is missing", ci.PackBlobID)
	case errors.Is(err, blob.ErrInvalidRange):
		d.Reason = fmt.Sprintf("pack %s is shorter than the index says", ci.PackBlobID)
	case err != nil:
		return nil, fmt.Errorf("%w: pack %s: %v", ErrStorage, ci.PackBlobID, err)
	case raw.Length() != int(ci.PackedLength):
		d.Reason = fmt.Sprintf("pack %s gave %d of %d bytes", ci.PackBlobID, raw.Length(), ci.PackedLength)
	default:
		d.Reason = fmt.Sprintf("stored bytes do not verify: %v", rerr)
	}
	return d, nil
}

// rawBuffer is a blob.OutputBuffer.
type rawBuffer struct{ bytes.Buffer }

func (b *rawBuffer) Length() int { return b.Len() }

// Quarantine is one quarantine, as recorded in the repository.
type Quarantine struct {
	ID       string    `json:"id"`
	Contents []Damage  `json:"contents"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

// Quarantine takes damaged contents out of deduplication (see quarantineType) and records why. Contents that are not in
// the index any more, or already marked deleted, need no change but are recorded all the same.
func (s *Store) Quarantine(ctx context.Context, ds []Damage, reason string) (Quarantine, error) {
	q := Quarantine{Contents: ds, Reason: reason, At: time.Now().UTC()}
	if len(ds) == 0 {
		return q, errors.New("nothing to quarantine")
	}
	dr, ok := s.r.(repo.DirectRepository)
	if !ok {
		return q, errors.New("quarantine needs a direct repository")
	}
	err := repo.DirectWriteSession(ctx, dr, repo.WriteSessionOptions{Purpose: "gaia-quarantine"}, func(wctx context.Context, w repo.DirectRepositoryWriter) error {
		for _, d := range ds {
			cid, err := content.ParseID(d.Content)
			if err != nil {
				return fmt.Errorf("content ID %q: %w", d.Content, err)
			}
			ci, err := w.ContentInfo(wctx, cid)
			if errors.Is(err, content.ErrContentNotFound) || (err == nil && ci.Deleted) {
				continue
			}
			if err != nil {
				return fmt.Errorf("content %v: %w", cid, err)
			}
			if err := w.ContentManager().DeleteContent(wctx, cid); err != nil {
				return fmt.Errorf("quarantine content %v: %w", cid, err)
			}
		}
		id, err := w.PutManifest(wctx, map[string]string{manifest.TypeLabelKey: quarantineType}, &q)
		q.ID = string(id)
		return err
	})
	return q, err
}

// Quarantines lists the repository's quarantines, oldest first.
func (s *Store) Quarantines(ctx context.Context) ([]Quarantine, error) {
	return quarantines(ctx, s.r)
}

func quarantines(ctx context.Context, r repo.Repository) ([]Quarantine, error) {
	ms, err := r.FindManifests(ctx, map[string]string{manifest.TypeLabelKey: quarantineType})
	if err != nil {
		return nil, err
	}
	out := make([]Quarantine, 0, len(ms))
	for _, m := range ms {
		var q Quarantine
		if _, err := r.GetManifest(ctx, m.ID, &q); err != nil {
			return nil, fmt.Errorf("quarantine %s: %w", m.ID, err)
		}
		q.ID = string(m.ID)
		out = append(out, q)
	}
	slices.SortFunc(out, func(a, b Quarantine) int { return a.At.Compare(b.At) })
	return out, nil
}

// quarantinedPacks maps each quarantined content to the pack its damaged copy is in.
func quarantinedPacks(ctx context.Context, r repo.Repository) (map[content.ID]blob.ID, error) {
	qs, err := quarantines(ctx, r)
	if err != nil {
		return nil, err
	}
	out := map[content.ID]blob.ID{}
	for _, q := range qs {
		for _, d := range q.Contents {
			cid, err := content.ParseID(d.Content)
			if err != nil {
				return nil, fmt.Errorf("quarantine %s: content ID %q: %w", q.ID, d.Content, err)
			}
			if d.Pack != "" {
				out[cid] = blob.ID(d.Pack)
			}
		}
	}
	return out, nil
}

// PointsUsing returns, for every point of the repository that references one of the given contents, the ones it
// references. A sampled verification reads only some blocks: this finds every point a damaged content affects.
// A point whose block index cannot be read is listed with the reason instead.
func (s *Store) PointsUsing(ctx context.Context, contents []string) (map[string][]string, error) {
	want := map[content.ID]bool{}
	for _, c := range contents {
		cid, err := content.ParseID(c)
		if err != nil {
			return nil, fmt.Errorf("content ID %q: %w", c, err)
		}
		want[cid] = true
	}
	ms, err := s.r.FindManifests(ctx, map[string]string{manifest.TypeLabelKey: pointType})
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, m := range ms {
		p, err := s.Point(ctx, string(m.ID))
		if err != nil {
			return nil, err
		}
		hit := map[content.ID]bool{}
		objects := []string{p.Index}
		idx, err := s.loadIndex(ctx, p)
		if err == nil {
			objects = append(objects, idx.Blocks...)
		}
		for _, b := range objects {
			if b == "" {
				continue
			}
			cids, err := s.objectContents(ctx, b)
			if err != nil {
				return nil, fmt.Errorf("point %s: %w", m.ID, err)
			}
			for _, c := range cids {
				if want[c] {
					hit[c] = true
				}
			}
		}
		if len(hit) > 0 {
			out[string(m.ID)] = sortedIDs(hit)
		}
	}
	return out, nil
}

// objectContents lists the contents behind an object without asking whether they still exist: a direct object (every
// block of a point) is its own content; an indirect one is resolved through its index, and a damaged index gives the
// index's own content only.
func (s *Store) objectContents(ctx context.Context, id string) ([]content.ID, error) {
	oid, err := object.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("object ID %q: %w", id, err)
	}
	if cid, _, ok := oid.ContentID(); ok {
		return []content.ID{cid}, nil
	}
	cids, err := s.r.VerifyObject(ctx, oid)
	if err == nil {
		return cids, nil
	}
	ds, derr := s.diagnose(ctx, id)
	if derr != nil {
		return nil, derr
	}
	if len(ds) == 0 {
		return nil, fmt.Errorf("object %s: %w", id, err)
	}
	var out []content.ID
	for _, d := range ds {
		cid, err := content.ParseID(d.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, nil
}

func sortedIDs(m map[content.ID]bool) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c.String())
	}
	slices.Sort(out)
	return out
}
