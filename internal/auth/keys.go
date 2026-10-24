package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// SigningKey is a key a person signs commits with (design 7.6).
type SigningKey struct {
	Fingerprint string    `json:"fingerprint"`
	Kind        string    `json:"kind"` // ssh | gpg
	Name        string    `json:"name"`
	PublicKey   string    `json:"publicKey"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ParseSigningKey reads an SSH public key (authorized_keys format) or an
// armored OpenPGP public key and returns its kind, fingerprint and the key
// as it is stored. SSH fingerprints are SHA256:...; OpenPGP ones are the
// primary key's, in upper-case hex.
func ParseSigningKey(text string) (kind, fingerprint, stored string, err error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "BEGIN PGP PUBLIC KEY BLOCK") {
		ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(text))
		if err != nil || len(ring) != 1 {
			return "", "", "", errors.New("want exactly one OpenPGP public key")
		}
		fp := strings.ToUpper(hex.EncodeToString(ring[0].PrimaryKey.Fingerprint))
		return "gpg", fp, text, nil
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return "", "", "", errors.New("want an SSH public key (ssh-ed25519 AAAA…) or an armored OpenPGP public key")
	}
	stored = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		stored += " " + comment
	}
	return "ssh", ssh.FingerprintSHA256(pub), stored, nil
}

// AddSigningKey registers a key for a person. A key belongs to one person.
func (a *Service) AddSigningKey(ctx context.Context, userID, name, text string) (*SigningKey, error) {
	kind, fp, stored, err := ParseSigningKey(text)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = kind + " key"
	}
	err = a.store.Q().InsertSigningKey(ctx, db.InsertSigningKeyParams{Fingerprint: fp, UserID: userID, Kind: kind, Name: trunc(name, 80), PublicKey: stored})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return nil, fmt.Errorf("the key %s is already registered", fp)
	}
	if err != nil {
		return nil, err
	}
	return &SigningKey{Fingerprint: fp, Kind: kind, Name: name, PublicKey: stored, CreatedAt: a.now()}, nil
}

// Signer is the person a live signing key belongs to.
type Signer struct {
	UserID, Username, Fingerprint, Kind, PublicKey string
}

// Signers returns every live key of enabled people, for verifying commit
// signatures.
func (a *Service) Signers(ctx context.Context) ([]Signer, error) {
	rows, err := a.store.Q().AllSigningKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Signer, 0, len(rows))
	for _, k := range rows {
		out = append(out, Signer{UserID: k.UserID, Username: k.Username, Fingerprint: k.Fingerprint, Kind: k.Kind, PublicKey: k.PublicKey})
	}
	return out, nil
}

func (a *Service) mySigningKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.Q().SigningKeys(r.Context(), From(r.Context()).UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	out := []SigningKey{}
	for _, k := range rows {
		out = append(out, SigningKey{Fingerprint: k.Fingerprint, Kind: k.Kind, Name: k.Name, PublicKey: k.PublicKey, CreatedAt: k.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Service) addSigningKey(w http.ResponseWriter, r *http.Request) {
	var b struct{ Name, PublicKey string }
	if !decode(w, r, &b) {
		return
	}
	k, err := a.AddSigningKey(r.Context(), From(r.Context()).UserID, b.Name, b.PublicKey)
	if err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, k)
}

func (a *Service) revokeSigningKey(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.Q().RevokeSigningKey(r.Context(), db.RevokeSigningKeyParams{Fingerprint: r.PathValue("fingerprint"), UserID: From(r.Context()).UserID})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "no such key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LoginKey is an SSH key a person logs in to servers with (design 14.4).
type LoginKey struct {
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"publicKey"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (a *Service) myLoginKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.Q().LoginKeys(r.Context(), From(r.Context()).UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	out := []LoginKey{}
	for _, k := range rows {
		out = append(out, LoginKey{Fingerprint: k.Fingerprint, Name: k.Name, PublicKey: k.PublicKey, CreatedAt: k.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Service) addLoginKey(w http.ResponseWriter, r *http.Request) {
	var b struct{ Name, PublicKey string }
	if !decode(w, r, &b) {
		return
	}
	kind, fp, stored, err := ParseSigningKey(b.PublicKey)
	if err != nil || kind != "ssh" {
		problem(w, http.StatusBadRequest, "want an SSH public key (ssh-ed25519 AAAA…)")
		return
	}
	if b.Name == "" {
		b.Name = "ssh key"
	}
	err = a.store.Q().InsertLoginKey(r.Context(), db.InsertLoginKeyParams{Fingerprint: fp, UserID: From(r.Context()).UserID, Name: trunc(b.Name, 80), PublicKey: stored})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		problem(w, http.StatusConflict, "the key "+fp+" is already registered")
		return
	}
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	a.loginKeysChanged(r.Context(), From(r.Context()).UserID)
	writeJSON(w, http.StatusCreated, LoginKey{Fingerprint: fp, Name: b.Name, PublicKey: stored, CreatedAt: a.now()})
}

func (a *Service) revokeLoginKey(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.Q().RevokeLoginKey(r.Context(), db.RevokeLoginKeyParams{Fingerprint: r.PathValue("fingerprint"), UserID: From(r.Context()).UserID})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "no such key")
		return
	}
	a.loginKeysChanged(r.Context(), From(r.Context()).UserID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Service) loginKeysChanged(ctx context.Context, userID string) {
	if a.OnLoginKeys != nil {
		a.OnLoginKeys(context.WithoutCancel(ctx), userID)
	}
}
