package store

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

// The capacity's facts on a full-size synthetic catalog (T29, CLAUDE.md rule 15): every VM's newest checked point's
// disk size is read with them (schema 17's index). It needs a catalog made by lab/scale/gen with -disks, in
// GAIA_SCALE_DB; without one it is skipped. It prints the times of 5 reads and fails when their median is over 2 s
// (the view is cached for 30 s and the scheduler reads one VM at a time):
//
//	go run ./lab/scale/gen -db /tmp/cat.db -disks && GAIA_SCALE_DB=/tmp/cat.db go test ./internal/store -run TestCapacityFactsAtScale -v
func TestCapacityFactsAtScale(t *testing.T) {
	path := os.Getenv("GAIA_SCALE_DB")
	if path == "" {
		t.Skip("GAIA_SCALE_DB is not set")
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.Migrate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var ms []float64
	var withDisks int
	for range 5 {
		t0 := time.Now()
		fs, err := d.CapacityFacts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
		withDisks = 0
		for _, f := range fs {
			if f.DiskBytes > 0 {
				withDisks++
			}
		}
		if len(fs) == 0 {
			t.Fatal("no VM with a policy in the catalog")
		}
	}
	slices.Sort(ms)
	t.Logf(`{"vms_with_disk_size":%d,"capacity_facts_ms":%v,"median_ms":%v}`, withDisks, ms, ms[2])
	if ms[2] > 2000 {
		t.Fatalf("median %v ms over 2 s", ms[2])
	}
}
