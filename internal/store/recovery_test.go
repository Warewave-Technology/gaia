package store

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveryKitsAndRestores(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.Migrate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ConfirmRecoveryKit(ctx, "admin", 1, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("confirm without a kit: %v", err)
	}
	if n, err := d.NextKitGeneration(ctx); err != nil || n != 1 {
		t.Fatalf("next generation %d %v", n, err)
	}
	k1, err := d.AddRecoveryKit(ctx, "gaia server init", RecoveryKit{Generation: 1, Reason: "server init", SHA256: "aa"})
	if err != nil || k1.Repositories == nil || k1.ConfirmedAt != "" {
		t.Fatalf("kit 1 %+v %v", k1, err)
	}
	if _, err := d.AddRecoveryKit(ctx, "admin", RecoveryKit{Generation: 1, Reason: "again", SHA256: "bb"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a generation twice: %v", err)
	}
	if _, err := d.AddRecoveryKit(ctx, "admin", RecoveryKit{Generation: 2, Reason: "repository main created", SHA256: "bb", Repositories: []string{"main"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ConfirmRecoveryKit(ctx, "admin", 1, "aa"); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirming an old generation: %v", err)
	}
	if _, err := d.ConfirmRecoveryKit(ctx, "admin", 2, "aa"); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirming another file: %v", err)
	}
	k2, err := d.ConfirmRecoveryKit(ctx, "admin", 2, "bb")
	if err != nil || k2.ConfirmedBy != "admin" || k2.ConfirmedAt == "" || k2.Repositories[0] != "main" {
		t.Fatalf("confirm %+v %v", k2, err)
	}
	if again, err := d.ConfirmRecoveryKit(ctx, "other", 2, "bb"); err != nil || again.ConfirmedBy != "admin" {
		t.Fatalf("confirming twice %+v %v", again, err)
	}
	ks, err := d.RecoveryKits(ctx)
	if err != nil || len(ks) != 2 || ks[0].Generation != 2 {
		t.Fatalf("kits %+v %v", ks, err)
	}
	ch, err := d.StateChanges(ctx, "recovery_kit", 2, 0, 10)
	if err != nil || len(ch) != 2 || ch[1].To != "confirmed" {
		t.Fatalf("kit 2 changes %+v %v", ch, err)
	}

	if tag, err := d.RestoreTag(ctx); err != nil || tag != "" {
		t.Fatalf("tag before a restore %q %v", tag, err)
	}
	r, err := d.AddStateRestore(ctx, "gaia server restore-state", StateRestore{Tag: "r1a2", StateBackup: "m1", StateBackupCreated: "2026-11-17T00:00:00Z", KitGeneration: 2})
	if err != nil || r.ID != 1 || string(r.Detail) != "{}" {
		t.Fatalf("restore %+v %v", r, err)
	}
	if tag, err := d.RestoreTag(ctx); err != nil || tag != "r1a2" {
		t.Fatalf("tag %q %v", tag, err)
	}

	repo, err := d.CreateRepository(ctx, Repository{Name: "main", Kind: "filesystem", Location: "/srv/a", Splitter: "FIXED-1M", Hash: "BLAKE2B-256-128"})
	if err != nil {
		t.Fatal(err)
	}
	if repo, err = d.SetRepositoryLocation(ctx, "restore-state", repo.ID, "/mnt/b", "mounted elsewhere after the rebuild"); err != nil || repo.Location != "/mnt/b" {
		t.Fatalf("location %+v %v", repo, err)
	}
	if ch, err := d.StateChanges(ctx, "repository", repo.ID, 0, 10); err != nil || len(ch) != 1 || ch[0].To != "location /mnt/b" {
		t.Fatalf("repository changes %+v %v", ch, err)
	}
}
