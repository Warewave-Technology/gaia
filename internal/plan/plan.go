package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// Plan is what applying contracts at one commit would do.
type Plan struct {
	Commit    string     `json:"commit"`
	Contracts []Contract `json:"contracts"`
	// Hash is over the canonical JSON of the plan without the hash: what
	// is approved is what is applied (design 10.1).
	Hash string `json:"hash"`
}

// Contract is the plan of one Server contract.
type Contract struct {
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels,omitempty"`
	Env        string            `json:"env,omitempty"`
	Approval   string            `json:"approval"` // required | none
	Image      string            `json:"image"`
	Size       Size              `json:"size"`
	Disks      []catalog.Disk    `json:"disks,omitempty"`
	Servers    []Server          `json:"servers"`
	Baseline   *Baseline         `json:"baseline,omitempty"`
	Roles      []Role            `json:"roles,omitempty"`
	Monitoring *Monitoring       `json:"monitoring,omitempty"`
	// Agents are installed before the contract's roles; mandatory ones
	// come from the catalog by the image's operating system.
	Agents []Agent `json:"agents,omitempty"`
	// Apps are deployed after the roles.
	Apps []App `json:"apps,omitempty"`
	// Access is who may log in (design 14.3), teams resolved: a change
	// to it is a change to the contract, approved like any. The people
	// in the teams are not part of the plan; they come from AD.
	Access []Access `json:"access,omitempty"`
	// Bastion is how the access reaches the servers, when the Platform
	// has a bastion and the contract has access.
	Bastion *Bastion `json:"bastion,omitempty"`
}

// Bastion is a contract's servers in Warpgate: one target each, SSH or,
// for Windows, RDP; with Sudo a second SSH target that logs in with sudo.
type Bastion struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"` // ssh | rdp
	Sudo     bool   `json:"sudo,omitempty"`
}

// BastionRole is the Warpgate role of a contract's access; sudo is the
// role of its sudo targets.
func BastionRole(contract string, sudo bool) string {
	if sudo {
		return "gaia/" + contract + "/sudo"
	}
	return "gaia/" + contract
}

// ContractHostGroup is the monitoring host group of a contract's servers
// alone.
func ContractHostGroup(contract string) string { return "Gaia/contract/" + contract }

// Agent is an agent and the role that installs it.
type Agent struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	Params    map[string]any `json:"params,omitempty"`
	Mandatory bool           `json:"mandatory,omitempty"`
}

// App is an application, its playbook, params and artifact.
type App struct {
	Name     string         `json:"name"`
	Playbook string         `json:"playbook"`
	Params   map[string]any `json:"params,omitempty"`
	Artifact string         `json:"artifact,omitempty"` // URL
	// Source is where Artifact is, for checking it before a run.
	Source *ArtifactSource `json:"source,omitempty"`
	// SHA256 is the artifact's checksum as the run found it: not part of
	// the plan, since the repository is read when the run starts.
	SHA256 string `json:"-"`
}

// ArtifactSource is a file in a nexus provider's repository.
type ArtifactSource struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
}

// Access is one access entry of a contract.
type Access struct {
	Via        string   `json:"via"` // group/<name> or team/<name>
	Teams      []string `json:"teams"`
	Sudo       bool     `json:"sudo,omitempty"`
	Privileged bool     `json:"privileged,omitempty"`
	Owner      string   `json:"owner,omitempty"` // the group's owner team
}

// Monitoring is where the contract's servers are registered and with
// which templates: the image OS's, then each role's.
type Monitoring struct {
	Provider  string   `json:"provider"`
	Templates []string `json:"templates,omitempty"`
}

type Size struct {
	Name      string `json:"name"`
	CPU       int    `json:"cpu"`
	MemoryGiB int    `json:"memoryGiB"`
	DiskGiB   int    `json:"diskGiB"`
}

// Actions on a server.
const (
	Create = "create"
	Keep   = "keep"
	Delete = "delete"
)

// Server is one server of a contract.
type Server struct {
	Action   string  `json:"action"` // create | keep | delete
	UID      string  `json:"uid,omitempty"`
	Status   string  `json:"status,omitempty"` // current status, for kept and deleted servers
	Hostname string  `json:"hostname"`
	Seq      int     `json:"seq"`
	Site     string  `json:"site"`
	Network  string  `json:"network"`
	Compute  Compute `json:"compute"`
	IPAM     IPAM    `json:"ipam"`
	// HostGroups are the server's monitoring host groups.
	HostGroups []string `json:"hostGroups,omitempty"`
	// DNS is where Gaia writes the server's records, when it does.
	DNS *DNS `json:"dns,omitempty"`
	// Domain is the directory the server joins, when it does.
	Domain *Domain `json:"domain,omitempty"`
}

// Domain is where a server's computer account is.
type Domain struct {
	Provider string `json:"provider"`
	OU       string `json:"ou"`
}

// DNS is a server's A record in Zone and, with Reverse, its PTR record.
type DNS struct {
	Provider string `json:"provider"`
	Zone     string `json:"zone"`
	Reverse  bool   `json:"reverse,omitempty"`
	TTL      int    `json:"ttl"`
}

type Compute struct {
	Provider      string   `json:"provider"`
	Type          string   `json:"type"`
	Template      string   `json:"template"`
	Customization string   `json:"customization,omitempty"`
	Nodes         []string `json:"nodes,omitempty"`
	Pool          string   `json:"pool,omitempty"`
	Storage       string   `json:"storage,omitempty"`
	Datacenter    string   `json:"datacenter,omitempty"`
	Cluster       string   `json:"cluster,omitempty"`
	Attachment    string   `json:"attachment"`
}

type IPAM struct {
	Provider string `json:"provider"`
	Pool     string `json:"pool"`
	Gateway  string `json:"gateway,omitempty"`
}

type Baseline struct {
	Name     string `json:"name"`
	Playbook string `json:"playbook"`
}

// Role is an AnsibleRole run on every server of the contract, with its
// params as the role will see them. Values of no_log params are masked.
type Role struct {
	Name     string         `json:"name"`
	Role     string         `json:"role"`
	Provider string         `json:"provider"`
	Params   map[string]any `json:"params,omitempty"`
}

// Masked replaces the value of a no_log param in a plan.
const Masked = "(no_log)"

// Build plans the given contracts, or every Server when names is empty,
// against the servers Gaia already manages. The graph must have resolved
// without errors.
func Build(ctx context.Context, g *catalog.Graph, names []string, st State) (*Plan, error) {
	servers := g.Catalog.List(catalog.KindServer)
	if len(names) > 0 {
		servers = servers[:0:0]
		for _, n := range names {
			o, ok := g.Catalog.Get(catalog.KindServer, n)
			if !ok {
				return nil, fmt.Errorf("no Server contract %q", n)
			}
			servers = append(servers, o)
		}
		sort.Slice(servers, func(i, j int) bool { return servers[i].Metadata.Name < servers[j].Metadata.Name })
	}

	p := &Plan{Commit: g.Catalog.Commit, Contracts: []Contract{}}
	owners := map[string]string{} // hostname -> contract
	for _, o := range servers {
		c, err := contract(ctx, g, o, st)
		if err != nil {
			return nil, err
		}
		for _, s := range c.Servers {
			if s.Action == Delete {
				continue
			}
			if s.Domain != nil && len(s.Hostname) > 15 {
				return nil, fmt.Errorf("%s: hostname %s is longer than 15 characters and cannot name a computer account in %s", c.Name, s.Hostname, s.Domain.Provider)
			}
			if other, dup := owners[s.Hostname]; dup {
				return nil, fmt.Errorf("hostname %s is planned for %s and %s", s.Hostname, other, c.Name)
			}
			owners[s.Hostname] = c.Name
		}
		p.Contracts = append(p.Contracts, c)
	}

	// The hash is what the plan does, not where it came from: a commit that
	// leaves a contract's plan alone keeps its hash, and with it the
	// approvals given for it (design 7.6).
	content := *p
	content.Commit = ""
	sum, err := hash(&content)
	if err != nil {
		return nil, err
	}
	p.Hash = sum
	return p, nil
}

func contract(ctx context.Context, g *catalog.Graph, o *catalog.Object, st State) (Contract, error) {
	s := o.Spec.(*catalog.ServerSpec)
	size := mustGet(g, catalog.KindSize, s.Size).Spec.(*catalog.SizeSpec)
	img := mustGet(g, catalog.KindImage, s.Image).Spec.(*catalog.ImageSpec)
	c := Contract{
		Name:     o.Metadata.Name,
		Labels:   o.Metadata.Labels,
		Env:      o.Metadata.Labels["env"],
		Approval: approval(g, o.Metadata.Labels["env"]),
		Image:    s.Image,
		Size:     Size{Name: s.Size, CPU: size.CPU, MemoryGiB: size.MemoryGiB, DiskGiB: size.DiskGiB},
		Disks:    s.Disks,
		Servers:  []Server{},
	}

	live, err := st.LiveHosts(ctx, c.Name)
	if err != nil {
		return Contract{}, fmt.Errorf("%s: %w", c.Name, err)
	}
	// Sequence numbers in use per site, including servers about to go:
	// they hold theirs until they are deleted.
	used := map[string]map[int]bool{}
	for _, h := range live {
		if used[h.Site] == nil {
			used[h.Site] = map[int]bool{}
		}
		used[h.Site][h.Seq] = true
	}
	retire := map[string]bool{}
	for _, name := range s.Retire {
		retire[name] = true
	}

	decommissioned := s.Lifecycle != nil && s.Lifecycle.State == "decommissioned"
	for _, pl := range placements(s) {
		if decommissioned {
			break
		}
		net := mustGet(g, catalog.KindNetwork, pl.Network)
		ns := net.Spec.(*catalog.NetworkSpec)
		site := mustGet(g, catalog.KindSite, ns.Site).Spec.(*catalog.SiteSpec)
		ptype := mustGet(g, catalog.KindProvider, site.Compute.Provider).Spec.(*catalog.ProviderSpec).Type
		tpl := template(img, site.Compute.Provider)
		desired := func(action string) Server {
			srv := Server{
				Action: action, Site: ns.Site, Network: pl.Network,
				Compute: Compute{
					Provider: site.Compute.Provider, Type: ptype,
					Template: templateName(tpl.Template), Customization: tpl.Customization,
					Nodes: site.Compute.Nodes, Pool: site.Compute.Pool,
					Datacenter: site.Compute.Datacenter, Cluster: site.Compute.Cluster,
					Attachment: attachment(ns.Attachment),
				},
				IPAM: IPAM{Provider: ns.IPAM.Provider, Pool: ns.IPAM.Pool, Gateway: ns.Gateway},
			}
			if site.Compute.Storage != nil {
				srv.Compute.Storage = site.Compute.Storage.Default
			}
			if d := ns.DNS; d != nil && d.Provider != "" {
				srv.DNS = &DNS{Provider: d.Provider, Zone: d.Zone, Reverse: d.Reverse, TTL: d.TTL}
				if srv.DNS.TTL == 0 {
					srv.DNS.TTL = 3600
				}
			}
			if d := ns.Domain; d != nil {
				srv.Domain = &Domain{Provider: d.Provider, OU: d.OU}
			}
			return srv
		}

		// Existing servers of this network: retired ones go, then the
		// highest sequence numbers beyond the replica count (design 9.4).
		var mine []Host
		for _, h := range live {
			if h.Network == pl.Network && h.Site == ns.Site && !retire[h.Hostname] {
				mine = append(mine, h)
			}
		}
		sort.Slice(mine, func(i, j int) bool { return mine[i].Seq < mine[j].Seq })
		keep := mine
		if len(keep) > pl.Replicas {
			keep = mine[:pl.Replicas]
		}
		for _, h := range keep {
			srv := desired(Keep)
			srv.UID, srv.Status, srv.Hostname, srv.Seq = h.UID, h.Status, h.Hostname, h.Seq
			c.Servers = append(c.Servers, srv)
		}

		for n := len(keep); n < pl.Replicas; n++ {
			seq, host, err := nextName(ctx, g, o, st, ns.Site, used)
			if err != nil {
				return Contract{}, fmt.Errorf("%s: %w", c.Name, err)
			}
			srv := desired(Create)
			srv.Hostname, srv.Seq = host, seq
			c.Servers = append(c.Servers, srv)
		}
	}

	// Everything else goes: retired, beyond the count, on a network the
	// contract no longer uses, or the whole contract when decommissioned.
	kept := map[string]bool{}
	for _, srv := range c.Servers {
		kept[srv.UID] = true
	}
	var gone []Host
	for _, h := range live {
		if !kept[h.UID] {
			gone = append(gone, h)
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].Site != gone[j].Site {
			return gone[i].Site < gone[j].Site
		}
		return gone[i].Seq > gone[j].Seq
	})
	for _, h := range gone {
		c.Servers = append(c.Servers, Server{
			Action: Delete, UID: h.UID, Status: h.Status, Hostname: h.Hostname,
			Seq: h.Seq, Site: h.Site, Network: h.Network,
		})
	}

	if s.Baseline != "" {
		b := mustGet(g, catalog.KindBaseline, s.Baseline).Spec.(*catalog.BaselineSpec)
		c.Baseline = &Baseline{Name: s.Baseline, Playbook: b.Playbook}
	}
	for _, rr := range s.AnsibleRoles {
		rs := mustGet(g, catalog.KindAnsibleRole, rr.Name).Spec.(*catalog.AnsibleRoleSpec)
		c.Roles = append(c.Roles, Role{
			Name: rr.Name, Role: rs.Source.Role, Provider: rs.Source.Provider,
			Params: mask(rs.Params, catalog.EffectiveParams(rs.Params, rr.Params)),
		})
	}
	seen := map[string]bool{}
	addAgent := func(name string, mandatory bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		as := mustGet(g, catalog.KindAgent, name).Spec.(*catalog.AgentSpec)
		rs := mustGet(g, catalog.KindAnsibleRole, as.AnsibleRole).Spec.(*catalog.AnsibleRoleSpec)
		c.Agents = append(c.Agents, Agent{Name: name, Role: rs.Source.Role, Mandatory: mandatory,
			Params: mask(rs.Params, catalog.EffectiveParams(rs.Params, as.Params))})
	}
	for _, o := range g.Catalog.List(catalog.KindAgent) {
		as := o.Spec.(*catalog.AgentSpec)
		if as.Mandatory && (len(as.OS) == 0 || slices.Contains(as.OS, img.OS)) {
			addAgent(o.Metadata.Name, true)
		}
	}
	for _, name := range s.Agents {
		addAgent(name, false)
	}
	for _, ar := range s.Apps {
		as := mustGet(g, catalog.KindApp, ar.Name).Spec.(*catalog.AppSpec)
		params := catalog.EffectiveParams(as.Params, ar.Params)
		app := App{Name: ar.Name, Playbook: as.Playbook, Params: mask(as.Params, params)}
		if a := as.Artifact; a != nil {
			ps := mustGet(g, catalog.KindProvider, a.Provider).Spec.(*catalog.ProviderSpec)
			path := strings.TrimLeft(expand(a.Path, params), "/")
			app.Artifact = strings.TrimRight(ps.Endpoint, "/") + "/repository/" + a.Repository + "/" + path
			app.Source = &ArtifactSource{Provider: a.Provider, Repository: a.Repository, Path: path}
		}
		c.Apps = append(c.Apps, app)
	}
	for _, a := range s.Access {
		if a.Team != "" {
			c.Access = append(c.Access, Access{Via: "team/" + a.Team, Teams: []string{a.Team}, Sudo: a.Sudo})
			continue
		}
		teams, privileged := g.GroupTeams(a.Group)
		owner := ""
		if o, ok := g.Catalog.Get(catalog.KindGroup, a.Group); ok {
			owner = o.Spec.(*catalog.GroupSpec).Owner
		}
		c.Access = append(c.Access, Access{Via: "group/" + a.Group, Teams: teams, Sudo: a.Sudo, Privileged: privileged, Owner: owner})
	}
	if b := g.Platform().Spec.(*catalog.PlatformSpec).Bastion; b != nil && len(c.Access) > 0 {
		c.Bastion = &Bastion{Provider: b.Provider, Kind: "ssh"}
		if img.OS == "windows" {
			c.Bastion.Kind = "rdp"
		}
		for _, a := range c.Access {
			c.Bastion.Sudo = c.Bastion.Sudo || (a.Sudo && c.Bastion.Kind == "ssh")
		}
	}
	if m := g.Platform().Spec.(*catalog.PlatformSpec).Monitoring; m != nil {
		if c.Monitoring, err = monitoring(g, m, img, s); err != nil {
			return Contract{}, fmt.Errorf("%s: %w", c.Name, err)
		}
		for i, srv := range c.Servers {
			if srv.Action == Delete {
				continue
			}
			if c.Servers[i].HostGroups, err = g.HostGroups(o, srv.Site, srv.Seq); err != nil {
				return Contract{}, fmt.Errorf("%s: %w", c.Name, err)
			}
			// The contract's own group: what its access gives teams rights
			// on in monitoring, and nothing else is in.
			if own := ContractHostGroup(c.Name); !slices.Contains(c.Servers[i].HostGroups, own) {
				c.Servers[i].HostGroups = append(c.Servers[i].HostGroups, own)
			}
		}
	}
	return c, nil
}

// monitoring lists the templates of the image's OS and of each role's
// monitoring names; a name without templates in the Platform is an error.
func monitoring(g *catalog.Graph, m *catalog.Monitoring, img *catalog.ImageSpec, s *catalog.ServerSpec) (*Monitoring, error) {
	out := &Monitoring{Provider: m.Provider}
	seen := map[string]bool{}
	add := func(ts []string) {
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				out.Templates = append(out.Templates, t)
			}
		}
	}
	add(m.Templates[img.OS])
	for _, rr := range s.AnsibleRoles {
		rs := mustGet(g, catalog.KindAnsibleRole, rr.Name).Spec.(*catalog.AnsibleRoleSpec)
		for _, name := range rs.Monitoring {
			ts, ok := m.Templates[name]
			if !ok {
				return nil, fmt.Errorf("role %s: monitoring %q has no templates in the Platform", rr.Name, name)
			}
			add(ts)
		}
	}
	return out, nil
}

// placements is the server's networks with their replica counts.
func placements(s *catalog.ServerSpec) []catalog.Placement {
	if s.Network == "" {
		return s.Topology
	}
	n := 1
	if s.Replicas != nil {
		n = *s.Replicas
	}
	return []catalog.Placement{{Network: s.Network, Replicas: n}}
}

func approval(g *catalog.Graph, env string) string {
	if pol := g.Policy(); pol != nil {
		if e, ok := pol.Spec.(*catalog.PolicySpec).Environments[env]; ok && e.Approval == "required" {
			return "required"
		}
	}
	return "none"
}

func template(img *catalog.ImageSpec, provider string) catalog.ImageTemplate {
	for _, t := range img.Templates {
		if t.Provider == provider {
			return t
		}
	}
	return catalog.ImageTemplate{} // Resolve guarantees one exists
}

func templateName(t catalog.TemplateRef) string {
	if t.VMID != 0 {
		return "vmid " + strconv.Itoa(t.VMID)
	}
	return t.Name
}

func attachment(a catalog.Attachment) string {
	switch {
	case a.VNet != "":
		return "vnet " + a.VNet
	case a.Bridge != "" && a.VLAN != 0:
		return fmt.Sprintf("bridge %s vlan %d", a.Bridge, a.VLAN)
	case a.Bridge != "":
		return "bridge " + a.Bridge
	}
	return "portgroup " + a.Portgroup
}

// expand replaces ${name} in s with params' values.
func expand(s string, params map[string]any) string {
	return os.Expand(s, func(k string) string {
		if v, ok := params[k]; ok {
			return fmt.Sprint(v)
		}
		return "${" + k + "}"
	})
}

// mask hides the values of no_log params. A bao:// reference is kept: it
// is not the secret, and the run resolves it (design 6.2).
func mask(schema map[string]catalog.Param, params map[string]any) map[string]any {
	for k, v := range params {
		if ref, _ := v.(string); schema[k].NoLog && !strings.HasPrefix(ref, "bao://") {
			params[k] = Masked
		}
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func mustGet(g *catalog.Graph, kind, name string) *catalog.Object {
	o, ok := g.Catalog.Get(kind, name)
	if !ok {
		panic(fmt.Sprintf("plan: %s/%s missing from a resolved graph", kind, name))
	}
	return o
}

func hash(p *Plan) (string, error) {
	cp := *p
	cp.Hash = ""
	data, err := json.Marshal(cp) // maps marshal with sorted keys
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ContractNames are the plan's contracts, in order.
func (p *Plan) ContractNames() []string {
	names := make([]string, 0, len(p.Contracts))
	for _, c := range p.Contracts {
		names = append(names, c.Name)
	}
	return names
}
