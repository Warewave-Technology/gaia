package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
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
	play, err := operationPlay(op)
	if err != nil {
		return nil, err
	}
	cfg := r.cfg
	job := ansible.Job{
		Name: fmt.Sprintf("gaia-%s-%s-%d", r.run, op.Name, n), Image: cfg.image, Playbook: sitePlaybook,
		Hosts: hosts, User: cfg.user, SSHKey: cfg.sshKey, Forks: cfg.forks, Timeout: cfg.timeout,
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
	for uid, i := range names {
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

// operationPlay is the playbook of an Operation's action, followed by a
// health check: the server answers again.
func operationPlay(op *plan.Operation) ([]byte, error) {
	var plays []any
	switch op.Action {
	case "reboot":
		plays = append(plays, map[string]any{"name": "Reboot", "hosts": "all", "become": true, "gather_facts": false,
			"tasks": []any{map[string]any{"name": "Reboot", "ansible.builtin.reboot": map[string]any{"reboot_timeout": 900}}}})
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
