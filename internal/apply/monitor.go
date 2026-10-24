package apply

import (
	"context"
	"fmt"
	"sort"

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
