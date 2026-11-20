package sandbox

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitQueued waits until n tests wait for admission.
func waitQueued(t *testing.T, s *slots, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if len(s.capacity().Waiting) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waiting %d, want %d", len(s.capacity().Waiting), n)
}

// Slots and memory admit tests; one that does not fit waits, a smaller one passes it until it has waited HeadWait;
// a guest larger than the limit is refused at once; a test given up while waiting leaves no trace.
func TestSlots(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s := newSlots(3, 4096, "test")
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }

	a, err := s.acquire(ctx, "linux-a", 768) // 1024 used
	if err != nil || a != 0 {
		t.Fatalf("first: %d %v", a, err)
	}
	b, err := s.acquire(ctx, "linux-b", 768) // 2048
	if err != nil || b != 1 {
		t.Fatalf("second: %d %v", b, err)
	}
	if _, err := s.acquire(ctx, "huge", 8192); err == nil || !strings.Contains(err.Error(), "memory limit is 4096") {
		t.Fatalf("a guest over the limit: %v", err)
	}

	// Windows (2048 + 256) does not fit beside 2048 MiB: it waits, with the reason.
	win := make(chan int, 1)
	go func() {
		slot, err := s.acquire(ctx, "windows", 2048)
		if err != nil {
			t.Error(err)
		}
		win <- slot
	}()
	waitQueued(t, s, 1)
	if c := s.capacity(); !strings.Contains(c.Waiting[0].Why, "not enough memory") || c.UsedMiB != 2048 || len(c.Running) != 2 {
		t.Fatalf("capacity while Windows waits: %+v", c)
	}
	// A small Linux test fits in the last slot and passes it.
	small, err := s.acquire(ctx, "linux-c", 256)
	if err != nil || small != 2 {
		t.Fatalf("a small test beside the waiting one: %d %v", small, err)
	}
	s.release(small)

	// Once Windows has waited HeadWait, nothing passes it: a small test waits too.
	mu.Lock()
	clock = clock.Add(HeadWait)
	mu.Unlock()
	cctx, cancel := context.WithCancel(ctx)
	blocked := make(chan error, 1)
	go func() {
		_, err := s.acquire(cctx, "linux-d", 256)
		blocked <- err
	}()
	waitQueued(t, s, 2)
	cancel() // given up: it leaves the queue
	if err := <-blocked; err == nil {
		t.Fatal("a cancelled wait was admitted")
	}
	waitQueued(t, s, 1)

	// Two Linux tests end: Windows is admitted.
	s.release(a)
	s.release(b)
	select {
	case slot := <-win:
		if c := s.capacity(); c.UsedMiB != 2048+OverheadMiB || len(c.Running) != 1 || c.Running[0].VM != "windows" || c.Running[0].Slot != slot {
			t.Fatalf("after Windows was admitted: %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Windows was not admitted")
	}
}

func TestDefaultSlots(t *testing.T) {
	if n, limit, basis, err := defaultSlots(4, 6000); err != nil || n != 4 || limit != 6000 || basis != "configured" {
		t.Fatalf("configured: %d %d %q %v", n, limit, basis, err)
	}
	if _, _, _, err := defaultSlots(9, 6000); err == nil {
		t.Fatal("9 slots accepted")
	}
	if n, _, basis, err := defaultSlots(0, 2000); err != nil || n != 1 || !strings.Contains(basis, "slots = min") {
		t.Fatalf("by memory: %d %q %v", n, basis, err)
	}
}
