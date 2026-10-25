package apply

import (
	"context"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// Discovery is what the hypervisors and monitoring have, next to what Gaia
// records (design 8.3, levels 1 and 2): read only, from their APIs.
type Discovery struct {
	TakenAt time.Time         `json:"takenAt"`
	VMs     []DiscoveredVM    `json:"vms"`
	Missing []MissingVM       `json:"missing"` // servers whose VM is not there
	Hosts   []DiscoveredHost  `json:"hosts"`   // monitoring hosts of servers Gaia no longer has
	Counts  map[string]int    `json:"counts"`
	Errors  map[string]string `json:"errors,omitempty"` // per provider
}

// DiscoveredVM is a VM and what it is to Gaia: managed (a server's),
// orphan (marked as Gaia's, but no server of Gaia's), unmanaged (an
// adopt candidate) or template.
type DiscoveredVM struct {
	Provider   string   `json:"provider"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	PowerState string   `json:"powerState"`
	IPs        []string `json:"ips,omitempty"`
	Category   string   `json:"category"`
	UID        string   `json:"uid,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`
	Contract   string   `json:"contract,omitempty"`
}

// MissingVM is a server whose recorded VM the hypervisor does not have.
type MissingVM struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	UID      string `json:"uid"`
	Hostname string `json:"hostname"`
	Contract string `json:"contract"`
}

// DiscoveredHost is a monitoring host that carries the UID of a server
// Gaia no longer has.
type DiscoveredHost struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	UID      string `json:"uid"`
}

// Discover reads every compute provider of the catalog and the monitoring
// provider. A provider that cannot be read is named in Errors; the rest
// of the report stands.
func Discover(ctx context.Context, g *catalog.Graph, d Deps) (*Discovery, error) {
	if d.Out == nil {
		d.Out = io.Discard
	}
	r := checker(g, d)
	q := d.Store.Q()
	rep := &Discovery{TakenAt: time.Now().UTC(), VMs: []DiscoveredVM{}, Missing: []MissingVM{}, Hosts: []DiscoveredHost{}, Counts: map[string]int{}, Errors: map[string]string{}}
	live, err := q.LiveVMs(ctx)
	if err != nil {
		return nil, err
	}
	known, err := q.KnownUIDs(ctx)
	if err != nil {
		return nil, err
	}
	type server struct{ uid, hostname, contract string }
	byVM := map[string]server{} // provider/id
	for _, v := range live {
		byVM[v.Provider+"/"+v.ExternalID] = server{v.Uid, v.Hostname, v.Contract}
	}
	for _, o := range g.Catalog.List(catalog.KindProvider) {
		p := o.Spec.(*catalog.ProviderSpec)
		if p.Type != "proxmox" && p.Type != "vsphere" {
			continue
		}
		name := o.Metadata.Name
		comp, err := r.computeOf(ctx, name)
		if err != nil {
			rep.Errors[name] = err.Error()
			continue
		}
		vms, err := comp.Discover(ctx)
		if err != nil {
			rep.Errors[name] = err.Error()
			continue
		}
		seen := map[string]bool{}
		for _, v := range vms {
			x := DiscoveredVM{Provider: name, ID: v.ID, Name: v.Name, PowerState: v.PowerState, IPs: v.IPs}
			seen[v.ID] = true
			switch s, ok := byVM[name+"/"+v.ID]; {
			case v.Template:
				x.Category = "template"
			case ok:
				x.Category, x.UID, x.Hostname, x.Contract = "managed", s.uid, s.hostname, s.contract
			case markedUID(v.Tags, v.Notes) != "":
				x.Category, x.UID = "orphan", markedUID(v.Tags, v.Notes)
			default:
				x.Category = "unmanaged"
			}
			rep.Counts[x.Category]++
			rep.VMs = append(rep.VMs, x)
		}
		for _, v := range live {
			if v.Provider == name && !seen[v.ExternalID] {
				rep.Missing = append(rep.Missing, MissingVM{Provider: name, ID: v.ExternalID, UID: v.Uid, Hostname: v.Hostname, Contract: v.Contract})
				rep.Counts["missing"]++
			}
		}
	}
	if m := g.Platform().Spec.(*catalog.PlatformSpec).Monitoring; m != nil {
		mon, err := r.monitor(ctx, m.Provider)
		var hosts map[string]string
		if err == nil {
			hosts, err = mon.GaiaHosts(ctx)
		}
		if err != nil {
			rep.Errors[m.Provider] = err.Error()
		}
		for uid, host := range hosts {
			if !slices.Contains(known, uid) {
				rep.Hosts = append(rep.Hosts, DiscoveredHost{Provider: m.Provider, Name: host, UID: uid})
				rep.Counts["monitoring orphan"]++
			}
		}
		sort.Slice(rep.Hosts, func(i, j int) bool { return rep.Hosts[i].Name < rep.Hosts[j].Name })
	}
	sort.Slice(rep.VMs, func(i, j int) bool {
		a, b := rep.VMs[i], rep.VMs[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Provider+a.Name < b.Provider+b.Name
	})
	return rep, nil
}

// markedUID is the gaia.uid a VM carries: a Proxmox tag gaia-uid_<uid>, or
// gaia.uid=<uid> in vSphere's annotation.
func markedUID(tags []string, notes string) string {
	for _, t := range tags {
		if uid, ok := strings.CutPrefix(t, "gaia-uid_"); ok {
			return uid
		}
	}
	for _, l := range strings.Split(notes, "\n") {
		if uid, ok := strings.CutPrefix(strings.TrimSpace(l), "gaia.uid="); ok {
			return uid
		}
	}
	return ""
}
