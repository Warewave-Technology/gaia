package plan

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeState is servers that already exist and names held by others.
type fakeState struct {
	hosts map[string][]Host // by contract
	held  map[string]string // hostname -> uid
}

func (f fakeState) LiveHosts(_ context.Context, contract string) ([]Host, error) {
	return f.hosts[contract], nil
}

func (f fakeState) NameHolder(_ context.Context, name string) (string, error) {
	if uid, ok := f.held[name]; ok {
		return uid, nil
	}
	for _, hs := range f.hosts {
		for _, h := range hs {
			if h.Hostname == name {
				return h.UID, nil
			}
		}
	}
	return "", nil
}

func host(seq int) Host {
	return Host{UID: fmt.Sprintf("uid%d", seq), Hostname: fmt.Sprintf("labtpoc%02d", seq), Site: "lab", Network: "lab-poc", Seq: seq, Status: "active"}
}

func withReplicas(t *testing.T, n int, extra string) []string {
	t.Helper()
	c := strings.Replace(readLab(t, "contracts/poc-web.yaml"), "replicas: 1", fmt.Sprintf("replicas: %d", n), 1)
	return []string{"contracts/poc-web.yaml", c + extra}
}

func actions(p *Plan) string {
	var out []string
	for _, s := range p.Contracts[0].Servers {
		out = append(out, s.Action+":"+s.Hostname)
	}
	return strings.Join(out, " ")
}

func TestKeepsExistingAndCreatesTheRest(t *testing.T) {
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1)}}}
	p := buildWith(t, st, nil, withReplicas(t, 3, "")...)
	if got := actions(p); got != "keep:labtpoc01 create:labtpoc02 create:labtpoc03" {
		t.Errorf("actions = %s", got)
	}
	if s := p.Contracts[0].Servers[0]; s.UID != "uid1" || s.Status != "active" || s.Compute.Provider != "pve-lab" {
		t.Errorf("kept server = %+v", s)
	}
}

func TestSkipsNamesHeldByOthers(t *testing.T) {
	st := fakeState{held: map[string]string{"labtpoc01": "someone-else"}}
	p := buildWith(t, st, nil, withReplicas(t, 2, "")...)
	if got := actions(p); got != "create:labtpoc02 create:labtpoc03" {
		t.Errorf("actions = %s", got)
	}
}

func TestFillsTheLowestFreeSeq(t *testing.T) {
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1), host(3)}}}
	p := buildWith(t, st, nil, withReplicas(t, 3, "")...)
	if got := actions(p); got != "keep:labtpoc01 keep:labtpoc03 create:labtpoc02" {
		t.Errorf("actions = %s", got)
	}
}

func TestScaleDownRemovesTheHighestSeq(t *testing.T) {
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1), host(2), host(3)}}}
	p := buildWith(t, st, nil, withReplicas(t, 1, "")...)
	if got := actions(p); got != "keep:labtpoc01 delete:labtpoc03 delete:labtpoc02" {
		t.Errorf("actions = %s", got)
	}
}

func TestRetireRemovesTheNamedServer(t *testing.T) {
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1), host(2), host(3)}}}
	p := buildWith(t, st, nil, withReplicas(t, 2, "  retire: [labtpoc01]\n")...)
	if got := actions(p); got != "keep:labtpoc02 keep:labtpoc03 delete:labtpoc01" {
		t.Errorf("actions = %s", got)
	}
}

func TestReplacingARetiredServerDoesNotReuseItsNameYet(t *testing.T) {
	// Keeping the count while retiring one creates a new server; the
	// retired one still holds its name and seq until it is deleted.
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1), host(2)}}}
	p := buildWith(t, st, nil, withReplicas(t, 2, "  retire: [labtpoc01]\n")...)
	if got := actions(p); got != "keep:labtpoc02 create:labtpoc03 delete:labtpoc01" {
		t.Errorf("actions = %s", got)
	}
}

func TestDecommissionDeletesEverything(t *testing.T) {
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1), host(2)}}}
	p := buildWith(t, st, nil, withReplicas(t, 2, "  lifecycle: { state: decommissioned }\n")...)
	if got := actions(p); got != "delete:labtpoc02 delete:labtpoc01" {
		t.Errorf("actions = %s", got)
	}
}

func TestServersOnARemovedNetworkGo(t *testing.T) {
	gone := host(5)
	gone.Network = "old-net"
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1), gone}}}
	p := buildWith(t, st, nil, withReplicas(t, 1, "")...)
	if got := actions(p); got != "keep:labtpoc01 delete:labtpoc05" {
		t.Errorf("actions = %s", got)
	}
}

func TestNoChangesAndHashFollowsState(t *testing.T) {
	st := fakeState{hosts: map[string][]Host{"poc-web": {host(1)}}}
	p := buildWith(t, st, nil)
	var b strings.Builder
	if err := WriteText(&b, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"= labtpoc01", "uid uid1 (active)", "no changes to servers", "provision  +0 =1 (pve-lab)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, b.String())
		}
	}
	if fresh := build(t, nil); fresh.Hash == p.Hash {
		t.Error("plan against existing servers hashes like a fresh one")
	}
}
