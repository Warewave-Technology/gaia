package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.yaml.in/yaml/v3"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/cel"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// roles runs an AnsibleRoleSource (design 5.3): each role directory its
// paths match becomes an AnsibleRole. A role whose metadata does not
// read, or whose AnsibleRole would not be valid, is left out and
// reported.
func (s *Syncer) roles(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, o *catalog.Object) Report {
	rep := Report{Source: catalog.KindRoleSource + "/" + o.Metadata.Name}
	fail := func(err error) Report {
		rep.Status, rep.Error = "failed", err.Error()
		return rep
	}
	spec := o.Spec.(*catalog.AnsibleRoleSourceSpec)
	rev, err := s.roleRepo(ctx, snap, g, spec.Provider)
	if err != nil {
		return fail(err)
	}
	gr, err := gitsrc.Open(s.Repos.Path(rev.Repo()))
	if err != nil {
		return fail(err)
	}
	c, err := gr.CommitObject(plumbing.NewHash(rev.Commit))
	if err != nil {
		return fail(err)
	}
	root, err := c.Tree()
	if err != nil {
		return fail(err)
	}
	base := ""
	if len(rev.Paths) > 0 {
		base = strings.Trim(rev.Paths[0], "/")
		if root, err = root.Tree(base); err != nil {
			return fail(fmt.Errorf("%s: %s: %w", spec.Provider, base, err))
		}
	}
	naming, err := cel.RoleString(spec.Naming)
	if err != nil {
		return fail(err)
	}
	maps := map[string]*cel.Program{}
	for field, src := range map[string]string{"os": spec.Map.OS, "labels": spec.Map.Labels} {
		if src == "" {
			continue
		}
		if maps[field], err = cel.RoleValue(src); err != nil {
			return fail(err)
		}
	}

	want := map[string][]byte{}
	from := map[string]string{}
	for _, dir := range roleDirs(root, spec.Paths, spec.Exclude) {
		problem := func(format string, a ...any) {
			rep.Problems = append(rep.Problems, dir+": "+fmt.Sprintf(format, a...))
		}
		t, err := root.Tree(dir)
		if err != nil {
			problem("%v", err)
			continue
		}
		var meta map[string]any
		var specs struct {
			ArgumentSpecs map[string]struct {
				ShortDescription string                   `yaml:"short_description"`
				Options          map[string]catalog.Param `yaml:"options"`
			} `yaml:"argument_specs"`
		}
		var extra struct {
			Monitoring          []string                 `yaml:"monitoring"`
			Conflicts           []string                 `yaml:"conflicts"`
			AllowedEnvironments []string                 `yaml:"allowedEnvironments"`
			CheckMode           string                   `yaml:"checkMode"`
			Runbook             bool                     `yaml:"runbook"`
			Rules               []catalog.Rule           `yaml:"rules"`
			Params              map[string]catalog.Param `yaml:"params"`
		}
		bad := false
		for _, f := range []struct {
			name string
			into any
		}{{"meta/main.yml", &meta}, {"meta/argument_specs.yml", &specs}, {"meta/gaia.yml", &extra}} {
			if err := readYAML(t, f.name, f.into); err != nil {
				problem("%s: %v", f.name, err)
				bad = true
			}
		}
		if bad {
			continue
		}
		vars := map[string]any{"role": roleVars(path.Join(base, dir)), "meta": meta, "name": ""}
		if vars["meta"] == nil {
			vars["meta"] = map[string]any{}
		}
		name, err := naming.EvalString(vars)
		if err != nil {
			problem("naming: %v", err)
			continue
		}
		if prev, dup := from[name]; dup {
			problem("%s and %s are both named %s; neither is written", prev, dir, name)
			delete(want, file(spec.Output, name))
			continue
		}
		from[name] = dir
		vars["name"] = name

		role := &catalog.AnsibleRoleSpec{
			Source:     catalog.RoleSource{Provider: spec.Provider, Role: vars["role"].(map[string]string)["fqcn"]},
			Monitoring: extra.Monitoring, Conflicts: extra.Conflicts, AllowedEnvironments: extra.AllowedEnvironments,
			CheckMode: extra.CheckMode, Runbook: extra.Runbook, Rules: extra.Rules,
		}
		main, ok := specs.ArgumentSpecs["main"]
		if ok && len(main.Options) > 0 {
			role.Params = main.Options
		}
		// The catalog card's text: the entry point's short description, or
		// galaxy_info's.
		role.Description = main.ShortDescription
		if gi, ok := meta["galaxy_info"].(map[string]any); ok && role.Description == "" {
			role.Description, _ = gi["description"].(string)
		}
		// meta/gaia.yml extends params with Gaia's own fields.
		for p, x := range extra.Params {
			cur, ok := role.Params[p]
			if !ok {
				problem("meta/gaia.yml: params.%s is not in argument_specs", p)
				bad = true
				continue
			}
			cur.Gaia = x.Gaia
			role.Params[p] = cur
		}
		var labels map[string]string
		for field, prg := range maps {
			v, err := prg.EvalValue(vars)
			if err != nil {
				problem("map.%s: %v", field, err)
				bad = true
				continue
			}
			switch field {
			case "os":
				list, ok := v.([]any)
				if !ok {
					problem("map.os: want a list")
					bad = true
					continue
				}
				for _, x := range list {
					role.OS = append(role.OS, fmt.Sprint(x))
				}
			case "labels":
				m, ok := v.(map[string]any)
				if !ok {
					problem("map.labels: want a map")
					bad = true
					continue
				}
				labels = map[string]string{}
				for k, x := range m {
					labels[k] = fmt.Sprint(x)
				}
			}
		}
		if bad {
			continue
		}
		if msgs := catalog.Validate(role); len(msgs) > 0 {
			problem("not an AnsibleRole: %s", strings.Join(msgs, "; "))
			continue
		}
		data, err := catalog.Render(catalog.KindAnsibleRole, name, labels, role, rep.Source, "tree:"+t.Hash.String())
		if err != nil {
			return fail(err)
		}
		want[file(spec.Output, name)] = data
	}
	sort.Strings(rep.Problems)
	s.write(ctx, snap, &rep, spec.Output, want)
	return rep
}

// roleRepo is the repository roles are read from: the snapshot's config
// repository when the source names it, else the provider at its head.
func (s *Syncer) roleRepo(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, provider string) (repo.Rev, error) {
	if c := snap.Config; c != nil && c.Name == provider {
		return *c, s.Repos.Ensure(ctx, c.Repo(), c.Commit)
	}
	p, ok := g.Catalog.Get(catalog.KindProvider, provider)
	if !ok {
		return repo.Rev{}, fmt.Errorf("no Provider %q", provider)
	}
	ps := p.Spec.(*catalog.ProviderSpec)
	rev := repo.Rev{Name: provider, URL: ps.URL, Ref: ps.Ref, Credentials: ps.Credentials}
	if ps.Path != "" {
		rev.Paths = []string{ps.Path}
	}
	var err error
	rev.Commit, err = s.Repos.Sync(ctx, rev.Repo())
	return rev, err
}

// roleDirs are the directories of t that a pattern of paths matches and
// none of exclude does, sorted.
func roleDirs(t *object.Tree, paths, exclude []string) []string {
	dirs := map[string]bool{}
	_ = t.Files().ForEach(func(f *object.File) error {
		for d := path.Dir(f.Name); d != "." && d != "/"; d = path.Dir(d) {
			dirs[d] = true
		}
		return nil
	})
	var out []string
	for d := range dirs {
		if matchAny(paths, d, false) && !matchAny(exclude, d, true) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// matchAny reports whether a pattern matches p; with suffix, a pattern
// also matches p's last segments ("*/roles/_*" excludes such roles
// anywhere).
func matchAny(patterns []string, p string, suffix bool) bool {
	segs := strings.Split(p, "/")
	for _, pat := range patterns {
		pat = strings.Trim(pat, "/")
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
		if n := strings.Count(pat, "/") + 1; suffix && n < len(segs) {
			if ok, _ := path.Match(pat, strings.Join(segs[len(segs)-n:], "/")); ok {
				return true
			}
		}
	}
	return false
}

// roleVars describe a role directory: collection roles live at
// .../ansible_collections/<namespace>/<collection>/roles/<name>.
func roleVars(dir string) map[string]string {
	parts := strings.Split(dir, "/")
	v := map[string]string{"name": parts[len(parts)-1], "path": dir, "collection": "", "namespace": "", "fqcn": parts[len(parts)-1]}
	for i := 0; i+4 < len(parts); i++ {
		if parts[i] == "ansible_collections" && parts[i+3] == "roles" && i+4 == len(parts)-1 {
			v["namespace"], v["collection"] = parts[i+1], parts[i+2]
			v["fqcn"] = parts[i+1] + "." + parts[i+2] + "." + parts[i+4]
		}
	}
	return v
}

// readYAML reads a file of a tree into v; a missing file leaves v alone.
func readYAML(t *object.Tree, name string, v any) error {
	f, err := t.File(name)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := f.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, v)
}
