// Command vsphere-probe checks which image-backup APIs a vSphere host allows.
//
//	GAIA_VSPHERE_URL=https://192.0.2.10/sdk GAIA_VSPHERE_USER=root GAIA_VSPHERE_PASSWORD=... \
//	  vsphere-probe -insecure -json report.json
//
// Exit codes follow the verdict: 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25/soap"

	"github.com/Warewave-Technology/gaia/internal/vsphereprobe"
)

func main() {
	rawURL := flag.String("url", os.Getenv("GAIA_VSPHERE_URL"), "vSphere SDK URL, e.g. https://esxi.lab/sdk")
	insecure := flag.Bool("insecure", false, "skip TLS verification (lab only)")
	vmName := flag.String("vm", "", "use this existing VM instead of creating gaia-probe")
	datastore := flag.String("datastore", "", "datastore for the throwaway VM (default: host default)")
	readMiB := flag.Int64("read-mib", 64, "MiB of disk data to read in the read steps")
	keep := flag.Bool("keep", false, "keep the throwaway VM")
	jsonOut := flag.String("json", "", "also write the report as JSON to this file")
	flag.Parse()

	if *rawURL == "" {
		fmt.Fprintln(os.Stderr, "vsphere-probe: -url or GAIA_VSPHERE_URL is required")
		os.Exit(3)
	}
	u, err := soap.ParseURL(*rawURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsphere-probe:", err)
		os.Exit(3)
	}
	if user := os.Getenv("GAIA_VSPHERE_USER"); user != "" {
		u.User = url.UserPassword(user, os.Getenv("GAIA_VSPHERE_PASSWORD"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client, err := govmomi.NewClient(ctx, u, *insecure)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsphere-probe: connect:", err)
		os.Exit(3)
	}
	defer func() { _ = client.Logout(context.Background()) }()

	r := vsphereprobe.Run(ctx, client.Client, vsphereprobe.Config{
		VMName: *vmName, Datastore: *datastore, ReadBytes: *readMiB << 20, Keep: *keep,
	})

	fmt.Printf("host:     %s (%s)\n", r.Product, r.APIType)
	for _, l := range r.Licenses {
		fmt.Printf("license:  %s\n", l)
	}
	fmt.Println()
	for _, s := range r.Steps {
		fmt.Printf("  %-4s  %-55s %6.2fs  %s\n", s.Status, s.Name, s.Seconds, s.Detail)
	}
	fmt.Printf("\nverdict:  %s — %s\n", r.Verdict, r.Summary)

	if *jsonOut != "" {
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "vsphere-probe: write json:", err)
		}
	}
	os.Exit(map[vsphereprobe.Verdict]int{
		vsphereprobe.OK: 0, vsphereprobe.Warning: 1, vsphereprobe.Critical: 2, vsphereprobe.Unknown: 3,
	}[r.Verdict])
}
