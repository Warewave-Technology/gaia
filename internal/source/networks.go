package source

import (
	"context"
	"fmt"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/cel"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// networks runs a NetworkSource (design 5.2): the IPAM pools it selects
// become Network objects. A pool whose expressions fail, or whose Network
// would not be valid, is left out and reported.
func (s *Syncer) networks(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, o *catalog.Object) Report {
	rep := Report{Source: catalog.KindNetworkSrc + "/" + o.Metadata.Name}
	fail := func(err error) Report {
		rep.Status, rep.Error = "failed", err.Error()
		return rep
	}
	spec := o.Spec.(*catalog.NetworkSourceSpec)
	p, ok := g.Catalog.Get(catalog.KindProvider, spec.Provider)
	if !ok {
		return fail(fmt.Errorf("no Provider %q", spec.Provider))
	}
	ps := p.Spec.(*catalog.ProviderSpec)
	sec, err := s.secret(ctx, g, ps, spec.Provider)
	if err != nil {
		return fail(err)
	}
	pools, err := ipamclient.New(ps.Endpoint, sec["token"]).Pools(ctx)
	if err != nil {
		return fail(err)
	}
	sel, err := cel.SubnetRule(spec.Select)
	if err != nil {
		return fail(err)
	}
	naming, err := cel.SubnetString(spec.Naming)
	if err != nil {
		return fail(err)
	}
	maps := map[string]*cel.Program{}
	for field, src := range spec.Map.Fields() {
		if src == "" {
			continue
		}
		if maps[field], err = cel.SubnetValue(src); err != nil {
			return fail(err)
		}
	}

	want := map[string][]byte{}
	from := map[string]string{}
	for _, pool := range pools {
		subnet := map[string]any{"cidr": pool.CIDR, "gateway": pool.Gateway, "name": pool.Name, "rangeStart": pool.RangeStart, "rangeEnd": pool.RangeEnd}
		labels := map[string]any{}
		for k, v := range pool.Labels {
			labels[k] = v
			if _, taken := subnet[k]; !taken {
				subnet[k] = v
			}
		}
		tags := []any{}
		for _, t := range pool.Tags {
			tags = append(tags, t)
		}
		subnet["labels"], subnet["tags"] = labels, tags
		vars := map[string]any{"subnet": subnet, "name": ""}
		problem := func(format string, a ...any) {
			rep.Problems = append(rep.Problems, pool.CIDR+": "+fmt.Sprintf(format, a...))
		}
		ok, err := sel.EvalBool(vars)
		if err != nil {
			problem("select: %v", err)
			continue
		}
		if !ok {
			continue
		}
		name, err := naming.EvalString(vars)
		if err != nil {
			problem("naming: %v", err)
			continue
		}
		if prev, dup := from[name]; dup {
			problem("%s and %s are both named %s; neither is written", prev, pool.CIDR, name)
			delete(want, file(spec.Output, name))
			continue
		}
		from[name] = pool.CIDR
		vars["name"] = name

		fields := map[string]any{
			"ipam": map[string]any{"provider": spec.Provider, "pool": pool.CIDR},
		}
		if pool.Gateway != "" {
			fields["gateway"] = pool.Gateway
		}
		netLabels := map[string]string{}
		bad := false
		for field, prg := range maps {
			v, err := prg.EvalValue(vars)
			if err != nil {
				problem("map.%s: %v", field, err)
				bad = true
				continue
			}
			if field == "labels" {
				m, ok := v.(map[string]any)
				if !ok {
					problem("map.labels: want a map")
					bad = true
					continue
				}
				for k, x := range m {
					netLabels[k] = fmt.Sprint(x)
				}
				continue
			}
			fields[field] = v
		}
		if bad {
			continue
		}
		var node yaml.Node
		if err := node.Encode(fields); err != nil {
			problem("%v", err)
			continue
		}
		var net catalog.NetworkSpec
		if err := node.Decode(&net); err != nil {
			problem("not a Network: %v", err)
			continue
		}
		if msgs := catalog.Validate(&net); len(msgs) > 0 {
			problem("not a Network: %v", msgs)
			continue
		}
		if len(netLabels) == 0 {
			netLabels = nil
		}
		data, err := catalog.Render(catalog.KindNetwork, name, netLabels, &net, rep.Source, "cidr:"+pool.CIDR)
		if err != nil {
			return fail(err)
		}
		want[file(spec.Output, name)] = data
	}
	sort.Strings(rep.Problems)
	s.write(ctx, snap, &rep, spec.Output, want)
	return rep
}
