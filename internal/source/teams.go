package source

import (
	"context"
	"fmt"
	"sort"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/cel"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// teams runs a TeamSource (design 14.1): the AD groups it selects become
// Team objects. Members are not written; they are synced separately.
func (s *Syncer) teams(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, o *catalog.Object) Report {
	rep := Report{Source: catalog.KindTeamSource + "/" + o.Metadata.Name}
	fail := func(err error) Report {
		rep.Status, rep.Error = "failed", err.Error()
		return rep
	}
	spec := o.Spec.(*catalog.TeamSourceSpec)
	dir, err := s.directory(ctx, g, spec.Provider)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = dir.Close() }()
	groups, err := dir.Groups()
	if err != nil {
		return fail(err)
	}
	sel, err := cel.GroupRule(spec.Select)
	if err != nil {
		return fail(err)
	}
	naming, err := cel.GroupString(spec.Naming)
	if err != nil {
		return fail(err)
	}
	maps := map[string]*cel.Program{}
	for k, src := range map[string]string{"displayName": spec.Map.DisplayName, "approvers": spec.Map.Approvers, "secretsPrefix": spec.Map.SecretsPrefix} {
		if src == "" {
			continue
		}
		if maps[k], err = cel.GroupString(src); err != nil {
			return fail(err)
		}
	}
	byCN := map[string]ad.Group{}
	for _, gr := range groups {
		byCN[gr.CN] = gr
	}

	want := map[string][]byte{}
	from := map[string]string{} // team → group, to find duplicates
	for _, gr := range groups {
		vars := map[string]any{"group": map[string]string{"dn": gr.DN, "cn": gr.CN, "description": gr.Description, "guid": gr.GUID}, "name": ""}
		ok, err := sel.EvalBool(vars)
		if err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s: select: %v", gr.DN, err))
			continue
		}
		if !ok {
			continue
		}
		name, err := naming.EvalString(vars)
		if err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s: naming: %v", gr.DN, err))
			continue
		}
		if prev, dup := from[name]; dup {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s and %s are both named %s; neither is written", prev, gr.DN, name))
			delete(want, file(spec.Output, name))
			continue
		}
		from[name] = gr.DN
		vars["name"] = name
		team := &catalog.TeamSpec{ADGroup: &catalog.ADGroup{Provider: spec.Provider, DN: gr.DN}}
		if p := maps["displayName"]; p != nil {
			if team.DisplayName, err = p.EvalString(vars); err != nil {
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s: map.displayName: %v", gr.DN, err))
			}
		}
		if p := maps["approvers"]; p != nil {
			cn, err := p.EvalString(vars)
			switch approvers, found := byCN[cn]; {
			case err != nil:
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s: map.approvers: %v", gr.DN, err))
			case !found:
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s: no approvers group %q; its members approve", name, cn))
			default:
				team.ApproversGroup = &catalog.ADGroup{DN: approvers.DN}
			}
		}
		if p := maps["secretsPrefix"]; p != nil {
			prefix, err := p.EvalString(vars)
			if err != nil {
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s: map.secretsPrefix: %v", gr.DN, err))
			} else {
				team.Secrets = &catalog.TeamSecrets{Prefix: prefix}
			}
		}
		data, err := catalog.Render(catalog.KindTeam, name, nil, team, rep.Source, "guid:"+gr.GUID)
		if err != nil {
			return fail(err)
		}
		want[file(spec.Output, name)] = data
	}
	sort.Strings(rep.Problems)
	s.write(ctx, snap, &rep, spec.Output, want)
	return rep
}

// directory opens the activedirectory provider name, with credentials
// from the Platform's OpenBao.
func (s *Syncer) directory(ctx context.Context, g *catalog.Graph, name string) (*ad.Directory, error) {
	p, ok := g.Catalog.Get(catalog.KindProvider, name)
	if !ok {
		return nil, fmt.Errorf("no Provider %q", name)
	}
	ps := p.Spec.(*catalog.ProviderSpec)
	sec, err := s.secret(ctx, g, ps, name)
	if err != nil {
		return nil, err
	}
	return ad.Open(ctx, ad.ConfigFrom(ps.Endpoint, ps.BaseDN, sec))
}
