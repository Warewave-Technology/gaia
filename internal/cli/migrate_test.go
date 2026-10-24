package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/itest"
)

func TestMigrate(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	out := run(t, NewRootCommand("dev"), "migrate", "--dsn", dsn)
	if !strings.Contains(out, "schema up to date") {
		t.Errorf("output = %q", out)
	}
}

func TestMigrateNeedsADatabase(t *testing.T) {
	t.Setenv("GAIA_DSN", "")
	root := NewRootCommand("dev")
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"migrate"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "GAIA_DSN") {
		t.Errorf("err = %v", err)
	}
}
