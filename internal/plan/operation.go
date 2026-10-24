package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/cel"
)

// Operation is what an Operation will do (design 6.4): the servers its
// selector takes now, in batches, and the steps on each. Approving it
// approves exactly these servers; ones added later are not touched.
type Operation struct {
	Name      string         `json:"name"`
	Action    string         `json:"action"`
	Targets   []Target       `json:"targets"`
	Batches   [][]string     `json:"batches"` // UIDs
	Steps     []string       `json:"steps"`
	Options   map[string]any `json:"options,omitempty"`
	Runbook   *Role          `json:"runbook,omitempty"`
	Window    *Window        `json:"window,omitempty"`
	NotAfter  *time.Time     `json:"notAfter,omitempty"`
	MaxFail   int            `json:"maxFailures"`
	Pause     string         `json:"pauseBetweenBatches,omitempty"`
	Contracts []string       `json:"contracts"`
	Owners    []string       `json:"owners"`
	Envs      []string       `json:"envs"`
	Hash      string         `json:"hash"`
}

// Target is a server an Operation works on.
type Target struct {
	UID      string `json:"uid"`
	Hostname string `json:"hostname"`
	Contract string `json:"contract"`
	Owner    string `json:"owner"`
	Env      string `json:"env"`
}

// Window is when an Operation may run.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// BuildOperation plans an Operation against the servers Gaia manages.
func BuildOperation(ctx context.Context, g *catalog.Graph, name string, st State) (*Operation, error) {
	o, ok := g.Catalog.Get(catalog.KindOperation, name)
	if !ok {
		return nil, fmt.Errorf("no Operation %q", name)
	}
	s := o.Spec.(*catalog.OperationSpec)
	sel, err := cel.Selector(s.Selector)
	if err != nil {
		return nil, fmt.Errorf("%s: selector: %w", name, err)
	}
	op := &Operation{Name: name, Action: s.Action, Options: s.Options, MaxFail: s.Strategy.MaxFailures,
		Pause: s.Strategy.PauseBetweenBatches, Targets: []Target{}, Batches: [][]string{}, Contracts: []string{}, Owners: []string{}, Envs: []string{}}
	start, end, notAfter, err := s.Times()
	if err != nil {
		return nil, err
	}
	if s.Window != nil {
		op.Window = &Window{Start: start.UTC(), End: end.UTC()}
	}
	if !notAfter.IsZero() {
		na := notAfter.UTC()
		op.NotAfter = &na
	}
	if s.Runbook != "" {
		rs := mustGet(g, catalog.KindAnsibleRole, s.Runbook).Spec.(*catalog.AnsibleRoleSpec)
		op.Runbook = &Role{Name: s.Runbook, Role: rs.Source.Role, Provider: rs.Source.Provider,
			Params: mask(rs.Params, catalog.EffectiveParams(rs.Params, s.Params))}
	}
	for _, c := range g.Catalog.List(catalog.KindServer) {
		labels := map[string]string{}
		for k, v := range c.Metadata.Labels {
			labels[k] = v
		}
		ok, err := sel.EvalBool(map[string]any{"contract": c.Metadata.Name, "labels": labels, "env": labels["env"],
			"owner": labels["owner"], "spec": c.SpecMap()})
		if err != nil {
			return nil, fmt.Errorf("%s: selector on %s: %w", name, c.Metadata.Name, err)
		}
		if !ok {
			continue
		}
		hosts, err := st.LiveHosts(ctx, c.Metadata.Name)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			if h.Status != "active" {
				continue
			}
			op.Targets = append(op.Targets, Target{UID: h.UID, Hostname: h.Hostname, Contract: c.Metadata.Name, Owner: labels["owner"], Env: labels["env"]})
		}
	}
	sort.Slice(op.Targets, func(i, j int) bool { return op.Targets[i].Hostname < op.Targets[j].Hostname })
	for _, t := range op.Targets {
		for _, x := range []struct {
			v   string
			set *[]string
		}{{t.Contract, &op.Contracts}, {t.Owner, &op.Owners}, {t.Env, &op.Envs}} {
			if x.v != "" && !slices.Contains(*x.set, x.v) {
				*x.set = append(*x.set, x.v)
			}
		}
	}
	sort.Strings(op.Contracts)
	sort.Strings(op.Owners)
	sort.Strings(op.Envs)
	i := 0
	for _, n := range s.Batches(len(op.Targets)) {
		var b []string
		for _, t := range op.Targets[i : i+n] {
			b = append(b, t.UID)
		}
		op.Batches = append(op.Batches, b)
		i += n
	}
	op.Steps = operationSteps(s)
	h := *op
	h.Hash = ""
	data, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	op.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return op, nil
}

// operationSteps are what happens on each server.
func operationSteps(s *catalog.OperationSpec) []string {
	steps := []string{"maintenance"}
	if s.Action == "snapshot" || s.Options["snapshot"] == true {
		steps = append(steps, "snapshot")
	}
	switch s.Action {
	case "patch":
		steps = append(steps, "patch")
		if r, _ := s.Options["reboot"].(string); r != "never" {
			steps = append(steps, "reboot (if needed)")
		}
	case "reboot":
		steps = append(steps, "reboot")
	case "runbook":
		steps = append(steps, "runbook "+s.Runbook)
	}
	if s.Action != "snapshot" {
		steps = append(steps, "health")
	}
	return append(steps, "end maintenance")
}
