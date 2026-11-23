package server

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/lab/scale/catalog"
)

// C11's aim as ADR-007 C17 restates it, measured on the T24 synthetic catalog with the one batching there is (250 VMs a
// transaction, no pause): while a full recompute runs, view requests keep T24's limit (p95 <= 300 ms), and a
// cold-start recompute of the T24 catalog finishes within 60 s on the gate VM.
//
// make check runs it at a reduced size (CLAUDE.md rule 15) and asserts generously, so a busy machine does not make it
// flaky: p95 <= 300 ms while the recompute runs, and the cold recompute projected linearly to 5,000 VMs within three
// times the gate's 60 s. At 5,000 VMs or more (the gate) the limit is the gate's own 60 s:
//
//	go test ./internal/server -run TestRecomputeKeepsViews -timeout 2h -recompute-vms 5000 -recompute-hosts 100 -recompute-points 3000000 -recompute-runs 3 -recompute-out out.json
var (
	rcVMs    = flag.Int("recompute-vms", 200, "TestRecomputeKeepsViews: VMs of the synthetic catalog")
	rcHosts  = flag.Int("recompute-hosts", 10, "TestRecomputeKeepsViews: hosts")
	rcPoints = flag.Int("recompute-points", 12000, "TestRecomputeKeepsViews: points")
	rcRuns   = flag.Int("recompute-runs", 1, "TestRecomputeKeepsViews: full recomputes with requests beside them")
	rcOut    = flag.String("recompute-out", "", "TestRecomputeKeepsViews: write the result here (JSON)")
)

// The limits (ADR-007 C17, T24).
const (
	recomputeViewP95MS = 300.0 // view requests while a full recompute runs
	recomputeColdS     = 60.0  // a cold-start recompute of the T24 catalog on the gate VM
	recomputeGateVMs   = 5000  // the T24 catalog
	recomputeSlack     = 3.0   // below the gate's size: the projection may take this many times the limit
)

// RecomputeRun is one full recompute with view requests beside it.
type RecomputeRun struct {
	Seconds   float64 `json:"recompute_s"`
	Requests  int     `json:"requests_during"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
	MaxMS     float64 `json:"max_ms"`
	IdleP95MS float64 `json:"idle_p95_ms"` // the same requests with no recompute running
	Changed   int     `json:"changed"`
}

func TestRecomputeKeepsViews(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	ctx := context.Background()
	f := newFixture(t)
	t0 := time.Now()
	sum, err := catalog.Fill(ctx, f.s.db.SQL(), catalog.Options{VMs: *rcVMs, Hosts: *rcHosts, Points: *rcPoints, Now: time.Now().UTC(), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	fill := time.Since(t0).Seconds()
	// Cold start: the status table is empty, as at the first start after the catalog was filled.
	cold, err := f.s.recompute(ctx, nil, "start")
	if err != nil {
		t.Fatal(err)
	}
	tok := f.tokens[store.RoleAuditor]
	paths := []string{"/api/v1/views/fleet", "/api/v1/views/attention", "/api/v1/views/vm-list?limit=50", "/api/v1/views/coverage?by=host",
		"/api/v1/views/vm-list?limit=50&status=WARNING"}
	req := func(i int) float64 {
		s := time.Now()
		if code := f.do(tok, "GET", paths[i%len(paths)], "", nil); code != 200 {
			t.Fatalf("%s: %d", paths[i%len(paths)], code)
		}
		return float64(time.Since(s).Microseconds()) / 1000
	}
	pct := func(v []float64, p float64) float64 {
		s := slices.Clone(v)
		slices.Sort(s)
		return s[min(len(s)-1, int(float64(len(s))*p))]
	}
	run := func() RecomputeRun {
		var idle []float64
		for i := range 100 {
			idle = append(idle, req(i))
		}
		var done atomic.Bool
		var st RecomputeStats
		var rerr error
		ch := make(chan struct{})
		go func() {
			st, rerr = f.s.recompute(ctx, nil, "hourly")
			done.Store(true)
			close(ch)
		}()
		var during []float64
		for i := 0; !done.Load() || len(during) == 0; i++ { // at least one request, however fast the recompute
			during = append(during, req(i))
		}
		<-ch
		if rerr != nil {
			t.Fatal(rerr)
		}
		return RecomputeRun{Seconds: st.Seconds, Requests: len(during), P50MS: pct(during, 0.5), P95MS: pct(during, 0.95), MaxMS: pct(during, 1),
			IdleP95MS: pct(idle, 0.95), Changed: st.Changed}
	}
	projected := cold.Seconds * recomputeGateVMs / float64(max(1, cold.VMs))
	limit := recomputeColdS
	if *rcVMs < recomputeGateVMs {
		limit *= recomputeSlack
	}
	out := map[string]any{"catalog": sum, "fill_s": fill, "batch": recomputeBatch, "cold_start": cold, "cold_start_projected_5000_s": projected,
		"limits": map[string]float64{"view_p95_ms": recomputeViewP95MS, "cold_start_s": recomputeColdS, "cold_start_limit_applied_s": limit},
		"go":     runtime.Version(), "cpus": runtime.NumCPU(), "goos": runtime.GOOS, "goarch": runtime.GOARCH}
	var runs []RecomputeRun
	for r := range *rcRuns {
		x := run()
		runs = append(runs, x)
		t.Logf("run %d: %+v", r+1, x)
	}
	out["runs"] = runs
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("%s", b)
	if *rcOut != "" {
		if err := os.WriteFile(*rcOut, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if cold.Errors != 0 || cold.VMs != *rcVMs {
		t.Fatalf("cold start: %+v", cold)
	}
	if projected > limit {
		t.Errorf("cold-start recompute %.1f s for %d VMs, %.1f s projected to %d: over %.0f s", cold.Seconds, cold.VMs, projected, recomputeGateVMs, limit)
	}
	for i, x := range runs {
		if x.P95MS > recomputeViewP95MS {
			t.Errorf("run %d: view p95 %.1f ms while the recompute ran, over %.0f ms", i+1, x.P95MS, recomputeViewP95MS)
		}
	}
}
