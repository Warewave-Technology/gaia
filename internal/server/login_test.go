package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// tryLogin posts a login and returns the status, the Retry-After header and the error message.
func tryLogin(t *testing.T, f *fixture, user, pw string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.ts.URL+"/api/v1/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, user, pw)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e apiError
	if resp.StatusCode != 200 {
		json.NewDecoder(resp.Body).Decode(&e)
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), e.Error
}

func lastAudit(t *testing.T, f *fixture) store.AuditEntry {
	t.Helper()
	a, err := f.s.db.Audit(context.Background(), 0, 100000)
	if err != nil || len(a) == 0 {
		t.Fatal(err)
	}
	return a[len(a)-1]
}

// T18 gate (c) in miniature: repeated wrong passwords lock the username out (429 with Retry-After even for the right
// password), every attempt is an audit entry, the lockout is a state change and ends on its own or by an admin; an
// unknown username behaves the same; a client address with too many wrong passwords is refused whatever it tries.
func TestLoginProtection(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	f.s.cfg.LoginMaxFailures, f.s.cfg.LoginLockout, f.s.cfg.LoginAddressFailures = 3, "1m", 1000
	op, opPW := "operator1", "password of operator1"
	for i := 1; i <= 3; i++ {
		if st, _, _ := tryLogin(t, f, op, "wrong"); st != 401 {
			t.Fatalf("wrong password %d: %d", i, st)
		}
		a := lastAudit(t, f)
		if a.Outcome != 401 || a.Target != "user:"+op || !strings.Contains(a.Detail, fmt.Sprintf("wrong password (%d in a row)", i)) {
			t.Fatalf("audit of wrong password %d: %+v", i, a)
		}
	}
	if a := lastAudit(t, f); !strings.Contains(a.Detail, "locked out until") {
		t.Fatalf("the third wrong password does not say it locks: %+v", a)
	}
	st, retry, msg := tryLogin(t, f, op, opPW)
	if st != 429 || retry == "" || !strings.Contains(msg, "too many wrong passwords") {
		t.Fatalf("the right password while locked: %d %q %q", st, retry, msg)
	}
	if a := lastAudit(t, f); a.Outcome != 429 || !strings.HasPrefix(a.Detail, "login refused") {
		t.Fatalf("audit of a refused login: %+v", a)
	}
	u, _ := f.s.db.UserByName(ctx, op)
	changes, _ := f.s.db.StateChanges(ctx, "user_lockout", u.ID, 0, 10)
	if len(changes) != 1 || !strings.HasPrefix(changes[0].To, "locked until") || changes[0].Reason != "3 wrong passwords in a row" {
		t.Fatalf("lockout state change %+v", changes)
	}
	// The lockout ends by itself; a good login resets the count.
	real := f.s.now
	f.s.now = func() time.Time { return real().Add(2 * time.Minute) }
	if st, _, _ := tryLogin(t, f, op, opPW); st != 200 {
		t.Fatalf("after the lockout: %d", st)
	}
	f.s.now = real
	if u, _ = f.s.db.UserByName(ctx, op); u.FailedLogins != 0 || u.LockedUntil != "" {
		t.Fatalf("after a good login %+v", u)
	}

	// An unknown username gets the same answers: a lockout tells nothing about which names exist.
	for i := 1; i <= 3; i++ {
		if st, _, _ := tryLogin(t, f, "nobody", "wrong"); st != 401 {
			t.Fatalf("unknown user %d: %d", i, st)
		}
	}
	if st, retry, msg := tryLogin(t, f, "nobody", "wrong"); st != 429 || retry == "" || !strings.Contains(msg, "too many wrong passwords") {
		t.Fatalf("unknown user locked: %d %q %q", st, retry, msg)
	}

	// An admin ends a lockout.
	aud, audPW := "auditor1", "password of auditor1"
	for range 3 {
		tryLogin(t, f, aud, "wrong")
	}
	if st, _, _ := tryLogin(t, f, aud, audPW); st != 429 {
		t.Fatalf("auditor not locked: %d", st)
	}
	ua, _ := f.s.db.UserByName(ctx, aud)
	if st := f.do(f.tokens[store.RoleOperator], "POST", fmt.Sprintf("/api/v1/users/%d/unlock", ua.ID), "", nil); st != 403 {
		t.Fatalf("an operator unlocks: %d", st)
	}
	var unlocked store.User
	if st := f.do(f.tokens[store.RoleAdmin], "POST", fmt.Sprintf("/api/v1/users/%d/unlock", ua.ID), "", &unlocked); st != 200 || unlocked.FailedLogins != 0 {
		t.Fatalf("unlock: %d %+v", st, unlocked)
	}
	if st, _, _ := tryLogin(t, f, aud, audPW); st != 200 {
		t.Fatalf("after the unlock: %d", st)
	}
	changes, _ = f.s.db.StateChanges(ctx, "user_lockout", ua.ID, 0, 10)
	if len(changes) != 2 || changes[1].To != "unlocked" || changes[1].Actor != "admin" {
		t.Fatalf("unlock state change %+v", changes)
	}

	// A wrong current password in a password change counts too.
	for range 3 {
		f.do(f.tokens[store.RoleOperator], "PUT", "/api/v1/me/password", `{"current_password":"wrong","new_password":"a new password for op"}`, nil)
	}
	if st, _, _ := tryLogin(t, f, op, opPW); st != 429 {
		t.Fatalf("wrong current passwords did not lock: %d", st)
	}

	// The client address: every wrong password above came from it.
	f.s.cfg.LoginAddressFailures = 5
	st, retry, msg = tryLogin(t, f, "admin", adminPassword)
	if st != 429 || retry == "" || !strings.Contains(msg, "wrong passwords from 127.0.0.1") {
		t.Fatalf("address limit: %d %q %q", st, retry, msg)
	}
	if v, err := f.s.db.VerifyAudit(ctx); err != nil || !v.Intact {
		t.Fatalf("audit chain %+v %v", v, err)
	}
}
