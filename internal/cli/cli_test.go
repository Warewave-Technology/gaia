package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func run(t *testing.T, root *cobra.Command, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

func TestVersion(t *testing.T) {
	for _, root := range []*cobra.Command{NewRootCommand("v1.2.3"), NewIPAMCommand("v1.2.3")} {
		if got := strings.TrimSpace(run(t, root, "version")); got != "v1.2.3" {
			t.Errorf("%s version = %q, want v1.2.3", root.Name(), got)
		}
	}
}
