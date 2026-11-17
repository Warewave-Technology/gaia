package server

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3Location is a fresh, empty place for a repository on the S3 test server ($GAIA_TEST_S3, see
// internal/diskstore/backend_test.go), as http[s]://ACCESS:SECRET@host/bucket/prefix; "" when there is none (a failure
// when $GAIA_REQUIRE_S3 is set).
func s3Location(t *testing.T) string {
	t.Helper()
	env := os.Getenv("GAIA_TEST_S3")
	if env == "" {
		if os.Getenv("GAIA_REQUIRE_S3") != "" {
			t.Fatal("GAIA_REQUIRE_S3 is set but GAIA_TEST_S3 is not")
		}
		t.Log("S3 variant skipped: GAIA_TEST_S3 is not set")
		return ""
	}
	u, err := url.Parse(env)
	if err != nil || u.User == nil {
		t.Fatalf("GAIA_TEST_S3 %q", env)
	}
	secret, _ := u.User.Password()
	bucket := strings.Trim(u.Path, "/")
	mc, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(u.User.Username(), secret, ""), Secure: u.Scheme == "https"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := mc.BucketExists(ctx, bucket); err != nil {
		t.Fatal(err)
	} else if !ok {
		if err := mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("server-%s-%x", strings.ReplaceAll(t.Name(), "/", "-"), rand.Uint64())
	t.Cleanup(func() {
		for o := range mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix + "/", Recursive: true}) {
			if o.Err == nil {
				mc.RemoveObject(ctx, bucket, o.Key, minio.RemoveObjectOptions{})
			}
		}
	})
	u.Path = "/" + bucket + "/" + prefix
	return u.String()
}

// s3RepoBody is the request that creates repository name at an s3Location.
func s3RepoBody(t *testing.T, name, loc string) string {
	t.Helper()
	u, _ := url.Parse(loc)
	secret, _ := u.User.Password()
	key := u.User.Username()
	u.User = nil
	return fmt.Sprintf(`{"name":%q,"kind":"s3","location":%q,"s3_access_key_id":%q,"s3_secret_access_key":%q,"kit_passphrase":%q}`,
		name, u.String(), key, secret, kitPassphrase)
}

func TestParseS3Location(t *testing.T) {
	for _, c := range []struct{ in, canon, err string }{
		{"https://s3.example.com/bucket", "https://s3.example.com/bucket/", ""},
		{"https://s3.example.com:9000/bucket/a/b/", "https://s3.example.com:9000/bucket/a/b/", ""},
		{"http://10.0.0.5:9000/gaia/repo?region=eu-1", "http://10.0.0.5:9000/gaia/repo/?region=eu-1", ""},
		{"s3://bucket/x", "", "https://endpoint/bucket"},
		{"https://key:secret@host/bucket", "", "without credentials"},
		{"https://host/", "", "no bucket"},
		{"https://host/bucket?versioning=1", "", "unknown parameter"},
		{"/srv/repo", "", "https://endpoint/bucket"},
	} {
		canon, _, err := parseS3Location(c.in)
		if c.err == "" && (err != nil || canon != c.canon) || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: %q %v", c.in, canon, err)
		}
	}
}

// An S3 repository: refused without its access key or where something is stored already; created, its access key
// kept 0600 and carried in the recovery kit; usable for a state backup.
func TestS3Repository(t *testing.T) {
	loc := s3Location(t)
	if loc == "" {
		return
	}
	ctx := context.Background()
	f := newFixture(t)
	admin := f.tokens[store.RoleAdmin]
	body := s3RepoBody(t, "objects", loc)
	if st := f.do(admin, "POST", "/api/v1/repositories", strings.Replace(body, `"s3_secret_access_key"`, `"s3_unused"`, 1), nil); st != 400 {
		t.Fatalf("no secret key: %d", st)
	}
	if st := f.do(admin, "POST", "/api/v1/repositories", strings.Replace(body, `"kind":"s3"`, `"kind":"filesystem"`, 1), nil); st != 400 {
		t.Fatalf("an S3 URL as a filesystem location: %d", st)
	}
	var r store.Repository
	if st := f.do(admin, "POST", "/api/v1/repositories", body, &r); st != 201 || r.Kind != "s3" || !strings.HasSuffix(r.Location, "/") {
		t.Fatalf("create: %d %+v", st, r)
	}
	fi, err := os.Stat(f.s.cfg.repoS3File("objects"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("access key file: %v %v", fi, err)
	}
	if st := f.do(admin, "POST", "/api/v1/repositories", strings.Replace(body, `"objects"`, `"again"`, 1), nil); st != 409 {
		t.Fatalf("a second repository at the same place: %d", st)
	}
	if _, err := os.Stat(f.s.cfg.repoPasswordFile("again")); !os.IsNotExist(err) {
		t.Fatalf("a refused repository left its password: %v", err)
	}
	kitFile, _ := os.ReadFile(f.s.cfg.kitPath())
	kit, err := recoverykit.Open(kitFile, kitPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	kr, ok := kit.Repository("objects")
	u, _ := url.Parse(loc)
	secret, _ := u.User.Password()
	if !ok || kr.S3 == nil || kr.S3.AccessKeyID != u.User.Username() || kr.S3.SecretAccessKey != secret {
		t.Fatalf("the kit's repository %+v", kr)
	}
	if b, err := f.s.BackupState(ctx, "admin", "s3 test"); err != nil || b.State != "ok" {
		t.Fatalf("state backup into S3: %+v %v", b, err)
	}
	audit, _ := f.s.db.Audit(ctx, 0, 1000)
	for _, a := range audit {
		if strings.Contains(a.Detail, secret) || strings.Contains(a.Target, secret) {
			t.Fatalf("the secret key is in the audit log: %+v", a)
		}
	}
}
