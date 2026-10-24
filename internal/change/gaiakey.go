package change

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// RegisterGaiaKey makes Gaia's signing key a registered key of the system
// account GaiaUser, so that Gaia's own commits pass the signature gate.
// The account cannot sign in: it has no passkey, password or OIDC
// subject, and enrollment is for local accounts only.
func RegisterGaiaKey(ctx context.Context, s *store.Store, pub ssh.PublicKey) error {
	q := s.Q()
	u, err := q.UserByName(ctx, GaiaUser)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		u.ID = store.NewUserID()
		err = q.InsertUser(ctx, db.InsertUserParams{
			ID: u.ID, Username: GaiaUser, DisplayName: pgtype.Text{String: "Gaia", Valid: true},
			Provider: "system", Subject: pgtype.Text{String: GaiaUser, Valid: true},
		})
		if err != nil {
			return err
		}
	case err != nil:
		return err
	case u.Provider != "system":
		return fmt.Errorf("the username %q is someone's account; Gaia needs it for its own key", GaiaUser)
	}
	fp := ssh.FingerprintSHA256(pub)
	k, err := q.SigningKey(ctx, fp)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return q.InsertSigningKey(ctx, db.InsertSigningKeyParams{
			Fingerprint: fp, UserID: u.ID, Kind: "ssh", Name: "gaia server",
			PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		})
	case err != nil:
		return err
	case k.UserID != u.ID:
		return fmt.Errorf("the signing key %s of Gaia is registered to %s", fp, k.Username)
	case k.RevokedAt.Valid:
		return fmt.Errorf("the signing key %s of Gaia was revoked; use a new one", fp)
	}
	return nil
}
