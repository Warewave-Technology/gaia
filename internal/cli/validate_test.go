package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
)

const size = `apiVersion: gaia/v1alpha1
kind: Size
metadata: { name: small }
spec: { cpu: 2, memoryGiB: 2, diskGiB: 20 }
`

func TestValidate(t *testing.T) {
	r := gittest.New(t)
	r.Write("platform/sizes.yaml", size, "contracts/README.md", "x")
	commit := r.Commit("first")

	out := run(t, NewRootCommand("dev"), "validate", "--repo", r.Dir, "--path", "platform")
	for _, want := range []string{"commit " + commit[:12] + " (HEAD), 1 files", "1 objects: Size 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestValidateReportsProblems(t *testing.T) {
	r := gittest.New(t)
	r.Write("platform/sizes.yaml", strings.Replace(size, "cpu: 2", "cpus: 2", 1))
	r.Commit("first")

	root := NewRootCommand("dev")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"validate", "--repo", r.Dir})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "problem(s) found") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), `platform/sizes.yaml:4:`) || !strings.Contains(out.String(), `unknown field "cpus"`) {
		t.Errorf("output:\n%s", out.String())
	}
}
