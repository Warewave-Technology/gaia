// Package repo keeps local mirrors of the Git repositories Gaia follows
// (design 7.6): the platform repository, the contract repositories and the
// config repository. A repository is either a local path, read in place,
// or a URL, cloned and fetched with credentials from OpenBao.
package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

// SecretReader reads bao:// references.
type SecretReader interface {
	Read(ctx context.Context, ref string) (map[string]string, error)
}

// Repo is a repository to follow: a git Provider, or the platform
// repository the server starts from.
type Repo struct {
	Name        string // provider name
	URL         string // https://…, ssh://…, git@host:path, or a local path
	Ref         string // branch followed; "" is HEAD
	Credentials string // bao:// with token (https) or private_key and known_hosts (ssh)
}

// Local reports whether the repository is a path on this machine, read in
// place. A file:// URL is mirrored like any remote.
func (r Repo) Local() bool { return filepath.IsAbs(r.URL) }

// Mirrors keeps one mirror per repository under Dir.
type Mirrors struct {
	Dir     string
	Secrets SecretReader
	// Settings is Gaia's settings store (design 17.2). Without it, or
	// before its first version, settings are read from Git as before.
	Settings SettingsReader

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (m *Mirrors) lock(url string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	l, ok := m.locks[url]
	if !ok {
		l = &sync.Mutex{}
		m.locks[url] = l
	}
	return l
}

// Path is where the repository can be read: the path itself for a local
// repository, else its mirror.
func (m *Mirrors) Path(r Repo) string {
	if r.Local() {
		return r.URL
	}
	sum := sha256.Sum256([]byte(r.URL))
	return filepath.Join(m.Dir, hex.EncodeToString(sum[:8])+".git")
}

// Sync fetches the followed ref and returns the commit it points at.
func (m *Mirrors) Sync(ctx context.Context, r Repo) (string, error) {
	if !r.Local() {
		if err := m.fetch(ctx, r); err != nil {
			return "", err
		}
	}
	return m.resolve(r)
}

// Fetch brings one more ref of the repository into the mirror, for a
// preview, and returns its commit: a branch, a tag (refs/tags/…), a
// GitHub pull request (pull/N) or a full commit SHA. The followed branch
// is left alone.
func (m *Mirrors) Fetch(ctx context.Context, r Repo, ref string) (string, error) {
	if isSHA(ref) {
		if err := m.Ensure(ctx, r, ref); err != nil {
			return "", err
		}
		return ref, nil
	}
	if strings.ContainsAny(ref, " ~^:?*[\\") || strings.Contains(ref, "..") || ref == "" {
		return "", fmt.Errorf("%s: %q is not a ref", r.Name, ref)
	}
	if r.Local() {
		g, err := gitsrc.Open(r.URL)
		if err != nil {
			return "", err
		}
		h, err := g.ResolveRevision(plumbing.Revision(ref))
		if err != nil {
			return "", fmt.Errorf("%s: %s: %w", r.Name, ref, err)
		}
		return h.String(), nil
	}
	src := "refs/heads/" + ref
	switch {
	case strings.HasPrefix(ref, "refs/"):
		src = ref
	case strings.HasPrefix(ref, "pull/"):
		src = "refs/" + ref + "/head"
	}
	if err := m.fetch(ctx, r); err != nil { // the mirror must exist
		return "", err
	}
	l := m.lock(r.URL)
	l.Lock()
	defer l.Unlock()
	auth, err := m.auth(ctx, r)
	if err != nil {
		return "", err
	}
	g, err := git.PlainOpen(m.Path(r))
	if err != nil {
		return "", err
	}
	dst := "refs/gaia/preview/" + strings.TrimPrefix(src, "refs/")
	err = g.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec("+" + src + ":" + dst)}, Auth: auth, Force: true, Tags: git.NoTags})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("%s: fetch %s: %w", r.Name, ref, err)
	}
	h, err := g.Reference(plumbing.ReferenceName(dst), true)
	if err != nil {
		return "", fmt.Errorf("%s: %s: %w", r.Name, ref, err)
	}
	return h.Hash().String(), nil
}

func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Ensure makes sure commit is in the mirror, fetching once if it is not.
func (m *Mirrors) Ensure(ctx context.Context, r Repo, commit string) error {
	if m.has(r, commit) {
		return nil
	}
	if r.Local() {
		return fmt.Errorf("%s: no commit %s", r.Name, short(commit))
	}
	if err := m.fetch(ctx, r); err != nil {
		return err
	}
	if !m.has(r, commit) {
		return fmt.Errorf("%s: no commit %s after fetching", r.Name, short(commit))
	}
	return nil
}

func (m *Mirrors) has(r Repo, commit string) bool {
	g, err := gitsrc.Open(m.Path(r))
	if err != nil {
		return false
	}
	_, err = g.CommitObject(plumbing.NewHash(commit))
	return err == nil
}

func (m *Mirrors) resolve(r Repo) (string, error) {
	g, err := gitsrc.Open(m.Path(r))
	if err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	name := plumbing.Revision("HEAD")
	switch {
	case r.Local() && r.Ref != "":
		name = plumbing.Revision(r.Ref)
	case !r.Local():
		// A mirror has the followed branch only, as fetched.
		name = plumbing.Revision("refs/heads/" + defaultRef(r.Ref))
	}
	h, err := g.ResolveRevision(name)
	if err != nil {
		return "", fmt.Errorf("%s: resolve %s: %w", r.Name, name, err)
	}
	return h.String(), nil
}

// fetch clones the mirror the first time and fetches the followed ref
// after that. Mirrors are bare and only Gaia writes them.
func (m *Mirrors) fetch(ctx context.Context, r Repo) error {
	l := m.lock(r.URL)
	l.Lock()
	defer l.Unlock()
	auth, err := m.auth(ctx, r)
	if err != nil {
		return err
	}
	path := m.Path(r)
	g, err := git.PlainOpen(path)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if err := os.MkdirAll(m.Dir, 0o750); err != nil {
			return err
		}
		g, err = git.PlainInit(path, true)
		if err == nil {
			_, err = g.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{r.URL}})
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", r.Name, err)
	}
	ref := defaultRef(r.Ref)
	spec := config.RefSpec("+refs/heads/" + ref + ":refs/heads/" + ref)
	err = g.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{spec}, Auth: auth, Force: true, Tags: git.NoTags})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("%s: fetch: %w", r.Name, err)
	}
	return nil
}

// auth builds the credentials of r: a token for https, a key and the
// server's host keys for ssh. Host keys are required: Gaia never trusts a
// server it does not know.
func (m *Mirrors) auth(ctx context.Context, r Repo) (transport.AuthMethod, error) {
	if r.Credentials == "" {
		return nil, nil
	}
	if m.Secrets == nil {
		return nil, fmt.Errorf("%s: credentials need OpenBao", r.Name)
	}
	f, err := m.Secrets.Read(ctx, r.Credentials)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(r.URL, "http://") || strings.HasPrefix(r.URL, "https://") {
		user := f["username"]
		if user == "" {
			user = "x-access-token"
		}
		if f["token"] == "" {
			return nil, fmt.Errorf("%s: %s has no token", r.Name, r.Credentials)
		}
		return &githttp.BasicAuth{Username: user, Password: f["token"]}, nil
	}
	if f["private_key"] == "" || f["known_hosts"] == "" {
		return nil, fmt.Errorf("%s: %s needs private_key and known_hosts", r.Name, r.Credentials)
	}
	keys, err := gitssh.NewPublicKeys("git", []byte(f["private_key"]), f["passphrase"])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name, err)
	}
	cb, err := hostKeys(f["known_hosts"])
	if err != nil {
		return nil, fmt.Errorf("%s: known_hosts: %w", r.Name, err)
	}
	keys.HostKeyCallback = cb
	return keys, nil
}

// hostKeys accepts exactly the servers and keys of a known_hosts text.
func hostKeys(text string) (ssh.HostKeyCallback, error) {
	type entry struct {
		hosts []string
		key   ssh.PublicKey
	}
	var entries []entry
	rest := []byte(text)
	for len(rest) > 0 {
		_, hosts, key, _, r, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			if len(entries) > 0 && strings.TrimSpace(string(rest)) == "" {
				break
			}
			return nil, err
		}
		entries = append(entries, entry{hosts: hosts, key: key})
		rest = r
	}
	if len(entries) == 0 {
		return nil, errors.New("no host keys")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		names := []string{hostname}
		if h, p, err := net.SplitHostPort(hostname); err == nil {
			names = append(names, h)
			if p == "22" {
				names = append(names, h)
			} else {
				names = append(names, "["+h+"]:"+p)
			}
		}
		for _, e := range entries {
			for _, h := range e.hosts {
				for _, n := range names {
					if h == n && string(e.key.Marshal()) == string(key.Marshal()) {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("unknown host key for %s", hostname)
	}, nil
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func defaultRef(ref string) string {
	if ref == "" {
		return "main"
	}
	return ref
}
