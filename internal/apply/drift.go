package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/plan"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/provider/warpgate"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Finding is a difference between what a run applied and what a system
// has now (design 7.5). Unknown: the system could not be asked.
type Finding struct {
	Contract string `json:"contract"`
	UID      string `json:"uid"`
	Hostname string `json:"hostname"`
	Area     string `json:"area"`
	Key      string `json:"key"`
	Want     string `json:"want"`
	Got      string `json:"got"`
	Unknown  bool   `json:"unknown,omitempty"`
}

// CheckRegistration compares the records of an applied plan's active
// servers with their systems, by the IDs Gaia recorded: the address in
// IPAM, the host in monitoring, the DNS records, the computer account and
// the bastion targets. Nothing is changed.
func CheckRegistration(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps) ([]Finding, error) {
	r := checker(g, d)
	var out []Finding
	for _, c := range p.Contracts {
		for _, s := range c.Servers {
			if s.Action == plan.Delete || s.UID == "" {
				continue
			}
			h, err := d.Store.Q().Host(ctx, s.UID)
			if err != nil || h.Status != db.HostStatusActive {
				continue
			}
			fs, err := r.checkServer(ctx, c, s)
			if err != nil {
				return out, err
			}
			out = append(out, fs...)
		}
	}
	return out, nil
}

func (r *runner) checkServer(ctx context.Context, c plan.Contract, s plan.Server) ([]Finding, error) {
	ids, err := r.d.Store.Q().ExternalIDs(ctx, s.UID)
	if err != nil {
		return nil, err
	}
	id := func(provider, kind string) string {
		for _, e := range ids {
			if e.Kind == kind && (provider == "" || e.Provider == provider) {
				return e.ExternalID
			}
		}
		return ""
	}
	var out []Finding
	add := func(key, want, got string) {
		out = append(out, Finding{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Area: "registration", Key: key, Want: want, Got: got})
	}
	unknown := func(key string, err error) {
		out = append(out, Finding{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Area: "registration", Key: key, Want: "checked", Got: err.Error(), Unknown: true})
	}

	ip := id(s.IPAM.Provider, "ip")
	if s.IPAM.Provider != "" {
		p := r.provider(s.IPAM.Provider)
		f, err := r.d.Secrets.Read(ctx, p.Credentials)
		if err == nil {
			var as []string
			got, aerr := ipamclient.New(p.Endpoint, f["token"]).Allocations(ctx, s.UID)
			err = aerr
			for _, a := range got {
				as = append(as, a.IP)
			}
			if err == nil && !slices.Contains(as, ip) {
				add("ipam/ip", ip, orNone(strings.Join(as, ",")))
			}
		}
		if err != nil {
			unknown("ipam/ip", err)
		}
	}

	if m := c.Monitoring; m != nil {
		mon, err := r.monitor(ctx, m.Provider)
		var st *zabbixState
		if err == nil {
			st, err = r.monitorState(ctx, mon, s.UID, id(m.Provider, "host"))
		}
		switch {
		case err != nil:
			unknown("monitoring/host", err)
		case st == nil:
			add("monitoring/host", "registered", "missing")
		default:
			if !st.Enabled {
				add("monitoring/enabled", "enabled", "disabled")
			}
			if st.IP != ip {
				add("monitoring/ip", ip, orNone(st.IP))
			}
			if miss := missing(s.HostGroups, st.Groups); len(miss) > 0 {
				add("monitoring/groups", strings.Join(s.HostGroups, ","), strings.Join(st.Groups, ","))
			}
			if miss := missing(m.Templates, st.Templates); len(miss) > 0 {
				add("monitoring/templates", strings.Join(m.Templates, ","), strings.Join(st.Templates, ","))
			}
		}
	}

	if dns := s.DNS; dns != nil || s.Domain != nil {
		provider := ""
		if dns != nil {
			provider = dns.Provider
		} else {
			provider = s.Domain.Provider
		}
		dir, err := r.directory(ctx, provider)
		if err != nil {
			unknown("directory", err)
		} else {
			guid := ""
			if s.Domain != nil {
				guid = id(s.Domain.Provider, "computer")
			}
			out = append(out, r.checkDirectory(c, s, dir, ip, guid)...)
			_ = dir.Close()
		}
	}

	if b := c.Bastion; b != nil {
		wg, err := r.bastion(ctx, b.Provider)
		if err != nil {
			unknown("bastion", err)
		} else {
			kinds := []string{"target"}
			if b.Sudo {
				kinds = append(kinds, "target-sudo")
			}
			for _, kind := range kinds {
				role := plan.BastionRole(c.Name, kind == "target-sudo")
				tid := id(b.Provider, kind)
				if tid == "" {
					add("bastion/"+kind, "registered", "none recorded")
					continue
				}
				t, err := wg.Target(ctx, tid)
				switch {
				case errors.Is(err, warpgate.ErrNotFound):
					add("bastion/"+kind, "registered", "missing")
					continue
				case err != nil:
					unknown("bastion/"+kind, err)
					continue
				}
				if t.Host != ip {
					add("bastion/"+kind+"/host", ip, orNone(t.Host))
				}
				roles, err := wg.TargetRoles(ctx, tid)
				if err == nil && !slices.Contains(roles, role) {
					add("bastion/"+kind+"/roles", role, orNone(strings.Join(roles, ",")))
				}
			}
		}
	}
	return out, nil
}

// checkDirectory checks a server's DNS records and computer account.
func (r *runner) checkDirectory(c plan.Contract, s plan.Server, dir Directory, ip, guid string) []Finding {
	var out []Finding
	add := func(key, want, got string, unknown bool) {
		out = append(out, Finding{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Area: "registration", Key: key, Want: want, Got: got, Unknown: unknown})
	}
	has := func(recs []ad.Record, typ uint16, data string) bool {
		return slices.ContainsFunc(recs, func(x ad.Record) bool { return x.Type == typ && strings.EqualFold(x.Data, data) })
	}
	datas := func(recs []ad.Record) string {
		var v []string
		for _, x := range recs {
			v = append(v, x.Data)
		}
		return orNone(strings.Join(v, ","))
	}
	if d := s.DNS; d != nil {
		fqdn := s.Hostname + "." + d.Zone
		recs, err := dir.Records(d.Zone, s.Hostname)
		switch {
		case err != nil:
			add("dns/a", ip, err.Error(), true)
		case !has(recs, ad.TypeA, ip):
			add("dns/a", ip, datas(recs), false)
		}
		if d.Reverse {
			if addr, err := netip.ParseAddr(ip); err == nil {
				zone, node, err := dir.ReverseZone(addr)
				var recs []ad.Record
				if err == nil {
					recs, err = dir.Records(zone, node)
				}
				switch {
				case err != nil:
					add("dns/ptr", fqdn, err.Error(), true)
				case !has(recs, ad.TypePTR, fqdn):
					add("dns/ptr", fqdn, datas(recs), false)
				}
			}
		}
	}
	if s.Domain != nil {
		if guid == "" {
			add("ad/computer", "created", "none recorded", false)
			return out
		}
		comp, err := dir.ComputerByGUID(guid)
		switch {
		case errors.Is(err, ad.ErrNoComputer):
			add("ad/computer", "present", "missing", false)
		case err != nil:
			add("ad/computer", "present", err.Error(), true)
		case comp.Disabled:
			add("ad/computer", "enabled", "disabled", false)
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// missing are the wanted values not in got.
func missing(want, got []string) []string {
	var out []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			out = append(out, w)
		}
	}
	return out
}

// zabbixState is the part of a monitoring host drift looks at.
type zabbixState struct {
	Enabled   bool
	IP        string
	Groups    []string
	Templates []string
}

func (r *runner) monitorState(ctx context.Context, m Monitor, uid, hostID string) (*zabbixState, error) {
	st, err := m.State(ctx, uid, hostID)
	if err != nil || st == nil {
		return nil, err
	}
	return &zabbixState{Enabled: st.Enabled, IP: st.IP, Groups: st.Groups, Templates: st.Templates}, nil
}

// String prints a finding in one line.
func (f Finding) String() string {
	s := fmt.Sprintf("%s %s %s: want %s, got %s", f.Contract, f.Hostname, f.Key, f.Want, f.Got)
	if f.Unknown {
		s += " (unknown)"
	}
	return s
}

// checker is a runner that only reads, for drift.
func checker(g *catalog.Graph, d Deps) *runner {
	if d.Out == nil {
		d.Out = io.Discard
	}
	return &runner{g: g, d: d, monitors: map[string]Monitor{}, executedAs: map[string]string{}, used: map[string]map[int]bool{}}
}

// CheckProvisioning refreshes each stack's VMs from the hypervisor and
// previews the VMs as the last run applied them (hosts.vm_spec) against
// them: whatever Pulumi would change was changed outside Gaia (design
// 7.5). Nothing is written.
func CheckProvisioning(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps) ([]Finding, error) {
	if d.PreviewDrift == nil {
		return nil, errors.New("no Pulumi preview")
	}
	r := checker(g, d)
	backend, err := r.backend(ctx)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, c := range p.Contracts {
		if c.Adopts() {
			continue // adopted VMs are not in a stack
		}
		w := &work{bySite: map[string][]pulumi.VM{}, siteCreds: map[string]proxmox.Credentials{}, creds: map[string]proxmox.Credentials{},
			siteVS: map[string]*pulumi.VSphereCredentials{}, vsCreds: map[string]*pulumi.VSphereCredentials{}}
		names := map[string]string{}
		for _, s := range c.Servers {
			if s.UID == "" {
				continue
			}
			h, err := d.Store.Q().Host(ctx, s.UID)
			if err != nil || h.Status != db.HostStatusActive && h.Status != db.HostStatusStopped {
				continue
			}
			names[s.UID] = s.Hostname
			vm, err := storedVM(h)
			if err != nil {
				out = append(out, Finding{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Area: "provisioning", Key: "provisioning/vm", Want: "as applied", Got: err.Error(), Unknown: true})
				continue
			}
			vm.Stopped = h.Status == db.HostStatusStopped
			provider, _ := r.vmProvider(ctx, s.UID)
			if provider == "" {
				provider = r.siteProvider(h.Site)
			}
			if err := r.siteHypervisor(ctx, w, h.Site, provider); err != nil {
				return out, err
			}
			w.bySite[h.Site] = append(w.bySite[h.Site], vm)
		}
		for _, site := range slices.Sorted(maps.Keys(w.bySite)) {
			st := pulumi.Stack{Name: c.Name + "-" + site, Proxmox: w.siteCreds[site], VSphere: w.siteVS[site], VMs: w.bySite[site]}
			drift, err := d.PreviewDrift(ctx, backend, st)
			if err != nil {
				for _, v := range st.VMs {
					out = append(out, Finding{Contract: c.Name, UID: v.UID, Hostname: v.Hostname, Area: "provisioning", Key: "provisioning/vm", Want: "as applied", Got: err.Error(), Unknown: true})
				}
				continue
			}
			for _, uid := range slices.Sorted(maps.Keys(drift)) {
				dr := drift[uid]
				got := dr.Op
				if len(dr.Diffs) > 0 {
					got += " " + strings.Join(dr.Diffs, ",")
				}
				host := names[uid]
				if host == "" {
					host = "vm-" + uid // in the stack, not a server of the plan
				}
				out = append(out, Finding{Contract: c.Name, UID: uid, Hostname: host, Area: "provisioning", Key: "provisioning/vm", Want: "as applied", Got: got})
			}
		}
	}
	return out, nil
}

// CheckConfiguration runs each contract's play in check mode on its
// active servers: the tasks that would change something show what was
// changed outside Gaia (design 7.5). A server the check cannot finish on
// is unknown.
func CheckConfiguration(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps) ([]Finding, error) {
	r := checker(g, d)
	r.run = "drift"
	_, privateKey, err := r.sshKeys(ctx)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, c := range p.Contracts {
		if !r.configures(ctx, c) {
			continue
		}
		if r.cfg == nil {
			if r.cfg, err = r.configSetup(ctx, privateKey); err != nil {
				return nil, err
			}
		}
		contract, ok := g.Catalog.Get(catalog.KindServer, c.Name)
		if !ok {
			continue
		}
		var targets []target
		for _, s := range c.Servers {
			if s.Action == plan.Delete || s.UID == "" {
				continue
			}
			if h, err := d.Store.Q().Host(ctx, s.UID); err != nil || h.Status != db.HostStatusActive {
				continue
			}
			ip, err := r.externalID(ctx, s.UID, s.IPAM.Provider, "ip")
			if err != nil || ip == "" {
				continue
			}
			targets = append(targets, target{s: s, address: ip})
		}
		if len(targets) == 0 {
			continue
		}
		job, names, err := r.configJob(ctx, c, contract, targets)
		if err != nil {
			return out, err
		}
		job.Check, job.Name = true, "gaia-drift-"+c.Name
		changed := map[string][]string{}
		res, err := d.Configure.Run(ctx, job, func(e ansible.Event) error {
			if e.Changed && e.Host != "" && !slices.Contains(changed[e.Host], e.Task) {
				changed[e.Host] = append(changed[e.Host], e.Task)
			}
			return nil
		})
		if err != nil {
			return out, fmt.Errorf("%s: check: %w", c.Name, err)
		}
		failed := res.Failed()
		for _, h := range job.Hosts {
			f := Finding{Contract: c.Name, UID: h.Name, Hostname: names[h.Name], Area: "configuration", Key: "configuration", Want: "no changes"}
			switch {
			case slices.Contains(failed, h.Name):
				f.Got, f.Unknown = "the check did not finish", true
			case len(changed[h.Name]) > 0:
				tasks := changed[h.Name]
				f.Got = fmt.Sprintf("%d task(s) would change: %s", len(tasks), strings.Join(firstN(tasks, 5), "; "))
			default:
				continue
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func firstN(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return append(slices.Clone(v[:n]), fmt.Sprintf("and %d more", len(v)-n))
}
