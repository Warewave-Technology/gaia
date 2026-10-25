package apply

import (
	"context"
	"errors"
	"fmt"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// join is what a server joins its domain with, for the gaia_domain_join
// role: the one-time password is empty once a host joined.
type join struct {
	Domain   string `json:"domain"`
	Password string `json:"password"`
}

// prestage makes each server's computer account before it is configured
// (design 14.6), with a one-time password the server joins with. The
// account is found by its recorded GUID; a name taken by an account that
// is not the server's stops the run (design 9.2). An account no host
// joined with yet gets a new password on every run, since the last one
// was not kept.
func (r *runner) prestage(ctx context.Context, c plan.Contract, targets []target) error {
	by := map[string][]target{}
	var providers []string
	for _, t := range targets {
		if d := t.s.Domain; d != nil {
			if by[d.Provider] == nil {
				providers = append(providers, d.Provider)
			}
			by[d.Provider] = append(by[d.Provider], t)
		}
	}
	for _, provider := range providers {
		dir, err := r.directory(ctx, provider)
		if err != nil {
			return err
		}
		for _, t := range by[provider] {
			if err = r.account(ctx, c, dir, provider, t); err != nil {
				break
			}
		}
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) account(ctx context.Context, c plan.Contract, dir Directory, provider string, t target) error {
	guid, err := r.externalID(ctx, t.s.UID, provider, "computer")
	if err != nil {
		return err
	}
	var comp ad.Computer
	action := ""
	if guid != "" {
		comp, err = dir.ComputerByGUID(guid)
		switch {
		case errors.Is(err, ad.ErrNoComputer):
			r.printf("  %s %s: computer account %s was deleted outside Gaia\n", provider, t.s.Hostname, guid)
			guid = ""
		case err != nil:
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
	}
	otp := ""
	if guid == "" || !comp.Joined {
		if otp, err = ad.OneTimePassword(); err != nil {
			return err
		}
	}
	switch {
	case guid == "":
		other, err := dir.Computer(t.s.Hostname)
		if err == nil {
			return fmt.Errorf("%s: computer account %s exists and is not the server's", t.s.Hostname, other.DN)
		}
		if !errors.Is(err, ad.ErrNoComputer) {
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
		info := fmt.Sprintf("Managed by Gaia. gaia.uid=%s contract=%s", t.s.UID, c.Name)
		if comp, err = dir.CreateComputer(t.s.Domain.OU, t.s.Hostname, info, otp); err != nil {
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
		if err := r.d.Store.Q().SetExternalID(ctx, db.SetExternalIDParams{Uid: t.s.UID, Provider: provider, Kind: "computer", ExternalID: comp.GUID}); err != nil {
			return err
		}
		action = "create"
	case !comp.Joined:
		if err := dir.ResetComputer(comp.DN, otp); err != nil {
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
		action = "reset for joining"
	default:
		action = "same"
	}
	if r.joins == nil {
		r.joins = map[string]join{}
	}
	r.joins[t.s.UID] = join{Domain: dir.Domain(), Password: otp}
	r.printf("  %s %s: computer account %s %s\n", provider, t.s.Hostname, comp.DN, action)
	r.change(runrecord.Change{Contract: c.Name, UID: t.s.UID, Hostname: t.s.Hostname, Stage: "directory", Action: action, Detail: provider + " " + comp.DN})
	return nil
}

// deleteComputers removes a doomed server's computer accounts, found by
// their GUIDs.
func (r *runner) deleteComputers(ctx context.Context, c plan.Contract, d doomed) error {
	for provider, guid := range d.computers {
		dir, err := r.directory(ctx, provider)
		if err != nil {
			return err
		}
		action, what := "delete", guid
		comp, err := dir.ComputerByGUID(guid)
		switch {
		case errors.Is(err, ad.ErrNoComputer):
			action, err = "already gone", nil
		case err == nil:
			what = comp.DN
			err = dir.DeleteComputer(comp.DN)
		}
		_ = dir.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", d.s.Hostname, err)
		}
		r.printf("  %s %s: computer account %s %s\n", provider, d.s.Hostname, what, action)
		r.change(runrecord.Change{Contract: c.Name, UID: d.s.UID, Hostname: d.s.Hostname, Stage: "decommission", Action: provider + " " + action, Detail: "computer account " + what})
	}
	return nil
}

func hasDomain(c plan.Contract) bool {
	for _, s := range c.Servers {
		if s.Domain != nil && s.Action != plan.Delete {
			return true
		}
	}
	return false
}
