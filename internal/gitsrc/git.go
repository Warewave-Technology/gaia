// Package gitsrc reads catalog files from one commit of a Git repository.
// Gaia never reads the working tree: what was committed is what is loaded,
// so a plan can be rebuilt from its commit.
package gitsrc

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// maxFileSize guards against loading something that is not a catalog file.
const maxFileSize = 1 << 20

// Files returns the .yaml and .yml files under dir at ref in the repository
// that contains repoPath, and the full commit hash ref resolved to. Paths
// are relative to the repository root. dir "" or "." is the whole tree.
func Files(repoPath, ref, dir string) ([]catalog.File, string, error) {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
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
			return nil, "", fmt.Errorf("%s: no such directory at %s", dir, hash.String()[:12])
		}
		if err != nil {
			return nil, "", err
		}
	}

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
	return out, hash.String(), nil
}
