package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
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
	// What was last written to the database as the last known slot count (ADR-007 C17).
	stored      time.Time
	storedSlots int
}

// lastKnownEvery is how often an unchanged slot count is written again as the last known one: slots_at is at most
// this much older than the helper's last answer.
const lastKnownEvery = time.Minute

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
	now := time.Now()
	s.sandboxCap.at, s.sandboxCap.err = now, err
	if err != nil {
		return
	}
	s.sandboxCap.cap = &c
	// The count it reported is the last known one while it does not answer, across restarts (ADR-007 C17).
	if c.Slots != s.sandboxCap.storedSlots || now.Sub(s.sandboxCap.stored) >= lastKnownEvery {
		if err := s.db.SetSandboxSlots(context.WithoutCancel(ctx), c.Slots, s.now()); err != nil {
			s.log.Error("the sandbox's slot count could not be stored as the last known one", "slots", c.Slots, "error", err)
			return
		}
		s.sandboxCap.stored, s.sandboxCap.storedSlots = now, c.Slots
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

// dispatchTests starts queued restore tests in C4's order while slots are free, one per VM. A test whose restored disks
// do not fit beside the running tests' waits for them (imagesAdmit); after waiting imagesPassFor, nothing passes it.
func (s *Server) dispatchTests(ctx context.Context) error {
	s.refreshSandbox(ctx)
	qs, err := s.db.QueuedTests(ctx)
	if err != nil {
		return err
	}
	n := 0
	s.testing.Range(func(_, _ any) bool { n++; return true })
	s.imagesForget(qs)
	free, ferr := freeBytes(s.cfg.SandboxImagesDir)
	for _, j := range qs {
		if n >= s.testSlots() {
			return nil
		}
		if j.VMID == nil {
			continue
		}
		if _, busy := s.testing.Load(*j.VMID); busy {
			continue
		}
		need, err := s.testNeed(ctx, *j.VMID)
		if err != nil {
			return err
		}
		if ok, waited := s.imagesAdmit(j, need, free, ferr); !ok {
			if waited >= imagesPassFor {
				return nil // it waited long enough: the tests after it wait for it (ADR-007 C13)
			}
			continue // a smaller test may pass it meanwhile
		}
		if _, busy := s.testing.LoadOrStore(*j.VMID, j.ID); busy {
			s.imagesDrop(j.ID) // started meanwhile by another dispatch
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
			defer s.imagesDrop(j.ID) // room reserved for a test that ended before its restore took it
			defer s.retryIfStopped(j)
			run(s.jobs, j)
		}(j)
	}
	return nil
}

// testNeed is what a VM's test would reserve in the images directory now: the full size of its newest checked point's
// disks (the point restoreTest takes); 0 without one (the test then says why it cannot run).
func (s *Server) testNeed(ctx context.Context, vmID int64) (int64, error) {
	p, err := s.db.LatestPoint(ctx, vmID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	disks, err := s.db.PointDisks(ctx, p.ID)
	if err != nil {
		return 0, err
	}
	return restoreReservation(disks), nil
}

// imagesReservation is the restored disks of the running tests, so parallel tests do not fill the images directory.
type imagesReservation struct {
	mu      sync.Mutex
	bytes   int64
	byJob   map[int64]int64      // reserved when the test was sent, until its restore takes it (reserveImages)
	waiting map[int64]imagesWait // tests held because the running tests hold the room they need
}

type imagesWait struct {
	vmID  int64
	need  int64
	since time.Time
}

// imagesPassFor is how long smaller tests may pass a test that waits for room; then nothing passes it (ADR-007 C13,
// as for memory in the sandbox helper).
const imagesPassFor = 10 * time.Minute

// imagesAdmit decides whether a queued test may be sent now (T29): it may when its disks fit beside what the running
// tests hold (the room is reserved for it now), or when they would not fit even with no test running (it runs and
// says why at once: UNKNOWN, as a restore larger than the directory always did). Otherwise it waits for the running
// tests, and waited is how long it has waited. A free space that cannot be read admits the test (its restore says why).
func (s *Server) imagesAdmit(j store.Job, need, free int64, ferr error) (ok bool, waited time.Duration) {
	if ferr != nil || need <= 0 {
		return true, 0
	}
	s.images.mu.Lock()
	defer s.images.mu.Unlock()
	if need <= free-s.images.bytes || need > free+s.images.bytes {
		if w, held := s.images.waiting[j.ID]; held {
			delete(s.images.waiting, j.ID)
			s.log.Info("a restore test waited for room in the sandbox images directory", "job", j.ID, "vm", w.vmID, "waited_s", int(time.Since(w.since).Seconds()))
		}
		if need <= free-s.images.bytes {
			if s.images.byJob == nil {
				s.images.byJob = map[int64]int64{}
			}
			s.images.bytes += need
			s.images.byJob[j.ID] = need
		}
		return true, 0
	}
	if s.images.waiting == nil {
		s.images.waiting = map[int64]imagesWait{}
	}
	w, held := s.images.waiting[j.ID]
	if !held {
		w = imagesWait{vmID: *j.VMID, need: need, since: time.Now()}
		s.images.waiting[j.ID] = w
		s.log.Info("a restore test waits for room in the sandbox images directory", "job", j.ID, "vm", *j.VMID, "need", need,
			"free", free, "held_by_running_tests", s.images.bytes)
	}
	return false, time.Since(w.since)
}

// imagesDrop gives back the room reserved for a test whose restore did not take it.
func (s *Server) imagesDrop(job int64) {
	s.images.mu.Lock()
	defer s.images.mu.Unlock()
	s.images.bytes -= s.images.byJob[job]
	delete(s.images.byJob, job)
	delete(s.images.waiting, job)
}

// imagesForget forgets the waits of tests no longer queued (cancelled, or the VM removed).
func (s *Server) imagesForget(queued []store.Job) {
	s.images.mu.Lock()
	defer s.images.mu.Unlock()
	for id := range s.images.waiting {
		if !slices.ContainsFunc(queued, func(j store.Job) bool { return j.ID == id }) {
			delete(s.images.waiting, id)
		}
	}
}

// imagesHeld is what the running tests hold in the images directory and the tests that wait for room, oldest first.
func (s *Server) imagesHeld() (int64, []ImagesWaiting) {
	s.images.mu.Lock()
	defer s.images.mu.Unlock()
	out := []ImagesWaiting{}
	for id, w := range s.images.waiting {
		out = append(out, ImagesWaiting{JobID: id, VMID: w.vmID, NeedBytes: w.need, Since: w.since.UTC()})
	}
	slices.SortFunc(out, func(a, b ImagesWaiting) int { return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.JobID, b.JobID)) })
	return s.images.bytes, out
}

// ImagesWaiting is a queued test that waits for the running tests to give back room in the sandbox images directory.
type ImagesWaiting struct {
	JobID     int64     `json:"job_id"`
	VMID      int64     `json:"vm_id"`
	VM        string    `json:"vm"`
	NeedBytes int64     `json:"need_bytes"`
	Since     time.Time `json:"since"`
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
// directory, beside what the running tests reserved; it says why when there is no room. The room reserved when the
// test was sent (imagesAdmit) is its own: it is taken back first. release gives it back.
func (s *Server) reserveImages(job, need int64) (release func(), err error) {
	free, err := freeBytes(s.cfg.SandboxImagesDir)
	s.images.mu.Lock()
	defer s.images.mu.Unlock()
	s.images.bytes -= s.images.byJob[job]
	delete(s.images.byJob, job)
	if err != nil {
		return nil, fmt.Errorf("the sandbox images directory %s: %w", s.cfg.SandboxImagesDir, err)
	}
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
