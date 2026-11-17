package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/libvirtbackup"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func TestFullReason(t *testing.T) {
	parent := &store.Point{ID: 7, RepositoryID: 1, Checkpoint: "gaia-1-7"}
	pd := []store.PointDisk{{Target: "vda", SizeBytes: 1 << 30}, {Target: "vdb", SizeBytes: 2 << 30}}
	disks := []libvirtbackup.Disk{{Target: "vda", Capacity: 1 << 30}, {Target: "vdb", Capacity: 2 << 30}}
	host := []string{"gaia-1-5", "gaia-1-7"}
	for _, c := range []struct {
		name   string
		demand string
		parent *store.Point
		host   []string
		disks  []libvirtbackup.Disk
		repo   int64
		want   string // "" incremental; else a substring of the reason
	}{
		{"incremental", "", parent, host, disks, 1, ""},
		{"quarantine demands a full", "full: repository verification 3 quarantined damaged contents", parent, host, disks, 1, "quarantined damaged contents"},
		{"first backup", "", nil, host, disks, 1, "no earlier checked point"},
		{"repository changed", "", parent, host, disks, 2, "repository changed"},
		{"checkpoint deleted on the host", "", parent, []string{"gaia-1-5"}, disks, 1, "checkpoint gaia-1-7 of point 7 is not on the host"},
		{"no checkpoints at all", "", parent, nil, disks, 1, "is not on the host"},
		{"disk resized", "", parent, host, []libvirtbackup.Disk{disks[0], {Target: "vdb", Capacity: 3 << 30}}, 1,
			fmt.Sprintf("disk vdb was resized from %d to %d bytes", 2<<30, 3<<30)},
		{"disk added", "", parent, host, append(disks, libvirtbackup.Disk{Target: "vdc", Capacity: 1}), 1, "disk vdc is new"},
		{"disk removed", "", parent, host, disks[:1], 1, "disk vdb was removed"},
	} {
		got := fullReason(c.demand, c.parent, pd, c.host, c.disks, c.repo)
		if c.want == "" && got != "" || c.want != "" && (!strings.HasPrefix(got, "full: ") || !strings.Contains(got, c.want)) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestKeepPoints(t *testing.T) {
	base := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) // a Monday
	var pts []store.Point
	for i := range 4 * 24 * 10 { // every 15 minutes for ten days, oldest first
		pts = append(pts, store.Point{ID: int64(i + 1), TakenAt: base.Add(time.Duration(i) * 15 * time.Minute).Format(time.RFC3339Nano)})
	}
	newest := pts[len(pts)-1].ID
	kept := func(k map[int64]bool) (n int) {
		for range k {
			n++
		}
		return
	}
	if k := keepPoints(pts, store.Policy{KeepLast: 8}); kept(k) != 8 || !k[newest] || !k[newest-7] || k[newest-8] {
		t.Fatalf("keep_last 8: %v", k)
	}
	// Daily: the newest point of each of the last 3 days; weekly: the newest of each of the last 2 ISO weeks.
	k := keepPoints(pts, store.Policy{KeepDaily: 3, KeepWeekly: 2})
	want := map[int64]bool{newest: true, newest - 96: true, newest - 192: true, // Nov 11, 10, 9
		int64(4 * 24 * 7): true} // Sunday Nov 8 23:45, the last point of week 45
	if kept(k) != len(want) {
		t.Fatalf("daily 3 weekly 2: kept %v, want %v", k, want)
	}
	for id := range want {
		if !k[id] {
			t.Fatalf("daily 3 weekly 2: %d not kept: %v", id, k)
		}
	}
	if k := keepPoints(pts, store.Policy{}); kept(k) != 1 || !k[newest] {
		t.Fatalf("an empty policy keeps only the newest: %v", k)
	}
	if k := keepPoints([]store.Point{{ID: 1, TakenAt: "garbage"}, {ID: 2, TakenAt: base.Format(time.RFC3339Nano)}}, store.Policy{}); !k[1] || !k[2] {
		t.Fatalf("an unreadable time is kept: %v", k)
	}
	if k := keepPoints(nil, store.Policy{KeepLast: 3}); len(k) != 0 {
		t.Fatalf("no points: %v", k)
	}
}
