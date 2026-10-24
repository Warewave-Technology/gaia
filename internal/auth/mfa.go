package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"

	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Transit encrypts secrets Gaia must keep but never store in clear: TOTP
// seeds (design 12.4).
type Transit interface {
	Encrypt(ctx context.Context, key string, plaintext []byte) (string, error)
	Decrypt(ctx context.Context, key, ciphertext string) ([]byte, error)
}

// TransitKey is OpenBao's transit key for sign-in secrets.
const TransitKey = "gaia-auth"

// minPassword is the shortest password a local account may set.
const minPassword = 12

// WithTransit lets local accounts use password and TOTP (require: mfa).
func (a *Service) WithTransit(t Transit) *Service {
	a.transit = t
	return a
}

var errMFA = errors.New("the username, password or code is wrong")

func (a *Service) requireMFA() error {
	cfg, _, err := a.config()
	if err != nil {
		return err
	}
	if cfg.Require != "mfa" {
		return errors.New("this Gaia signs in with passkeys only")
	}
	if a.transit == nil {
		return errors.New("password and TOTP need OpenBao transit, which this server has not")
	}
	return nil
}

// TOTPSetup is what an authenticator app needs.
type TOTPSetup struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// BeginTOTP makes a new TOTP seed for an enrolling account and keeps it,
// encrypted, until the account confirms it with a code.
func (a *Service) BeginTOTP(ctx context.Context, token string) (*TOTPSetup, error) {
	if err := a.requireMFA(); err != nil {
		return nil, err
	}
	s, err := a.enrollment(ctx, token)
	if err != nil {
		return nil, err
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Gaia", AccountName: s.Username, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return nil, err
	}
	ct, err := a.transit.Encrypt(ctx, TransitKey, []byte(key.Secret()))
	if err != nil {
		return nil, err
	}
	if err := a.store.Q().SetTOTP(ctx, db.SetTOTPParams{UserID: s.UserID, Ciphertext: ct}); err != nil {
		return nil, err
	}
	return &TOTPSetup{Secret: key.Secret(), URI: key.URL()}, nil
}

// FinishTOTP sets the account's password, confirms its TOTP seed with a
// code, ends the enrollment link and signs the person in.
func (a *Service) FinishTOTP(ctx context.Context, w http.ResponseWriter, r *http.Request, token, password, code string) ([]string, error) {
	if err := a.requireMFA(); err != nil {
		return nil, err
	}
	s, err := a.enrollment(ctx, token)
	if err != nil {
		return nil, err
	}
	if len([]rune(password)) < minPassword {
		return nil, fmt.Errorf("the password needs at least %d characters", minPassword)
	}
	if err := a.checkTOTP(ctx, s.UserID, code, true); err != nil {
		return nil, err
	}
	err = a.store.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetPasswordHash(ctx, db.SetPasswordHashParams{ID: s.UserID, PasswordHash: text(hashPassword(password))}); err != nil {
			return err
		}
		if err := q.ConfirmTOTP(ctx, s.UserID); err != nil {
			return err
		}
		_, err := q.RevokeSession(ctx, db.RevokeSessionParams{ID: s.ID, UserID: s.UserID})
		return err
	})
	if err != nil {
		return nil, err
	}
	// The code that confirmed the seed is used up.
	_ = a.checkTOTP(ctx, s.UserID, code, false)
	codes, err := a.newRecoveryCodes(ctx, s.UserID)
	if err != nil {
		return nil, err
	}
	return codes, a.uiSession(ctx, w, r, s.UserID, true)
}

// PasswordLogin signs a local account in with its password and a TOTP
// code.
func (a *Service) PasswordLogin(ctx context.Context, w http.ResponseWriter, r *http.Request, username, password, code string) error {
	if err := a.requireMFA(); err != nil {
		return err
	}
	u, err := a.store.Q().UserByName(ctx, username)
	if err != nil || u.Provider != "local" || !u.Enabled || !u.PasswordHash.Valid {
		// The same work as a real check, so that timing does not tell
		// which usernames exist.
		_ = checkPassword(dummyHash, password)
		return errMFA
	}
	if !checkPassword(u.PasswordHash.String, password) {
		return errMFA
	}
	if err := a.checkTOTP(ctx, u.ID, code, false); err != nil {
		return errMFA
	}
	return a.uiSession(ctx, w, r, u.ID, true)
}

// checkTOTP checks a code against the account's seed. Unless pending (the
// seed is being confirmed), the code's time step is used up.
func (a *Service) checkTOTP(ctx context.Context, userID, code string, pending bool) error {
	row, err := a.store.Q().TOTP(ctx, userID)
	if err != nil {
		return errMFA
	}
	secret, err := a.transit.Decrypt(ctx, TransitKey, row.Ciphertext)
	if err != nil {
		return err
	}
	now := a.now()
	code = strings.TrimSpace(code)
	// One step either way for clock skew; find the step that matched.
	for _, off := range []int64{0, -1, 1} {
		t := now.Add(time.Duration(off*30) * time.Second)
		ok, err := totp.ValidateCustom(code, string(secret), t, totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil || !ok {
			continue
		}
		if pending {
			return nil
		}
		n, err := a.store.Q().UseTOTPStep(ctx, db.UseTOTPStepParams{UserID: userID, LastStep: t.Unix() / 30})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("this code was already used; wait for the next one")
		}
		return nil
	}
	return errMFA
}

// Argon2id parameters (RFC 9106, second recommended option).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonLen     = 32
)

func hashPassword(p string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	h := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, argonLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h))
}

func checkPassword(encoded, p string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &threads); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, t, m, threads, uint32(len(want))) //nolint:gosec // our own short hash
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash = hashPassword("not a password anyone has")
