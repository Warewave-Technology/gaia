// Package apply runs a plan against the systems: IPAM, compute and, later,
// configuration and registration. It is the only writer of Gaia's store.
package apply

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
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
		out[i] = plan.Host{UID: h.Uid, Hostname: h.Hostname, Site: h.Site, Network: h.Network, Seq: int(h.Seq), Status: string(h.Status),
			Adopted: strings.HasPrefix(h.CreatedRun.String, "adopt:")}
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

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// StackSettings keeps Pulumi stack settings in Gaia's database.
type StackSettings struct{ S *store.Store }

func (s StackSettings) LoadStack(ctx context.Context, name string) (pulumi.Settings, bool, error) {
	row, err := s.S.Q().PulumiStack(ctx, name)
	if store.IsNotFound(err) {
		return pulumi.Settings{}, false, nil
	}
	if err != nil {
		return pulumi.Settings{}, false, err
	}
	return pulumi.Settings{SecretsProvider: row.SecretsProvider, EncryptedKey: row.EncryptedKey}, true, nil
}

func (s StackSettings) SaveStack(ctx context.Context, name string, v pulumi.Settings) error {
	return s.S.Q().SavePulumiStack(ctx, db.SavePulumiStackParams{Name: name, SecretsProvider: v.SecretsProvider, EncryptedKey: v.EncryptedKey})
}
