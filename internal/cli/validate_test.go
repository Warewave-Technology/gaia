package cli

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
)

const size = `apiVersion: gaia/v1alpha1
kind: Size
metadata: { name: small }
spec: { cpu: 2, memoryGiB: 2, diskGiB: 20 }
`

// The lab examples are a complete platform and contract repository.
func labRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	r := gittest.New(t)
	root := filepath.Join("..", "..", "examples", "lab")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		r.Write(filepath.ToSlash(rel), string(data))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestValidate(t *testing.T) {
	r := labRepo(t)
	commit := r.Commit("first")

	out := run(t, NewRootCommand("dev"), "validate", "--repo", r.Dir, "--path", "platform", "--path", "contracts")
	for _, want := range []string{"commit " + commit[:12] + " (HEAD), 16 files", "18 objects (", "Server 1", "references"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func validateFails(t *testing.T, r *gittest.Repo, wants ...string) {
	t.Helper()
	root := NewRootCommand("dev")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"validate", "--repo", r.Dir})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "problem(s) found") {
		t.Fatalf("err = %v", err)
	}
	for _, w := range wants {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}

func TestValidateReportsLoadProblems(t *testing.T) {
	r := gittest.New(t)
	r.Write("platform/sizes.yaml", strings.Replace(size, "cpu: 2", "cpus: 2", 1))
	r.Commit("first")
	validateFails(t, r, "platform/sizes.yaml:4:", `unknown field "cpus"`)
}

func TestValidateReportsReferenceProblems(t *testing.T) {
	r := labRepo(t)
	r.Write("contracts/poc-web.yaml", strings.Replace(mustRead(t, "contracts/poc-web.yaml"), "size: small", "size: smal", 1))
	r.Commit("typo")
	validateFails(t, r, "contracts/poc-web.yaml:9:", `spec.size: Size "smal" not found (did you mean "small"?)`)
}

func mustRead(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "lab", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
