package catalog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/teambition/rrule-go"

	"github.com/Warewave-Technology/gaia/internal/cel"
)

// Ref is one reference from a field of an object to another object.
type Ref struct {
	From  Key
	Field string
	To    Key
}

// Graph is a catalog whose references all resolve.
type Graph struct {
	Catalog *Catalog
	out     map[Key][]Ref
	in      map[Key][]Ref
	naming  *cel.Program
	groups  []*cel.Program            // Platform.monitoring.hostGroups
	rules   map[string][]*cel.Program // by AnsibleRole name, in spec order
}

// DefaultNaming is used when there is no Policy or it has no naming.
const DefaultNaming = `contract + "-" + pad(seq, 2)`

// Hostname evaluates the naming expression for a server of contract o at
// site with the given sequence number.
func (g *Graph) Hostname(o *Object, site string, seq int) (string, error) {
	name, err := g.naming.EvalString(cel.NamingVars(o.Metadata.Name, o.Metadata.Labels, site, seq))
	if err != nil {
		return "", fmt.Errorf("naming %s: %w", g.naming, err)
	}
	if !hostnameRe.MatchString(name) {
		return "", fmt.Errorf("naming %s: %q is not a valid hostname (lowercase letters, digits and '-', at most 63)", g.naming, name)
	}
	return name, nil
}

// HostGroups evaluates the Platform's monitoring host groups for a server
// of contract o at site, in order, without empty or repeated names.
func (g *Graph) HostGroups(o *Object, site string, seq int) ([]string, error) {
	vars := cel.NamingVars(o.Metadata.Name, o.Metadata.Labels, site, seq)
	var out []string
	seen := map[string]bool{}
	for _, p := range g.groups {
		name, err := p.EvalString(vars)
		if err != nil {
			return nil, fmt.Errorf("host group %s: %w", p, err)
		}
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// Refs returns the references an object makes, in field order.
func (g *Graph) Refs(k Key) []Ref { return g.out[k] }

// UsedBy returns the references that point at an object.
func (g *Graph) UsedBy(k Key) []Ref { return g.in[k] }

// Platform returns the single Platform object.
func (g *Graph) Platform() *Object { return g.Catalog.List(KindPlatform)[0] }

// Policy returns the Policy, or nil when there is none.
func (g *Graph) Policy() *Object {
	if p := g.Catalog.List(KindPolicy); len(p) > 0 {
		return p[0]
	}
	return nil
}

// Team returns a team's spec, or nil when the catalog has no such Team.
func (g *Graph) Team(name string) *TeamSpec {
	if o, ok := g.Catalog.Get(KindTeam, name); ok {
		return o.Spec.(*TeamSpec)
	}
	return nil
}

// AuthConfig is the catalog's AuthConfig, or nil when it has none.
func (g *Graph) AuthConfig() *AuthConfigSpec {
	if a := g.Catalog.List(KindAuthConfig); len(a) > 0 {
		return a[0].Spec.(*AuthConfigSpec)
	}
	return nil
}

// RolesOf returns the Gaia roles the Policy gives to any of teams, viewer
// always among them.
func (g *Graph) RolesOf(teams []string) []string {
	roles := []string{"viewer"}
	pol := g.Policy()
	if pol == nil {
		return roles
	}
	gr := pol.Spec.(*PolicySpec).GaiaRoles
	for _, role := range GaiaRoles {
		for _, t := range gr[role] {
			if contains(teams, t) && !contains(roles, role) {
				roles = append(roles, role)
				break
			}
		}
	}
	// platform-admin can do everything an operator can (design 11.4).
	if contains(roles, "platform-admin") && !contains(roles, "operator") {
		roles = append(roles, "operator")
	}
	return roles
}

// Resolve checks every reference of the catalog and the rules that span
// objects (provider types, hypervisor-specific fields, templates per site,
// environments, OS, role conflicts and params). It reports every problem
// at once; when the error is non-nil the graph must not be used.
func Resolve(cat *Catalog) (*Graph, error) {
	r := &resolver{g: &Graph{Catalog: cat, out: map[Key][]Ref{}, in: map[Key][]Ref{}, rules: map[string][]*cel.Program{}}}
	r.singletons()
	r.naming()
	for _, o := range cat.List(KindPlatform) {
		r.platform(o)
	}
	for _, o := range cat.List(KindSite) {
		r.site(o)
	}
	for _, o := range cat.List(KindNetwork) {
		r.network(o)
	}
	for _, o := range cat.List(KindImage) {
		r.image(o)
	}
	for _, o := range cat.List(KindAnsibleRole) {
		r.ansibleRole(o)
	}
	for _, o := range cat.List(KindTeamSource) {
		r.teamSource(o)
	}
	for _, o := range cat.List(KindRoleSource) {
		s := o.Spec.(*AnsibleRoleSourceSpec)
		r.ref(o, "spec.provider", KindProvider, s.Provider, "git")
		if _, err := cel.RoleString(s.Naming); err != nil {
			r.fail(o, "spec.naming", "%v", err)
		}
		for field, src := range map[string]string{"os": s.Map.OS, "labels": s.Map.Labels} {
			if src == "" {
				continue
			}
			if _, err := cel.RoleValue(src); err != nil {
				r.fail(o, "spec.map."+field, "%v", err)
			}
		}
	}
	for _, o := range cat.List(KindNetworkSrc) {
		s := o.Spec.(*NetworkSourceSpec)
		r.ref(o, "spec.provider", KindProvider, s.Provider, "ipam")
		if _, err := cel.SubnetRule(s.Select); err != nil {
			r.fail(o, "spec.select", "%v", err)
		}
		if _, err := cel.SubnetString(s.Naming); err != nil {
			r.fail(o, "spec.naming", "%v", err)
		}
		for field, src := range s.Map.Fields() {
			if src == "" {
				continue
			}
			if _, err := cel.SubnetValue(src); err != nil {
				r.fail(o, "spec.map."+field, "%v", err)
			}
		}
	}
	for _, o := range cat.List(KindAuthConfig) {
		for i, p := range o.Spec.(*AuthConfigSpec).Providers {
			if p.Directory != nil {
				r.ref(o, fmt.Sprintf("spec.providers[%d].directory.provider", i), KindProvider, p.Directory.Provider, "activedirectory")
			}
		}
	}
	for _, o := range cat.List(KindTeam) {
		r.team(o)
	}
	for _, o := range cat.List(KindGroup) {
		r.group(o)
	}
	for _, o := range cat.List(KindApp) {
		r.app(o)
	}
	for _, o := range cat.List(KindAgent) {
		as := o.Spec.(*AgentSpec)
		if role := r.ref(o, "spec.ansibleRole", KindAnsibleRole, as.AnsibleRole); role != nil {
			rs := role.Spec.(*AnsibleRoleSpec)
			for _, name := range sortedKeys(as.Params) {
				if p, ok := rs.Params[name]; !ok {
					r.fail(o, "spec.params", "AnsibleRole %q has no param %q", as.AnsibleRole, name)
				} else {
					for _, msg := range checkValue(name, p, as.Params[name]) {
						r.fail(o, "spec.params."+name, "%s", msg)
					}
				}
			}
		}
	}
	for _, o := range cat.List(KindServer) {
		r.server(o)
	}
	for _, o := range cat.List(KindOperation) {
		r.operation(o)
	}
	for _, o := range cat.List(KindSchedule) {
		s := o.Spec.(*OperationScheduleSpec)
		if _, err := rrule.StrToRRule(s.Recurrence); err != nil {
			r.fail(o, "spec.recurrence", "%v", err)
		}
		if _, err := cel.Selector(s.Template.Selector); err != nil {
			r.fail(o, "spec.template.selector", "%v", err)
		}
		if s.Template.Runbook != "" {
			if role := r.ref(o, "spec.template.runbook", KindAnsibleRole, s.Template.Runbook); role != nil && !role.Spec.(*AnsibleRoleSpec).Runbook {
				r.fail(o, "spec.template.runbook", "AnsibleRole %q is not a runbook", s.Template.Runbook)
			}
		}
	}
	if len(r.errs) > 0 {
		sortErrors(r.errs)
		return nil, r.errs
	}
	return r.g, nil
}

type resolver struct {
	g    *Graph
	errs Errors
}

func (r *resolver) fail(o *Object, field, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if field != "" {
		msg = field + ": " + msg
	}
	r.errs = append(r.errs, Error{File: o.Source.File, Line: o.Line(field), Object: o.Key().String(), Msg: msg})
}

// ref resolves name as an object of kind, records the edge and returns the
// target. For Providers, types restricts which provider types fit.
func (r *resolver) ref(from *Object, field, kind, name string, types ...string) *Object {
	to, ok := r.g.Catalog.Get(kind, name)
	if !ok {
		r.fail(from, field, "%s %q not found%s", kind, name, r.suggest(kind, name))
		return nil
	}
	if kind == KindProvider && len(types) > 0 {
		if t := to.Spec.(*ProviderSpec).Type; !contains(types, t) {
			r.fail(from, field, "Provider %q has type %s, want %s", name, t, strings.Join(types, " or "))
			return nil
		}
	}
	ref := Ref{From: from.Key(), Field: field, To: to.Key()}
	r.g.out[ref.From] = append(r.g.out[ref.From], ref)
	r.g.in[ref.To] = append(r.g.in[ref.To], ref)
	return to
}

// suggest names the closest existing object of the kind, if any is close.
func (r *resolver) suggest(kind, name string) string {
	var names []string
	for _, o := range r.g.Catalog.List(kind) {
		names = append(names, o.Metadata.Name)
	}
	return nearest(name, names)
}

func (r *resolver) singletons() {
	plats := r.g.Catalog.List(KindPlatform)
	switch {
	case len(plats) == 0:
		r.errs = append(r.errs, Error{Msg: "no Platform object; exactly one is required"})
	case len(plats) > 1:
		for _, o := range plats[1:] {
			r.fail(o, "", "only one Platform is allowed (also %s)", plats[0].Source)
		}
	}
	for _, kind := range []string{KindPolicy, KindAuthConfig} {
		if objs := r.g.Catalog.List(kind); len(objs) > 1 {
			for _, o := range objs[1:] {
				r.fail(o, "", "only one %s is allowed (also %s)", kind, objs[0].Source)
			}
		}
	}
}

// naming compiles the Policy's naming expression, or the default.
func (r *resolver) naming() {
	src := DefaultNaming
	pol := r.g.Policy()
	if pol != nil && pol.Spec.(*PolicySpec).Naming != "" {
		src = pol.Spec.(*PolicySpec).Naming
	}
	p, err := cel.Naming(src)
	if err != nil {
		r.fail(pol, "spec.naming", "%v", err)
		return
	}
	r.g.naming = p
}

func (r *resolver) platform(o *Object) {
	s := o.Spec.(*PlatformSpec)
	r.ref(o, "spec.secrets", KindProvider, s.Secrets, "openbao")
	r.ref(o, "spec.engines.provisioning.state", KindProvider, s.Engines.Provisioning.State, "postgres", "s3")
	r.ref(o, "spec.engines.configuration.source", KindProvider, s.Engines.Configuration.Source, "git")
	if s.RunArchive != "" {
		if a := r.ref(o, "spec.runArchive", KindProvider, s.RunArchive, "s3"); a != nil && a.Spec.(*ProviderSpec).Retention == "" {
			r.fail(o, "spec.runArchive", "Provider %q has no retention; run records are locked for it", s.RunArchive)
		}
	}
	for i, c := range s.Contracts {
		r.ref(o, fmt.Sprintf("spec.contracts[%d].provider", i), KindProvider, c.Provider, "git")
	}
	if s.RunLogs != "" {
		r.ref(o, "spec.runLogs", KindProvider, s.RunLogs, "s3")
	}
	if n := s.Notifications; n != nil && n.Email != "" {
		r.ref(o, "spec.notifications.email", KindProvider, n.Email, "smtp")
	}
	if m := s.Monitoring; m != nil {
		r.ref(o, "spec.monitoring.provider", KindProvider, m.Provider, "zabbix")
		for i, src := range m.HostGroups {
			p, err := cel.Naming(src)
			if err != nil {
				r.fail(o, fmt.Sprintf("spec.monitoring.hostGroups[%d]", i), "%v", err)
				continue
			}
			r.g.groups = append(r.g.groups, p)
		}
	}
}

func (r *resolver) team(o *Object) {
	s := o.Spec.(*TeamSpec)
	if s.ADGroup != nil && s.ADGroup.Provider != "" {
		r.ref(o, "spec.adGroup.provider", KindProvider, s.ADGroup.Provider, "activedirectory")
	}
	for i, n := range s.Notifications {
		field := fmt.Sprintf("spec.notifications[%d]", i)
		p := r.ref(o, field+".provider", KindProvider, n.Provider, NotificationTypes...)
		if p == nil {
			continue
		}
		smtp := p.Spec.(*ProviderSpec).Type == "smtp"
		switch {
		case smtp && len(n.To) == 0:
			r.fail(o, field+".to", "an smtp channel needs addresses")
		case !smtp && len(n.To) > 0:
			r.fail(o, field+".to", "only for smtp channels")
		}
	}
}

func (r *resolver) group(o *Object) {
	s := o.Spec.(*GroupSpec)
	r.ref(o, "spec.owner", KindTeam, s.Owner)
	for i, t := range s.Teams {
		r.ref(o, fmt.Sprintf("spec.teams[%d]", i), KindTeam, t)
	}
	for i, gr := range s.Groups {
		if r.ref(o, fmt.Sprintf("spec.groups[%d]", i), KindGroup, gr) == nil {
			continue
		}
		if r.g.reaches(gr, o.Metadata.Name, map[string]bool{}) {
			r.fail(o, fmt.Sprintf("spec.groups[%d]", i), "Group %q contains %s again", gr, o.Metadata.Name)
		}
	}
}

// reaches reports whether group from contains group to, through nesting.
func (g *Graph) reaches(from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	o, ok := g.Catalog.Get(KindGroup, from)
	if !ok {
		return false
	}
	for _, n := range o.Spec.(*GroupSpec).Groups {
		if g.reaches(n, to, seen) {
			return true
		}
	}
	return false
}

// GroupTeams returns the teams of a group, nested groups opened, sorted,
// and whether it or a group inside it is privileged.
func (g *Graph) GroupTeams(name string) (teams []string, privileged bool) {
	set := map[string]bool{}
	var walk func(string, map[string]bool)
	walk = func(n string, seen map[string]bool) {
		if seen[n] {
			return
		}
		seen[n] = true
		o, ok := g.Catalog.Get(KindGroup, n)
		if !ok {
			return
		}
		s := o.Spec.(*GroupSpec)
		privileged = privileged || s.Privileged
		for _, t := range s.Teams {
			set[t] = true
		}
		for _, sub := range s.Groups {
			walk(sub, seen)
		}
	}
	walk(name, map[string]bool{})
	for t := range set {
		teams = append(teams, t)
	}
	sort.Strings(teams)
	return teams, privileged
}

func (r *resolver) teamSource(o *Object) {
	s := o.Spec.(*TeamSourceSpec)
	r.ref(o, "spec.provider", KindProvider, s.Provider, "activedirectory")
	if _, err := cel.GroupRule(s.Select); err != nil {
		r.fail(o, "spec.select", "%v", err)
	}
	for field, src := range map[string]string{"spec.naming": s.Naming, "spec.map.displayName": s.Map.DisplayName,
		"spec.map.approvers": s.Map.Approvers, "spec.map.secretsPrefix": s.Map.SecretsPrefix} {
		if src == "" && field != "spec.naming" {
			continue
		}
		if _, err := cel.GroupString(src); err != nil {
			r.fail(o, field, "%v", err)
		}
	}
}

func (r *resolver) site(o *Object) {
	c := o.Spec.(*SiteSpec).Compute
	p := r.ref(o, "spec.compute.provider", KindProvider, c.Provider, "proxmox", "vsphere")
	if p == nil {
		return
	}
	switch p.Spec.(*ProviderSpec).Type {
	case "proxmox":
		if len(c.Nodes) == 0 {
			r.fail(o, "spec.compute.nodes", "at least one node for Proxmox")
		}
		if c.Datacenter != "" || c.Folder != "" {
			r.fail(o, "spec.compute", "datacenter and folder are vSphere fields")
		}
		if c.VMIDs == nil {
			r.fail(o, "spec.compute.vmids", "the VM ID range Gaia may use is required for Proxmox")
		}
	case "vsphere":
		if c.Datacenter == "" || c.Cluster == "" {
			r.fail(o, "spec.compute", "datacenter and cluster are required for vSphere")
		}
		if len(c.Nodes) > 0 || c.Pool != "" || c.VMIDs != nil {
			r.fail(o, "spec.compute", "nodes, pool and vmids are Proxmox fields")
		}
	}
}

// computeType returns the hypervisor type behind a Site, or "".
func (r *resolver) computeType(site *Object) (provider, typ string) {
	name := site.Spec.(*SiteSpec).Compute.Provider
	if p, ok := r.g.Catalog.Get(KindProvider, name); ok {
		return name, p.Spec.(*ProviderSpec).Type
	}
	return name, ""
}

func (r *resolver) network(o *Object) {
	s := o.Spec.(*NetworkSpec)
	r.ref(o, "spec.ipam.provider", KindProvider, s.IPAM.Provider, "ipam")
	if s.DNS != nil && s.DNS.Provider != "" {
		if p := r.ref(o, "spec.dns.provider", KindProvider, s.DNS.Provider, "activedirectory"); p != nil && p.Spec.(*ProviderSpec).WriteCredentials == "" {
			r.fail(o, "spec.dns.provider", "Provider %q has no writeCredentials: Gaia cannot write DNS records", s.DNS.Provider)
		}
	}
	if s.Domain != nil {
		if p := r.ref(o, "spec.domain.provider", KindProvider, s.Domain.Provider, "activedirectory"); p != nil && p.Spec.(*ProviderSpec).WriteCredentials == "" {
			r.fail(o, "spec.domain.provider", "Provider %q has no writeCredentials: Gaia cannot create computer accounts", s.Domain.Provider)
		}
	}
	site := r.ref(o, "spec.site", KindSite, s.Site)
	if site == nil {
		return
	}
	switch _, typ := r.computeType(site); typ {
	case "proxmox":
		if s.Attachment.Portgroup != "" {
			r.fail(o, "spec.attachment", "Site %q is on Proxmox: use bridge or vnet, not portgroup", s.Site)
		}
	case "vsphere":
		if s.Attachment.Portgroup == "" {
			r.fail(o, "spec.attachment", "Site %q is on vSphere: use portgroup", s.Site)
		}
	}
}

func (r *resolver) image(o *Object) {
	seen := map[string]bool{}
	for i, t := range o.Spec.(*ImageSpec).Templates {
		field := fmt.Sprintf("spec.templates[%d]", i)
		if seen[t.Provider] {
			r.fail(o, field+".provider", "provider %q has more than one template", t.Provider)
		}
		seen[t.Provider] = true
		p := r.ref(o, field+".provider", KindProvider, t.Provider, "proxmox", "vsphere")
		if p != nil && p.Spec.(*ProviderSpec).Type == "vsphere" && (t.Template.Name == "" || t.Template.VMID != 0) {
			r.fail(o, field+".template", "vSphere templates are named; vmid is a Proxmox field")
		}
	}
}

func (r *resolver) ansibleRole(o *Object) {
	s := o.Spec.(*AnsibleRoleSpec)
	r.ref(o, "spec.source.provider", KindProvider, s.Source.Provider, "git")
	for i, c := range s.Conflicts {
		r.ref(o, fmt.Sprintf("spec.conflicts[%d]", i), KindAnsibleRole, c)
	}
	for i, rule := range s.Rules {
		p, err := cel.Rule(rule.Rule)
		if err != nil {
			r.fail(o, fmt.Sprintf("spec.rules[%d].rule", i), "%v", err)
			continue
		}
		r.g.rules[o.Metadata.Name] = append(r.g.rules[o.Metadata.Name], p)
	}
}

func (r *resolver) operation(o *Object) {
	s := o.Spec.(*OperationSpec)
	if _, err := cel.Selector(s.Selector); err != nil {
		r.fail(o, "spec.selector", "%v", err)
	}
	if s.Runbook == "" {
		return
	}
	role := r.ref(o, "spec.runbook", KindAnsibleRole, s.Runbook)
	if role == nil {
		return
	}
	rs := role.Spec.(*AnsibleRoleSpec)
	if !rs.Runbook {
		r.fail(o, "spec.runbook", "AnsibleRole %q is not a runbook (runbook: true in its meta/gaia.yml)", s.Runbook)
		return
	}
	for _, name := range sortedKeys(s.Params) {
		p, ok := rs.Params[name]
		if !ok {
			r.fail(o, "spec.params", "AnsibleRole %q has no param %q%s", s.Runbook, name, nearest(name, sortedKeys(rs.Params)))
			continue
		}
		for _, msg := range checkValue(name, p, s.Params[name]) {
			r.fail(o, "spec.params."+name, "%s", msg)
		}
		if v, _ := s.Params[name].(string); strings.HasPrefix(v, "bao://") {
			r.fail(o, "spec.params."+name, "an Operation takes no secrets (yet)")
		}
	}
}

// SpecMap is an object's spec as written, for expressions.
func (o *Object) SpecMap() map[string]any {
	var m map[string]any
	if n := specNode(o.node); n != nil {
		_ = n.Decode(&m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func (r *resolver) app(o *Object) {
	s := o.Spec.(*AppSpec)
	for i, req := range s.Requires {
		r.ref(o, fmt.Sprintf("spec.requires[%d]", i), KindAnsibleRole, req)
	}
	if s.Artifact != nil {
		r.ref(o, "spec.artifact.provider", KindProvider, s.Artifact.Provider, "nexus")
	}
	for i, rule := range s.Rules {
		p, err := cel.Rule(rule.Rule)
		if err != nil {
			r.fail(o, fmt.Sprintf("spec.rules[%d].rule", i), "%v", err)
			continue
		}
		r.g.rules["app/"+o.Metadata.Name] = append(r.g.rules["app/"+o.Metadata.Name], p)
	}
}

func (r *resolver) server(o *Object) {
	s := o.Spec.(*ServerSpec)
	labels := o.Metadata.Labels
	env := labels["env"]

	if pol := r.g.Policy(); pol != nil {
		ps := pol.Spec.(*PolicySpec)
		for _, l := range ps.RequiredLabels {
			if labels[l] == "" {
				r.fail(o, "metadata.labels", "label %q is required by Policy %q", l, pol.Metadata.Name)
			}
		}
		if _, ok := ps.Environments[env]; env != "" && len(ps.Environments) > 0 && !ok {
			r.fail(o, "metadata.labels.env", "environment %q is not defined in Policy %q (%s)", env, pol.Metadata.Name, strings.Join(sortedKeys(ps.Environments), ", "))
		}
	}

	// The owner is a Team once the catalog has teams; catalogs from before
	// Team objects name owners freely.
	if owner := labels["owner"]; owner != "" && len(r.g.Catalog.List(KindTeam)) > 0 {
		r.ref(o, "metadata.labels.owner", KindTeam, owner)
	}

	for i, a := range s.Access {
		if a.Group != "" {
			r.ref(o, fmt.Sprintf("spec.access[%d].group", i), KindGroup, a.Group)
		} else {
			r.ref(o, fmt.Sprintf("spec.access[%d].team", i), KindTeam, a.Team)
		}
	}

	img := r.ref(o, "spec.image", KindImage, s.Image)
	r.ref(o, "spec.size", KindSize, s.Size)
	var imgOS string
	if img != nil {
		imgOS = img.Spec.(*ImageSpec).OS
	}

	placements := s.Topology
	fields := make([]string, len(placements))
	for i := range placements {
		fields[i] = fmt.Sprintf("spec.topology[%d].network", i)
	}
	if s.Network != "" {
		placements = []Placement{{Network: s.Network}}
		fields = []string{"spec.network"}
	}
	var nets []*Object
	for i, p := range placements {
		if net := r.placement(o, fields[i], p.Network, env, img); net != nil {
			nets = append(nets, net)
			r.hostname(o, fields[i], net)
		}
	}

	if s.Baseline != "" {
		if b := r.ref(o, "spec.baseline", KindBaseline, s.Baseline); b != nil && imgOS != "" {
			if bos := b.Spec.(*BaselineSpec).OS; bos != imgOS {
				r.fail(o, "spec.baseline", "Baseline %q is for %s, Image %q is %s", s.Baseline, bos, s.Image, imgOS)
			}
		}
	}

	roles := map[string]*Object{}
	for i, rr := range s.AnsibleRoles {
		field := fmt.Sprintf("spec.ansibleRoles[%d]", i)
		role := r.ref(o, field, KindAnsibleRole, rr.Name)
		if role == nil {
			continue
		}
		roles[rr.Name] = role
		rs := role.Spec.(*AnsibleRoleSpec)
		if imgOS != "" && len(rs.OS) > 0 && !contains(rs.OS, imgOS) {
			r.fail(o, field, "AnsibleRole %q supports %s, Image %q is %s", rr.Name, strings.Join(rs.OS, ", "), s.Image, imgOS)
		}
		if env != "" && len(rs.AllowedEnvironments) > 0 && !contains(rs.AllowedEnvironments, env) {
			r.fail(o, field, "AnsibleRole %q is not allowed in environment %q", rr.Name, env)
		}
		if r.roleParams(o, field, rr, rs) {
			r.roleRules(o, field, rr, rs, nets)
		}
	}
	for i, ar := range s.Apps {
		field := fmt.Sprintf("spec.apps[%d]", i)
		app := r.ref(o, field+".name", KindApp, ar.Name)
		if app == nil {
			continue
		}
		as := app.Spec.(*AppSpec)
		for _, req := range as.Requires {
			if _, ok := roles[req]; !ok {
				r.fail(o, field, "App %q needs AnsibleRole %q in ansibleRoles", ar.Name, req)
			}
		}
		if r.params(o, field, "App "+strconv.Quote(ar.Name), as.Params, ar.Params) {
			r.evalRules(o, field, "App "+strconv.Quote(ar.Name), r.g.rules["app/"+ar.Name], as.Rules, EffectiveParams(as.Params, ar.Params), nets)
		}
	}
	for i, name := range s.Agents {
		field := fmt.Sprintf("spec.agents[%d]", i)
		agent := r.ref(o, field, KindAgent, name)
		if agent == nil {
			continue
		}
		if as := agent.Spec.(*AgentSpec); imgOS != "" && len(as.OS) > 0 && !contains(as.OS, imgOS) {
			r.fail(o, field, "Agent %q is for %s, Image %q is %s", name, strings.Join(as.OS, ", "), s.Image, imgOS)
		}
	}
	reported := map[[2]string]bool{}
	for _, name := range sortedKeys(roles) {
		for _, c := range roles[name].Spec.(*AnsibleRoleSpec).Conflicts {
			pair := [2]string{min(name, c), max(name, c)}
			if _, both := roles[c]; both && !reported[pair] {
				reported[pair] = true
				r.fail(o, "spec.ansibleRoles", "AnsibleRole %q conflicts with %q", name, c)
			}
		}
	}
}

// placement checks one network of a Server: it exists, admits the
// environment and its site's hypervisor has a template of the image.
func (r *resolver) placement(o *Object, field, name, env string, img *Object) *Object {
	net := r.ref(o, field, KindNetwork, name)
	if net == nil {
		return nil
	}
	ns := net.Spec.(*NetworkSpec)
	if env != "" && len(ns.AllowedEnvironments) > 0 && !contains(ns.AllowedEnvironments, env) {
		r.fail(o, field, "Network %q does not allow environment %q (%s)", name, env, strings.Join(ns.AllowedEnvironments, ", "))
	}
	site, ok := r.g.Catalog.Get(KindSite, ns.Site)
	if !ok || img == nil {
		return net
	}
	provider, _ := r.computeType(site)
	for _, t := range img.Spec.(*ImageSpec).Templates {
		if t.Provider == provider {
			return net
		}
	}
	r.fail(o, field, "Image %q has no template for provider %q (Site %q of Network %q)", img.Metadata.Name, provider, ns.Site, name)
	return net
}

// hostname checks that naming gives a valid name at the network's site.
func (r *resolver) hostname(o *Object, field string, net *Object) {
	if r.g.naming == nil {
		return
	}
	if _, err := r.g.Hostname(o, net.Spec.(*NetworkSpec).Site, 1); err != nil {
		r.fail(o, field, "%v", err)
	}
}

// roleRules evaluates the role's rules for each network of the server.
func (r *resolver) roleRules(o *Object, field string, rr RoleRef, rs *AnsibleRoleSpec, nets []*Object) {
	r.evalRules(o, field, "AnsibleRole "+strconv.Quote(rr.Name), r.g.rules[rr.Name], rs.Rules, EffectiveParams(rs.Params, rr.Params), nets)
}

// evalRules evaluates rules of a role or an app for each network of the
// server; what names them in messages.
func (r *resolver) evalRules(o *Object, field, what string, progs []*cel.Program, rules []Rule, params map[string]any, nets []*Object) {
	if len(progs) == 0 {
		return
	}
	s := o.Spec.(*ServerSpec)
	size, ok1 := r.g.Catalog.Get(KindSize, s.Size)
	img, ok2 := r.g.Catalog.Get(KindImage, s.Image)
	if !ok1 || !ok2 {
		return
	}
	sz := size.Spec.(*SizeSpec)
	labels := map[string]string{}
	for k, v := range o.Metadata.Labels {
		labels[k] = v
	}
	for _, net := range nets {
		vars := map[string]any{
			"params":  params,
			"size":    map[string]any{"cpu": sz.CPU, "memoryGiB": sz.MemoryGiB, "diskGiB": sz.DiskGiB},
			"image":   map[string]any{"name": img.Metadata.Name, "os": img.Spec.(*ImageSpec).OS},
			"network": map[string]any{"name": net.Metadata.Name, "site": net.Spec.(*NetworkSpec).Site},
			"labels":  labels,
		}
		for i, p := range progs {
			ok, err := p.EvalBool(vars)
			switch {
			case err != nil:
				r.fail(o, field, "%s rule %d: %v", what, i, err)
			case !ok:
				r.fail(o, field, "%s: %s", what, rules[i].Message)
			}
		}
	}
}

// roleParams checks that given params exist in the role's schema and that
// required ones without a default are given. Values are checked later,
// with the full params schema.
func (r *resolver) roleParams(o *Object, field string, rr RoleRef, rs *AnsibleRoleSpec) bool {
	return r.params(o, field, "AnsibleRole "+strconv.Quote(rr.Name), rs.Params, rr.Params)
}

// params checks the params a contract gives a role or an app: they exist
// in the schema, their values fit, required ones are given, and secret
// references stay under the owner team's prefix (design 6.2, rule 2).
func (r *resolver) params(o *Object, field, what string, schema map[string]Param, given map[string]any) bool {
	before := len(r.errs)
	prefix, checkPrefix := "", false
	if owner := o.Metadata.Labels["owner"]; owner != "" && len(r.g.Catalog.List(KindTeam)) > 0 {
		checkPrefix = true
		if t := r.g.Team(owner); t != nil && t.Secrets != nil {
			prefix = t.Secrets.Prefix
		}
	}
	for _, name := range sortedKeys(given) {
		p, ok := schema[name]
		if !ok {
			r.fail(o, field+".params", "%s has no param %q%s", what, name, nearest(name, sortedKeys(schema)))
			continue
		}
		msgs := checkValue(name, p, given[name])
		for _, msg := range msgs {
			r.fail(o, field+".params."+name, "%s: %s", what, msg)
		}
		v, _ := given[name].(string)
		switch {
		case len(msgs) > 0:
		case p.NoLog && !strings.HasPrefix(v, "bao://"):
			r.fail(o, field+".params."+name, "%s: %s is no_log; give a bao:// reference, not a value in Git", what, name)
		case strings.HasPrefix(v, "bao://") && checkPrefix && prefix == "":
			r.fail(o, field+".params."+name, "%s: Team %q has no secrets.prefix; it may refer to no secret", what, o.Metadata.Labels["owner"])
		case strings.HasPrefix(v, "bao://") && checkPrefix && !strings.HasPrefix(v, prefix):
			r.fail(o, field+".params."+name, "%s: %s is not under Team %q's secrets (%s)", what, v, o.Metadata.Labels["owner"], prefix)
		}
	}
	for _, name := range sortedKeys(schema) {
		p := schema[name]
		if _, ok := given[name]; p.Required && p.Default == nil && !ok {
			r.fail(o, field, "%s requires param %q", what, name)
		}
	}
	return len(r.errs) == before
}

func nearest(name string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if d := distance(name, c); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// distance is the Levenshtein distance, for "did you mean" hints.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func sortErrors(errs Errors) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i], errs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
}
