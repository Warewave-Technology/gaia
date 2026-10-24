// Package apply runs a plan against the systems: IPAM, compute and, later,
// configuration and registration. It is the only writer of Gaia's store.
package apply

import (
	"context"

	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// StoreState reads a plan's State from Gaia's database.
type StoreState struct{ S *store.Store }

func (s StoreState) LiveHosts(ctx context.Context, contract string) ([]plan.Host, error) {
	rows, err := s.S.Q().LiveHosts(ctx, contract)
	if err != nil {
		return nil, err
	}
	out := make([]plan.Host, len(rows))
	for i, h := range rows {
		out[i] = plan.Host{UID: h.Uid, Hostname: h.Hostname, Site: h.Site, Network: h.Network, Seq: int(h.Seq), Status: string(h.Status)}
	}
	return out, nil
}

func (s StoreState) NameHolder(ctx context.Context, name string) (string, error) {
	uid, err := s.S.Q().NameHolder(ctx, name)
	if store.IsNotFound(err) {
		return "", nil
	}
	return uid, err
}
