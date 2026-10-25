package auth

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp/totp"

	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// setupLDAP is the lab with a Samba directory to sign in with, require
// mfa, and the directory's people Gaia knows (as a membership sync leaves
// them).
func setupLDAP(t *testing.T) (*env, *itest.Samba) {
	t.Helper()
	itest.Require(t)
	s := itest.StartSamba(t, filepath.Join("..", "..", "deploy", "dev", "ad-seed.sh"))
	g := labGraphWith(t, func(path, content string) string {
		if path != "platform/auth.yaml" {
			return content
		}
		content = strings.Replace(content, "require: passkey", "require: mfa", 1)
		content = strings.Replace(content, "  providers:\n", "  providers:\n    - name: corp\n      type: ldap\n      directory: { provider: lab-ad }\n", 1)
		return content + "---\napiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: lab-ad }\nspec: { type: activedirectory, endpoint: \"" + s.URL + "\", credentials: bao://kv/platform/ad }\n"
	})
	e := setupWith(t, g)
	e.auth.WithSecrets(secretMap{"bao://kv/platform/ad": {"username": "gaia-reader@gaia.dev", "password": s.Password("gaia-reader"), "ca": string(s.CA), "server_name": "dc1.gaia.dev"}})
	ctx := context.Background()
	for _, name := range []string{"can", "mehmet", "ali"} {
		id := store.NewUserID()
		if err := e.store.Q().InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{ID: id, Username: name, Provider: "lab-ad",
			DirectoryName: pgtype.Text{String: name, Valid: true}, Enabled: name != "ali", DisabledBy: pgtype.Text{String: "directory", Valid: name == "ali"}}); err != nil {
			t.Fatal(err)
		}
		team := "team-web"
		if name == "mehmet" {
			team = "team-platform"
		}
		if err := e.store.Q().AddTeam(ctx, db.AddTeamParams{UserID: id, Team: team, Source: "ad:lab-ad"}); err != nil {
			t.Fatal(err)
		}
	}
	return e, s
}

func TestLDAPSignInWithTOTP(t *testing.T) {
	e, s := setupLDAP(t)
	ctx := context.Background()
	b := e.noRedirect(nil)
	login := func(user, password string) (int, map[string]any) {
		return b.do("POST", "/api/v1/auth/ldap", map[string]string{"username": user, "password": password, "next": "/changes"})
	}
	if code, out := b.do("GET", "/api/v1/auth/config", nil); code != 200 || !strings.Contains(toJSON(out), `"type":"ldap"`) {
		t.Errorf("config: %d %v", code, out)
	}
	if code, out := login("can", "wrong"); code != 401 || out["error"] != errLDAP.Error() {
		t.Errorf("a wrong password: %d %v", code, out)
	}
	if code, _ := login("nobody", "x"); code != 401 {
		t.Errorf("no such account: %d", code)
	}
	if code, out := login("ali", s.Password("ali")); code != 403 || !strings.Contains(toJSON(out), "disabled") {
		t.Errorf("an account closed in the directory: %d %v", code, out)
	}
	if code, out := login("zeynep", s.Password("zeynep")); code != 403 || !strings.Contains(toJSON(out), "not in a team Gaia knows") {
		t.Errorf("someone Gaia does not know: %d %v", code, out)
	}

	// can's first sign-in: the password, then a TOTP to register.
	code, st := login("Can", s.Password("can"))
	if code != 200 || st["require"] != "mfa" || st["passkey"] != false || st["totp"] != false || st["username"] != "can" {
		t.Fatalf("can: %d %v", code, st)
	}
	if code, _ := b.do("GET", "/api/v1/me", nil); code != 401 {
		t.Fatalf("signed in with the password alone: %d", code)
	}
	if code, again := b.do("POST", "/api/v1/auth/pending", nil); code != 200 || again["totp"] != false {
		t.Errorf("pending: %d %v", code, again)
	}
	if code, _ := b.do("POST", "/api/v1/auth/pending/totp", map[string]string{"code": "000000"}); code != 409 {
		t.Errorf("a code before there is a TOTP: %d", code)
	}
	code, setup := b.do("POST", "/api/v1/auth/pending/totp/begin", nil)
	if code != 200 || setup["secret"] == "" {
		t.Fatalf("totp begin: %d %v", code, setup)
	}
	secret := setup["secret"].(string)
	now := time.Now()
	c1, _ := totp.GenerateCode(secret, now)
	code, done := b.do("POST", "/api/v1/auth/pending/totp/finish", map[string]string{"code": c1})
	if code != 200 || done["next"] != "/changes" || len(done["recoveryCodes"].([]any)) != recoveryCodes {
		t.Fatalf("totp finish: %d %v", code, done)
	}
	if code, me := b.do("GET", "/api/v1/me", nil); code != 200 || me["username"] != "can" {
		t.Errorf("me: %d %v", code, me)
	}

	// Next time: the password, then a code; no new TOTP.
	e.auth.now = func() time.Time { return now.Add(31 * time.Second) }
	b2 := e.noRedirect(nil)
	b = b2
	if code, st := login("can", s.Password("can")); code != 200 || st["totp"] != true {
		t.Fatalf("second sign-in: %d %v", code, st)
	}
	if code, _ := b2.do("POST", "/api/v1/auth/pending/totp/begin", nil); code != 409 {
		t.Errorf("a second TOTP over the first: %d", code)
	}
	if code, _ := b2.do("POST", "/api/v1/auth/pending/totp", map[string]string{"code": "000000"}); code != 401 {
		t.Errorf("a wrong code: %d", code)
	}
	c2, _ := totp.GenerateCode(secret, now.Add(31*time.Second))
	if code, out := b2.do("POST", "/api/v1/auth/pending/totp", map[string]string{"code": c2}); code != 200 {
		t.Fatalf("code: %d %v", code, out)
	}
	if code, _ := b2.do("GET", "/api/v1/me", nil); code != 200 {
		t.Errorf("after the code: %d", code)
	}

	log, _ := e.store.Q().AuditLog(ctx, db.AuditLogParams{MaxRows: 50, Action: pgtype.Text{String: "auth.", Valid: true}})
	ok, denied := 0, 0
	for _, l := range log {
		if l.Action == "auth.login" && l.Result == "ok" {
			ok++
		}
		if l.Action == "auth.login" && l.Result == "denied" {
			denied++
		}
	}
	if ok != 2 || denied < 4 {
		t.Errorf("audit: %d signed in, %d refused: %+v", ok, denied, log)
	}
}

func TestLDAPSignInWithAFirstPasskey(t *testing.T) {
	e, s := setupLDAP(t)
	ctx := context.Background()
	u, err := e.store.Q().UserByName(ctx, "mehmet")
	if err != nil {
		t.Fatal(err)
	}
	b := e.noRedirect([]byte(u.ID))
	if code, st := b.do("POST", "/api/v1/auth/ldap", map[string]string{"username": "mehmet", "password": s.Password("mehmet")}); code != 200 || st["passkey"] != false {
		t.Fatalf("mehmet: %d %v", code, st)
	}
	code, start := b.do("POST", "/api/v1/auth/pending/passkey/begin", nil)
	if code != 200 || start["register"] != true {
		t.Fatalf("passkey begin: %d %v", code, start)
	}
	att, _ := virtualwebauthn.ParseAttestationOptions(options(t, start))
	resp := virtualwebauthn.CreateAttestationResponse(rp, b.auth, b.cred, *att)
	b.auth.AddCredential(b.cred)
	if code, done := b.do("POST", "/api/v1/auth/pending/passkey/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp)}); code != 200 || done["next"] != "/" {
		t.Fatalf("passkey finish: %d %v", code, done)
	}
	if code, me := b.do("GET", "/api/v1/me", nil); code != 200 || !strings.Contains(toJSON(me), "platform-admin") {
		t.Errorf("me: %d %v", code, me)
	}

	// With a directory, local accounts are break-glass platform admins.
	if _, err := e.auth.CreateLocalUser(ctx, "temp", "", []string{"team-web"}, false); err == nil || !strings.Contains(err.Error(), "break-glass") {
		t.Errorf("a plain local account: %v", err)
	}
	if _, err := e.auth.CreateLocalUser(ctx, "temp", "", []string{"team-web"}, true); err == nil || !strings.Contains(err.Error(), "platform admin") {
		t.Errorf("a break-glass account without the role: %v", err)
	}
	if _, err := e.auth.CreateLocalUser(ctx, "breakglass", "", []string{"team-platform"}, true); err != nil {
		t.Errorf("a break-glass account: %v", err)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
