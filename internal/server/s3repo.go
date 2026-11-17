package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// An S3 repository (ADR-007 §4) is located by a URL, scheme://endpoint/bucket[/prefix][?region=name]: https, or http
// only when asked for in so many words. Its access key is a secret like the repository password: a file of its own
// under the secrets directory, and part of every recovery kit.

// parseS3Location checks an S3 location and returns its canonical form and its parts.
func parseS3Location(loc string) (string, diskstore.S3, error) {
	var s diskstore.S3
	u, err := url.Parse(loc)
	if err != nil {
		return "", s, fmt.Errorf("%w: location %q: %v", store.ErrInvalid, loc, err)
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return "", s, fmt.Errorf("%w: location %q: https://endpoint/bucket[/prefix] (or http://)", store.ErrInvalid, loc)
	case u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "":
		return "", s, fmt.Errorf("%w: location %q: an endpoint (host[:port]) without credentials; the access key is a field of its own", store.ErrInvalid, loc)
	}
	for k := range u.Query() {
		if k != "region" {
			return "", s, fmt.Errorf("%w: location %q: unknown parameter %s (region)", store.ErrInvalid, loc, k)
		}
	}
	parts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 2)
	if parts[0] == "" {
		return "", s, fmt.Errorf("%w: location %q: no bucket", store.ErrInvalid, loc)
	}
	s.Endpoint, s.Bucket, s.Region, s.Insecure = u.Host, parts[0], u.Query().Get("region"), u.Scheme == "http"
	if len(parts) == 2 && parts[1] != "" {
		s.Prefix = strings.TrimSuffix(parts[1], "/") + "/"
	}
	canon := u.Scheme + "://" + s.Endpoint + "/" + s.Bucket + "/" + s.Prefix
	if s.Region != "" {
		canon += "?region=" + url.QueryEscape(s.Region)
	}
	return canon, s, nil
}

func (c Config) repoS3File(name string) string {
	return filepath.Join(c.secretsDir(), "repository-"+name+".s3.json")
}

// readS3Secret reads an S3 repository's access key.
func (c Config) readS3Secret(name string) (*recoverykit.S3, error) {
	b, err := os.ReadFile(c.repoS3File(name))
	if err != nil {
		return nil, fmt.Errorf("repository %s: S3 access key: %w", name, err)
	}
	var k recoverykit.S3
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, fmt.Errorf("repository %s: S3 access key: %w", name, err)
	}
	return &k, nil
}

func writeS3Secret(path string, k *recoverykit.S3) error {
	b, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return writeExclusive(path, append(b, '\n'), 0o600)
}

// repoOptionsFor are a repository's options from its kind, location, password and (for S3) access key.
func (c Config) repoOptionsFor(name, kind, location, password string, k *recoverykit.S3) (diskstore.RepoOptions, error) {
	o := c.repoOptions(name, location, password)
	if kind != "s3" {
		return o, nil
	}
	_, s3, err := parseS3Location(location)
	if err != nil {
		return o, err
	}
	if k == nil {
		return o, fmt.Errorf("repository %s: no S3 access key", name)
	}
	s3.AccessKeyID, s3.SecretAccessKey = k.AccessKeyID, k.SecretAccessKey
	if k.CAPEM != "" {
		s3.RootCA = []byte(k.CAPEM)
	}
	o.Path, o.S3 = "", &s3
	return o, nil
}

// repoOpts are the options of a repository Gaia created, with its stored password and access key.
func (s *Server) repoOpts(r store.Repository) (diskstore.RepoOptions, error) {
	return s.cfg.storedRepoOptions(r.Name, r.Kind, r.Location)
}

func (c Config) storedRepoOptions(name, kind, location string) (diskstore.RepoOptions, error) {
	pw, err := os.ReadFile(c.repoPasswordFile(name))
	if err != nil {
		return diskstore.RepoOptions{}, fmt.Errorf("repository %s: password: %w", name, err)
	}
	var k *recoverykit.S3
	if kind == "s3" {
		if k, err = c.readS3Secret(name); err != nil {
			return diskstore.RepoOptions{}, err
		}
	}
	return c.repoOptionsFor(name, kind, location, strings.TrimSpace(string(pw)), k)
}
