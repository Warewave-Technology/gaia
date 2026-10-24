package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Writer writes Gaia's own commits (design 7.6): signed with Gaia's key,
// straight to the followed branch, with the person who asked for them in
// a trailer.
type Writer struct {
	Mirrors *Mirrors
	Signer  git.Signer
	Name    string // author
	Email   string
}

// Trailers of Gaia's commits.
const (
	TrailerRequestedBy = "Gaia-Requested-By"
	TrailerReverts     = "Gaia-Reverts"
)

// Errors a writer is refused with.
var (
	ErrMoved    = errors.New("the branch moved while Gaia wrote; try again")
	ErrConflict = errors.New("later commits changed the same files")
	ErrMerge    = errors.New("a merge commit cannot be reverted; revert the commits it brings")
	ErrNotOnRef = errors.New("the commit is not on the followed branch")
	ErrNoCommit = errors.New("no such commit on the followed branch")
)

// Revert writes a commit that takes back what commit did, on top of the
// branch's head. Files a later commit changed again are not touched: the
// revert is refused and they are named.
func (w *Writer) Revert(ctx context.Context, r Repo, commit, requestedBy string) (string, error) {
	return w.write(ctx, r, func(g *git.Repository, wt *git.Worktree, head *object.Commit) (string, error) {
		c, err := g.CommitObject(plumbing.NewHash(commit))
		if err != nil {
			return "", fmt.Errorf("%w: %s %s", ErrNoCommit, r.Name, short(commit))
		}
		if c.NumParents() != 1 {
			if c.NumParents() == 0 {
				return "", fmt.Errorf("%s: the first commit cannot be reverted", r.Name)
			}
			return "", ErrMerge
		}
		if ok, err := c.IsAncestor(head); err != nil || !ok && c.Hash != head.Hash {
			return "", ErrNotOnRef
		}
		parent, err := c.Parent(0)
		if err != nil {
			return "", err
		}
		from, err := parent.Tree()
		if err != nil {
			return "", err
		}
		to, err := c.Tree()
		if err != nil {
			return "", err
		}
		now, err := head.Tree()
		if err != nil {
			return "", err
		}
		changes, err := object.DiffTreeWithOptions(ctx, from, to, &object.DiffTreeOptions{})
		if err != nil {
			return "", err
		}
		var conflicts []string
		root := wt.Filesystem.Root()
		for _, ch := range changes {
			before, after := ch.From, ch.To // parent → commit
			// The head must still have what the commit left.
			if after.Name != "" {
				f, err := now.FindEntry(after.Name)
				if err != nil || f.Hash != after.TreeEntry.Hash {
					conflicts = append(conflicts, after.Name)
					continue
				}
			} else if _, err := now.FindEntry(before.Name); err == nil {
				conflicts = append(conflicts, before.Name)
				continue
			}
			if after.Name != "" && after.Name != before.Name {
				if _, err := wt.Remove(after.Name); err != nil {
					return "", err
				}
			}
			if before.Name == "" {
				continue
			}
			blob, err := g.BlobObject(before.TreeEntry.Hash)
			if err != nil {
				return "", err
			}
			if err := writeBlob(filepath.Join(root, filepath.FromSlash(before.Name)), blob, before.TreeEntry.Mode.IsFile()); err != nil {
				return "", err
			}
			if _, err := wt.Add(before.Name); err != nil {
				return "", err
			}
		}
		if len(conflicts) > 0 {
			sort.Strings(conflicts)
			return "", fmt.Errorf("%w: %s", ErrConflict, strings.Join(conflicts, ", "))
		}
		subject, _, _ := strings.Cut(c.Message, "\n")
		return fmt.Sprintf("Revert %q\n\nThis reverts commit %s.\n\n%s: %s\n%s: %s\n", subject, c.Hash, TrailerReverts, c.Hash, TrailerRequestedBy, requestedBy), nil
	})
}

func writeBlob(path string, blob *object.Blob, regular bool) error {
	if !regular {
		return fmt.Errorf("%s: only regular files can be reverted", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	rd, err := blob.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	f, err := os.Create(path) //nolint:gosec // inside Gaia's own temporary clone
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rd); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// write clones the followed branch, lets edit change the work tree and
// say the message, then commits, signs and pushes. Nothing is pushed when
// the commit would change nothing.
func (w *Writer) write(ctx context.Context, r Repo, edit func(*git.Repository, *git.Worktree, *object.Commit) (string, error)) (string, error) {
	if w.Signer == nil {
		return "", errors.New("no signing key for Gaia's commits (gaia server --signing-key)")
	}
	if r.Local() {
		// A working copy is someone's: Gaia writes only to repositories it
		// mirrors (a file:// URL for a bare repository on this machine).
		return "", fmt.Errorf("%s: Gaia does not write to a local working copy; follow it by URL", r.Name)
	}
	if _, err := w.Mirrors.Sync(ctx, r); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "gaia-write-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	opts := &git.CloneOptions{URL: w.Mirrors.Path(r), SingleBranch: true, Tags: git.NoTags,
		ReferenceName: plumbing.NewBranchReferenceName(defaultRef(r.Ref))}
	g, err := git.PlainCloneContext(ctx, dir, false, opts)
	if err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	headRef, err := g.Head()
	if err != nil {
		return "", err
	}
	head, err := g.CommitObject(headRef.Hash())
	if err != nil {
		return "", err
	}
	wt, err := g.Worktree()
	if err != nil {
		return "", err
	}
	msg, err := edit(g, wt, head)
	if err != nil {
		return "", err
	}
	st, err := wt.Status()
	if err != nil {
		return "", err
	}
	if st.IsClean() {
		return "", errors.New("the commit would change nothing")
	}
	h, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: w.Name, Email: w.Email, When: time.Now()},
		Signer: w.Signer,
	})
	if err != nil {
		return "", err
	}
	// Push to the repository itself, not to the mirror the clone came from.
	if _, err := g.CreateRemote(&config.RemoteConfig{Name: "upstream", URLs: []string{r.URL}}); err != nil {
		return "", err
	}
	auth, err := w.Mirrors.auth(ctx, r)
	if err != nil {
		return "", err
	}
	branch := headRef.Name()
	err = g.PushContext(ctx, &git.PushOptions{
		RemoteName: "upstream", Auth: auth,
		RefSpecs: []config.RefSpec{config.RefSpec(branch + ":" + branch)},
		// Only from the head Gaia wrote on: anyone's newer commit wins.
		RequireRemoteRefs: []config.RefSpec{config.RefSpec(head.Hash.String() + ":" + branch.String())},
	})
	if err != nil {
		if strings.Contains(err.Error(), "non-fast-forward") || strings.Contains(err.Error(), "remote ref") {
			return "", ErrMoved
		}
		return "", fmt.Errorf("%s: push: %w", r.Name, err)
	}
	if _, err := w.Mirrors.Sync(ctx, r); err != nil {
		return "", err
	}
	return h.String(), nil
}

// Trailer returns a trailer's value in a commit message, or "".
func Trailer(message, key string) string {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	for i := len(lines) - 1; i >= 0 && strings.TrimSpace(lines[i]) != ""; i-- {
		if v, ok := strings.CutPrefix(lines[i], key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
