package runrecord

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is an archive in an S3 bucket with Object Lock (SeaweedFS in the
// PoC). Objects are written in COMPLIANCE mode: nobody, an admin
// included, can delete or overwrite them before retention ends.
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 connects to endpoint (http:// or https://) with an access key.
func NewS3(endpoint, bucket, accessKey, secretKey string) (*S3, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("s3 endpoint %q: want a URL", endpoint)
	}
	c, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, err
	}
	return &S3{client: c, bucket: bucket}, nil
}

// Put writes the object with a COMPLIANCE retention and returns its
// s3:// location with the version.
func (s *S3) Put(ctx context.Context, key string, data []byte, retainUntil time.Time) (string, error) {
	info, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType:     "application/json",
		Mode:            minio.Compliance,
		RetainUntilDate: retainUntil.UTC().Truncate(time.Second),
		SendContentMd5:  true, // Object Lock requires it
	})
	if err != nil {
		return "", fmt.Errorf("archive %s/%s: %w", s.bucket, key, err)
	}
	loc := fmt.Sprintf("s3://%s/%s", s.bucket, key)
	if info.VersionID != "" {
		loc += "?versionId=" + info.VersionID
	}
	return loc, nil
}

// Retention returns the object's retention mode and date, for checks.
func (s *S3) Retention(ctx context.Context, key string) (string, time.Time, error) {
	mode, until, err := s.client.GetObjectRetention(ctx, s.bucket, key, "")
	if err != nil {
		return "", time.Time{}, err
	}
	m := ""
	if mode != nil {
		m = string(*mode)
	}
	var t time.Time
	if until != nil {
		t = *until
	}
	return m, t, nil
}
