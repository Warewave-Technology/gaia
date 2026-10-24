package cli

import (
	"bytes"
	"context"
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

func TestIPAMServeAndPool(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	if out := run(t, NewIPAMCommand("dev"), "migrate", "--dsn", dsn); !strings.Contains(out, "schema up to date") {
		t.Fatalf("migrate: %q", out)
	}
	t.Setenv("GAIA_IPAM_TOKEN", "s3cret")

	addr := freeAddr(t)
	serve := NewIPAMCommand("dev")
	var out syncBuffer
	serve.SetOut(&out)
	serve.SetArgs([]string{"serve", "--dsn", dsn, "--listen", addr, "--quarantine", "0s"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve.ExecuteContext(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	waitHTTP(t, "http://"+addr+"/healthz")

	got := run(t, NewIPAMCommand("dev"), "pool", "set", "10.50.0.0/24", "--endpoint", "http://"+addr,
		"--gateway", "10.50.0.1", "--range", "10.50.0.100-10.50.0.199")
	if !strings.Contains(got, "pool 10.50.0.0/24: 10.50.0.100-10.50.0.199") {
		t.Errorf("pool set: %q", got)
	}
}
