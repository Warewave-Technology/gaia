// Package gittest builds throwaway Git repositories for tests.
package gittest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Repo is a repository in a temporary directory.
type Repo struct {
	t    *testing.T
	Dir  string
	repo *git.Repository
	n    int
}

// New creates an empty repository.
func New(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return &Repo{t: t, Dir: dir, repo: r}
}

// Write writes files (path, content pairs) to the working tree.
func (r *Repo) Write(kv ...string) {
	r.t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		p := filepath.Join(r.Dir, filepath.FromSlash(kv[i]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(kv[i+1]), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// Commit stages everything and commits it; it returns the commit hash.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	return r.CommitSigned(msg, nil)
}

// CommitSigned is Commit signed with signer (nil: unsigned). Commits get
// increasing times, so that history has an order.
func (r *Repo) CommitSigned(msg string, signer git.Signer) string {
	r.t.Helper()
	r.n++
	wt, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		r.t.Fatal(err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1700000000+int64(r.n), 0)},
		Signer: signer, AllowEmptyCommits: true,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return h.String()
}
