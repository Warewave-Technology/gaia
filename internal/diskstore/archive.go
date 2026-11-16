package diskstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/repo/object"
)

const archiveType = "gaia-archive"

// Archive is a stream stored whole in the repository, e.g. a backup of Gaia's own state (ADR-007 §2).
type Archive struct {
	ID       string            `json:"id"`        // manifest ID
	Kind     string            `json:"kind"`      // e.g. "state"
	ObjectID string            `json:"object_id"` // the stream
	Bytes    int64             `json:"bytes"`
	SHA256   string            `json:"sha256"` // of the stream, as written
	Created  time.Time         `json:"created"`
	Labels   map[string]string `json:"labels"`
}

// PutArchive stores the stream r as one object and records it with a manifest of the given kind and labels.
func (s *Store) PutArchive(ctx context.Context, kind string, r io.Reader, labels map[string]string) (Archive, error) {
	a := Archive{Kind: kind, Created: time.Now().UTC(), Labels: labels}
	err := repo.WriteSession(ctx, s.r, repo.WriteSessionOptions{Purpose: "gaia-archive"}, func(wctx context.Context, w repo.RepositoryWriter) error {
		ow := w.NewObjectWriter(wctx, object.WriterOptions{Description: "gaia " + kind + " archive", Compressor: Compression})
		defer ow.Close()
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(ow, h), r)
		if err != nil {
			return err
		}
		oid, err := ow.Result()
		if err != nil {
			return err
		}
		a.ObjectID, a.Bytes, a.SHA256 = oid.String(), n, hex.EncodeToString(h.Sum(nil))
		ml := map[string]string{manifest.TypeLabelKey: archiveType, "kind": kind}
		for k, v := range labels {
			if _, taken := ml[k]; !taken {
				ml[k] = v
			}
		}
		mid, err := w.PutManifest(wctx, ml, &a)
		a.ID = string(mid)
		return err
	})
	if err != nil {
		return Archive{}, fmt.Errorf("store %s archive: %w", kind, err)
	}
	return a, nil
}

// Archives lists the archives of a kind, oldest first.
func (s *Store) Archives(ctx context.Context, kind string) ([]Archive, error) {
	ms, err := s.r.FindManifests(ctx, map[string]string{manifest.TypeLabelKey: archiveType, "kind": kind})
	if err != nil {
		return nil, err
	}
	var out []Archive
	for _, m := range ms {
		var a Archive
		if _, err := s.r.GetManifest(ctx, m.ID, &a); err != nil {
			return nil, fmt.Errorf("archive %s: %w", m.ID, err)
		}
		a.ID = string(m.ID)
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Archive) int { return a.Created.Compare(b.Created) })
	return out, nil
}

// ReadArchive copies an archive's stream to w and checks it against the recorded size and sha256.
func (s *Store) ReadArchive(ctx context.Context, id string, w io.Writer) (Archive, error) {
	var a Archive
	if _, err := s.r.GetManifest(ctx, manifest.ID(id), &a); err != nil {
		return a, fmt.Errorf("archive %s: %w", id, err)
	}
	a.ID = id
	oid, err := object.ParseID(a.ObjectID)
	if err != nil {
		return a, err
	}
	or, err := s.r.OpenObject(ctx, oid)
	if err != nil {
		return a, err
	}
	defer or.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), or)
	if err != nil {
		return a, err
	}
	if n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return a, fmt.Errorf("archive %s: read %d bytes with sha256 %s, recorded %d and %s", id, n, hex.EncodeToString(h.Sum(nil)), a.Bytes, a.SHA256)
	}
	return a, nil
}
