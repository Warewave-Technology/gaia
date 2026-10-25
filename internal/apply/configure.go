package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/apply/access"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Configurer runs Ansible jobs in an execution environment.
type Configurer interface {
	Run(ctx context.Context, j ansible.Job, on func(ansible.Event) error) (ansible.Result, error)
	// Image returns the image's reference pinned to its digest.
	Image(ctx context.Context, ref string) (string, error)
}

// sitePlaybook is the playbook Gaia writes into the project: the baseline,
// then the contract's roles.
const sitePlaybook = ".gaia/site.yml"

// config is what every configure job of a run shares.
type config struct {
	repo, dir, commit string // config repository, its subtree, the commit
	image             string // pinned to a digest
	user, sshKey      string
	forks             int
	timeout           time.Duration
}

// configSetup resolves the config repository's commit and the execution
// environment once per run, and records both on the run.
func (r *runner) configSetup(ctx context.Context, privateKey string) (*config, error) {
	ce := r.g.Platform().Spec.(*catalog.PlatformSpec).Engines.Configuration
	src := r.provider(ce.Source)
	var repo, commit string
	if pinned := r.d.Config; pinned != nil {
		repo, commit = pinned.Path, pinned.Commit
		if _, err := gitsrc.Resolve(repo, commit, src.Path); err != nil {
			return nil, fmt.Errorf("%s: %w", ce.Source, err)
		}
	} else {
		var err error
		if repo, err = localRepo(src.URL); err != nil {
			return nil, fmt.Errorf("%s: %w", ce.Source, err)
		}
		ref := src.Ref
		if ref == "" {
			ref = "HEAD"
		}
		if commit, err = gitsrc.Resolve(repo, ref, src.Path); err != nil {
			return nil, fmt.Errorf("%s: %w", ce.Source, err)
		}
	}
	image, err := r.d.Configure.Image(ctx, ce.ExecutionEnvironment)
	if err != nil {
		return nil, fmt.Errorf("execution environment: %w", err)
	}
	c := &config{repo: repo, dir: src.Path, commit: commit, image: image, user: User, sshKey: privateKey, forks: ce.Limits.Forks}
	if ce.Limits.Timeout != "" {
		if c.timeout, err = time.ParseDuration(ce.Limits.Timeout); err != nil {
			return nil, err
		}
	}
	if err := r.d.Store.Q().SetRunConfiguration(ctx, db.SetRunConfigurationParams{ID: r.run, ConfigCommit: pgText(commit), EeImage: pgText(image)}); err != nil {
		return nil, err
	}
	r.printf("config %s at %s, ee %s\n", ce.Source, commit[:12], image)
	return c, nil
}

// ConfigRepo is the config repository's local copy and the commit a run
// uses.
type ConfigRepo struct {
	Path, Commit string
	Paths        []string
}

// localRepo is the path of a config repository Gaia can read. The PoC
// reads a local clone; fetching a remote one comes with the platform
// service.
func localRepo(u string) (string, error) {
	p := strings.TrimPrefix(u, "file://")
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("url %q: the PoC reads the config repository from a local path", u)
	}
	return p, nil
}

// target is a server to configure.
type target struct {
	s       plan.Server
	address string // without prefix
}

// configure runs the contract's baseline and roles on its servers.
func (r *runner) configure(ctx context.Context, c plan.Contract, contract *catalog.Object, targets []target) error {
	if !r.configures(ctx, c) || len(targets) == 0 {
		return nil
	}
	if r.cfg == nil {
		return errors.New("configure: no configuration setup")
	}
	var users []access.User
	if r.managesAccess(ctx, c) {
		var err error
		if users, err = r.accessUsers(ctx, c); err != nil {
			return err
		}
		wg, err := r.bastionAccounts(ctx, c)
		if err != nil {
			return err
		}
		users = append(users, wg...)
	}
	resolved, secrets, err := r.resolveSecrets(ctx, c)
	if err != nil {
		return err
	}
	if err := r.checkArtifacts(ctx, &resolved); err != nil {
		return err
	}
	for _, t := range targets {
		if j, ok := r.joins[t.s.UID]; ok && j.Password != "" {
			secrets = append(secrets, j.Password)
		}
	}
	site, err := sitePlay(resolved, users, r.managesAccess(ctx, c), hasDomain(c))
	if err != nil {
		return err
	}
	names := map[string]string{} // UID -> hostname, for output
	var hosts []ansible.Host
	for _, t := range targets {
		names[t.s.UID] = t.s.Hostname
		hosts = append(hosts, ansible.Host{Name: t.s.UID, Address: t.address, Vars: r.hostVars(c, contract, t.s)})
	}
	sort.Slice(hosts, func(i, j int) bool { return names[hosts[i].Name] < names[hosts[j].Name] })
	addrs := map[string]string{}
	for _, t := range targets {
		addrs[t.s.UID] = t.address
	}
	known, err := r.knownHosts(ctx, addrs)
	if err != nil {
		return err
	}

	cfg := r.cfg
	job := ansible.Job{
		Name: "gaia-" + r.run + "-" + c.Name, Image: cfg.image, Playbook: sitePlaybook,
		Hosts: hosts, User: cfg.user, SSHKey: cfg.sshKey, Forks: cfg.forks, Timeout: cfg.timeout, Secrets: secrets, KnownHosts: known,
		Project: func(dir string) error {
			if _, err := gitsrc.Export(cfg.repo, cfg.commit, cfg.dir, dir); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(dir, ".gaia"), 0o750); err != nil {
				return err
			}
			if err := access.WriteRoles(filepath.Join(dir, ".gaia")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, sitePlaybook), site, 0o600)
		},
	}
	r.printf("  configure %s (%d server(s))\n", c.Name, len(hosts))
	res, err := r.d.Configure.Run(ctx, job, func(e ansible.Event) error {
		if e.Failed {
			r.printf("    %s: %s failed\n", names[e.Host], e.Task)
		}
		r.events.add(Event{Stage: "configure", Type: e.Type, Host: e.Host, Task: e.Task, Changed: e.Changed, Failed: e.Failed, Stdout: e.Stdout, Data: e.Data, Created: e.Created})
		return nil
	})
	if err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	for _, h := range hosts {
		st := res.Stats
		detail := fmt.Sprintf("ok=%d changed=%d failed=%d unreachable=%d", st.OK[h.Name], st.Changed[h.Name], st.Failures[h.Name], st.Unreachable[h.Name])
		r.printf("    %s %s\n", names[h.Name], detail)
		r.change(runrecord.Change{Contract: c.Name, UID: h.Name, Hostname: names[h.Name], Stage: "configure", Action: "ansible", Detail: detail})
	}
	if failed := res.Failed(); len(failed) > 0 {
		var hs []string
		for _, uid := range failed {
			hs = append(hs, names[uid])
		}
		sort.Strings(hs)
		return fmt.Errorf("configure failed on %s", strings.Join(hs, ", "))
	}
	if res.RC != 0 {
		return fmt.Errorf("configure: ansible-runner exited %d", res.RC)
	}
	return nil
}

// checkArtifacts finds each App's artifact in its repository before any
// server is touched, and keeps its checksum for the playbook. Nexus is
// read with the provider's credentials when it has them (username,
// password), anonymously when not.
func (r *runner) checkArtifacts(ctx context.Context, c *plan.Contract) error {
	if r.d.Artifact == nil {
		return nil
	}
	apps := append([]plan.App(nil), c.Apps...)
	for i, a := range apps {
		src := a.Source
		if src == nil {
			continue
		}
		p := r.provider(src.Provider)
		var f map[string]string
		if p.Credentials != "" {
			var err error
			if f, err = r.d.Secrets.Read(ctx, p.Credentials); err != nil {
				return err
			}
			r.executedAs[src.Provider] = f["username"]
		}
		asset, err := r.d.Artifact(ctx, p.Endpoint, f["username"], f["password"], src.Repository, src.Path)
		if err != nil {
			return fmt.Errorf("app %s: %w", a.Name, err)
		}
		apps[i].SHA256 = asset.SHA256
		sum := asset.SHA256
		if sum == "" {
			sum = "no checksum"
		}
		r.printf("  app %s: %s/%s (%s)\n", a.Name, src.Repository, src.Path, sum)
		r.change(runrecord.Change{Contract: c.Name, Stage: "configure", Action: "artifact", Detail: fmt.Sprintf("%s %s %s/%s sha256 %s", a.Name, src.Provider, src.Repository, src.Path, sum)})
	}
	c.Apps = apps
	return nil
}

// hostVars are the variables Gaia gives every server (ansible/README.md).
func (r *runner) hostVars(c plan.Contract, contract *catalog.Object, s plan.Server) map[string]any {
	v := map[string]any{
		"gaia_uid": s.UID, "gaia_hostname": s.Hostname, "gaia_contract": c.Name, "gaia_site": s.Site,
		"gaia_labels": contract.Metadata.Labels, "gaia_env": c.Env, "gaia_ntp": []string{}, "gaia_dns": []string{},
	}
	if site, ok := r.g.Catalog.Get(catalog.KindSite, s.Site); ok {
		if ntp := site.Spec.(*catalog.SiteSpec).NTP; len(ntp) > 0 {
			v["gaia_ntp"] = ntp
		}
	}
	if net, ok := r.g.Catalog.Get(catalog.KindNetwork, s.Network); ok {
		if dns := net.Spec.(*catalog.NetworkSpec).DNS; dns != nil && len(dns.Servers) > 0 {
			v["gaia_dns"] = dns.Servers
		}
	}
	if j, ok := r.joins[s.UID]; ok {
		v["gaia_domain_join"] = j
	}
	return v
}

// configures reports whether a contract's servers are configured: a
// baseline, agents, roles, apps, a domain to join, or access to
// distribute or take back.
func (r *runner) configures(ctx context.Context, c plan.Contract) bool {
	return c.Baseline != nil || len(c.Roles) > 0 || len(c.Agents) > 0 || len(c.Apps) > 0 || hasDomain(c) || r.managesAccess(ctx, c)
}

// given are the params passed to Ansible: those masked in the plan (a
// no_log default the role has itself) are left to the role.
func given(params map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range params {
		if v != plan.Masked {
			out[k] = v
		}
	}
	return out
}

// resolveSecrets replaces bao:// references in the contract's params with
// their values, for this run only, and returns the values so that they
// are masked in its events (design 6.2). A reference is
// bao://<path>#<field>; without a field, the secret's "value".
func (r *runner) resolveSecrets(ctx context.Context, c plan.Contract) (plan.Contract, []string, error) {
	var secrets []string
	cache := map[string]map[string]string{}
	resolve := func(params map[string]any) (map[string]any, error) {
		if len(params) == 0 {
			return params, nil
		}
		out := make(map[string]any, len(params))
		for k, v := range params {
			ref, ok := v.(string)
			if !ok || !strings.HasPrefix(ref, "bao://") {
				out[k] = v
				continue
			}
			path, field, _ := strings.Cut(ref, "#")
			if field == "" {
				field = "value"
			}
			sec, ok := cache[path]
			if !ok {
				var err error
				if sec, err = r.d.Secrets.Read(ctx, path); err != nil {
					return nil, fmt.Errorf("param %s: %w", k, err)
				}
				cache[path] = sec
			}
			val, ok := sec[field]
			if !ok {
				return nil, fmt.Errorf("param %s: %s has no field %q", k, path, field)
			}
			out[k] = val
			secrets = append(secrets, val)
		}
		return out, nil
	}
	out := c
	var err error
	out.Roles = append([]plan.Role(nil), c.Roles...)
	for i := range out.Roles {
		if out.Roles[i].Params, err = resolve(out.Roles[i].Params); err != nil {
			return c, nil, fmt.Errorf("role %s: %w", out.Roles[i].Name, err)
		}
	}
	out.Agents = append([]plan.Agent(nil), c.Agents...)
	for i := range out.Agents {
		if out.Agents[i].Params, err = resolve(out.Agents[i].Params); err != nil {
			return c, nil, fmt.Errorf("agent %s: %w", out.Agents[i].Name, err)
		}
	}
	out.Apps = append([]plan.App(nil), c.Apps...)
	for i := range out.Apps {
		if out.Apps[i].Params, err = resolve(out.Apps[i].Params); err != nil {
			return c, nil, fmt.Errorf("app %s: %w", out.Apps[i].Name, err)
		}
	}
	return out, secrets, nil
}

// managesAccess reports whether Gaia manages the contract's accounts: it
// has access entries, or had them when last applied (to take them back).
func (r *runner) managesAccess(ctx context.Context, c plan.Contract) bool {
	if len(c.Access) > 0 {
		return true
	}
	st, err := r.d.Store.Q().ContractState(ctx, c.Name)
	if err != nil || len(st.AppliedPlan) == 0 {
		return false
	}
	var ap plan.Plan
	if json.Unmarshal(st.AppliedPlan, &ap) != nil {
		return false
	}
	for _, x := range ap.Contracts {
		if x.Name == c.Name && len(x.Access) > 0 {
			return true
		}
	}
	return false
}

// linuxName is what a Linux account name may be (useradd's default rule).
var linuxName = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

// accessUsers are the accounts a contract's access gives: the enabled
// members of its teams now, with their login keys, sudo when any entry
// that names them says so. The run record keeps who and why.
func (r *runner) accessUsers(ctx context.Context, c plan.Contract) ([]access.User, error) {
	byName := map[string]*access.User{}
	var order []string
	for _, a := range c.Access {
		rows, err := r.d.Store.Q().TeamLogins(ctx, a.Teams)
		if err != nil {
			return nil, err
		}
		grant := runrecord.AccessGrant{Contract: c.Name, Via: a.Via, Teams: a.Teams, Users: []string{}, Sudo: a.Sudo}
		for _, row := range rows {
			if !linuxName.MatchString(row.Username) {
				r.printf("  access: %s is not a Linux account name; skipped\n", row.Username)
				continue
			}
			u, ok := byName[row.Username]
			if !ok {
				u = &access.User{Name: row.Username, Keys: []string{}}
				byName[row.Username] = u
				order = append(order, row.Username)
			}
			u.Sudo = u.Sudo || a.Sudo
			if row.PublicKey != "" && !slices.Contains(u.Keys, row.PublicKey) {
				u.Keys = append(u.Keys, row.PublicKey)
			}
			if !slices.Contains(grant.Users, row.Username) {
				grant.Users = append(grant.Users, row.Username)
			}
		}
		r.access = append(r.access, grant)
	}
	sort.Strings(order)
	out := make([]access.User, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// sitePlay is the playbook of a contract: joining the domain, its
// baseline, its roles with their params, then the access Gaia manages.
// JSON is YAML.
func sitePlay(c plan.Contract, users []access.User, manageAccess, joins bool) ([]byte, error) {
	var plays []any
	if joins {
		plays = append(plays, map[string]any{"name": "Domain of " + c.Name, "hosts": "all", "become": true,
			"roles": []any{map[string]any{"role": access.JoinRole, "when": "gaia_domain_join is defined"}}})
	}
	if c.Baseline != nil {
		plays = append(plays, map[string]any{"import_playbook": "../" + c.Baseline.Playbook})
	}
	if len(c.Agents) > 0 {
		var roles []any
		for _, a := range c.Agents {
			roles = append(roles, map[string]any{"role": a.Role, "vars": given(a.Params)})
		}
		plays = append(plays, map[string]any{"name": "Agents of " + c.Name, "hosts": "all", "become": true, "roles": roles})
	}
	if len(c.Roles) > 0 {
		var roles []any
		for _, ro := range c.Roles {
			roles = append(roles, map[string]any{"role": ro.Role, "vars": given(ro.Params)})
		}
		plays = append(plays, map[string]any{"name": "Roles of " + c.Name, "hosts": "all", "become": true, "roles": roles})
	}
	for _, a := range c.Apps {
		vars := given(a.Params)
		vars["gaia_app"] = map[string]any{"name": a.Name, "artifact": a.Artifact, "sha256": a.SHA256}
		plays = append(plays, map[string]any{"import_playbook": "../" + a.Playbook, "vars": vars})
	}
	if manageAccess {
		if users == nil {
			users = []access.User{}
		}
		plays = append(plays, map[string]any{"name": "Access to " + c.Name, "hosts": "all", "become": true,
			"roles": []any{map[string]any{"role": access.Role, "vars": map[string]any{"gaia_access_group": access.Group, "gaia_access_users": users}}}})
	}
	return json.MarshalIndent(plays, "", "  ")
}
