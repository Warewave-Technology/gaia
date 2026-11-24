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
	disks := flag.Bool("disks", false, "give every point a disk (8 to 128 GiB by VM), for measuring what reads them (T29)")
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
	if *disks {
		// One disk per point, its size fixed per VM: 8 GiB times 1 to 16. (The catalog's points have none: nothing
		// restores them.)
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO point_disks (point_id, target, size_bytes, block_size, store_id,
			changed_bytes, read_bytes, written_bytes) SELECT id, 'vda', (8 << 30) * (1 + vm_id % 16), 4194304, 'gen', 0, 0, 0 FROM points
			WHERE NOT EXISTS (SELECT 1 FROM point_disks k WHERE k.point_id = points.id)`); err != nil {
			fail(err)
		}
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
