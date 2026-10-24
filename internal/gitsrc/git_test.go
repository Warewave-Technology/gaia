package gitsrc

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
)

func paths(fs []catalog.File) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestReadsTheCommitNotTheWorkingTree(t *testing.T) {
	r := gittest.New(t)
	r.Write("platform/a.yaml", "v: 1", "platform/notes.md", "x", "contracts/c.yaml", "c: 1")
	first := r.Commit("first")
	r.Write("platform/a.yaml", "v: 2", "platform/b.yaml", "b: 1")
	second := r.Commit("second")
	r.Write("platform/a.yaml", "v: uncommitted", "platform/new.yaml", "n: 1")

	fs, commit, err := Files(r.Dir, "HEAD", "platform")
	if err != nil {
		t.Fatal(err)
	}
	if commit != second {
		t.Errorf("commit = %s, want %s", commit, second)
	}
	if got := paths(fs); got != "platform/a.yaml,platform/b.yaml" {
		t.Errorf("files = %s", got)
	}
	for _, f := range fs {
		if f.Path == "platform/a.yaml" && string(f.Data) != "v: 2" {
			t.Errorf("a.yaml = %q, want the committed content", f.Data)
		}
	}

	fs, commit, err = Files(r.Dir, first[:8], "")
	if err != nil {
		t.Fatal(err)
	}
	if commit != first || paths(fs) != "contracts/c.yaml,platform/a.yaml" {
		t.Errorf("at first: commit %s, files %s", commit, paths(fs))
	}
}

func TestFindsTheRepositoryFromASubdirectory(t *testing.T) {
	r := gittest.New(t)
	r.Write("platform/a.yaml", "v: 1")
	r.Commit("first")
	if _, _, err := Files(filepath.Join(r.Dir, "platform"), "HEAD", "platform"); err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	r := gittest.New(t)
	r.Write("platform/a.yaml", "v: 1")
	r.Commit("first")
	if _, _, err := Files(r.Dir, "no-such-branch", ""); err == nil || !strings.Contains(err.Error(), "no-such-branch") {
		t.Errorf("unknown ref: %v", err)
	}
	if _, _, err := Files(r.Dir, "HEAD", "missing"); err == nil || !strings.Contains(err.Error(), "missing: no such directory") {
		t.Errorf("missing dir: %v", err)
	}
	if _, _, err := Files(t.TempDir(), "HEAD", ""); err == nil {
		t.Error("not a repository: no error")
	}
}

func TestExportWritesTheSubtreeOfTheCommit(t *testing.T) {
	r := gittest.New(t)
	r.Write("ansible/site.yml", "- hosts: all", "ansible/roles/x/tasks/main.yml", "- debug: {}", "platform/p.yaml", "p: 1")
	commit := r.Commit("first")
	r.Write("ansible/site.yml", "uncommitted")

	dest := t.TempDir()
	got, err := Export(r.Dir, "HEAD", "ansible", dest)
	if err != nil {
		t.Fatal(err)
	}
	if got != commit {
		t.Errorf("commit = %s, want %s", got, commit)
	}
	data, err := os.ReadFile(filepath.Join(dest, "site.yml"))
	if err != nil || string(data) != "- hosts: all" {
		t.Errorf("site.yml = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "roles", "x", "tasks", "main.yml")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "p.yaml")); !os.IsNotExist(err) {
		t.Errorf("a file outside the subtree was exported: %v", err)
	}
}
