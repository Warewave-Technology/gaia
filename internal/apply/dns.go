package apply

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Directory writes DNS records and computer accounts in Active Directory
// (ad.Directory).
type Directory interface {
	SetRecord(zone, name string, rec ad.Record, owned bool) (bool, error)
	DeleteRecord(zone, name string, rec ad.Record) (bool, error)
	ReverseZone(ip netip.Addr) (zone, node string, err error)
	Domain() string
	Computer(name string) (ad.Computer, error)
	ComputerByGUID(guid string) (ad.Computer, error)
	CreateComputer(ou, name, info, password string) (ad.Computer, error)
	ResetComputer(dn, password string) error
	DeleteComputer(dn string) error
	SetComputerEnabled(dn string, enabled bool) error
	Close() error
}

// directory opens an activedirectory provider with its write account.
func (r *runner) directory(ctx context.Context, provider string) (Directory, error) {
	p := r.provider(provider)
	if p.WriteCredentials == "" {
		return nil, fmt.Errorf("%s: no writeCredentials", provider)
	}
	f, err := r.d.Secrets.Read(ctx, p.WriteCredentials)
	if err != nil {
		return nil, err
	}
	d, err := r.d.Directory(ctx, ad.ConfigFrom(p.Endpoint, p.BaseDN, f))
	if err != nil {
		return nil, err
	}
	r.executedAs[provider] = f["username"]
	return d, nil
}

// registerDNS writes each server's A record and, where the network asks
// for it, its PTR record, and records both names as the server's. A name
// that holds another address and is not recorded as the server's stops
// the run (design 9.2).
func (r *runner) registerDNS(ctx context.Context, c plan.Contract, targets []target) error {
	by := map[string][]target{}
	var providers []string
	for _, t := range targets {
		if d := t.s.DNS; d != nil {
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
		err = r.writeDNS(ctx, c, dir, provider, by[provider])
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) writeDNS(ctx context.Context, c plan.Contract, dir Directory, provider string, targets []target) error {
	q := r.d.Store.Q()
	for _, t := range targets {
		d := t.s.DNS
		fqdn := t.s.Hostname + "." + d.Zone
		ttl := uint32(d.TTL)
		set := func(kind, zone, node string, rec ad.Record) error {
			name := node + "." + zone
			mine, err := r.externalID(ctx, t.s.UID, provider, kind)
			if err != nil {
				return err
			}
			changed, err := dir.SetRecord(zone, node, rec, mine == name)
			if err != nil {
				return fmt.Errorf("%s: %w", t.s.Hostname, err)
			}
			if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: t.s.UID, Provider: provider, Kind: kind, ExternalID: name}); err != nil {
				return err
			}
			action := "same"
			if changed {
				action = "set"
			}
			kindName := strings.ToUpper(strings.TrimPrefix(kind, "dns-"))
			r.printf("  %s %s: %s %s %s\n", provider, t.s.Hostname, kindName, name, action)
			r.change(runrecord.Change{Contract: c.Name, UID: t.s.UID, Hostname: t.s.Hostname, Stage: "dns", Action: action, Detail: fmt.Sprintf("%s %s %s %s", provider, kindName, name, rec.Data)})
			return nil
		}
		if err := set("dns-a", d.Zone, t.s.Hostname, ad.Record{Type: ad.TypeA, TTL: ttl, Data: t.address}); err != nil {
			return err
		}
		if !d.Reverse {
			continue
		}
		ip, err := netip.ParseAddr(t.address)
		if err != nil {
			return fmt.Errorf("%s: address %q", t.s.Hostname, t.address)
		}
		zone, node, err := dir.ReverseZone(ip)
		if err != nil {
			return fmt.Errorf("%s: %w", t.s.Hostname, err)
		}
		if err := set("dns-ptr", zone, node, ad.Record{Type: ad.TypePTR, TTL: ttl, Data: fqdn}); err != nil {
			return err
		}
	}
	return nil
}

// unregisterDNS removes a doomed server's records, found by the names
// recorded for it and its address.
func (r *runner) unregisterDNS(ctx context.Context, c plan.Contract, d doomed) error {
	for provider, fqdn := range d.dnsA {
		dir, err := r.directory(ctx, provider)
		if err != nil {
			return err
		}
		err = r.deleteDNS(c, dir, provider, d, fqdn)
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) deleteDNS(c plan.Contract, dir Directory, provider string, d doomed, fqdn string) error {
	host, zone, _ := strings.Cut(fqdn, ".")
	done := func(what string, removed bool) {
		action := "delete"
		if !removed {
			action = "already gone"
		}
		r.printf("  %s %s: %s %s\n", provider, d.s.Hostname, what, action)
		r.change(runrecord.Change{Contract: c.Name, UID: d.s.UID, Hostname: d.s.Hostname, Stage: "decommission", Action: provider + " " + action, Detail: what})
	}
	removed, err := dir.DeleteRecord(zone, host, ad.Record{Type: ad.TypeA, Data: d.ip})
	if err != nil {
		return fmt.Errorf("%s: %w", d.s.Hostname, err)
	}
	done("A "+fqdn, removed)
	ptr, ok := d.dnsPTR[provider]
	if !ok {
		return nil
	}
	ip, err := netip.ParseAddr(d.ip)
	if err != nil {
		return fmt.Errorf("%s: address %q", d.s.Hostname, d.ip)
	}
	rz, node, err := dir.ReverseZone(ip)
	if errors.Is(err, ad.ErrNoZone) {
		done("PTR "+ptr, false)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", d.s.Hostname, err)
	}
	if node+"."+rz != ptr {
		return fmt.Errorf("%s: PTR %s was recorded, the address is now %s.%s", d.s.Hostname, ptr, node, rz)
	}
	removed, err = dir.DeleteRecord(rz, node, ad.Record{Type: ad.TypePTR, Data: fqdn})
	if err != nil {
		return fmt.Errorf("%s: %w", d.s.Hostname, err)
	}
	done("PTR "+ptr, removed)
	return nil
}
