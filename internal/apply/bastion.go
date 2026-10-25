package apply

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/apply/access"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/provider/warpgate"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Bastion keeps servers in Warpgate (warpgate.Client).
type Bastion interface {
	EnsureTarget(ctx context.Context, t warpgate.Target, id, mark string) (warpgate.Result, error)
	DeleteTarget(ctx context.Context, id string) (bool, error)
	Target(ctx context.Context, id string) (warpgate.Target, error)
	TargetRoles(ctx context.Context, id string) ([]string, error)
	EnsureRole(ctx context.Context, name string) (string, error)
	SetTargetRoles(ctx context.Context, target string, roles []string) ([]string, error)
	Users(ctx context.Context) ([]warpgate.User, error)
	SetRoleUsers(ctx context.Context, role string, users []warpgate.User) (added, removed []string, err error)
	OwnKeys(ctx context.Context) ([]string, error)
	SetHostKeys(ctx context.Context, host string, port int, keys []ssh.PublicKey) (bool, error)
}

// The accounts Warpgate logs in to a contract's SSH targets as, with its
// own key: one without sudo, one with it for access that has sudo. Who
// logged in is Warpgate's session record.
const (
	BastionUser     = "gaia-wg"
	BastionSudoUser = "gaia-wg-sudo"
)

// bastion opens a warpgate provider.
func (r *runner) bastion(ctx context.Context, provider string) (Bastion, error) {
	if b, ok := r.bastions[provider]; ok {
		return b, nil
	}
	p := r.provider(provider)
	f, err := r.d.Secrets.Read(ctx, p.Credentials)
	if err != nil {
		return nil, err
	}
	if f["token"] == "" {
		return nil, fmt.Errorf("%s: no token", p.Credentials)
	}
	insecure := f["insecure"] == "true"
	b, err := r.d.Bastion(p.Endpoint, f["token"], insecure, []byte(f["ca"]))
	if err != nil {
		return nil, err
	}
	if r.bastions == nil {
		r.bastions = map[string]Bastion{}
	}
	r.bastions[provider] = b
	r.executedAs[provider] = "api token"
	return b, nil
}

// bastionAccounts are the accounts Warpgate logs in as, for gaia_access.
func (r *runner) bastionAccounts(ctx context.Context, c plan.Contract) ([]access.User, error) {
	b := c.Bastion
	if b == nil || b.Kind != "ssh" {
		return nil, nil
	}
	wg, err := r.bastion(ctx, b.Provider)
	if err != nil {
		return nil, err
	}
	keys, err := wg.OwnKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := []access.User{{Name: BastionUser, Keys: keys}}
	if b.Sudo {
		out = append(out, access.User{Name: BastionSudoUser, Keys: keys, Sudo: true})
	}
	return out, nil
}

// bastionAccess puts a contract's servers in Warpgate and its access's
// people in the contract's roles there (design 14.4). It runs after the
// servers were configured: their host keys are known by then, and the
// accounts Warpgate logs in as exist. A contract without access, or a
// Platform without a bastion, has its targets taken away.
func (r *runner) bastionAccess(ctx context.Context, c plan.Contract, targets []target) error {
	b := c.Bastion
	if b == nil {
		return r.dropTargets(ctx, c, targets, "target", "target-sudo")
	}
	wg, err := r.bastion(ctx, b.Provider)
	if err != nil {
		return err
	}
	role, err := wg.EnsureRole(ctx, plan.BastionRole(c.Name, false))
	if err != nil {
		return err
	}
	sudoRole := ""
	if b.Sudo {
		if sudoRole, err = wg.EnsureRole(ctx, plan.BastionRole(c.Name, true)); err != nil {
			return err
		}
	} else if err := r.dropTargets(ctx, c, targets, "target-sudo"); err != nil {
		return err
	}
	q := r.d.Store.Q()
	for _, t := range targets {
		mark := "gaia.uid=" + t.s.UID
		desc := fmt.Sprintf("Managed by Gaia. %s contract=%s", mark, c.Name)
		ensure := func(kind, name, user string, port int, roleID string) error {
			id, err := r.externalID(ctx, t.s.UID, b.Provider, kind)
			if err != nil {
				return err
			}
			res, err := wg.EnsureTarget(ctx, warpgate.Target{Name: name, Description: desc, Kind: b.Kind, Host: t.address, Port: port, Username: user}, id, mark)
			if err != nil {
				return fmt.Errorf("%s: %w", t.s.Hostname, err)
			}
			if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: t.s.UID, Provider: b.Provider, Kind: kind, ExternalID: res.ID}); err != nil {
				return err
			}
			if _, err := wg.SetTargetRoles(ctx, res.ID, []string{roleID}); err != nil {
				return fmt.Errorf("%s: %w", t.s.Hostname, err)
			}
			r.printf("  %s %s: %s target %s %s\n", b.Provider, t.s.Hostname, strings.ToUpper(b.Kind), name, res.Action)
			r.change(runrecord.Change{Contract: c.Name, UID: t.s.UID, Hostname: t.s.Hostname, Stage: "bastion", Action: res.Action, Detail: fmt.Sprintf("%s %s target %s", b.Provider, b.Kind, name)})
			return nil
		}
		if b.Kind == "rdp" {
			if err := ensure("target", t.s.Hostname, "", 3389, role); err != nil {
				return err
			}
			continue
		}
		if err := ensure("target", t.s.Hostname, BastionUser, 22, role); err != nil {
			return err
		}
		if b.Sudo {
			if err := ensure("target-sudo", t.s.Hostname+"-sudo", BastionSudoUser, 22, sudoRole); err != nil {
				return err
			}
		}
		keys, err := r.hostKeys(ctx, t.s.UID)
		if err != nil {
			return err
		}
		if _, err := wg.SetHostKeys(ctx, t.address, 22, keys); err != nil {
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
	}
	return r.bastionPeople(ctx, c, wg, role, sudoRole)
}

// bastionPeople makes the contract's roles hold exactly the enabled
// people of its access (the sudo role those of access with sudo) who have
// a Warpgate user of the same name.
func (r *runner) bastionPeople(ctx context.Context, c plan.Contract, wg Bastion, role, sudoRole string) error {
	users, err := wg.Users(ctx)
	if err != nil {
		return err
	}
	byName := map[string]warpgate.User{}
	for _, u := range users {
		byName[strings.ToLower(u.Username)] = u
	}
	in, sudo := map[string]bool{}, map[string]bool{}
	for _, a := range c.Access {
		rows, err := r.d.Store.Q().TeamLogins(ctx, a.Teams)
		if err != nil {
			return err
		}
		for _, row := range rows {
			in[row.Username] = true
			sudo[row.Username] = sudo[row.Username] || a.Sudo
		}
	}
	var members, sudoMembers []warpgate.User
	var missing []string
	for name := range in {
		u, ok := byName[strings.ToLower(name)]
		if !ok {
			missing = append(missing, name)
			continue
		}
		members = append(members, u)
		if sudo[name] {
			sudoMembers = append(sudoMembers, u)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		r.printf("  %s: no Warpgate user for %s\n", c.Bastion.Provider, strings.Join(missing, ", "))
	}
	set := func(name, id string, us []warpgate.User) error {
		added, removed, err := wg.SetRoleUsers(ctx, id, us)
		if err != nil {
			return err
		}
		if len(added)+len(removed) > 0 {
			r.printf("  %s role %s: +%v -%v\n", c.Bastion.Provider, name, added, removed)
			r.change(runrecord.Change{Contract: c.Name, Stage: "bastion", Action: "role", Detail: fmt.Sprintf("%s %s +%v -%v", c.Bastion.Provider, name, added, removed)})
		}
		return nil
	}
	if err := set(plan.BastionRole(c.Name, false), role, members); err != nil {
		return err
	}
	if sudoRole != "" {
		return set(plan.BastionRole(c.Name, true), sudoRole, sudoMembers)
	}
	return nil
}

// hostKeys are the SSH host keys Gaia recorded for a server.
func (r *runner) hostKeys(ctx context.Context, uid string) ([]ssh.PublicKey, error) {
	rows, err := r.d.Store.Q().HostKeys(ctx, uid)
	if err != nil {
		return nil, err
	}
	var out []ssh.PublicKey
	for _, k := range rows {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
		if err != nil {
			return nil, fmt.Errorf("host key of %s: %w", uid, err)
		}
		out = append(out, pk)
	}
	return out, nil
}

// dropTargets deletes the servers' recorded targets of the kinds, from
// whichever bastion recorded them.
func (r *runner) dropTargets(ctx context.Context, c plan.Contract, targets []target, kinds ...string) error {
	for _, t := range targets {
		ids, err := r.d.Store.Q().ExternalIDs(ctx, t.s.UID)
		if err != nil {
			return err
		}
		for _, e := range ids {
			if !slices.Contains(kinds, e.Kind) || r.provider(e.Provider).Type != "warpgate" {
				continue
			}
			if err := r.deleteTarget(ctx, c, t.s, e.Provider, e.Kind, e.ExternalID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *runner) deleteTarget(ctx context.Context, c plan.Contract, s plan.Server, provider, kind, id string) error {
	wg, err := r.bastion(ctx, provider)
	if err != nil {
		return err
	}
	gone, err := wg.DeleteTarget(ctx, id)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Hostname, err)
	}
	if err := r.d.Store.Q().DeleteExternalID(ctx, db.DeleteExternalIDParams{Uid: s.UID, Provider: provider, Kind: kind}); err != nil {
		return err
	}
	action := "delete"
	if !gone {
		action = "already gone"
	}
	r.printf("  %s %s: %s %s %s\n", provider, s.Hostname, kind, id, action)
	r.change(runrecord.Change{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Stage: "bastion", Action: provider + " " + action, Detail: kind + " " + id})
	return nil
}
