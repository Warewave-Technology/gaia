// Package nexus finds artifacts in Sonatype Nexus Repository 3: an App's
// artifact is checked before a run configures servers with it, and its
// SHA-256 goes to the playbook to verify the download (design 4.3).
package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client reads a Nexus with an optional account; anonymous when empty.
type Client struct {
	endpoint, username, password string
	http                         *http.Client
}

// New returns a client for the Nexus at endpoint (https://nexus).
func New(endpoint, username, password string) *Client {
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), username: username, password: password, http: &http.Client{Timeout: 30 * time.Second}}
}

// ErrNoArtifact: the repository has no such file.
var ErrNoArtifact = errors.New("nexus: no such artifact")

// Asset is a file in a repository.
type Asset struct {
	URL    string
	SHA256 string // empty when Nexus did not say
}

// URL is where a repository serves a path.
func (c *Client) URL(repository, path string) string {
	return c.endpoint + "/repository/" + repository + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) get(ctx context.Context, method, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nexus: %w", err)
	}
	return resp, nil
}

// Asset finds path in repository with the search API, which gives its
// checksums. Formats name components differently, so a search that finds
// nothing falls back to asking for the file itself.
func (c *Client) Asset(ctx context.Context, repository, path string) (Asset, error) {
	path = strings.TrimLeft(path, "/")
	q := url.Values{"repository": {repository}, "name": {path}}
	resp, err := c.get(ctx, http.MethodGet, c.endpoint+"/service/rest/v1/search/assets?"+q.Encode())
	if err != nil {
		return Asset{}, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	if err != nil {
		return Asset{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("nexus: search %s in %s: HTTP %d", path, repository, resp.StatusCode)
	}
	var page struct {
		Items []struct {
			Path        string            `json:"path"`
			DownloadURL string            `json:"downloadUrl"`
			Checksum    map[string]string `json:"checksum"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return Asset{}, fmt.Errorf("nexus: search: %w", err)
	}
	for _, it := range page.Items {
		if strings.TrimLeft(it.Path, "/") == path {
			u := it.DownloadURL
			if u == "" {
				u = c.URL(repository, path)
			}
			return Asset{URL: u, SHA256: it.Checksum["sha256"]}, nil
		}
	}
	u := c.URL(repository, path)
	resp, err = c.get(ctx, http.MethodHead, u)
	if err != nil {
		return Asset{}, err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return Asset{URL: u}, nil
	case http.StatusNotFound:
		return Asset{}, fmt.Errorf("%w: %s in %s", ErrNoArtifact, path, repository)
	}
	return Asset{}, fmt.Errorf("nexus: %s: HTTP %d", u, resp.StatusCode)
}
