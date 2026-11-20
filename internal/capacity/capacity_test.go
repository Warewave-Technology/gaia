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
