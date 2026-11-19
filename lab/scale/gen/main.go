// Command gen is a fixture (T24): it fills a Gaia server's database with the synthetic fleet of lab/scale/catalog.
//
//	gen -db /var/lib/gaia/gaia.db [-vms 5000] [-hosts 100] [-points 3000000] [-seed 1] [-now RFC3339]
//
// The database must come from `gaia server init` and hold no hosts or VMs; the server must be stopped. It prints a
// JSON summary (the seeded failures included) and exits 0, or prints the error and exits 3.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/lab/scale/catalog"
)

func main() {
	dbPath := flag.String("db", "", "the server's database (gaia.db)")
	vms := flag.Int("vms", 5000, "VMs")
	hosts := flag.Int("hosts", 100, "hosts")
	points := flag.Int("points", 3_000_000, "points in all")
	seed := flag.Uint64("seed", 1, "random seed")
	nowS := flag.String("now", "", "the history ends here (RFC 3339; default: now)")
	flag.Parse()
	now := time.Now().UTC()
	if *nowS != "" {
		t, err := time.Parse(time.RFC3339, *nowS)
		if err != nil {
			fail(err)
		}
		now = t
	}
	if *dbPath == "" {
		fail(fmt.Errorf("-db is required"))
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		fail(err)
	}
	defer db.Close()
	if _, err := db.Migrate(context.Background(), 0); err != nil {
		fail(err)
	}
	sum, err := catalog.Fill(context.Background(), db.SQL(), catalog.Options{VMs: *vms, Hosts: *hosts, Points: *points, Now: now, Seed: *seed})
	if err != nil {
		fail(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sum); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "UNKNOWN: gen:", err)
	os.Exit(3)
}
