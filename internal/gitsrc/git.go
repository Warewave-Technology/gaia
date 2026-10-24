// Package gitsrc reads catalog files, and exports the config repository,
// from one commit of a Git repository.
// Gaia never reads the working tree: what was committed is what is loaded,
// so a plan can be rebuilt from its commit.
package gitsrc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// maxFileSize guards against loading something that is not a catalog file.
const maxFileSize = 1 << 20

// Files returns the .yaml and .yml files under dir at ref in the repository
// that contains repoPath, and the full commit hash ref resolved to. Paths
// are relative to the repository root. dir "" or "." is the whole tree.
func Files(repoPath, ref, dir string) ([]catalog.File, string, error) {
	tree, hash, err := treeAt(repoPath, ref, dir)
	if err != nil {
		return nil, "", err
	}
	dir = strings.Trim(path.Clean("/"+dir), "/")

	var out []catalog.File
	err = tree.Files().ForEach(func(f *object.File) error {
		if ext := path.Ext(f.Name); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		p := path.Join(dir, f.Name)
		if f.Size > maxFileSize {
			return fmt.Errorf("%s: %d bytes, larger than %d", p, f.Size, maxFileSize)
		}
		data, err := f.Contents()
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, catalog.File{Path: p, Data: []byte(data)})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, hash, nil
}

// ErrNoDirectory: the commit has no such directory.
var ErrNoDirectory = errors.New("no such directory")

// treeAt returns the tree of dir at ref and the full commit hash.
func treeAt(repoPath, ref, dir string) (*object.Tree, string, error) {
	repo, err := Open(repoPath)
	if err != nil {
		return nil, "", fmt.Errorf("open repository %s: %w", repoPath, err)
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, "", fmt.Errorf("resolve %q: %w", ref, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, "", fmt.Errorf("commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, "", err
	}
	dir = strings.Trim(path.Clean("/"+dir), "/")
	if dir != "" {
		tree, err = tree.Tree(dir)
		if errors.Is(err, object.ErrDirectoryNotFound) {
			return nil, "", fmt.Errorf("%s: %w at %s", dir, ErrNoDirectory, hash.String()[:12])
		}
		if err != nil {
			return nil, "", err
		}
	}
	return tree, hash.String(), nil
}

// Export writes every file under dir at ref of the repository at repoPath
// into dest, keeping modes for executables, and returns the full commit
// hash. dest must exist. Symbolic links and submodules are skipped.
func Export(repoPath, ref, dir, dest string) (string, error) {
	tree, hash, err := treeAt(repoPath, ref, dir)
	if err != nil {
		return "", err
	}
	err = tree.Files().ForEach(func(f *object.File) error {
		if !f.Mode.IsFile() {
			return nil
		}
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
			return fmt.Errorf("%s: outside the export directory", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		mode := os.FileMode(0o640)
		if f.Mode == filemode.Executable {
			mode = 0o750
		}
		r, err := f.Reader()
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			_ = w.Close()
			return err
		}
		return w.Close()
	})
	if err != nil {
		return "", err
	}
	return hash, nil
}

// Resolve returns the full commit hash of ref and checks that dir exists
// at it.
func Resolve(repoPath, ref, dir string) (string, error) {
	_, hash, err := treeAt(repoPath, ref, dir)
	return hash, err
}

// Signed reports whether the commit carries a signature. It does not
// verify the signature against a keyring.
func Signed(repoPath, commit string) (bool, error) {
	repo, err := Open(repoPath)
	if err != nil {
		return false, err
	}
	c, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return false, err
	}
	return c.PGPSignature != "", nil
}

// Load reads the catalog files of several directories at one ref. paths
// empty reads the whole tree. It returns the commit the ref resolved to.
func Load(repoPath, ref string, paths []string) ([]catalog.File, string, error) {
	if len(paths) == 0 {
		paths = []string{""}
	}
	var all []catalog.File
	var commit string
	for _, p := range paths {
		files, c, err := Files(repoPath, ref, p)
		if err != nil {
			return nil, "", err
		}
		all = append(all, files...)
		commit = c
	}
	return all, commit, nil
}

// Root returns the top directory of the repository that contains path.
func Root(path string) (string, error) {
	repo, err := Open(path)
	if err != nil {
		return "", fmt.Errorf("open repository %s: %w", path, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return filepath.Abs(path)
	}
	return wt.Filesystem.Root(), nil
}

// Open opens the repository at path: a bare repository (Gaia's mirrors) or
// the repository a working tree directory is in.
func Open(path string) (*git.Repository, error) {
	if r, err := git.PlainOpen(path); err == nil {
		return r, nil
	}
	return git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
}
