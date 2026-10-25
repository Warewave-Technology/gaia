package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/compute"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// OperationResult is how an Operation went on one server.
type OperationResult struct {
	UID      string `json:"uid"`
	Hostname string `json:"hostname"`
	Contract string `json:"contract"`
	Batch    int    `json:"batch"`
	Result   string `json:"result"` // ok | failed | skipped | not run
	Detail   string `json:"detail,omitempty"`
}

// OperationOutcome is how an Operation went: succeeded when every server
// did, partial when some did, failed when none did or it stopped at its
// failure threshold before doing anything.
type OperationOutcome struct {
	Status  string            `json:"status"`
	Results []OperationResult `json:"results"`
}

// RunOperation runs an approved Operation (design 6.4): batch after batch,
// each holding the locks of its contracts, the action and a health check
// on every server. Servers gone since approval are skipped; once more
// than maxFailures fail, the remaining batches are not run.
func RunOperation(ctx context.Context, g *catalog.Graph, op *plan.Operation, d Deps) (string, OperationOutcome, error) {
	var out OperationOutcome
	contracts := make([]plan.Contract, 0, len(op.Contracts))
	for _, c := range op.Contracts {
		contracts = append(contracts, plan.Contract{Name: c})
	}
	p := &plan.Plan{Commit: g.Catalog.Commit, Contracts: contracts, Hash: op.Hash}
	run, err := run(ctx, g, p, d, func(ctx context.Context, r *runner) error {
		var err error
		out, err = r.operation(ctx, op)
		return err
	})
	return run, out, err
}

func (r *runner) operation(ctx context.Context, op *plan.Operation) (OperationOutcome, error) {
	out := OperationOutcome{Results: []OperationResult{}}
	byUID := map[string]plan.Target{}
	for _, t := range op.Targets {
		byUID[t.UID] = t
	}
	_, privateKey, err := r.sshKeys(ctx)
	if err != nil {
		return out, err
	}
	if r.cfg, err = r.configSetup(ctx, privateKey); err != nil {
		return out, err
	}
	failures, done := 0, 0
	for i, batch := range op.Batches {
		if failures > op.MaxFail {
			for _, uid := range batch {
				t := byUID[uid]
				out.Results = append(out.Results, OperationResult{UID: uid, Hostname: t.Hostname, Contract: t.Contract, Batch: i + 1, Result: "not run", Detail: "stopped: too many failures"})
			}
			continue
		}
		if i > 0 && op.Pause != "" {
			if d, err := time.ParseDuration(op.Pause); err == nil {
				r.printf("pause %s\n", d)
				select {
				case <-ctx.Done():
					return out, ctx.Err()
				case <-time.After(d):
				}
			}
		}
		res, err := r.batch(ctx, op, i+1, batch, byUID)
		if err != nil {
			return out, err
		}
		for _, x := range res {
			if x.Result == "failed" {
				failures++
			}
			if x.Result == "ok" {
				done++
			}
		}
		out.Results = append(out.Results, res...)
	}
	switch {
	case failures == 0:
		out.Status = "succeeded"
	case done > 0:
		out.Status = "partial"
	default:
		out.Status = "failed"
	}
	if failures > 0 {
		return out, fmt.Errorf("%d server(s) failed", failures)
	}
	return out, nil
}

// batch runs one batch, holding the locks of its contracts.
func (r *runner) batch(ctx context.Context, op *plan.Operation, n int, uids []string, byUID map[string]plan.Target) ([]OperationResult, error) {
	var res []OperationResult
	var contracts []string
	for _, uid := range uids {
		if c := byUID[uid].Contract; !slices.Contains(contracts, c) {
			contracts = append(contracts, c)
		}
	}
	sort.Strings(contracts)
	var run func(ctx context.Context, i int) error
	run = func(ctx context.Context, i int) error {
		if i < len(contracts) {
			return r.d.Store.WithContractLock(ctx, contracts[i], func(ctx context.Context) error { return run(ctx, i+1) })
		}
		var err error
		res, err = r.batchLocked(ctx, op, n, uids, byUID)
		return err
	}
	// A contract busy with a run makes the batch wait for it.
	for {
		err := run(ctx, 0)
		if !errors.Is(err, store.ErrLocked) {
			return res, err
		}
		r.printf("batch %d waits: %v\n", n, err)
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func (r *runner) batchLocked(ctx context.Context, op *plan.Operation, n int, uids []string, byUID map[string]plan.Target) ([]OperationResult, error) {
	q := r.d.Store.Q()
	var res []OperationResult
	var hosts []ansible.Host
	names := map[string]int{} // UID → index in res
	for _, uid := range uids {
		t := byUID[uid]
		x := OperationResult{UID: uid, Hostname: t.Hostname, Contract: t.Contract, Batch: n}
		h, err := q.Host(ctx, uid)
		if err != nil || h.Status != "active" {
			x.Result, x.Detail = "skipped", "no longer an active server"
			res = append(res, x)
			continue
		}
		ip := ""
		ids, err := q.ExternalIDs(ctx, uid)
		if err != nil {
			return nil, err
		}
		for _, e := range ids {
			if e.Kind == "ip" {
				ip = e.ExternalID
			}
		}
		if ip == "" {
			x.Result, x.Detail = "skipped", "no address recorded"
			res = append(res, x)
			continue
		}
		names[uid] = len(res)
		res = append(res, x)
		hosts = append(hosts, ansible.Host{Name: uid, Address: ip, Vars: map[string]any{"gaia_uid": uid, "gaia_hostname": t.Hostname, "gaia_contract": t.Contract}})
	}
	if len(hosts) == 0 {
		return res, nil
	}
	fail := func(uid, detail string) {
		i := names[uid]
		res[i].Result, res[i].Detail = "failed", detail
		hosts = slices.DeleteFunc(hosts, func(h ansible.Host) bool { return h.Name == uid })
		r.printf("  %s failed: %s\n", res[i].Hostname, detail)
		r.change(runrecord.Change{Contract: res[i].Contract, UID: uid, Hostname: res[i].Hostname, Stage: "operation", Action: op.Action, Detail: "failed " + detail})
	}
	// Maintenance in monitoring for the batch, ended whatever happens.
	ends, err := r.maintenance(ctx, op, n, hosts)
	if err != nil {
		for _, h := range append([]ansible.Host(nil), hosts...) {
			fail(h.Name, "maintenance: "+err.Error())
		}
		return res, nil
	}
	defer func() {
		for _, end := range ends {
			end()
		}
	}()
	if slices.Contains(op.Steps, "snapshot") {
		for _, h := range append([]ansible.Host(nil), hosts...) {
			if err := r.snapshot(ctx, op, h.Name, byUID[h.Name].Hostname); err != nil {
				fail(h.Name, "snapshot: "+err.Error())
			}
		}
	}
	if op.Action == "power" || op.Action == "revert" {
		for _, h := range append([]ansible.Host(nil), hosts...) {
			if err := r.hypervisor(ctx, op, h.Name, byUID[h.Name].Hostname); err != nil {
				fail(h.Name, op.Action+": "+err.Error())
			}
		}
	}
	if op.Action == "snapshot" || op.Action == "power" && op.Options["state"] == "off" {
		for _, h := range hosts {
			i := names[h.Name]
			res[i].Result, res[i].Detail = "ok", op.Action+" done"
			r.change(runrecord.Change{Contract: res[i].Contract, UID: h.Name, Hostname: res[i].Hostname, Stage: "operation", Action: op.Action, Detail: "ok"})
		}
		return res, nil
	}
	if len(hosts) == 0 {
		return res, nil
	}
	play, err := operationPlay(op)
	if err != nil {
		return nil, err
	}
	addrs := map[string]string{}
	for _, h := range hosts {
		addrs[h.Name] = h.Address
	}
	known, err := r.knownHosts(ctx, addrs)
	if err != nil {
		return nil, err
	}
	cfg := r.cfg
	job := ansible.Job{
		Name: fmt.Sprintf("gaia-%s-%s-%d", r.run, op.Name, n), Image: cfg.image, Playbook: sitePlaybook,
		Hosts: hosts, User: cfg.user, SSHKey: cfg.sshKey, Forks: cfg.forks, Timeout: cfg.timeout, KnownHosts: known,
		Project: func(dir string) error {
			if _, err := gitsrc.Export(cfg.repo, cfg.commit, cfg.dir, dir); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(dir, ".gaia"), 0o750); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, sitePlaybook), play, 0o600)
		},
	}
	r.printf("batch %d: %s on %d server(s)\n", n, op.Action, len(hosts))
	result, err := r.d.Configure.Run(ctx, job, func(e ansible.Event) error {
		r.events.add(Event{Stage: "operation", Type: e.Type, Host: e.Host, Task: e.Task, Changed: e.Changed, Failed: e.Failed, Stdout: e.Stdout, Data: e.Data, Created: e.Created})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("batch %d: %w", n, err)
	}
	st := result.Stats
	for _, h := range hosts {
		uid, i := h.Name, names[h.Name]
		x := &res[i]
		x.Detail = fmt.Sprintf("ok=%d changed=%d failed=%d unreachable=%d", st.OK[uid], st.Changed[uid], st.Failures[uid], st.Unreachable[uid])
		x.Result = "ok"
		if st.Failures[uid] > 0 || st.Unreachable[uid] > 0 {
			x.Result = "failed"
		}
		r.printf("  %s %s %s\n", x.Hostname, x.Result, x.Detail)
		r.change(runrecord.Change{Contract: x.Contract, UID: uid, Hostname: x.Hostname, Stage: "operation", Action: op.Action, Detail: x.Result + " " + x.Detail})
	}
	return res, nil
}

// maintenance puts the batch's servers in maintenance in their
// monitoring, for the rest of the window (two hours without one), and
// returns how to end it.
func (r *runner) maintenance(ctx context.Context, op *plan.Operation, n int, hosts []ansible.Host) ([]func(), error) {
	byProvider := map[string][]string{}
	for _, h := range hosts {
		ids, err := r.d.Store.Q().ExternalIDs(ctx, h.Name)
		if err != nil {
			return nil, err
		}
		for _, e := range ids {
			if e.Kind == "host" {
				byProvider[e.Provider] = append(byProvider[e.Provider], e.ExternalID)
			}
		}
	}
	until := time.Now().Add(2 * time.Hour)
	if op.Window != nil {
		until = op.Window.End
	}
	var ends []func()
	for _, p := range slices.Sorted(maps.Keys(byProvider)) {
		m, err := r.monitor(ctx, p)
		if err != nil {
			return ends, err
		}
		id, err := m.Maintenance(ctx, fmt.Sprintf("Gaia: %s, batch %d", op.Name, n), byProvider[p], until)
		if err != nil {
			return ends, err
		}
		r.printf("  maintenance %s (%d server(s))\n", p, len(byProvider[p]))
		ends = append(ends, func() {
			if err := m.EndMaintenance(context.WithoutCancel(ctx), id); err != nil {
				r.printf("  ending maintenance %s: %v\n", id, err)
			}
		})
	}
	return ends, nil
}

// dayTwo returns the day-2 API of a server's VM and the VM's ID there.
func (r *runner) dayTwo(ctx context.Context, uid string) (compute.Compute, string, error) {
	ids, err := r.d.Store.Q().ExternalIDs(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	for _, e := range ids {
		if e.Kind != "vm" {
			continue
		}
		p := r.provider(e.Provider)
		sec, err := r.d.Secrets.Read(ctx, p.Credentials)
		if err != nil {
			return nil, "", err
		}
		if r.d.Compute == nil {
			return nil, "", errors.New("no day-2 API")
		}
		c, err := r.d.Compute(ctx, p.Type, p.Endpoint, sec)
		if err != nil {
			return nil, "", err
		}
		r.executedAs[e.Provider] = sec["token_id"] + sec["username"]
		return c, e.ExternalID, nil
	}
	return nil, "", errors.New("no VM recorded for it")
}

// snapshot takes a snapshot of a server's VM, named after the Operation.
func (r *runner) snapshot(ctx context.Context, op *plan.Operation, uid, hostname string) error {
	c, id, err := r.dayTwo(ctx, uid)
	if err != nil {
		return err
	}
	name := proxmox.SnapshotName("gaia_" + op.Name + "_" + time.Now().UTC().Format("20060102T1504"))
	if err := c.Snapshot(ctx, id, name, "Gaia: Operation "+op.Name+" ("+r.run+")"); err != nil {
		return err
	}
	r.printf("  %s snapshot %s\n", hostname, name)
	return nil
}

// hypervisor runs a power or revert action on a server's VM.
func (r *runner) hypervisor(ctx context.Context, op *plan.Operation, uid, hostname string) error {
	c, id, err := r.dayTwo(ctx, uid)
	if err != nil {
		return err
	}
	switch op.Action {
	case "power":
		state, _ := op.Options["state"].(string)
		r.printf("  %s power %s\n", hostname, state)
		return c.Power(ctx, id, state)
	case "revert":
		name, _ := op.Options["snapshot"].(string)
		r.printf("  %s revert to %s\n", hostname, name)
		return c.Revert(ctx, id, name)
	}
	return fmt.Errorf("%s is not a hypervisor action", op.Action)
}

// operationPlay is the playbook of an Operation's action, followed by a
// health check: the server answers again.
func operationPlay(op *plan.Operation) ([]byte, error) {
	var plays []any
	switch op.Action {
	case "reboot":
		plays = append(plays, map[string]any{"name": "Reboot", "hosts": "all", "become": true, "gather_facts": false,
			"tasks": []any{map[string]any{"name": "Reboot", "ansible.builtin.reboot": map[string]any{"reboot_timeout": 900}}}})
	case "patch":
		plays = append(plays, patchPlay(op.Options))
	case "power", "revert":
		// Done through the hypervisor; the server is checked below.
	case "runbook":
		if op.Runbook == nil {
			return nil, errors.New("a runbook operation without its role")
		}
		plays = append(plays, map[string]any{"name": "Runbook " + op.Runbook.Name, "hosts": "all", "become": true,
			"roles": []any{map[string]any{"role": op.Runbook.Role, "vars": given(op.Runbook.Params)}}})
	default:
		return nil, fmt.Errorf("the %s action is not supported yet", op.Action)
	}
	plays = append(plays, map[string]any{"name": "Health", "hosts": "all", "gather_facts": false, "tasks": []any{
		map[string]any{"name": "Answers", "ansible.builtin.wait_for_connection": map[string]any{"timeout": 300}},
		map[string]any{"name": "Ping", "ansible.builtin.ping": map[string]any{}},
	}})
	return json.MarshalIndent(plays, "", "  ")
}

// patchPlay updates the operating system (design 6.4): on the Red Hat
// family with dnf (scope security, the default, or all; exclude lists
// packages to leave alone) and needs-restarting to tell whether a reboot
// is needed; on Debian with apt (scope all) and /var/run/reboot-required.
// reboot: auto (the default) reboots when needed, always or never.
func patchPlay(options map[string]any) map[string]any {
	scope, _ := options["scope"].(string)
	if scope == "" {
		scope = "security"
	}
	reboot, _ := options["reboot"].(string)
	if reboot == "" {
		reboot = "auto"
	}
	var exclude []string
	if list, ok := options["exclude"].([]any); ok {
		for _, x := range list {
			exclude = append(exclude, fmt.Sprint(x))
		}
	}
	rh := `ansible_facts.os_family == "RedHat"`
	deb := `ansible_facts.os_family == "Debian"`
	tasks := []any{
		map[string]any{"name": "Update (dnf)", "when": rh, "ansible.builtin.dnf": map[string]any{
			"name": "*", "state": "latest", "security": scope == "security", "exclude": exclude, "update_only": true}},
		map[string]any{"name": "Is a reboot needed (dnf)", "when": rh, "register": "gaia_needs_restarting", "changed_when": false,
			"failed_when":             "gaia_needs_restarting.rc not in [0, 1]",
			"ansible.builtin.command": map[string]any{"cmd": "needs-restarting -r"}},
		map[string]any{"name": "Update (apt)", "when": deb, "ansible.builtin.apt": map[string]any{"update_cache": true, "upgrade": "safe"}},
		map[string]any{"name": "Is a reboot needed (apt)", "when": deb, "register": "gaia_reboot_required",
			"ansible.builtin.stat": map[string]any{"path": "/var/run/reboot-required"}},
	}
	switch reboot {
	case "always":
		tasks = append(tasks, map[string]any{"name": "Reboot", "ansible.builtin.reboot": map[string]any{"reboot_timeout": 900}})
	case "auto":
		tasks = append(tasks, map[string]any{"name": "Reboot when needed", "ansible.builtin.reboot": map[string]any{"reboot_timeout": 900},
			"when": "(gaia_needs_restarting.rc | default(0)) == 1 or (gaia_reboot_required.stat.exists | default(false))"})
	}
	return map[string]any{"name": "Patch", "hosts": "all", "become": true, "gather_facts": true, "tasks": tasks}
}
