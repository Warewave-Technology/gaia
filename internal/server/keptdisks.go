package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Kept disks (ADR-005 F, G): what a converted guest that did not pass wrote to its disk is kept for the converter to
// read virt-v2v's first-boot log. A migration check removes its copies when it finishes (migration.go); a server
// that stopped in between leaves them behind, so the scheduler removes any older than keptDiskMaxAge. Every removal
// is logged.
const (
	keptDiskMaxAge = 24 * time.Hour
	keptSweepGap   = time.Hour
)

// keptDiskPatterns are where kept disks are: the sandbox's (<bundle id>-after-<target>.qcow2) and the converter's
// copy in a job's out directory (<disk>.after.qcow2).
func (s *Server) keptDiskPatterns() []string {
	var p []string
	if s.cfg.SandboxImagesDir != "" {
		p = append(p, filepath.Join(s.cfg.SandboxImagesDir, "*-after-*.qcow2"))
	}
	if s.cfg.ConvertJobsDir != "" {
		p = append(p, filepath.Join(s.cfg.ConvertJobsDir, "*-out", "*.after.qcow2"))
	}
	return p
}

// sweepKeptDisks removes kept disks older than keptDiskMaxAge, at most every keptSweepGap.
func (s *Server) sweepKeptDisks(now time.Time) error {
	if now.Sub(s.keptSweepAt) < keptSweepGap {
		return nil
	}
	s.keptSweepAt = now
	var errs []error
	for _, pat := range s.keptDiskPatterns() {
		files, err := filepath.Glob(pat)
		if err != nil {
			return err
		}
		for _, f := range files {
			fi, err := os.Lstat(f)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, err)
				}
				continue
			}
			if !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < keptDiskMaxAge {
				continue
			}
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("removing a kept disk %s: %w", f, err))
				continue
			}
			s.log.Info("removed a guest's kept disk", "file", f, "reason", "older than 24 h (ADR-005 G)", "modified", fi.ModTime().UTC().Format(time.RFC3339))
		}
	}
	return errors.Join(errs...)
}

// removeKeptDisk removes one kept disk when its check is done, and logs it.
func (s *Server) removeKeptDisk(f, why string) {
	if err := os.Remove(f); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Error("removing a guest's kept disk", "file", f, "error", err)
		}
		return
	}
	s.log.Info("removed a guest's kept disk", "file", f, "reason", why)
}
