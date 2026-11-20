package sandbox

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/verify"
)

// Slots (ADR-007 C4): the helper runs up to Slots restore tests at once, each in its own slot (network namespace,
// veth, dnsmasq, overlay, address range 10.254.<slot>.0/24; package verify). A test is admitted only when a slot is
// free and its guest's memory plus OverheadMiB fits under the sandbox's memory limit with the guests already running.
// Waiting tests are taken in the order they came, except that one which fits may pass one that does not (a large
// Windows guest waiting for memory does not hold back small Linux tests), until the first waiter has waited
// HeadWait: then nothing passes it.

// OverheadMiB is what a running test costs beyond its guest's memory: QEMU, the overlay's cache, dnsmasq, the probes.
const OverheadMiB = 256

// HeadWait is how long the first waiting test may be passed by smaller ones.
const HeadWait = 10 * time.Minute

// Capacity is what the helper can run, and what it runs now (GET /v1/capacity).
type Capacity struct {
	Slots          int       `json:"slots"`
	MemoryLimitMiB int       `json:"memory_limit_mib"`
	OverheadMiB    int       `json:"overhead_mib"`
	UsedMiB        int       `json:"used_mib"`
	Running        []Running `json:"running"`
	Waiting        []Waiting `json:"waiting"`
	// How the defaults were reached, when they were not set.
	Basis string `json:"basis"`
}

// Running is a test in a slot.
type Running struct {
	Slot      int       `json:"slot"`
	VM        string    `json:"vm"`
	MemoryMiB int       `json:"memory_mib"`
	Since     time.Time `json:"since"`
}

// Waiting is a test waiting for admission.
type Waiting struct {
	VM        string    `json:"vm"`
	MemoryMiB int       `json:"memory_mib"`
	Since     time.Time `json:"since"`
	Why       string    `json:"why"` // no free slot, or not enough memory
}

type waiter struct {
	vm    string
	need  int // guest memory + overhead
	since time.Time
	slot  chan int
}

type slots struct {
	mu      sync.Mutex
	n       int
	limit   int
	basis   string
	busy    []*Running
	usedMiB int
	queue   []*waiter
	now     func() time.Time
}

func newSlots(n, limitMiB int, basis string) *slots {
	return &slots{n: n, limit: limitMiB, basis: basis, busy: make([]*Running, n), now: time.Now}
}

// acquire waits for a slot and memory for a guest of memMiB; it returns the slot, or an error when the guest can never
// fit or ctx ends first.
func (s *slots) acquire(ctx context.Context, vm string, memMiB int) (int, error) {
	need := memMiB + OverheadMiB
	s.mu.Lock()
	if need > s.limit {
		s.mu.Unlock()
		return 0, fmt.Errorf("the guest needs %d MiB (%d + %d overhead); the sandbox's memory limit is %d MiB", need, memMiB, OverheadMiB, s.limit)
	}
	w := &waiter{vm: vm, need: need, since: s.now(), slot: make(chan int, 1)}
	s.queue = append(s.queue, w)
	s.admit()
	s.mu.Unlock()
	select {
	case slot := <-w.slot:
		return slot, nil
	case <-ctx.Done():
		s.mu.Lock()
		defer s.mu.Unlock()
		select {
		case slot := <-w.slot: // admitted just now: give it back
			s.freeLocked(slot)
		default:
			for i, q := range s.queue {
				if q == w {
					s.queue = append(s.queue[:i], s.queue[i+1:]...)
					break
				}
			}
		}
		s.admit()
		return 0, ctx.Err()
	}
}

// admit hands free slots to waiters that fit (the lock is held).
func (s *slots) admit() {
	for i := 0; i < len(s.queue); {
		w := s.queue[i]
		free := -1
		for k, b := range s.busy {
			if b == nil {
				free = k
				break
			}
		}
		if free < 0 {
			return
		}
		if s.usedMiB+w.need > s.limit {
			if i == 0 && s.now().Sub(w.since) >= HeadWait {
				return // the first waiter has waited long enough: nothing passes it
			}
			i++
			continue
		}
		s.busy[free] = &Running{Slot: free, VM: w.vm, MemoryMiB: w.need - OverheadMiB, Since: s.now()}
		s.usedMiB += w.need
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		w.slot <- free
	}
}

func (s *slots) release(slot int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeLocked(slot)
	s.admit()
}

func (s *slots) freeLocked(slot int) {
	if b := s.busy[slot]; b != nil {
		s.usedMiB -= b.MemoryMiB + OverheadMiB
		s.busy[slot] = nil
	}
}

func (s *slots) capacity() Capacity {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := Capacity{Slots: s.n, MemoryLimitMiB: s.limit, OverheadMiB: OverheadMiB, UsedMiB: s.usedMiB, Running: []Running{}, Waiting: []Waiting{},
		Basis: s.basis}
	free := false
	for _, b := range s.busy {
		if b != nil {
			c.Running = append(c.Running, *b)
		} else {
			free = true
		}
	}
	for _, w := range s.queue {
		why := "no free slot"
		if free {
			why = fmt.Sprintf("not enough memory: %d MiB used of %d, the guest needs %d", s.usedMiB, s.limit, w.need)
		}
		c.Waiting = append(c.Waiting, Waiting{VM: w.vm, MemoryMiB: w.need - OverheadMiB, Since: w.since, Why: why})
	}
	return c
}

// defaultSlots is the number of slots and the memory limit when the configuration does not set them: the limit is
// three quarters of the host's memory (the rest for the OS, libvirt and gaia server); a slot per two CPUs and per
// default-sized guest (verify's default, 1 GiB, plus the overhead) that fits; at least 1, at most verify.MaxSlots.
func defaultSlots(slots, limitMiB int) (int, int, string, error) {
	var parts []string
	if limitMiB <= 0 {
		total, err := memTotalMiB()
		if err != nil {
			return 0, 0, "", err
		}
		limitMiB = total * 3 / 4
		parts = append(parts, fmt.Sprintf("memory limit 3/4 of %d MiB", total))
	}
	if slots <= 0 {
		byCPU := max(1, runtime.NumCPU()/2)
		byMem := max(1, limitMiB/(1024+OverheadMiB))
		slots = min(verify.MaxSlots, byCPU, byMem)
		parts = append(parts, fmt.Sprintf("slots = min(%d, %d CPUs / 2, %d MiB / %d MiB)", verify.MaxSlots, runtime.NumCPU(), limitMiB, 1024+OverheadMiB))
	}
	if slots > verify.MaxSlots {
		return 0, 0, "", fmt.Errorf("%d slots: at most %d", slots, verify.MaxSlots)
	}
	basis := "configured"
	if len(parts) > 0 {
		basis = strings.Join(parts, "; ")
	}
	return slots, limitMiB, basis, nil
}

func memTotalMiB() (int, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			kb, err := strconv.Atoi(strings.Fields(v)[0])
			return kb / 1024, err
		}
	}
	return 0, fmt.Errorf("no MemTotal in /proc/meminfo")
}
