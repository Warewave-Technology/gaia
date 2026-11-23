package server

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ADR-005 G: a kept disk is removed at the latest after 24 hours, and the removal is logged.
func TestSweepKeptDisks(t *testing.T) {
	images, jobs := t.TempDir(), t.TempDir()
	var logged bytes.Buffer
	s := &Server{cfg: Config{SandboxImagesDir: images, ConvertJobsDir: jobs}, log: slog.New(slog.NewTextHandler(&logged, nil))}
	now := time.Now()
	old, fresh := now.Add(-25*time.Hour), now.Add(-time.Hour)
	os.MkdirAll(filepath.Join(jobs, "mc3-out"), 0o750)
	files := map[string]time.Time{
		filepath.Join(images, "win-20261123T091350Z-036bd4-after-sda.qcow2"): old,
		filepath.Join(images, "win-20261123T101350Z-0abcde-after-sda.qcow2"): fresh,
		filepath.Join(images, "mc3-sda.qcow2"):                               old, // a disk handed to the sandbox, not a kept one
		filepath.Join(jobs, "mc3-out", "win-sda.after.qcow2"):                old,
		filepath.Join(jobs, "mc3-out", "win-sda"):                            old,
	}
	for f, at := range files {
		os.WriteFile(f, []byte("x"), 0o640)
		os.Chtimes(f, at, at)
	}
	if err := s.sweepKeptDisks(now); err != nil {
		t.Fatal(err)
	}
	for f, at := range files {
		_, err := os.Stat(f)
		kept := strings.Contains(f, "after") && at.Equal(old)
		if (err == nil) == kept {
			t.Errorf("%s: exists %v", filepath.Base(f), err == nil)
		}
	}
	if n := strings.Count(logged.String(), "removed a guest's kept disk"); n != 2 {
		t.Errorf("%d removals logged:\n%s", n, logged.String())
	}
	// at most every hour
	os.WriteFile(filepath.Join(jobs, "mc3-out", "win-sda.after.qcow2"), []byte("x"), 0o640)
	os.Chtimes(filepath.Join(jobs, "mc3-out", "win-sda.after.qcow2"), old, old)
	if err := s.sweepKeptDisks(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(jobs, "mc3-out", "win-sda.after.qcow2")); err != nil {
		t.Errorf("swept again within the hour: %v", err)
	}
}
