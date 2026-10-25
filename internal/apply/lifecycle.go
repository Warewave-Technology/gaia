package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/compute"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// The decommission flow (design 9.8). With a grace period, a server that
// goes is first stopped: monitoring is switched off for it, a last
// snapshot is taken when the Policy asks for one, it is shut down, its
// computer account and bastion targets are disabled. Its VM stays in the
// stack as it was last applied, switched off, until the grace period
// ends; wanted again before that (the commit is reverted), it is started
// and switched back on. When the period is over, a decommission run
// deletes it: its records go, its VM leaves the stack and is deleted
// through the hypervisor's API, its address goes into quarantine.

// stopper is a server that starts its grace period in this run.
type stopper struct {
	s plan.Server
	h db.Host
}

// now is the run's clock.
func (r *runner) now() time.Time {
	if r.d.Now != nil {
		return r.d.Now()
	}
	return time.Now()
}

// stays decides what happens to a server the plan deletes: it reports
// true when the server stays, stopped, in this run's stack, and false when
// it is deleted now.
func (r *runner) stays(ctx context.Context, c plan.Contract, s plan.Server, w *work) (bool, error) {
	grace := c.Decommission.Grace()
	if grace <= 0 {
		return false, nil
	}
	h, err := r.d.Store.Q().Host(ctx, s.UID)
	if err != nil {
		return false, err
	}
	switch {
	case h.Status == db.HostStatusDecommissioning:
		// A deletion that stopped half way goes on.
		return false, nil
	case h.Status == db.HostStatusStopped && h.DeleteAfter.Valid && !r.now().Before(h.DeleteAfter.Time):
		return false, nil
	}
	vm, err := storedVM(h)
	if err != nil {
		return false, fmt.Errorf("%s: %w", s.Hostname, err)
	}
	vm.Stopped = true
	provider := r.siteProvider(h.Site)
	if id, _ := r.vmProvider(ctx, s.UID); id != "" {
		provider = id
	}
	if err := r.siteHypervisor(ctx, w, h.Site, provider); err != nil {
		return false, err
	}
	w.bySite[h.Site] = append(w.bySite[h.Site], vm)
	if h.Status == db.HostStatusStopped {
		r.printf("  %s: stopped, deleted after %s\n", s.Hostname, h.DeleteAfter.Time.UTC().Format(time.RFC3339))
		return true, nil
	}
	w.stopping = append(w.stopping, stopper{s: s, h: h})
	return true, nil
}

// vmProvider is the compute provider that holds a server's VM.
func (r *runner) vmProvider(ctx context.Context, uid string) (string, error) {
	ids, err := r.d.Store.Q().ExternalIDs(ctx, uid)
	for _, e := range ids {
		if e.Kind == "vm" {
			return e.Provider, err
		}
	}
	return "", err
}

// storedVM is the VM as the last run applied it.
func storedVM(h db.Host) (pulumi.VM, error) {
	var vm pulumi.VM
	if len(h.VmSpec) == 0 {
		return vm, errors.New("no VM recorded as applied; it cannot wait out a grace period (run an apply that keeps it first)")
	}
	err := json.Unmarshal(h.VmSpec, &vm)
	return vm, err
}

// stopServers starts the grace period of servers: nothing is deleted, and
// everything can be switched on again.
func (r *runner) stopServers(ctx context.Context, c plan.Contract, servers []stopper) error {
	until := r.now().Add(c.Decommission.Grace()).UTC().Truncate(time.Second)
	for _, st := range servers {
		s := st.s
		ids, err := r.d.Store.Q().ExternalIDs(ctx, s.UID)
		if err != nil {
			return err
		}
		var done []string
		for _, e := range ids {
			if e.Kind != "host" {
				continue
			}
			m, err := r.monitor(ctx, e.Provider)
			if err != nil {
				return err
			}
			if err := m.SetEnabled(ctx, e.ExternalID, false); err != nil {
				return fmt.Errorf("%s: %w", s.Hostname, err)
			}
			done = append(done, e.Provider+" monitoring off")
		}
		comp, vm, err := r.dayTwo(ctx, s.UID)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Hostname, err)
		}
		if c.Decommission.FinalSnapshot {
			name := proxmox.SnapshotName("gaia_final_" + r.now().UTC().Format("20060102T1504"))
			if err := comp.Snapshot(ctx, vm, name, "Gaia: last snapshot before deletion ("+r.run+")"); err != nil {
				return fmt.Errorf("%s: last snapshot: %w", s.Hostname, err)
			}
			done = append(done, "snapshot "+name)
		}
		if err := comp.Power(ctx, vm, compute.Off); err != nil {
			return fmt.Errorf("%s: shut down: %w", s.Hostname, err)
		}
		done = append(done, "shut down")
		for _, e := range ids {
			switch e.Kind {
			case "computer":
				if err := r.setComputer(ctx, e.Provider, e.ExternalID, false); err != nil {
					return fmt.Errorf("%s: %w", s.Hostname, err)
				}
				done = append(done, e.Provider+" account disabled")
			case "target", "target-sudo":
				wg, err := r.bastion(ctx, e.Provider)
				if err != nil {
					return err
				}
				if _, err := wg.SetTargetRoles(ctx, e.ExternalID, nil); err != nil {
					return fmt.Errorf("%s: %w", s.Hostname, err)
				}
				done = append(done, e.Provider+" "+e.Kind+" closed")
			}
		}
		if err := r.d.Store.Q().StopHost(ctx, db.StopHostParams{Uid: s.UID, DeleteAfter: pgTime(until)}); err != nil {
			return err
		}
		detail := strings.Join(done, ", ") + "; deleted after " + until.Format(time.RFC3339)
		r.printf("  %s: stopped (%s)\n", s.Hostname, detail)
		r.change(runrecord.Change{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Stage: "decommission", Action: "stop", Detail: detail})
	}
	return nil
}

// restoreServers brings stopped servers back: the commit that removed
// them was undone within their grace period.
func (r *runner) restoreServers(ctx context.Context, c plan.Contract, targets []target) error {
	for _, t := range targets {
		s := t.s
		comp, vm, err := r.dayTwo(ctx, s.UID)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Hostname, err)
		}
		if err := comp.Power(ctx, vm, compute.On); err != nil {
			return fmt.Errorf("%s: start: %w", s.Hostname, err)
		}
		if err := r.waitSSH(ctx, t.address); err != nil {
			return fmt.Errorf("%s: %w", s.Hostname, err)
		}
		ids, err := r.d.Store.Q().ExternalIDs(ctx, s.UID)
		if err != nil {
			return err
		}
		for _, e := range ids {
			switch e.Kind {
			case "host":
				m, err := r.monitor(ctx, e.Provider)
				if err != nil {
					return err
				}
				if err := m.SetEnabled(ctx, e.ExternalID, true); err != nil {
					return fmt.Errorf("%s: %w", s.Hostname, err)
				}
			case "computer":
				if err := r.setComputer(ctx, e.Provider, e.ExternalID, true); err != nil {
					return fmt.Errorf("%s: %w", s.Hostname, err)
				}
			}
		}
		// The bastion's roles come back with the access stage.
		if err := r.d.Store.Q().RestoreHost(ctx, s.UID); err != nil {
			return err
		}
		r.printf("  %s: started again; its records are on\n", s.Hostname)
		r.change(runrecord.Change{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Stage: "restore", Action: "restore", Detail: "started, monitoring and account on"})
	}
	return nil
}

// waitSSH waits for a started server's SSH server, five minutes at most.
func (r *runner) waitSSH(ctx context.Context, addr string) error {
	deadline := r.now().Add(5 * time.Minute)
	for {
		_, err := r.d.ScanHostKey(ctx, addr, ssh.KeyAlgoED25519)
		if err == nil {
			return nil
		}
		if r.now().After(deadline) {
			return fmt.Errorf("no SSH after it was started: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (r *runner) setComputer(ctx context.Context, provider, guid string, enabled bool) error {
	dir, err := r.directory(ctx, provider)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	comp, err := dir.ComputerByGUID(guid)
	if errors.Is(err, ad.ErrNoComputer) {
		return nil
	}
	if err != nil {
		return err
	}
	return dir.SetComputerEnabled(comp.DN, enabled)
}

// DecommissionRequester is who the runs that end grace periods are
// requested by.
const DecommissionRequester = "gaia: decommission"

// RunDecommission deletes the servers of an applied plan whose grace
// period is over, as the approved change that stopped them asked. Nothing
// else of the contract runs: the VMs leave their stacks without a Pulumi
// update and are deleted through the hypervisor's API. p must be the plan
// last applied.
func RunDecommission(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps) (string, error) {
	return run(ctx, g, p, d, func(ctx context.Context, r *runner) error { return r.endGrace(ctx, p) })
}

func (r *runner) endGrace(ctx context.Context, p *plan.Plan) error {
	backend, err := r.backend(ctx)
	if err != nil {
		return err
	}
	for _, c := range p.Contracts {
		err := r.d.Store.WithContractLock(ctx, c.Name, func(ctx context.Context) error {
			return r.stage(ctx, c.Name, "decommission", func() error { return r.deleteDue(ctx, c, backend) })
		})
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

// deleteDue deletes the contract's stopped servers whose time has come.
func (r *runner) deleteDue(ctx context.Context, c plan.Contract, backend pulumi.Backend) error {
	q := r.d.Store.Q()
	var gone []doomed
	bySite := map[string][]string{}
	w := &work{bySite: map[string][]pulumi.VM{}, siteCreds: map[string]proxmox.Credentials{}, creds: map[string]proxmox.Credentials{},
		siteVS: map[string]*pulumi.VSphereCredentials{}, vsCreds: map[string]*pulumi.VSphereCredentials{}}
	for _, s := range c.Servers {
		if s.Action != plan.Delete {
			continue
		}
		h, err := q.Host(ctx, s.UID)
		if err != nil {
			return err
		}
		due := h.Status == db.HostStatusDecommissioning ||
			h.Status == db.HostStatusStopped && h.DeleteAfter.Valid && !r.now().Before(h.DeleteAfter.Time)
		if !due {
			continue
		}
		d, err := r.doom(ctx, s)
		if err != nil {
			return err
		}
		gone = append(gone, d)
		if d.compute != "" {
			bySite[h.Site] = append(bySite[h.Site], s.UID)
			if err := r.siteHypervisor(ctx, w, h.Site, d.compute); err != nil {
				return err
			}
		}
	}
	if len(gone) == 0 {
		r.printf("  %s: nothing is due\n", c.Name)
		return nil
	}
	if err := r.unregister(ctx, c, gone); err != nil {
		return err
	}
	sites := make([]string, 0, len(bySite))
	for site := range bySite {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	for _, site := range sites {
		st := pulumi.Stack{Name: c.Name + "-" + site, Proxmox: w.siteCreds[site], VSphere: w.siteVS[site]}
		found, err := r.d.Forget(ctx, backend, st, bySite[site])
		if err != nil {
			return err
		}
		r.printf("  %s: %d VM(s) left the stack\n", st.Name, len(found))
	}
	for _, d := range gone {
		if d.compute == "" {
			continue
		}
		comp, vm, err := r.dayTwo(ctx, d.s.UID)
		if err != nil {
			return fmt.Errorf("%s: %w", d.s.Hostname, err)
		}
		action := "deleted"
		if err := comp.Delete(ctx, vm); errors.Is(err, compute.ErrNoVM) {
			action = "already gone"
		} else if err != nil {
			return fmt.Errorf("%s: delete VM %s: %w", d.s.Hostname, vm, err)
		}
		r.printf("  %s: VM %s %s\n", d.s.Hostname, vm, action)
	}
	return r.release(ctx, c, gone, w.creds)
}

// recordVM keeps the VM a run applied, for a grace period to come.
func (r *runner) recordVM(ctx context.Context, v pulumi.VM) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.d.Store.Q().SetVMSpec(ctx, db.SetVMSpecParams{Uid: v.UID, VmSpec: data})
}

func pgTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// retain lets a server of a contract decommissioned with deletionPolicy
// retain go from Gaia's management (design 9.6): its VM leaves the stack
// before the stack runs, so that Pulumi does not delete it.
func (r *runner) retain(ctx context.Context, s plan.Server, w *work) error {
	provider, err := r.vmProvider(ctx, s.UID)
	if err != nil {
		return err
	}
	if provider == "" {
		provider = r.siteProvider(s.Site)
	}
	if err := r.siteHypervisor(ctx, w, s.Site, provider); err != nil {
		return err
	}
	if _, ok := w.bySite[s.Site]; !ok {
		w.bySite[s.Site] = nil
	}
	if w.retained == nil {
		w.retained = map[string][]plan.Server{}
	}
	w.retained[s.Site] = append(w.retained[s.Site], s)
	return nil
}

// letGo drops retained servers from their stack and marks them
// unmanaged. Their records, address and name stay: the server lives on.
func (r *runner) letGo(ctx context.Context, c plan.Contract, st pulumi.Stack, backend pulumi.Backend, servers []plan.Server) error {
	uids := make([]string, 0, len(servers))
	for _, s := range servers {
		uids = append(uids, s.UID)
	}
	if _, err := r.d.Forget(ctx, backend, st, uids); err != nil {
		return err
	}
	for _, s := range servers {
		if err := r.d.Store.Q().SetHostStatus(ctx, db.SetHostStatusParams{Uid: s.UID, Status: db.HostStatusUnmanaged}); err != nil {
			return err
		}
		r.printf("  %s: retained; no longer managed by Gaia\n", s.Hostname)
		r.change(runrecord.Change{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Stage: "provision", Action: "retain", Detail: "left the stack " + st.Name + "; records kept"})
	}
	return nil
}
