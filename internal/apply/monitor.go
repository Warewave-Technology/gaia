package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/provider/zabbix"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Monitor registers servers in a monitoring system.
type Monitor interface {
	Register(ctx context.Context, h zabbix.Host, hostID string) (zabbix.Result, error)
	Delete(ctx context.Context, uid, hostID string) (bool, error)
	SetEnabled(ctx context.Context, hostID string, enabled bool) error
	State(ctx context.Context, uid, hostID string) (*zabbix.HostState, error)
	GaiaHosts(ctx context.Context) (map[string]string, error)
	UserGroup(ctx context.Context, name string, rights map[string]int, usernames []string) ([]string, error)
	Maintenance(ctx context.Context, name string, hostIDs []string, until time.Time) (string, error)
	EndMaintenance(ctx context.Context, id string) error
}

// monitor returns the client of the contract's monitoring provider.
func (r *runner) monitor(ctx context.Context, provider string) (Monitor, error) {
	if m, ok := r.monitors[provider]; ok {
		return m, nil
	}
	p := r.provider(provider)
	f, err := r.d.Secrets.Read(ctx, p.Credentials)
	if err != nil {
		return nil, err
	}
	if f["token"] == "" {
		return nil, fmt.Errorf("%s: no token", p.Credentials)
	}
	m := r.d.Monitor(p.Endpoint, f["token"])
	r.executedAs[provider] = "api token"
	r.monitors[provider] = m
	return m, nil
}

// register puts the contract's servers in monitoring with their host
// groups, templates and tags, and records each host's ID.
func (r *runner) register(ctx context.Context, c plan.Contract, contract *catalog.Object, targets []target) error {
	if c.Monitoring == nil || len(targets) == 0 {
		return nil
	}
	m, err := r.monitor(ctx, c.Monitoring.Provider)
	if err != nil {
		return err
	}
	q := r.d.Store.Q()
	for _, t := range targets {
		hostID, err := r.externalID(ctx, t.s.UID, c.Monitoring.Provider, "host")
		if err != nil {
			return err
		}
		h := zabbix.Host{
			UID: t.s.UID, Hostname: t.s.Hostname, IP: t.address,
			Groups: t.s.HostGroups, Templates: c.Monitoring.Templates, Tags: monitorTags(c.Name, contract.Metadata.Labels),
		}
		if site, ok := r.g.Catalog.Get(catalog.KindSite, t.s.Site); ok {
			h.Proxy = site.Spec.(*catalog.SiteSpec).ZabbixProxy
		}
		res, err := m.Register(ctx, h, hostID)
		if err != nil {
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
		if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: t.s.UID, Provider: c.Monitoring.Provider, Kind: "host", ExternalID: res.HostID}); err != nil {
			return err
		}
		r.printf("  %s %s: %s (host %s)\n", c.Monitoring.Provider, t.s.Hostname, res.Action, res.HostID)
		r.change(runrecord.Change{Contract: c.Name, UID: t.s.UID, Hostname: t.s.Hostname, Stage: "monitoring", Action: res.Action, Detail: c.Monitoring.Provider + " host " + res.HostID})
	}
	return nil
}

// monitorTags are the contract and its labels; the UID tag is added by
// the client.
func monitorTags(contract string, labels map[string]string) []zabbix.Tag {
	tags := []zabbix.Tag{{Tag: "gaia.contract", Value: contract}}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tags = append(tags, zabbix.Tag{Tag: k, Value: labels[k]})
	}
	return tags
}

// externalID returns the recorded ID of uid in provider, or "".
func (r *runner) externalID(ctx context.Context, uid, provider, kind string) (string, error) {
	ids, err := r.d.Store.Q().ExternalIDs(ctx, uid)
	if err != nil {
		return "", err
	}
	for _, e := range ids {
		if e.Provider == provider && e.Kind == kind {
			return e.ExternalID, nil
		}
	}
	return "", nil
}

// monitorAccess gives the teams of a contract's access its host groups in
// monitoring (design 14.4): one user group per team, Gaia/<team>, with
// read rights, or read-write where the access has sudo, on the own host
// group of every contract the team has access to; as members, the
// team's people who have a user there. Teams the contract no longer
// names are updated too, so that they lose its host groups.
func (r *runner) monitorAccess(ctx context.Context, c plan.Contract) error {
	if c.Monitoring == nil {
		return nil
	}
	plans := map[string]plan.Contract{c.Name: c}
	teams := map[string]bool{}
	for _, a := range c.Access {
		for _, t := range a.Teams {
			teams[t] = true
		}
	}
	states, err := r.d.Store.Q().ContractStates(ctx)
	if err != nil {
		return err
	}
	for _, st := range states {
		var p plan.Plan
		if len(st.AppliedPlan) == 0 || json.Unmarshal(st.AppliedPlan, &p) != nil {
			continue
		}
		for _, x := range p.Contracts {
			if x.Name == c.Name {
				for _, a := range x.Access {
					for _, t := range a.Teams {
						teams[t] = true // named before: may lose it now
					}
				}
				continue
			}
			plans[x.Name] = x
		}
	}
	if len(teams) == 0 {
		return nil
	}
	m, err := r.monitor(ctx, c.Monitoring.Provider)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(teams))
	for t := range teams {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, team := range names {
		rights := map[string]int{}
		for _, x := range plans {
			if x.Monitoring == nil || x.Monitoring.Provider != c.Monitoring.Provider {
				continue
			}
			perm := 0
			for _, a := range x.Access {
				if slices.Contains(a.Teams, team) {
					p := zabbix.PermissionRead
					if a.Sudo {
						p = zabbix.PermissionReadWrite
					}
					perm = max(perm, p)
				}
			}
			if perm == 0 {
				continue
			}
			// Only the contract's own group: the others (environment,
			// site...) hold other contracts' servers too.
			if running(x) > 0 {
				hg := plan.ContractHostGroup(x.Name)
				rights[hg] = max(rights[hg], perm)
			}
		}
		rows, err := r.d.Store.Q().TeamLogins(ctx, []string{team})
		if err != nil {
			return err
		}
		var users []string
		for _, row := range rows {
			if !slices.Contains(users, row.Username) {
				users = append(users, row.Username)
			}
		}
		missing, err := m.UserGroup(ctx, zabbix.UserGroupPrefix+team, rights, users)
		if err != nil {
			return fmt.Errorf("monitoring access of %s: %w", team, err)
		}
		r.printf("  monitoring access %s%s: %d host group(s), %d user(s)\n", zabbix.UserGroupPrefix, team, len(rights), len(users)-len(missing))
		if len(missing) > 0 {
			r.printf("    no monitoring user for %s\n", strings.Join(missing, ", "))
		}
		r.change(runrecord.Change{Contract: c.Name, Stage: "access", Action: "monitoring-group", Detail: fmt.Sprintf("%s%s: %d host groups, %d users", zabbix.UserGroupPrefix, team, len(rights), len(users)-len(missing))})
	}
	return nil
}
