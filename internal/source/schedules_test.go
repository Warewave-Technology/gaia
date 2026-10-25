package source

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func TestWindows(t *testing.T) {
	spec := &catalog.OperationScheduleSpec{Recurrence: "FREQ=MONTHLY;BYDAY=2SU", Start: "02:00", Timezone: "Europe/Istanbul", Duration: "4h", CreateAhead: "20d"}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	got, err := Windows(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	// November 2026: the 1st is a Sunday, so the second Sunday is the 8th.
	if len(got) != 1 || got[0].Format(time.RFC3339) != "2026-11-08T02:00:00+03:00" {
		t.Errorf("windows = %v", got)
	}
	spec.CreateAhead = "7d"
	if got, _ := Windows(spec, now); len(got) != 0 {
		t.Errorf("within a week: %v", got)
	}
}

func TestSchedulesWriteOperations(t *testing.T) {
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	platform := labRepo(t, map[string]string{
		"platform/schedules.yaml": `apiVersion: gaia/v1alpha1
kind: OperationSchedule
metadata: { name: weekly-reboot }
spec:
  recurrence: "FREQ=WEEKLY;BYDAY=SA"
  start: "03:00"
  duration: 2h
  createAhead: 10d
  approval: once
  output: contracts/operations
  template:
    action: reboot
    selector: 'labels.env == "test"'
    strategy: { batchSize: 50% }
`,
	})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	m := &repo.Mirrors{Dir: t.TempDir()}
	s := &Syncer{Store: st, Repos: m, Writer: &repo.Writer{Mirrors: m, Signer: signature.SSHSigner{Key: key}, Name: "Gaia", Email: "gaia@example.com"}}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) // a Sunday
	run := func() Report {
		t.Helper()
		snap, _ := m.Head(ctx, platform)
		g, err := m.Graph(ctx, snap)
		if err != nil {
			t.Fatal(err)
		}
		o, _ := g.Catalog.Get(catalog.KindSchedule, "weekly-reboot")
		return s.schedules(ctx, snap, g, o, now)
	}
	r := run()
	if r.Status != "committed" || strings.Join(r.Added, ",") != "contracts/operations/weekly-reboot-20261031t0300.yaml" {
		t.Fatalf("first run: %+v", r)
	}
	snap, _ := m.Head(ctx, platform)
	g, err := m.Graph(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := g.Catalog.Get(catalog.KindOperation, "weekly-reboot-20261031t0300")
	if !ok || !o.Generated() {
		t.Fatal("no generated Operation")
	}
	op := o.Spec.(*catalog.OperationSpec)
	if op.Window.Start != "2026-10-31T03:00:00Z" || op.Window.End != "2026-10-31T05:00:00Z" || op.NotAfter != op.Window.End || op.Strategy.BatchSize != "50%" {
		t.Errorf("operation = %+v", op)
	}
	// A week on, the first stays and the next comes.
	now = now.Add(7 * 24 * time.Hour)
	if r := run(); r.Status != "committed" || strings.Join(r.Added, ",") != "contracts/operations/weekly-reboot-20261107t0300.yaml" || len(r.Removed) != 0 {
		t.Errorf("a week on: %+v", r)
	}
}
