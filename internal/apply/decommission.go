package apply

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/plan"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// doomed is a server being decommissioned, with what it holds in other
// systems. Those are found by its recorded IDs, never by name (design
// 9.1): the catalog may no longer describe where it lives.
type doomed struct {
	s       plan.Server
	compute string // compute provider; "" when no VM was recorded
	vmid    int    // Proxmox
	uuid    string // vSphere
	ipam    string // IPAM provider; "" when no address was recorded
	ip      string
	monitor map[string]string // monitoring provider -> host ID
	dnsA    map[string]string // DNS provider -> the A record's name
	dnsPTR  map[string]string // DNS provider -> the PTR record's name
}

func (r *runner) doom(ctx context.Context, s plan.Server) (doomed, error) {
	d := doomed{s: s, monitor: map[string]string{}, dnsA: map[string]string{}, dnsPTR: map[string]string{}}
	ids, err := r.d.Store.Q().ExternalIDs(ctx, s.UID)
	if err != nil {
		return d, err
	}
	for _, e := range ids {
		switch e.Kind {
		case "vm":
			d.compute = e.Provider
			if r.provider(e.Provider).Type == "vsphere" {
				d.uuid = e.ExternalID
				continue
			}
			if d.vmid, err = strconv.Atoi(e.ExternalID); err != nil {
				return d, fmt.Errorf("%s: VM ID %q", s.Hostname, e.ExternalID)
			}
		case "ip":
			d.ipam, d.ip = e.Provider, e.ExternalID
		case "host":
			d.monitor[e.Provider] = e.ExternalID
		case "dns-a":
			d.dnsA[e.Provider] = e.ExternalID
		case "dns-ptr":
			d.dnsPTR[e.Provider] = e.ExternalID
		}
	}
	if err := r.d.Store.Q().SetHostStatus(ctx, db.SetHostStatusParams{Uid: s.UID, Status: db.HostStatusDecommissioning}); err != nil {
		return d, err
	}
	r.printf("  %s: decommissioning (uid %s)\n", s.Hostname, s.UID)
	return d, nil
}

// unregister removes the doomed servers' DNS records, and removes them
// from monitoring with their history (design 9.3).
func (r *runner) unregister(ctx context.Context, c plan.Contract, gone []doomed) error {
	for _, d := range gone {
		if err := r.unregisterDNS(ctx, c, d); err != nil {
			return err
		}
		for provider, hostID := range d.monitor {
			m, err := r.monitor(ctx, provider)
			if err != nil {
				return err
			}
			removed, err := m.Delete(ctx, d.s.UID, hostID)
			if err != nil {
				return fmt.Errorf("%s: %w", d.s.Hostname, err)
			}
			action := "delete"
			if !removed {
				action = "already gone"
			}
			r.printf("  %s %s: %s (host %s)\n", provider, d.s.Hostname, action, hostID)
			r.change(runrecord.Change{Contract: c.Name, UID: d.s.UID, Hostname: d.s.Hostname, Stage: "decommission", Action: provider + " " + action, Detail: "host " + hostID})
		}
	}
	return nil
}

// release runs after Pulumi destroyed the VMs: it checks they are gone,
// returns the addresses to IPAM (into quarantine) and deletes the hosts,
// which frees their names.
func (r *runner) release(ctx context.Context, c plan.Contract, gone []doomed, creds map[string]proxmox.Credentials) error {
	used := map[string]map[int]bool{}
	for _, d := range gone {
		var done []string
		if d.uuid != "" {
			// Pulumi destroyed it; vSphere UUIDs are not used again.
			done = append(done, "vm "+d.uuid+" destroyed")
		} else if d.compute != "" {
			u, err := cached(used, d.compute, func() (map[int]bool, error) { return r.d.UsedVMIDs(ctx, creds[d.compute]) })
			if err != nil {
				return err
			}
			if u[d.vmid] {
				return fmt.Errorf("%s: VM %d still exists after pulumi up", d.s.Hostname, d.vmid)
			}
			done = append(done, fmt.Sprintf("vm %d destroyed", d.vmid))
			delete(r.used[d.compute], d.vmid)
		}
		if d.ipam != "" {
			p := r.provider(d.ipam)
			f, err := r.d.Secrets.Read(ctx, p.Credentials)
			if err != nil {
				return err
			}
			if _, err := ipamclient.New(p.Endpoint, f["token"]).Release(ctx, d.s.UID); err != nil {
				return fmt.Errorf("%s: %w", d.s.Hostname, err)
			}
			done = append(done, "ip "+d.ip+" released")
		}
		if err := r.d.Store.DeleteHost(ctx, d.s.UID); err != nil {
			return err
		}
		done = append(done, "name "+d.s.Hostname+" released")
		detail := strings.Join(done, ", ")
		r.printf("  - %s: deleted (%s)\n", d.s.Hostname, detail)
		r.change(runrecord.Change{Contract: c.Name, UID: d.s.UID, Hostname: d.s.Hostname, Stage: "decommission", Action: "delete", Detail: detail})
	}
	return nil
}
