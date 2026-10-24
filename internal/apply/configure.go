package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
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
	repo, err := localRepo(src.URL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ce.Source, err)
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	commit, err := gitsrc.Resolve(repo, ref, src.Path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ce.Source, err)
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
	if c.Baseline == nil && len(c.Roles) == 0 {
		return nil
	}
	if r.cfg == nil {
		return errors.New("configure: no configuration setup")
	}
	site, err := sitePlay(c)
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

	cfg := r.cfg
	job := ansible.Job{
		Name: "gaia-" + r.run + "-" + c.Name, Image: cfg.image, Playbook: sitePlaybook,
		Hosts: hosts, User: cfg.user, SSHKey: cfg.sshKey, Forks: cfg.forks, Timeout: cfg.timeout,
		Project: func(dir string) error {
			if _, err := gitsrc.Export(cfg.repo, cfg.commit, cfg.dir, dir); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(dir, ".gaia"), 0o750); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, sitePlaybook), site, 0o600)
		},
	}
	r.printf("  configure %s (%d server(s))\n", c.Name, len(hosts))
	q := r.d.Store.Q()
	res, err := r.d.Configure.Run(ctx, job, func(e ansible.Event) error {
		r.seq++
		if e.Failed {
			r.printf("    %s: %s failed\n", names[e.Host], e.Task)
		}
		created := e.Created
		if created.IsZero() {
			created = time.Now()
		}
		return q.InsertRunEvent(ctx, db.InsertRunEventParams{
			RunID: r.run, Seq: int32(r.seq), Stage: "configure", HostUid: pgText(e.Host), Event: e.Type, //nolint:gosec // event counts stay far below 2^31
			Task: pgText(e.Task), Changed: e.Changed, Failed: e.Failed, Stdout: pgText(e.Stdout), Data: e.Data,
			CreatedAt: pgtype.Timestamptz{Time: created, Valid: true},
		})
	})
	if err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	for _, h := range hosts {
		st := res.Stats
		r.printf("    %s ok=%d changed=%d failed=%d unreachable=%d\n",
			names[h.Name], st.OK[h.Name], st.Changed[h.Name], st.Failures[h.Name], st.Unreachable[h.Name])
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
	return v
}

// sitePlay is the playbook of a contract: its baseline, then its roles
// with their params. JSON is YAML.
func sitePlay(c plan.Contract) ([]byte, error) {
	var plays []any
	if c.Baseline != nil {
		plays = append(plays, map[string]any{"import_playbook": "../" + c.Baseline.Playbook})
	}
	if len(c.Roles) > 0 {
		var roles []any
		for _, ro := range c.Roles {
			for k, v := range ro.Params {
				if v == plan.Masked {
					return nil, fmt.Errorf("role %s: param %s is no_log; secret params are not supported yet", ro.Name, k)
				}
			}
			roles = append(roles, map[string]any{"role": ro.Role, "vars": ro.Params})
		}
		plays = append(plays, map[string]any{"name": "Roles of " + c.Name, "hosts": "all", "become": true, "roles": roles})
	}
	return json.MarshalIndent(plays, "", "  ")
}
