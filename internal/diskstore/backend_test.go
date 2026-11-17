package diskstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/object"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// The same tests on every kind of repository storage (ADR-007 §4: S3 with the same defaults and tests as a filesystem).
// S3 runs against the server in $GAIA_TEST_S3 (http[s]://ACCESS:SECRET@host:port/bucket, the bucket is made if
// missing); without it the S3 variant is skipped, unless $GAIA_REQUIRE_S3 is set (CI), where that fails.

type backend struct {
	name string
	opts func(t *testing.T) RepoOptions
	// flip changes one byte of a stored blob.
	flip func(t *testing.T, o RepoOptions, blobID string, off int64)
}

func backends(t *testing.T) []backend {
	t.Helper()
	bs := []backend{{name: "filesystem", opts: func(t *testing.T) RepoOptions {
		dir := t.TempDir()
		return RepoOptions{Path: filepath.Join(dir, "repo"), ConfigFile: filepath.Join(dir, "kopia.config"), Password: "test-only",
			CacheDir: filepath.Join(dir, "cache"), ContentCacheMB: 64, ContentCacheLimitMB: 64}
	}, flip: func(t *testing.T, o RepoOptions, blobID string, off int64) {
		var file string
		filepath.Walk(o.Path, func(p string, fi os.FileInfo, err error) error {
			rel, _ := filepath.Rel(o.Path, p) // the filesystem storage shards a blob ID into directories
			if err == nil && !fi.IsDir() && strings.TrimSuffix(strings.ReplaceAll(rel, string(filepath.Separator), ""), ".f") == blobID {
				file = p
			}
			return nil
		})
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		b[off] ^= 0xff
		if err := os.WriteFile(file, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}}}
	env := os.Getenv("GAIA_TEST_S3")
	if env == "" {
		if os.Getenv("GAIA_REQUIRE_S3") != "" {
			t.Fatal("GAIA_REQUIRE_S3 is set but GAIA_TEST_S3 is not")
		}
		t.Log("S3 variant skipped: GAIA_TEST_S3 is not set")
		return bs
	}
	u, err := url.Parse(env)
	if err != nil || u.User == nil {
		t.Fatalf("GAIA_TEST_S3 %q: want http[s]://ACCESS:SECRET@host:port/bucket", env)
	}
	secret, _ := u.User.Password()
	bucket := strings.Trim(u.Path, "/")
	mc, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(u.User.Username(), secret, ""), Secure: u.Scheme == "https"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := mc.BucketExists(ctx, bucket); err != nil {
		t.Fatalf("S3 test server %s: %v", u.Host, err)
	} else if !ok {
		if err := mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return append(bs, backend{name: "s3", opts: func(t *testing.T) RepoOptions {
		dir := t.TempDir()
		prefix := fmt.Sprintf("%s-%x/", strings.ReplaceAll(t.Name(), "/", "-"), rand.Uint64())
		t.Cleanup(func() {
			for o := range mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
				if o.Err == nil {
					mc.RemoveObject(ctx, bucket, o.Key, minio.RemoveObjectOptions{})
				}
			}
		})
		return RepoOptions{S3: &S3{Endpoint: u.Host, Bucket: bucket, Prefix: prefix, AccessKeyID: u.User.Username(), SecretAccessKey: secret,
			Insecure: u.Scheme == "http"}, ConfigFile: filepath.Join(dir, "kopia.config"), Password: "test-only",
			CacheDir: filepath.Join(dir, "cache"), ContentCacheMB: 64, ContentCacheLimitMB: 64}
	}, flip: func(t *testing.T, o RepoOptions, blobID string, off int64) {
		key := o.S3.Prefix + blobID
		obj, err := mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(obj)
		obj.Close()
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		b[off] ^= 0xff
		if _, err := mc.PutObject(ctx, bucket, key, bytes.NewReader(b), int64(len(b)), minio.PutObjectOptions{}); err != nil {
			t.Fatal(err)
		}
	}})
}

func openBackend(t *testing.T, b backend) (*Store, RepoOptions) {
	t.Helper()
	ctx := context.Background()
	o := b.opts(t)
	if empty, err := Empty(ctx, o); err != nil || !empty {
		t.Fatalf("new storage: empty %v %v", empty, err)
	}
	if err := Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	if empty, err := Empty(ctx, o); err != nil || empty {
		t.Fatalf("after create: empty %v %v", empty, err)
	}
	return reopen(t, o), o
}

// damageBlock flips a byte inside the stored copy of block i of point p.
func damageBlock(t *testing.T, b backend, s *Store, o RepoOptions, p *Point, i int) string {
	t.Helper()
	ctx := context.Background()
	idx, err := s.loadIndex(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	oid, _ := object.ParseID(idx.Blocks[i])
	cid, _, _ := oid.ContentID()
	ci, err := s.r.ContentInfo(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	b.flip(t, o, string(ci.PackBlobID), int64(ci.PackOffset)+int64(ci.PackedLength)/2)
	return cid.String()
}

// Full, incremental, restore, post-backup check, verification, quarantine, a new point that does not reuse the damage,
// and maintenance: on every storage.
func TestBackends(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			s, o := openBackend(t, b)
			p1, data1 := full(t, s, "vm1", 1)
			restoreAndCompare(t, s, p1, data1)
			// An incremental: block 1 written, the rest from the parent.
			d, _ := testDisk()
			copy(d.data, data1)
			fill(d.data[BlockSize:BlockSize+4096], 9)
			changed := []Range{{BlockSize, 4096}}
			d.allowed = changed
			p2, err := s.Backup(ctx, BackupRequest{Domain: "vm1", Disk: "vda", Checkpoint: "c2", Size: uint64(len(d.data)), Changed: changed,
				Parent: p1, Sources: []Source{d}})
			if err != nil {
				t.Fatal(err)
			}
			restoreAndCompare(t, reopen(t, o), p2, d.data)
			for _, p := range []*Point{p1, p2} {
				if c, err := s.CheckPoint(ctx, p.ID); err != nil || len(c.Problems) != 0 {
					t.Fatalf("check %s: %+v %v", p.ID, c, err)
				}
				if v, err := uncached(t, o).VerifyPoint(ctx, p.ID, 1, rand.New(rand.NewPCG(1, 2))); err != nil || len(v.Problems) != 0 {
					t.Fatalf("verify %s: %+v %v", p.ID, v, err)
				}
			}
			// Damage block 0 (shared by both points), quarantine, and back the same data up again.
			damaged := damageBlock(t, b, s, o, p1, 0)
			v, err := uncached(t, o).VerifyPoint(ctx, p2.ID, 1, rand.New(rand.NewPCG(1, 2)))
			if err != nil || len(v.Damaged) != 1 || v.Damaged[0].Content != damaged {
				t.Fatalf("verification after the damage: %+v %v", v, err)
			}
			users, err := s.PointsUsing(ctx, []string{damaged})
			if err != nil || len(users) != 2 {
				t.Fatalf("points using %s: %v %v", damaged, users, err)
			}
			if _, err := uncached(t, o).Quarantine(ctx, v.Damaged, "test"); err != nil {
				t.Fatal(err)
			}
			p3, _ := full(t, reopen(t, o), "vm1", 1)
			if v, err := uncached(t, o).VerifyPoint(ctx, p3.ID, 1, rand.New(rand.NewPCG(1, 2))); err != nil || len(v.Problems) != 0 {
				t.Fatalf("the new point: %+v %v", v, err)
			}
			restoreAndCompare(t, reopen(t, o), p3, data1)
			if err := s.DeletePoint(ctx, p1.ID); err != nil {
				t.Fatal(err)
			}
			if res, err := reopen(t, o).Maintain(ctx, true, maintenance.SafetyNone); err != nil || res.Mode != "full" {
				t.Fatalf("maintenance %+v %v", res, err)
			}
			restoreAndCompare(t, reopen(t, o), p3, data1)
		})
	}
}
