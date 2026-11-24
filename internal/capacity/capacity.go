// Package capacity is the proof capacity of the sandbox (ADR-007 C4): can it keep every VM's proof fresh? It is a
// pure computation from the VMs' policies, their expected test durations and the number of slots, so the numbers
// can be checked by hand (a spreadsheet of the same inputs, T25) and a what-if changes only the inputs.
//
//	demand    = sum over VMs of (tests a day from the policy: 86,400 / test interval) x the VM's expected test duration
//	capacity  = slots x 86,400 slot-seconds a day
//	coverage  = capacity / demand (at most 1)
//	slots needed = ceil(demand / 86,400)
//
// The expected duration of a VM's test is the median of its last 10 tests that ran, else the median of its OS
// class (linux or windows: from the VM's os: tag, else its policy's check template; ADR-007 C13), else a fixed default
// (Linux 180 s, Windows 600 s).
//
// The queue (C4): retests of CRITICAL VMs first, then VMs never proven, then by the time their proof expires, then by
// policy name, then by when the test was queued. Simulate runs that queue on the slots for the next Horizon: every
// queued test's estimated start, and the VMs whose proof expires before their next test finishes.
//
// A test also needs room for its restored disks in the sandbox images directory (their full size, ADR-007 C13): with
// the directory's room given, a test whose disks do not fit beside the running ones waits for them, and smaller tests
// may pass it for 10 minutes, as the scheduler does (T29). A VM whose disks do not fit even alone is never tested.
package capacity

import (
	"cmp"
	"container/heap"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"
)

// Default expected durations when neither the VM nor its class has a test that ran.
const (
	DefaultLinuxS   = 180.0
	DefaultWindowsS = 600.0
	Day             = 86400.0
	// Horizon is how far the simulation looks ahead.
	Horizon = 24 * time.Hour
)

// VM is one VM with a policy, as the capacity sees it.
type VM struct {
	ID            int64      `json:"vm_id"`
	Name          string     `json:"vm"`
	PolicyID      int64      `json:"policy_id"`
	Policy        string     `json:"policy"`
	Class         string     `json:"os_class"` // linux, windows
	ClassBasis    string     `json:"-"`        // where the class came from (the os: tag, the check template's name)
	TestIntervalS int64      `json:"test_interval_s"`
	MaxProofAgeS  int64      `json:"max_proof_age_s"`
	Durations     []float64  `json:"-"`             // its last tests that ran, newest first (seconds)
	ProofAt       *time.Time `json:"proof_at"`      // its newest passing test finished; nil: never proven
	Critical      bool       `json:"critical"`      // its status is CRITICAL
	LastQueued    *time.Time `json:"last_queued"`   // its newest restore test job was created (the scheduler's clock)
	QueuedJob     int64      `json:"queued_job"`    // a queued test job, 0 if none
	QueuedAt      *time.Time `json:"queued_at"`     // and when it was queued
	RunningSince  *time.Time `json:"running_since"` // a running test started; nil if none
	DiskBytes     int64      `json:"disk_bytes"`    // what its test reserves in the images directory; 0: unknown
}

// Input is what the capacity is computed from.
type Input struct {
	Now   time.Time
	Slots int
	VMs   []VM
	// ImagesRoomBytes is the sandbox images directory's room with no test running; 0: not known, not limited.
	ImagesRoomBytes int64
}

// PassFor is how long smaller tests may pass a test that waits for room; then nothing passes it (ADR-007 C13).
const PassFor = 10 * time.Minute

// TooLarge is a VM whose restored disks do not fit the images directory even with no other test running.
type TooLarge struct {
	VMID      int64  `json:"vm_id"`
	VM        string `json:"vm"`
	DiskBytes int64  `json:"disk_bytes"`
}

// Group is the demand of a set of VMs (an OS class, a policy).
type Group struct {
	Name        string  `json:"name"`
	VMs         int     `json:"vms"`
	TestsPerDay float64 `json:"tests_per_day"`
	DemandS     float64 `json:"demand_slot_s_per_day"`
	MedianS     float64 `json:"median_test_s"` // of its VMs' expected durations
}

// Expiring is a VM whose proof expires within the horizon before its next test finishes.
type Expiring struct {
	VMID          int64      `json:"vm_id"`
	VM            string     `json:"vm"`
	Policy        string     `json:"policy"`
	ProofExpires  time.Time  `json:"proof_expires_at"`
	NextTestStart *time.Time `json:"next_test_start"` // nil: no test of it starts within the horizon
	NextTestEnd   *time.Time `json:"next_test_finish"`
}

// QueueItem is a queued test with its place in the order and when it should start.
type QueueItem struct {
	JobID          int64     `json:"job_id"`
	VMID           int64     `json:"vm_id"`
	VM             string    `json:"vm"`
	Position       int       `json:"queue_position"` // 1 starts next
	Reason         string    `json:"order_reason"`   // why it is where it is
	ExpectedS      float64   `json:"expected_s"`
	DurationBasis  string    `json:"duration_basis"`
	EstimatedStart time.Time `json:"estimated_start"`
	EstimatedEnd   time.Time `json:"estimated_finish"`
}

// Result is the capacity.
type Result struct {
	Now            time.Time   `json:"now"`
	Slots          int         `json:"slots"`
	VMs            int         `json:"vms"`
	DemandS        float64     `json:"demand_slot_s_per_day"`
	CapacityS      float64     `json:"capacity_slot_s_per_day"`
	Coverage       float64     `json:"coverage"` // capacity / demand, at most 1; 1 with no demand
	SlotsNeeded    int         `json:"slots_needed"`
	ByClass        []Group     `json:"by_os_class"`
	ByPolicy       []Group     `json:"by_policy"`
	Expiring       []Expiring  `json:"expiring"`
	Queue          []QueueItem `json:"queue"`
	HorizonS       float64     `json:"horizon_s"`
	TestsInHorizon int         `json:"tests_in_horizon"` // tests the simulation started
	// The images directory's room the simulation counted (0: not limited), and the VMs it can never test.
	ImagesRoomBytes int64      `json:"images_room_bytes"`
	TooLarge        []TooLarge `json:"disks_too_large"`
}

// Expected is a VM's expected test duration and where it came from.
func Expected(v VM, classMedian map[string]float64) (float64, string) {
	if len(v.Durations) > 0 {
		d := v.Durations[:min(10, len(v.Durations))]
		return median(d), "median of its last " + strconv.Itoa(len(d)) + " tests"
	}
	if m, ok := classMedian[v.Class]; ok {
		return m, "median of the " + v.Class + " class"
	}
	if v.Class == "windows" {
		return DefaultWindowsS, "default for windows"
	}
	return DefaultLinuxS, "default for linux"
}

// ClassMedians is the median, per OS class, of the expected durations of the VMs with their own tests.
func ClassMedians(vms []VM) map[string]float64 {
	by := map[string][]float64{}
	for _, v := range vms {
		if len(v.Durations) > 0 {
			by[v.Class] = append(by[v.Class], median(v.Durations[:min(10, len(v.Durations))]))
		}
	}
	out := map[string]float64{}
	for c, ds := range by {
		out[c] = median(ds)
	}
	return out
}

// Compute is the demand, the capacity and the simulated queue.
func Compute(in Input) Result {
	cm := ClassMedians(in.VMs)
	r := Result{Now: in.Now, Slots: in.Slots, VMs: len(in.VMs), CapacityS: float64(in.Slots) * Day, ByClass: []Group{}, ByPolicy: []Group{},
		Expiring: []Expiring{}, Queue: []QueueItem{}, HorizonS: Horizon.Seconds(), ImagesRoomBytes: in.ImagesRoomBytes, TooLarge: []TooLarge{}}
	type acc struct {
		g  Group
		ds []float64
	}
	classes, policies := map[string]*acc{}, map[string]*acc{}
	exp := make([]float64, len(in.VMs))
	basis := make([]string, len(in.VMs))
	for i, v := range in.VMs {
		exp[i], basis[i] = Expected(v, cm)
		perDay := Day / float64(v.TestIntervalS)
		r.DemandS += perDay * exp[i]
		for _, m := range []struct {
			to  map[string]*acc
			key string
		}{{classes, v.Class}, {policies, v.Policy}} {
			a := m.to[m.key]
			if a == nil {
				a = &acc{g: Group{Name: m.key}}
				m.to[m.key] = a
			}
			a.g.VMs++
			a.g.TestsPerDay += perDay
			a.g.DemandS += perDay * exp[i]
			a.ds = append(a.ds, exp[i])
		}
	}
	for _, m := range []struct {
		from map[string]*acc
		to   *[]Group
	}{{classes, &r.ByClass}, {policies, &r.ByPolicy}} {
		for _, a := range m.from {
			a.g.MedianS = median(a.ds)
			*m.to = append(*m.to, a.g)
		}
		slices.SortFunc(*m.to, func(a, b Group) int { return cmp.Or(cmp.Compare(b.DemandS, a.DemandS), cmp.Compare(a.Name, b.Name)) })
	}
	r.Coverage = 1
	if r.DemandS > 0 {
		r.Coverage = math.Min(1, r.CapacityS/r.DemandS)
	}
	r.SlotsNeeded = int(math.Ceil(r.DemandS/Day - 1e-9))
	simulate(in, exp, basis, &r)
	return r
}

// One VM in the simulation.
type simVM struct {
	i          int
	v          *VM
	due        time.Time // when its next test is queued (it waits from then)
	queued     time.Time // the same, before it was moved to now: how long a test queued before now has waited
	job        int64     // the real queued job, for its first test
	proofEnd   *time.Time
	critical   bool
	never      bool
	firstStart *time.Time
	firstEnd   *time.Time
	index      int // in the due heap
}

func (a *simVM) expiry() time.Time {
	if a.proofEnd == nil {
		return time.Time{}
	}
	return *a.proofEnd
}

// orderLess is C4's queue order.
func orderLess(a, b *simVM) bool {
	if a.critical != b.critical {
		return a.critical
	}
	if a.never != b.never {
		return a.never
	}
	if ea, eb := a.expiry(), b.expiry(); !ea.Equal(eb) {
		return ea.Before(eb)
	}
	if a.v.Policy != b.v.Policy {
		return a.v.Policy < b.v.Policy
	}
	if !a.due.Equal(b.due) {
		return a.due.Before(b.due)
	}
	return a.v.ID < b.v.ID
}

func reasonOf(a *simVM) string {
	switch {
	case a.critical:
		return "retest of a CRITICAL VM"
	case a.never:
		return "never proven"
	default:
		return "proof expires " + a.expiry().UTC().Format(time.RFC3339) + " (policy " + a.v.Policy + ")"
	}
}

type byDue []*simVM

func (h byDue) Len() int           { return len(h) }
func (h byDue) Less(i, j int) bool { return h[i].due.Before(h[j].due) }
func (h byDue) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *byDue) Push(x any)        { *h = append(*h, x.(*simVM)) }
func (h *byDue) Pop() any          { o := *h; n := len(o); x := o[n-1]; *h = o[:n-1]; return x }

type byOrder []*simVM

func (h byOrder) Len() int           { return len(h) }
func (h byOrder) Less(i, j int) bool { return orderLess(h[i], h[j]) }
func (h byOrder) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *byOrder) Push(x any)        { *h = append(*h, x.(*simVM)) }
func (h *byOrder) Pop() any          { o := *h; n := len(o); x := o[n-1]; *h = o[:n-1]; return x }

// simulate runs the queue on the slots for the horizon (every test assumed to pass).
func simulate(in Input, exp []float64, basis []string, r *Result) {
	end := in.Now.Add(Horizon)
	free := make([]time.Time, in.Slots) // when each slot is free
	for i := range free {
		free[i] = in.Now
	}
	var notDue byDue
	var ready byOrder
	sims := make([]*simVM, len(in.VMs))
	slot := 0
	room := in.ImagesRoomBytes
	var runs []simRun  // the tests in the slots, with their disks
	var never []*simVM // too large for the images directory: never started
	for i := range in.VMs {
		v := &in.VMs[i]
		a := &simVM{i: i, v: v, critical: v.Critical, never: v.ProofAt == nil}
		if v.ProofAt != nil {
			e := v.ProofAt.Add(time.Duration(v.MaxProofAgeS) * time.Second)
			a.proofEnd = &e
		}
		sims[i] = a
		switch {
		case v.RunningSince != nil: // in a slot now: that slot frees when it should end (not before now)
			fin := v.RunningSince.Add(time.Duration(exp[i] * float64(time.Second)))
			if fin.Before(in.Now) {
				fin = in.Now
			}
			if in.Slots > 0 {
				free[slot%in.Slots] = maxTime(free[slot%in.Slots], fin)
				slot++
			}
			a.firstStart, a.firstEnd = v.RunningSince, &fin
			runs = append(runs, simRun{end: fin, disk: v.DiskBytes})
			a.critical, a.never = false, false
			e := fin.Add(time.Duration(v.MaxProofAgeS) * time.Second)
			a.proofEnd = &e
			a.due = v.RunningSince.Add(time.Duration(v.TestIntervalS) * time.Second)
			if v.LastQueued != nil {
				a.due = v.LastQueued.Add(time.Duration(v.TestIntervalS) * time.Second)
			}
		case v.QueuedJob != 0 && v.QueuedAt != nil:
			a.due, a.job = *v.QueuedAt, v.QueuedJob
		case v.LastQueued != nil:
			a.due = v.LastQueued.Add(time.Duration(v.TestIntervalS) * time.Second)
		default:
			a.due = in.Now
		}
		a.queued = a.due
		if a.due.Before(in.Now) {
			a.due = in.Now
		}
		if room > 0 && v.DiskBytes > room {
			r.TooLarge = append(r.TooLarge, TooLarge{VMID: v.ID, VM: v.Name, DiskBytes: v.DiskBytes})
			never = append(never, a)
			continue
		}
		heap.Push(&notDue, a)
	}
	if in.Slots <= 0 {
		for notDue.Len() > 0 { // no slot: nothing runs; every proof that ends in the horizon expires
			a := heap.Pop(&notDue).(*simVM)
			if a.job != 0 {
				r.Queue = append(r.Queue, QueueItem{JobID: a.job, VMID: a.v.ID, VM: a.v.Name, Reason: reasonOf(a), ExpectedS: exp[a.i], DurationBasis: basis[a.i]})
			}
		}
		for i := range r.Queue {
			r.Queue[i].Position = i + 1
		}
		expiring(in, sims, end, r)
		return
	}
	pos := 0
	for {
		// The slot that frees first.
		k := 0
		for i := range free {
			if free[i].Before(free[k]) {
				k = i
			}
		}
		t := free[k]
		if !t.Before(end) {
			break
		}
		for notDue.Len() > 0 && !notDue[0].due.After(t) {
			heap.Push(&ready, heap.Pop(&notDue))
		}
		runs = slices.DeleteFunc(runs, func(x simRun) bool { return !x.end.After(t) }) // t only grows: at most a test per slot stays
		if ready.Len() == 0 {
			if notDue.Len() == 0 {
				break
			}
			free[k] = notDue[0].due // idle until the next test is queued
			continue
		}
		a := pick(&ready, runs, room, t)
		if a == nil { // nothing fits beside the running tests: this slot waits for the first of them to end
			next := time.Time{}
			for _, x := range runs {
				if next.IsZero() || x.end.Before(next) {
					next = x.end
				}
			}
			if next.IsZero() {
				break // cannot happen: a test that does not fit alone is never queued here
			}
			free[k] = next
			continue
		}
		d := time.Duration(exp[a.i] * float64(time.Second))
		start, fin := t, t.Add(d)
		if a.job != 0 {
			pos++
			r.Queue = append(r.Queue, QueueItem{JobID: a.job, VMID: a.v.ID, VM: a.v.Name, Position: pos, Reason: reasonOf(a), ExpectedS: exp[a.i],
				DurationBasis: basis[a.i], EstimatedStart: start, EstimatedEnd: fin})
			a.job = 0
		}
		if a.firstStart == nil {
			a.firstStart, a.firstEnd = &start, &fin
		}
		r.TestsInHorizon++
		free[k] = fin
		if room > 0 {
			runs = append(runs, simRun{end: fin, disk: a.v.DiskBytes})
		}
		e := fin.Add(time.Duration(a.v.MaxProofAgeS) * time.Second)
		a.proofEnd, a.critical, a.never = &e, false, false
		a.due = a.due.Add(time.Duration(a.v.TestIntervalS) * time.Second) // the scheduler queues at its interval
		if a.due.Before(start) {
			a.due = a.due.Add(time.Duration(math.Ceil(start.Sub(a.due).Seconds()/float64(a.v.TestIntervalS))) * time.Duration(a.v.TestIntervalS) * time.Second)
		}
		a.queued = a.due
		heap.Push(&notDue, a)
	}
	// Queued jobs that do not start in the horizon keep their order after the ones that do.
	var late []*simVM
	for _, h := range [][]*simVM{ready, notDue, never} {
		for _, a := range h {
			if a.job != 0 {
				late = append(late, a)
			}
		}
	}
	sort.SliceStable(late, func(i, j int) bool { return orderLess(late[i], late[j]) })
	for _, a := range late {
		pos++
		r.Queue = append(r.Queue, QueueItem{JobID: a.job, VMID: a.v.ID, VM: a.v.Name, Position: pos, Reason: reasonOf(a), ExpectedS: exp[a.i], DurationBasis: basis[a.i]})
	}
	expiring(in, sims, end, r)
}

// simRun is a test in a slot: when it ends and the room it holds in the images directory.
type simRun struct {
	end  time.Time
	disk int64
}

// pick takes the next test to start at t from the ready ones, in C4's order: the first whose disks fit beside the
// running tests' (room 0: no limit). Smaller tests may pass one that does not fit until it has waited PassFor since it
// was queued; then nothing passes it. nil: nothing may start now.
func pick(ready *byOrder, runs []simRun, room int64, t time.Time) *simVM {
	if room <= 0 {
		return heap.Pop(ready).(*simVM)
	}
	var used int64
	for _, x := range runs {
		used += x.disk // runs holds the tests still running at t
	}
	var passed []*simVM
	var got *simVM
	for ready.Len() > 0 {
		a := heap.Pop(ready).(*simVM)
		if a.v.DiskBytes <= room-used {
			got = a
			break
		}
		passed = append(passed, a)
		if len(passed) == 1 && t.Sub(a.queued) >= PassFor {
			break // the first in the order has waited long enough: nobody passes it
		}
	}
	for _, a := range passed {
		heap.Push(ready, a)
	}
	return got
}

// expiring lists the VMs whose current proof expires within the horizon before their next test finishes.
func expiring(in Input, sims []*simVM, end time.Time, r *Result) {
	for _, a := range sims {
		v := a.v
		if v.ProofAt == nil {
			continue // never proven: nothing to expire (the status already says UNKNOWN)
		}
		e := v.ProofAt.Add(time.Duration(v.MaxProofAgeS) * time.Second)
		if e.After(end) || (a.firstEnd != nil && !a.firstEnd.After(e)) {
			continue
		}
		x := Expiring{VMID: v.ID, VM: v.Name, Policy: v.Policy, ProofExpires: e}
		if a.firstEnd != nil {
			x.NextTestStart, x.NextTestEnd = a.firstStart, a.firstEnd
		}
		r.Expiring = append(r.Expiring, x)
	}
	slices.SortFunc(r.Expiring, func(a, b Expiring) int {
		return cmp.Or(a.ProofExpires.Compare(b.ProofExpires), cmp.Compare(a.VMID, b.VMID))
	})
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
