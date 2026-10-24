package runrecord

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// TestS3Lab writes to a real bucket with Object Lock when $GAIA_S3_URL,
// $GAIA_S3_BUCKET, $GAIA_S3_ACCESS_KEY and $GAIA_S3_SECRET_KEY are set. The
// object stays locked for an hour.
func TestS3Lab(t *testing.T) {
	url, bucket := os.Getenv("GAIA_S3_URL"), os.Getenv("GAIA_S3_BUCKET")
	if url == "" || bucket == "" {
		t.Skip("GAIA_S3_URL and GAIA_S3_BUCKET not set")
	}
	a, err := NewS3(url, bucket, os.Getenv("GAIA_S3_ACCESS_KEY"), os.Getenv("GAIA_S3_SECRET_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "test/" + time.Now().UTC().Format("20060102T150405") + ".json"
	loc, err := a.Put(ctx, key, []byte(`{"test":true}`), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loc, "versionId=") {
		t.Errorf("location %s has no version", loc)
	}
	mode, until, err := a.Retention(ctx, key)
	if err != nil || mode != "COMPLIANCE" || time.Until(until) < 50*time.Minute {
		t.Errorf("retention %s until %s, %v", mode, until, err)
	}
	version := loc[strings.Index(loc, "versionId=")+len("versionId="):]
	err = a.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{VersionID: version, GovernanceBypass: true})
	if err == nil {
		t.Error("a locked version was deleted")
	}
}
