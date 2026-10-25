package ipam

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/ipam"
	"github.com/Warewave-Technology/gaia/internal/itest"
)

// serve starts gaia-ipam on a fresh database and returns a client.
func serve(t *testing.T, dsn string, quarantine time.Duration) *Client {
	t.Helper()
	ctx := context.Background()
	if err := ipam.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	srv, err := ipam.NewServer(ctx, dsn, "s3cret", quarantine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return New(hs.URL, "s3cret")
}

func TestAllocateReleaseQuarantine(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	c := serve(t, dsn, time.Hour)
	ctx := context.Background()

	if err := c.SetPool(ctx, ipam.Pool{CIDR: "10.50.0.0/24", Gateway: "10.50.0.1", RangeStart: "10.50.0.100", RangeEnd: "10.50.0.102"}); err != nil {
		t.Fatal(err)
	}
	a, err := c.Allocate(ctx, "10.50.0.0/24", "uid-a", "labtpoc01")
	if err != nil || a.IP != "10.50.0.100" || a.Prefix != 24 || a.Gateway != "10.50.0.1" {
		t.Fatalf("a = %+v, %v", a, err)
	}
	// Asking again for the same owner is not a new address.
	if again, _ := c.Allocate(ctx, "10.50.0.0/24", "uid-a", "labtpoc01"); again.IP != a.IP {
		t.Errorf("again = %s, want %s", again.IP, a.IP)
	}
	b, _ := c.Allocate(ctx, "10.50.0.0/24", "uid-b", "labtpoc02")
	d, _ := c.Allocate(ctx, "10.50.0.0/24", "uid-d", "labtpoc03")
	if b.IP != "10.50.0.101" || d.IP != "10.50.0.102" {
		t.Errorf("b, d = %s, %s", b.IP, d.IP)
	}
	if _, err := c.Allocate(ctx, "10.50.0.0/24", "uid-e", "labtpoc04"); err == nil || !strings.Contains(err.Error(), "pool exhausted (409)") {
		t.Errorf("exhausted: %v", err)
	}

	if live, _ := c.Allocations(ctx, "uid-a"); len(live) != 1 || live[0].IP != a.IP {
		t.Errorf("Allocations(a) = %+v", live)
	}
	rel, err := c.Release(ctx, "uid-a", 0)
	if err != nil || len(rel) != 1 || rel[0].IP != a.IP {
		t.Fatalf("Release = %+v, %v", rel, err)
	}
	if live, _ := c.Allocations(ctx, "uid-a"); len(live) != 0 {
		t.Errorf("after release: %+v", live)
	}
	// .100 is in quarantine: still exhausted.
	if _, err := c.Allocate(ctx, "10.50.0.0/24", "uid-e", "labtpoc04"); err == nil {
		t.Error("a quarantined address was given out")
	}

	// Without quarantine the released address is given out again.
	c0 := serve(t, dsn, 0)
	if _, err := c0.Release(ctx, "uid-b", 0); err != nil {
		t.Fatal(err)
	}
	if e, err := c0.Allocate(ctx, "10.50.0.0/24", "uid-e", "labtpoc04"); err != nil || e.IP != "10.50.0.101" {
		t.Errorf("after release without quarantine: %+v, %v", e, err)
	}
	// An adopted server keeps its address, even outside the range; not one
	// someone else has.
	if a, err := c0.Adopt(ctx, "10.50.0.0/24", "uid-old", "legacy", "10.50.0.20"); err != nil || a.IP != "10.50.0.20" {
		t.Errorf("adopt = %+v %v", a, err)
	}
	if _, err := c0.Adopt(ctx, "10.50.0.0/24", "uid-other", "x", "10.50.0.20"); err == nil {
		t.Error("adopted an address someone has")
	}
	if _, err := c0.Adopt(ctx, "10.50.0.0/24", "uid-other", "x", "10.60.0.20"); err == nil {
		t.Error("adopted an address outside the pool")
	}
	// A replaced server's address goes to the one replacing it.
	if a, err := c0.Reassign(ctx, "uid-e", "uid-e2"); err != nil || len(a) != 1 || a[0].IP != "10.50.0.101" || a[0].Owner != "uid-e2" {
		t.Errorf("reassign = %+v %v", a, err)
	}
	if a, _ := c0.Allocations(ctx, "uid-e"); len(a) != 0 {
		t.Errorf("the old owner still has %+v", a)
	}
	if _, err := c0.Reassign(ctx, "uid-e2", "uid-e"); err != nil {
		t.Fatal(err)
	}
	// A quarantine given with the release holds the address back.
	if _, err := c0.Release(ctx, "uid-e", 48*time.Hour); err != nil {
		t.Fatal(err)
	}
	if e, err := c0.Allocate(ctx, "10.50.0.0/24", "uid-f", "labtpoc05"); err == nil && e.IP == "10.50.0.101" {
		t.Errorf("the released address was given out during its quarantine: %+v", e)
	}
}

func TestConcurrentAllocationsGetDistinctAddresses(t *testing.T) {
	itest.Require(t)
	c := serve(t, itest.StartPostgres(t), 0)
	ctx := context.Background()
	if err := c.SetPool(ctx, ipam.Pool{CIDR: "10.60.0.0/24", RangeStart: "10.60.0.10", RangeEnd: "10.60.0.59"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]string{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := "uid-" + string(rune('a'+i))
			a, err := c.Allocate(ctx, "10.60.0.0/24", owner, owner)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if other, dup := seen[a.IP]; dup {
				t.Errorf("%s given to %s and %s", a.IP, other, owner)
			}
			seen[a.IP] = owner
		}(i)
	}
	wg.Wait()
}

func TestErrors(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	c := serve(t, dsn, 0)
	ctx := context.Background()

	if _, err := c.Allocate(ctx, "10.99.0.0/24", "uid-a", "x"); err == nil || !strings.Contains(err.Error(), "unknown pool (404)") {
		t.Errorf("unknown pool: %v", err)
	}
	for _, p := range []ipam.Pool{
		{CIDR: "10.50.0.5/24", RangeStart: "10.50.0.10", RangeEnd: "10.50.0.20"},
		{CIDR: "10.50.0.0/24", RangeStart: "10.50.1.10", RangeEnd: "10.50.1.20"},
		{CIDR: "10.50.0.0/24", RangeStart: "10.50.0.20", RangeEnd: "10.50.0.10"},
		{CIDR: "10.50.0.0/24", Gateway: "10.9.9.9", RangeStart: "10.50.0.10", RangeEnd: "10.50.0.20"},
	} {
		if err := c.SetPool(ctx, p); err == nil || !strings.Contains(err.Error(), "(400)") {
			t.Errorf("SetPool(%+v) = %v, want 400", p, err)
		}
	}
	bad := New(strings.TrimSuffix(c.endpoint, "/"), "wrong")
	if _, err := bad.Allocations(ctx, "uid-a"); err == nil || !strings.Contains(err.Error(), "unauthorized (401)") {
		t.Errorf("wrong token: %v", err)
	}
}

func TestPoolsSayWhatTheyAreFor(t *testing.T) {
	itest.Require(t)
	c := serve(t, itest.StartPostgres(t), time.Hour)
	ctx := context.Background()
	p := ipam.Pool{CIDR: "10.20.1.0/24", Gateway: "10.20.1.1", RangeStart: "10.20.1.10", RangeEnd: "10.20.1.200",
		Name: "ank-prod-app", Labels: map[string]string{"site": "ank", "env": "prod", "vlan": "120"}, Tags: []string{"gaia-managed"}}
	if err := c.SetPool(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPool(ctx, ipam.Pool{CIDR: "10.20.2.0/24", RangeStart: "10.20.2.10", RangeEnd: "10.20.2.20"}); err != nil {
		t.Fatal(err)
	}
	pools, err := c.Pools(ctx)
	if err != nil || len(pools) != 2 {
		t.Fatalf("pools = %+v, %v", pools, err)
	}
	got := pools[0]
	if got.Name != "ank-prod-app" || got.Labels["vlan"] != "120" || strings.Join(got.Tags, ",") != "gaia-managed" || got.Gateway != "10.20.1.1" {
		t.Errorf("pool = %+v", got)
	}
	if pools[1].Name != "" || len(pools[1].Labels) != 0 || len(pools[1].Tags) != 0 {
		t.Errorf("a pool without labels = %+v", pools[1])
	}
}
