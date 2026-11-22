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

// The batched startup recompute (ADR-007 C11), measured on the T24 synthetic catalog: the full recompute's time and
// the view requests' latency while it runs, with the earlier batching (250 VMs a transaction, no pause) and with the
// current one, in the same run on the same database. Not part of make check (it needs -recompute-vms):
//
//	go test ./internal/server -run TestRecomputeYields -timeout 1h -recompute-vms 1000 -recompute-hosts 20 -recompute-points 600000 -recompute-out out.json
var (
	rcVMs    = flag.Int("recompute-vms", 0, "TestRecomputeYields: VMs of the synthetic catalog (0: skip)")
	rcHosts  = flag.Int("recompute-hosts", 20, "TestRecomputeYields: hosts")
	rcPoints = flag.Int("recompute-points", 600000, "TestRecomputeYields: points")
	rcRuns   = flag.Int("recompute-runs", 3, "TestRecomputeYields: recomputes per configuration")
	rcOut    = flag.String("recompute-out", "", "TestRecomputeYields: write the result here (JSON)")
)

// RecomputeRun is one full recompute with requests beside it.
type RecomputeRun struct {
	Batch          int     `json:"batch"`
	PauseMS        float64 `json:"pause_ms"`
	Seconds        float64 `json:"recompute_s"`
	Requests       int     `json:"requests_during"`
	P50MS          float64 `json:"p50_ms"`
	P95MS          float64 `json:"p95_ms"`
	MaxMS          float64 `json:"max_ms"`
	IdleP95MS      float64 `json:"idle_p95_ms"` // the same requests with no recompute running
	ChangedOnRerun int     `json:"changed"`
}

func TestRecomputeYields(t *testing.T) {
	if *rcVMs == 0 {
		t.Skip("-recompute-vms not set")
	}
	ctx := context.Background()
	f := newFixture(t)
	t0 := time.Now()
	sum, err := catalog.Fill(ctx, f.s.db.SQL(), catalog.Options{VMs: *rcVMs, Hosts: *rcHosts, Points: *rcPoints, Now: time.Now().UTC(), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	fill := time.Since(t0).Seconds()
	if _, err := f.s.recompute(ctx, nil, "requested by test"); err != nil { // the table exists, as at a real restart
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
	run := func(batch int, pause time.Duration) RecomputeRun {
		fullRecomputeBatch, fullRecomputePause = batch, pause
		var idle []float64
		for i := range 100 {
			idle = append(idle, req(i))
		}
		var done atomic.Bool
		var st RecomputeStats
		var rerr error
		ch := make(chan struct{})
		go func() {
			st, rerr = f.s.recompute(ctx, nil, "start")
			done.Store(true)
			close(ch)
		}()
		var during []float64
		for i := 0; !done.Load(); i++ {
			during = append(during, req(i))
		}
		<-ch
		if rerr != nil {
			t.Fatal(rerr)
		}
		return RecomputeRun{Batch: batch, PauseMS: float64(pause) / 1e6, Seconds: st.Seconds, Requests: len(during), P50MS: pct(during, 0.5),
			P95MS: pct(during, 0.95), MaxMS: pct(during, 1), IdleP95MS: pct(idle, 0.95), ChangedOnRerun: st.Changed}
	}
	defer func(b int, p time.Duration) { fullRecomputeBatch, fullRecomputePause = b, p }(fullRecomputeBatch, fullRecomputePause)
	type config struct {
		Name  string
		Batch int
		Pause time.Duration
	}
	configs := []config{{"before (T24: 250 a transaction, no pause)", recomputeBatch, 0}, {"after (C11)", fullRecomputeBatch, fullRecomputePause}}
	out := map[string]any{"catalog": sum, "fill_s": fill, "go": runtime.Version(), "cpus": runtime.NumCPU(), "goos": runtime.GOOS, "goarch": runtime.GOARCH}
	results := map[string][]RecomputeRun{}
	for r := range *rcRuns {
		for _, c := range configs {
			x := run(c.Batch, c.Pause)
			results[c.Name] = append(results[c.Name], x)
			t.Logf("run %d %s: %+v", r+1, c.Name, x)
		}
	}
	out["runs"] = results
	b, _ := json.MarshalIndent(out, "", "  ")
	if *rcOut != "" {
		if err := os.WriteFile(*rcOut, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
