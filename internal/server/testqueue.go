package server

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Parallel restore tests (ADR-007 C4): gaia server sends as many tests at once as the sandbox helper has slots; the
// helper admits each by memory (internal/sandbox/slots.go). The slot count is read from the helper at every tick.

// sandboxState is the helper's capacity as last read.
type sandboxState struct {
	mu  sync.Mutex
	cap *sandbox.Capacity
	at  time.Time
	err error
}

// refreshSandbox reads the helper's capacity (at most every 10 s).
func (s *Server) refreshSandbox(ctx context.Context) {
	s.sandboxCap.mu.Lock()
	fresh := time.Since(s.sandboxCap.at) < 10*time.Second
	s.sandboxCap.mu.Unlock()
	if fresh || s.sandbox == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := s.sandbox.Capacity(cctx)
	s.sandboxCap.mu.Lock()
	defer s.sandboxCap.mu.Unlock()
	s.sandboxCap.at, s.sandboxCap.err = time.Now(), err
	if err == nil {
		s.sandboxCap.cap = &c
	}
}

// sandboxCapacity is the helper's capacity and how old it is; nil when it was never read.
func (s *Server) sandboxCapacity() (*sandbox.Capacity, error) {
	s.sandboxCap.mu.Lock()
	defer s.sandboxCap.mu.Unlock()
	return s.sandboxCap.cap, s.sandboxCap.err
}

// testSlots is how many restore tests may run at once: the helper's slots; 1 while it does not answer (the test then
// shows why it could not run, as before).
func (s *Server) testSlots() int {
	c, err := s.sandboxCapacity()
	if err != nil || c == nil || c.Slots < 1 {
		return 1
	}
	return c.Slots
}

// slotFreed tells the scheduler that a restore test ended, so the next queued test starts now rather than at the next
// tick: with tests of a few tens of seconds, a slot that waits for the tick idles a large part of the time (T25).
func (s *Server) slotFreed() {
	select {
	case s.testFreed <- struct{}{}:
	default:
	}
}

// dispatchTests starts queued restore tests in C4's order while slots are free, one per VM.
func (s *Server) dispatchTests(ctx context.Context) error {
	s.refreshSandbox(ctx)
	qs, err := s.db.QueuedTests(ctx)
	if err != nil {
		return err
	}
	n := 0
	s.testing.Range(func(_, _ any) bool { n++; return true })
	for _, j := range qs {
		if n >= s.testSlots() {
			return nil
		}
		if j.VMID == nil {
			continue
		}
		if _, busy := s.testing.LoadOrStore(*j.VMID, j.ID); busy {
			continue
		}
		n++
		s.jobsWG.Add(1)
		run := s.runTest
		if s.testRun != nil {
			run = s.testRun
		}
		go func(j store.Job) {
			defer s.jobsWG.Done()
			defer s.stateChanged()
			defer s.slotFreed()
			defer s.testing.Delete(*j.VMID)
			defer s.retryIfStopped(j)
			run(s.jobs, j)
		}(j)
	}
	return nil
}

// imagesReservation is the restored disks of the running tests, so parallel tests do not fill the images directory.
type imagesReservation struct {
	mu    sync.Mutex
	bytes int64
}

// restoreReservation is what a restore reserves in the images directory: the full (virtual) size of every disk of the
// point, not its data (ADR-007 C13): a restored image may be written in full.
func restoreReservation(disks []store.PointDisk) int64 {
	var need int64
	for _, k := range disks {
		need += k.SizeBytes
	}
	return need
}

// reserveImages reserves room for a test's restored disks (their full size: the safe bound) in the sandbox images
// directory, beside what the running tests reserved; it says why when there is no room. release gives it back.
func (s *Server) reserveImages(need int64) (release func(), err error) {
	free, err := freeBytes(s.cfg.SandboxImagesDir)
	if err != nil {
		return nil, fmt.Errorf("the sandbox images directory %s: %w", s.cfg.SandboxImagesDir, err)
	}
	s.images.mu.Lock()
	defer s.images.mu.Unlock()
	if free-s.images.bytes < need {
		return nil, fmt.Errorf("the sandbox images directory %s has %d bytes free, %d of them held by running tests; this restore needs up to %d",
			s.cfg.SandboxImagesDir, free, s.images.bytes, need)
	}
	s.images.bytes += need
	return func() {
		s.images.mu.Lock()
		s.images.bytes -= need
		s.images.mu.Unlock()
	}, nil
}

func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
