package catalog

import (
	"fmt"
	"sort"
	"strings"

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
	for _, o := range cat.List(KindServer) {
		r.server(o)
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
	if pols := r.g.Catalog.List(KindPolicy); len(pols) > 1 {
		for _, o := range pols[1:] {
			r.fail(o, "", "only one Policy is allowed (also %s)", pols[0].Source)
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
	r.ref(o, "spec.engines.provisioning.state", KindProvider, s.Engines.Provisioning.State, "s3")
	r.ref(o, "spec.engines.configuration.source", KindProvider, s.Engines.Configuration.Source, "git")
	if s.RunArchive != "" {
		r.ref(o, "spec.runArchive", KindProvider, s.RunArchive, "s3")
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
	case "vsphere":
		if c.Datacenter == "" || c.Cluster == "" {
			r.fail(o, "spec.compute", "datacenter and cluster are required for vSphere")
		}
		if len(c.Nodes) > 0 || c.Pool != "" {
			r.fail(o, "spec.compute", "nodes and pool are Proxmox fields")
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
		r.ref(o, "spec.dns.provider", KindProvider, s.DNS.Provider, "activedirectory")
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
	progs := r.g.rules[rr.Name]
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
	params := effectiveParams(rs.Params, rr.Params)
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
				r.fail(o, field, "AnsibleRole %q rule %d: %v", rr.Name, i, err)
			case !ok:
				r.fail(o, field, "AnsibleRole %q: %s", rr.Name, rs.Rules[i].Message)
			}
		}
	}
}

// roleParams checks that given params exist in the role's schema and that
// required ones without a default are given. Values are checked later,
// with the full params schema.
func (r *resolver) roleParams(o *Object, field string, rr RoleRef, rs *AnsibleRoleSpec) bool {
	before := len(r.errs)
	for _, name := range sortedKeys(rr.Params) {
		p, ok := rs.Params[name]
		if !ok {
			r.fail(o, field+".params", "AnsibleRole %q has no param %q%s", rr.Name, name, nearest(name, sortedKeys(rs.Params)))
			continue
		}
		for _, msg := range checkValue(name, p, rr.Params[name]) {
			r.fail(o, field+".params."+name, "AnsibleRole %q: %s", rr.Name, msg)
		}
	}
	for _, name := range sortedKeys(rs.Params) {
		p := rs.Params[name]
		if _, given := rr.Params[name]; p.Required && p.Default == nil && !given {
			r.fail(o, field, "AnsibleRole %q requires param %q", rr.Name, name)
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
