package capacity

import (
	"math"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func at(min int) *time.Time { t := now.Add(time.Duration(min) * time.Minute); return &t }

// Demand, capacity, coverage and slots needed, by hand: a Linux VM tested hourly for 60 s is 24 x 60 = 1,440 slot-s a
// day; a Windows VM tested twice a day for 600 s is 1,200; 2,640 in all against 86,400 for one slot.
func TestDemand(t *testing.T) {
	vms := []VM{
		{ID: 1, Name: "lin", Policy: "p-lin", Class: "linux", TestIntervalS: 3600, MaxProofAgeS: 7200, Durations: []float64{60, 60, 60}, ProofAt: at(-10)},
		{ID: 2, Name: "win", Policy: "p-win", Class: "windows", TestIntervalS: 43200, MaxProofAgeS: 86400, Durations: []float64{600}, ProofAt: at(-10)},
	}
	r := Compute(Input{Now: now, Slots: 1, VMs: vms})
	if r.DemandS != 2640 || r.CapacityS != 86400 || r.Coverage != 1 || r.SlotsNeeded != 1 {
		t.Fatalf("demand %v capacity %v coverage %v needed %d", r.DemandS, r.CapacityS, r.Coverage, r.SlotsNeeded)
	}
	if len(r.ByClass) != 2 || r.ByClass[0].Name != "linux" || r.ByClass[0].DemandS != 1440 || r.ByClass[1].TestsPerDay != 2 {
		t.Fatalf("by class %+v", r.ByClass)
	}
	// 100 hourly Linux VMs of 1,200 s: 2,880,000 slot-s a day; 8 slots give 691,200, 24 % of it; 34 slots needed.
	var many []VM
	for i := range 100 {
		many = append(many, VM{ID: int64(i + 1), Policy: "p", Class: "linux", TestIntervalS: 3600, MaxProofAgeS: 7200, Durations: []float64{1200}})
	}
	r = Compute(Input{Now: now, Slots: 8, VMs: many})
	if r.DemandS != 2880000 || math.Abs(r.Coverage-0.24) > 1e-12 || r.SlotsNeeded != 34 {
		t.Fatalf("100 VMs: demand %v coverage %v needed %d", r.DemandS, r.Coverage, r.SlotsNeeded)
	}
}

// Expected duration: the median of the VM's last 10, else its class's median, else the default.
func TestExpected(t *testing.T) {
	own := VM{Class: "linux", Durations: []float64{10, 30, 20, 1000, 40, 50, 60, 70, 80, 90, 5000}} // the 11th is ignored
	cm := ClassMedians([]VM{own, {Class: "linux", Durations: []float64{100}}})
	if d, b := Expected(own, cm); d != 55 || b != "median of its last 10 tests" {
		t.Fatalf("own %v %q", d, b)
	}
	if d, b := Expected(VM{Class: "linux"}, cm); d != (55+100)/2.0 || b != "median of the linux class" {
		t.Fatalf("class %v %q", d, b)
	}
	if d, _ := Expected(VM{Class: "windows"}, cm); d != DefaultWindowsS {
		t.Fatalf("default %v", d)
	}
}

// The queue order of C4, and the estimated starts, on one slot: the CRITICAL VM's retest, then the never-proven VM,
// then by proof expiry. Each starts when the one before it ends.
func TestQueueOrder(t *testing.T) {
	q := func(id int64, name string, crit bool, proof *time.Time, maxAge int64) VM {
		return VM{ID: id, Name: name, Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: maxAge, Durations: []float64{600},
			ProofAt: proof, Critical: crit, QueuedJob: 100 + id, QueuedAt: at(-5)}
	}
	vms := []VM{
		q(1, "late-expiry", false, at(-10), 7200),  // expires in 110 min
		q(2, "early-expiry", false, at(-60), 7200), // expires in 60 min
		q(3, "never", false, nil, 7200),
		q(4, "critical", true, at(-10), 7200),
	}
	r := Compute(Input{Now: now, Slots: 1, VMs: vms})
	want := []string{"critical", "never", "early-expiry", "late-expiry"}
	if len(r.Queue) != 4 {
		t.Fatalf("queue %+v", r.Queue)
	}
	for i, w := range want {
		x := r.Queue[i]
		if x.VM != w || x.Position != i+1 || !x.EstimatedStart.Equal(now.Add(time.Duration(i)*10*time.Minute)) {
			t.Fatalf("position %d: %+v, want %s starting at +%d min", i+1, x, w, i*10)
		}
	}
	if r.Queue[0].Reason != "retest of a CRITICAL VM" || r.Queue[1].Reason != "never proven" {
		t.Fatalf("reasons %q %q", r.Queue[0].Reason, r.Queue[1].Reason)
	}
	// With two slots, the first two start now.
	r = Compute(Input{Now: now, Slots: 2, VMs: vms})
	if !r.Queue[0].EstimatedStart.Equal(now) || !r.Queue[1].EstimatedStart.Equal(now) || !r.Queue[2].EstimatedStart.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("two slots: %+v", r.Queue)
	}
}

// A VM whose proof expires before its next test can finish is listed: one slot, a CRITICAL VM's hour-long retest
// first, and a VM whose proof ends in 30 minutes behind it.
func TestExpiring(t *testing.T) {
	vms := []VM{
		{ID: 1, Name: "crit", Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: 86400, Durations: []float64{3600}, ProofAt: at(-10),
			Critical: true, QueuedJob: 11, QueuedAt: at(-1)},
		{ID: 2, Name: "soon", Policy: "p", Class: "linux", TestIntervalS: 3600, MaxProofAgeS: 3600, Durations: []float64{60}, ProofAt: at(-30),
			LastQueued: at(-35)},
		{ID: 3, Name: "fine", Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: 2 * 86400, Durations: []float64{60}, ProofAt: at(-60),
			LastQueued: at(-60)},
	}
	r := Compute(Input{Now: now, Slots: 1, VMs: vms})
	if len(r.Expiring) != 1 || r.Expiring[0].VM != "soon" || !r.Expiring[0].ProofExpires.Equal(*at(30)) ||
		r.Expiring[0].NextTestEnd == nil || !r.Expiring[0].NextTestEnd.Equal(*at(61)) {
		t.Fatalf("expiring %+v", r.Expiring)
	}
	// With a second slot it is tested at once: nothing expires.
	if r = Compute(Input{Now: now, Slots: 2, VMs: vms}); len(r.Expiring) != 0 {
		t.Fatalf("two slots: expiring %+v", r.Expiring)
	}
	// No slot (the sandbox is down): every proof that ends in the horizon expires, with no next test.
	if r = Compute(Input{Now: now, Slots: 0, VMs: vms}); len(r.Expiring) != 2 || r.Expiring[0].NextTestEnd != nil || r.Coverage != 0 {
		t.Fatalf("no slot: %+v coverage %v", r.Expiring, r.Coverage)
	}
}

// A running test holds its slot until it should end; it counts as the VM's next test.
func TestRunning(t *testing.T) {
	vms := []VM{
		{ID: 1, Name: "running", Policy: "p", Class: "windows", TestIntervalS: 86400, MaxProofAgeS: 86400, Durations: []float64{1200},
			ProofAt: at(-1000), RunningSince: at(-5), LastQueued: at(-6)},
		{ID: 2, Name: "waiting", Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: 86400, Durations: []float64{60},
			ProofAt: at(-10), QueuedJob: 22, QueuedAt: at(-1)},
	}
	r := Compute(Input{Now: now, Slots: 1, VMs: vms})
	if len(r.Queue) != 1 || !r.Queue[0].EstimatedStart.Equal(*at(15)) {
		t.Fatalf("queue behind a running test: %+v", r.Queue)
	}
}

// The images directory (T29): a test whose disks do not fit beside the running tests' waits for them, as the scheduler
// holds it. On the T29 lab, 4 slots and a 36 GB directory: the Windows VM (32 GiB of disks, about 8 minutes) and
// gaia-t23-uefi (6 GiB, under a minute) are due at the same minute; they never run together, and both are tested every
// hour. Without the room counted, they would run at once (what the scheduler refused before 0.2.1).
func TestImagesRoom(t *testing.T) {
	const gib = int64(1) << 30
	vms := []VM{
		{ID: 1, Name: "linux2", Policy: "t23-linux", Class: "linux", TestIntervalS: 3600, MaxProofAgeS: 5400, Durations: []float64{48},
			ProofAt: at(-20), QueuedJob: 11, QueuedAt: at(0), DiskBytes: 4 * gib},
		{ID: 2, Name: "uefi", Policy: "t23-linux", Class: "linux", TestIntervalS: 3600, MaxProofAgeS: 5400, Durations: []float64{50},
			ProofAt: at(-60), QueuedJob: 12, QueuedAt: at(0), DiskBytes: 6 * gib},
		{ID: 3, Name: "win", Policy: "t23-windows", Class: "windows", TestIntervalS: 3600, MaxProofAgeS: 5400, Durations: []float64{460},
			ProofAt: at(-30), QueuedJob: 13, QueuedAt: at(0), DiskBytes: 32 * gib},
	}
	room := int64(36_406_611_968)
	r := Compute(Input{Now: now, Slots: 4, VMs: vms, ImagesRoomBytes: room})
	if r.TestsInHorizon != 72 || len(r.Expiring) != 0 || len(r.TooLarge) != 0 || r.ImagesRoomBytes != room {
		t.Fatalf("tests %d expiring %+v too large %+v", r.TestsInHorizon, r.Expiring, r.TooLarge)
	}
	// uefi's proof ends first, so it is first; linux2 (4 GiB) fits beside it; win waits until both end.
	starts := map[string]time.Time{}
	for _, q := range r.Queue {
		starts[q.VM] = q.EstimatedStart
	}
	if !starts["uefi"].Equal(now) || !starts["linux2"].Equal(now) || !starts["win"].Equal(now.Add(50*time.Second)) {
		t.Fatalf("starts %v", starts)
	}
	// Not counted, they all start at once.
	r = Compute(Input{Now: now, Slots: 4, VMs: vms})
	for _, q := range r.Queue {
		if !q.EstimatedStart.Equal(now) {
			t.Fatalf("without the room: %+v", r.Queue)
		}
	}
	// A VM whose disks do not fit even alone is never tested: listed, and its proof expires with no next test.
	vms[2].DiskBytes = 40 * gib
	r = Compute(Input{Now: now, Slots: 4, VMs: vms, ImagesRoomBytes: room})
	if len(r.TooLarge) != 1 || r.TooLarge[0].VM != "win" || len(r.Expiring) != 1 || r.Expiring[0].VM != "win" || r.Expiring[0].NextTestStart != nil ||
		r.TestsInHorizon != 48 || r.Queue[len(r.Queue)-1].VM != "win" || !r.Queue[len(r.Queue)-1].EstimatedStart.IsZero() {
		t.Fatalf("too large: %+v expiring %+v tests %d queue %+v", r.TooLarge, r.Expiring, r.TestsInHorizon, r.Queue)
	}
}

// Smaller tests may pass one that waits for room only for PassFor since it was due (ADR-007 C13); then nothing
// passes it. One 10 GiB directory, a long 6 GiB test running, a 6 GiB test waiting (queued 9 or 11 minutes ago), and a
// 1 GiB test behind it.
func TestImagesPassFor(t *testing.T) {
	const gib = int64(1) << 30
	vms := func(waited int) []VM {
		return []VM{
			{ID: 1, Name: "running", Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: 86400, Durations: []float64{3600},
				ProofAt: at(-100), RunningSince: at(-1), LastQueued: at(-2), DiskBytes: 6 * gib},
			{ID: 2, Name: "big", Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: 86400, Durations: []float64{60},
				ProofAt: at(-50), QueuedJob: 22, QueuedAt: at(-waited), DiskBytes: 6 * gib},
			{ID: 3, Name: "small", Policy: "p", Class: "linux", TestIntervalS: 86400, MaxProofAgeS: 86400, Durations: []float64{60},
				ProofAt: at(-40), QueuedJob: 23, QueuedAt: at(-1), DiskBytes: 1 * gib},
		}
	}
	start := func(r Result, vm string) time.Time {
		for _, q := range r.Queue {
			if q.VM == vm {
				return q.EstimatedStart
			}
		}
		return time.Time{}
	}
	r := Compute(Input{Now: now, Slots: 3, VMs: vms(9), ImagesRoomBytes: 10 * gib})
	if !start(r, "small").Equal(now) || !start(r, "big").Equal(*at(59)) {
		t.Fatalf("waited 9 min: small %v big %v", start(r, "small"), start(r, "big"))
	}
	r = Compute(Input{Now: now, Slots: 3, VMs: vms(11), ImagesRoomBytes: 10 * gib})
	if !start(r, "small").Equal(*at(59)) || !start(r, "big").Equal(*at(59)) {
		t.Fatalf("waited 11 min: small %v big %v", start(r, "small"), start(r, "big"))
	}
}
