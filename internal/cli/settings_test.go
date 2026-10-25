package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func TestSettingsImportExport(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	lab := filepath.Join("..", "..", "examples", "lab", "platform")
	out := run(t, NewRootCommand("dev"), "settings", "import", lab, "--dsn", dsn)
	if !strings.HasPrefix(out, "settings@1: ") {
		t.Fatalf("import: %q", out)
	}
	if out := run(t, NewRootCommand("dev"), "settings", "versions", "--dsn", dsn); !strings.Contains(out, "gaia settings import") {
		t.Errorf("versions: %q", out)
	}
	dir := t.TempDir()
	if out := run(t, NewRootCommand("dev"), "settings", "export", dir, "--dsn", dsn); !strings.Contains(out, "settings@1") {
		t.Errorf("export: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Network", "lab-poc.yaml"))
	if err != nil || !strings.Contains(string(data), "gateway: 10.50.0.1") {
		t.Errorf("exported network: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Baseline")); err == nil {
		t.Error("a Git kind was exported")
	}

	// Validate reads the exported settings instead of Git's copies: a
	// broken size in Git does not count, a broken one in the settings does.
	r := labRepo(t)
	r.Write("platform/catalog/sizes.yaml", "apiVersion: gaia/v1alpha1\nkind: Size\nmetadata: { name: small }\nspec: { cpu: 0, memoryGiB: 2, diskGiB: 20 }\n")
	r.Commit("lab")
	if out := run(t, NewRootCommand("dev"), "validate", "--repo", r.Dir, "--path", "platform", "--path", "contracts", "--settings", dir); !strings.Contains(out, "files") {
		t.Errorf("validate with settings: %q", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "Size", "small.yaml"), []byte("apiVersion: gaia/v1alpha1\nkind: Size\nmetadata: { name: small }\nspec: { cpu: 0, memoryGiB: 2, diskGiB: 20 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := NewRootCommand("dev")
	var badOut strings.Builder
	bad.SetArgs([]string{"validate", "--repo", r.Dir, "--path", "platform", "--path", "contracts", "--settings", dir})
	bad.SetOut(&badOut)
	bad.SetErr(&badOut)
	if err := bad.Execute(); err == nil || !strings.Contains(badOut.String(), "settings:Size/small.yaml") {
		t.Errorf("a broken setting: %v %s", err, badOut.String())
	}

	again := NewRootCommand("dev")
	again.SetArgs([]string{"settings", "import", lab, "--dsn", dsn})
	again.SetOut(&strings.Builder{})
	again.SetErr(&strings.Builder{})
	if err := again.Execute(); err == nil || !strings.Contains(err.Error(), "there are settings already") {
		t.Errorf("a second import: %v", err)
	}
}
