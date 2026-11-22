package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The T26 gate: one test per ADR-008 rule. The subtests' names start with the rule's section; the report's table of
// rule -> test -> result is made from `go test -json -run 'TestADR008'` (lab/lab-vm/t26-gate.sh).

// testClock is a clock the tests move: the server's and the store's.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (f *fixture) clock() *testClock {
	c := &testClock{t: time.Now().UTC()}
	f.s.now = c.now
	f.s.db.SetClock(c.now)
	return c
}

// call sends a request and returns the status and the error code of a refusal.
func (f *fixture) call(token, method, path, body string, headers map[string]string, out any) (int, apiError) {
	f.t.Helper()
	var raw []byte
	st := f.doH(token, method, path, body, headers, &raw)
	var e apiError
	if st >= 400 {
		json.Unmarshal(raw, &e)
	} else if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			f.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return st, e
}

// newUser makes a local user through the API (with the admin's step-up).
func (f *fixture) newUser(name, role string) store.User {
	f.t.Helper()
	var u store.User
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/users", fmt.Sprintf(`{"username":%q,"role":%q,"password":%q}`, name, role, userPassword(name)), &u); st != 201 {
		f.t.Fatalf("user %s: %d", name, st)
	}
	return u
}

// enrolTOTP signs a new user in for the first time with an authenticator app; it returns the session, the secret and
// the recovery codes.
func (f *fixture) enrolTOTP(name string) (string, []byte, []string) {
	f.t.Helper()
	var st SigninStep
	f.call("", "POST", "/api/v1/login", fmt.Sprintf(`{"username":%q,"password":%q}`, name, userPassword(name)), nil, &st)
	if st.Stage != stageEnrol {
		f.t.Fatalf("first sign-in of %s: %+v", name, st)
	}
	var ch EnrolChallenge
	if c, e := f.call("", "POST", "/api/v1/login/enrol", fmt.Sprintf(`{"signin_token":%q,"kind":"totp"}`, st.SigninToken), nil, &ch); c != 200 {
		f.t.Fatalf("enrol totp: %d %+v", c, e)
	}
	secret, _ := auth.ParseTOTPSecret(ch.TOTPSecret)
	code := auth.TOTPCode(secret, auth.TOTPStepAt(f.s.now()))
	if c, e := f.call("", "POST", "/api/v1/login/enrol/finish", fmt.Sprintf(`{"signin_token":%q,"kind":"totp","code":%q}`, st.SigninToken, code), nil, &st); c != 200 || st.Stage != stageDone {
		f.t.Fatalf("enrol finish: %d %+v %+v", c, e, st)
	}
	f.owners[st.Token] = name
	return st.Token, secret, st.RecoveryCodes
}

// password starts a sign-in and returns its step.
func (f *fixture) password(name, pw string, extra string) (int, apiError, SigninStep) {
	f.t.Helper()
	var st SigninStep
	c, e := f.call("", "POST", "/api/v1/login", fmt.Sprintf(`{"username":%q,"password":%q%s}`, name, pw, extra), nil, &st)
	return c, e, st
}

func (f *fixture) totpSignIn(name string, code string) (int, apiError, SigninStep) {
	f.t.Helper()
	c, e, st := f.password(name, userPassword(name), "")
	if c != 200 {
		return c, e, st
	}
	c, e = f.call("", "POST", "/api/v1/login/factor", fmt.Sprintf(`{"signin_token":%q,"method":"totp","code":%q}`, st.SigninToken, code), nil, &st)
	return c, e, st
}

func (f *fixture) audits(t *testing.T) []store.AuditEntry {
	t.Helper()
	es, err := f.s.db.Audit(context.Background(), 0, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func hasAudit(es []store.AuditEntry, action, detail string) bool {
	for _, e := range es {
		if e.Action == action && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

func (f *fixture) stateChanges(t *testing.T, entity string) []store.StateChange {
	t.Helper()
	cs, err := f.s.db.StateChanges(context.Background(), entity, 0, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestADR008Local(t *testing.T) {
	f := newFixture(t)
	f.seed()
	clk := f.clock()
	admin, op, aud := f.tokens[store.RoleAdmin], f.tokens[store.RoleOperator], f.tokens[store.RoleAuditor]
	ctx := context.Background()
	// relogin gives the fixture's users fresh sessions after a subtest moved the clock past their idle limit.
	relogin := func() {
		admin, op, aud = f.login(store.RoleAdmin), f.login(store.RoleOperator), f.login(store.RoleAuditor)
		f.tokens[store.RoleOperator], f.tokens[store.RoleAdmin], f.tokens[store.RoleAuditor] = op, admin, aud
	}

	t.Run("§10 no API call that changes state succeeds without a session or token", func(t *testing.T) {
		// The sign-in steps are the only public calls that are not reads; the role matrix shows every other route 401
		// without a session (TestRoleMatrix).
		signin := []string{"/api/v1/login", "/api/v1/login/factor", "/api/v1/login/enrol", "/api/v1/login/enrol/finish", "/api/v1/login/password",
			"/api/v1/login/cli", "/api/v1/login/cli/token", "/api/v1/oidc/device", "/api/v1/oidc/device/token"}
		for _, rt := range f.s.routes {
			if rt.roles == nil && rt.method != "GET" && !slices.Contains(signin, rt.path) {
				t.Errorf("%s %s is public and not a sign-in step", rt.method, rt.path)
			}
			if rt.roles != nil {
				if st, _ := f.call("", rt.method, f.path(rt), rt.example, nil, nil); st != 401 {
					t.Errorf("%s %s without a session: %d", rt.method, rt.path, st)
				}
			}
		}
	})

	t.Run("§3 enrolment happens at the first sign-in and cannot be skipped", func(t *testing.T) {
		f.newUser("fresh1", store.RoleOperator)
		c, _, st := f.password("fresh1", userPassword("fresh1"), "")
		if c != 200 || st.Stage != stageEnrol || st.Token != "" || !slices.Equal(st.Enrol, []string{"webauthn", "totp"}) {
			t.Fatalf("%d %+v", c, st)
		}
		if c, _ := f.call(st.SigninToken, "GET", "/api/v1/me", "", nil, nil); c != 401 {
			t.Fatalf("the sign-in token as a session: %d", c)
		}
		if c, _ := f.call("", "POST", "/api/v1/login/factor", fmt.Sprintf(`{"signin_token":%q,"method":"totp","code":"000000"}`, st.SigninToken), nil, nil); c != 400 {
			t.Fatalf("a factor instead of the enrolment: %d", c)
		}
	})

	t.Run("§3 an admin's second factor is a security key only", func(t *testing.T) {
		u := f.newUser("admin2", store.RoleAdmin)
		_, _, st := f.password("admin2", userPassword("admin2"), "")
		if !slices.Equal(st.Enrol, []string{"webauthn"}) {
			t.Fatalf("an admin may enrol %v", st.Enrol)
		}
		if c, _ := f.call("", "POST", "/api/v1/login/enrol", fmt.Sprintf(`{"signin_token":%q,"kind":"totp"}`, st.SigninToken), nil, nil); c != 400 {
			t.Fatalf("an admin enrols TOTP: %d", c)
		}
		if _, err := f.s.db.AddFactor(ctx, "test", u.ID, store.FactorTOTP, "app", "", "AAAA", 0); err == nil {
			t.Fatal("the store took a TOTP factor for an admin")
		}
	})

	var opTOTP string
	var opSecret []byte
	var opCodes []string
	t.Run("§3 TOTP: 30 s, 6 digits, ±1 step, a used code is rejected", func(t *testing.T) {
		f.newUser("totp1", store.RoleOperator)
		opTOTP, opSecret, opCodes = f.enrolTOTP("totp1")
		clk.add(31 * time.Second)
		cur := auth.TOTPStepAt(clk.now())
		if c, e, st := f.totpSignIn("totp1", auth.TOTPCode(opSecret, cur+1)); c != 200 || st.Stage != stageDone {
			t.Fatalf("one step ahead: %d %+v", c, e)
		}
		if c, _, _ := f.totpSignIn("totp1", auth.TOTPCode(opSecret, cur+1)); c != 401 {
			t.Fatalf("the same code again: %d", c)
		}
		clk.add(90 * time.Second)
		cur = auth.TOTPStepAt(clk.now())
		if c, _, _ := f.totpSignIn("totp1", auth.TOTPCode(opSecret, cur-2)); c != 401 {
			t.Fatalf("two steps old: %d", c)
		}
		f.s.db.SQL().Exec(`DELETE FROM signin_failures`)
	})

	t.Run("§3 10 single-use recovery codes, stored hashed, each use audited", func(t *testing.T) {
		if len(opCodes) != 10 {
			t.Fatalf("%d codes", len(opCodes))
		}
		var n int
		f.s.db.SQL().QueryRow(`SELECT count(*) FROM recovery_codes WHERE code_sha256 = ?`, opCodes[0]).Scan(&n)
		if n != 0 {
			t.Fatal("a recovery code is stored in clear")
		}
		signin := func(code string) int {
			_, _, st := f.password("totp1", userPassword("totp1"), "")
			c, _ := f.call("", "POST", "/api/v1/login/factor", fmt.Sprintf(`{"signin_token":%q,"method":"recovery_code","code":%q}`, st.SigninToken, code), nil, &st)
			return c
		}
		if c := signin(strings.ToUpper(opCodes[3])); c != 200 {
			t.Fatalf("a recovery code: %d", c)
		}
		if c := signin(opCodes[3]); c != 401 {
			t.Fatalf("the same recovery code again: %d", c)
		}
		if !hasAudit(f.audits(t), "POST /api/v1/login/factor", "recovery code (9 left) accepted") {
			t.Fatal("the use of a recovery code is not in the audit log")
		}
		f.s.db.SQL().Exec(`DELETE FROM signin_failures`)
	})

	t.Run("§3 an admin's reset of a user's factors: the user enrols again", func(t *testing.T) {
		var u store.User
		f.s.db.SQL().QueryRow(`SELECT id FROM users WHERE username = 'totp1'`).Scan(&u.ID)
		if st := f.do(admin, "POST", fmt.Sprintf("/api/v1/users/%d/factors/reset", u.ID), "", nil); st != 204 {
			t.Fatalf("reset: %d", st)
		}
		if c, e := f.call(opTOTP, "GET", "/api/v1/me", "", nil, nil); c != 401 || e.Reason != store.EndFactorChange {
			t.Fatalf("the user's session after the reset: %d %+v", c, e)
		}
		if _, _, st := f.password("totp1", userPassword("totp1"), ""); st.Stage != stageEnrol {
			t.Fatalf("after the reset: %+v", st)
		}
	})

	t.Run("§4 every step-up action fails without a fresh factor", func(t *testing.T) {
		n := 0
		for _, rt := range f.s.routes {
			if rt.stepUp != stepAlways {
				continue
			}
			n++
			if c, e := f.call(admin, rt.method, f.path(rt), rt.example, nil, nil); c != 403 || e.Code != "step_up_required" {
				t.Errorf("%s %s without a step-up: %d %+v", rt.method, rt.path, c, e)
			}
		}
		if n < 15 {
			t.Fatalf("only %d step-up actions", n)
		}
	})

	t.Run("§4 a step-up older than 120 s, bound to another action, or used twice fails", func(t *testing.T) {
		forget := fmt.Sprintf("/api/v1/points/%d/forget", f.ids["points"])
		other := "/api/v1/evidence-export"
		h := f.stepUp(admin, "GET", other)
		if c, e := f.call(admin, "POST", forget, `{"reason":""}`, h, nil); c != 403 || e.Code != "step_up_failed" || !strings.Contains(e.Error, "not for this action") {
			t.Fatalf("a step-up for another action: %d %+v", c, e)
		}
		h = f.stepUp(admin, "GET", other)
		clk.add(121 * time.Second)
		if c, e := f.call(admin, "GET", other, "", h, nil); c != 403 || !strings.Contains(e.Error, "older than 120 seconds") {
			t.Fatalf("121 s old: %d %+v", c, e)
		}
		h = f.stepUp(admin, "GET", other)
		if c, _ := f.call(admin, "GET", other, "", h, nil); c != 200 {
			t.Fatalf("a fresh step-up: %d", c)
		}
		if c, e := f.call(admin, "GET", other, "", h, nil); c != 403 || !strings.Contains(e.Error, "used already") {
			t.Fatalf("the same step-up twice: %d %+v", c, e)
		}
		// Another user's session cannot use it either.
		h = f.stepUp(admin, "GET", other)
		if c, e := f.call(aud, "GET", other, "", h, nil); c != 403 || !strings.Contains(e.Error, "another session") {
			t.Fatalf("another session: %d %+v", c, e)
		}
		es := f.audits(t)
		for _, d := range []string{"step-up asked", "step-up passed", "step-up failed"} {
			if !hasAudit(es, "POST /api/v1/step-up", d) && !hasAudit(es, "step_up", d) {
				t.Errorf("no audit entry %q", d)
			}
		}
	})

	t.Run("§4 a step-up approved in the user's browser passes the CLI's action, and only the same user's", func(t *testing.T) {
		cli, web := f.login(store.RoleAdmin), f.login(store.RoleAdmin)
		var ch StepUpChallenge
		f.call(cli, "POST", "/api/v1/step-up", `{"method":"GET","path":"/api/v1/evidence-export"}`, nil, &ch)
		if ch.ApproveURL == "" {
			t.Fatalf("no approval page for a security key: %+v", ch)
		}
		if c, _ := f.call(cli, "GET", "/api/v1/evidence-export", "", map[string]string{"X-Gaia-Step-Up": ch.ID}, nil); c != 403 {
			t.Fatalf("before the approval: %d", c)
		}
		f.call(cli, "POST", "/api/v1/step-up", `{"method":"GET","path":"/api/v1/evidence-export"}`, nil, &ch)
		body, _ := json.Marshal(StepUpApprove{ID: ch.ID, StepUpProof: StepUpProof{Method: "webauthn", WebAuthn: f.keys["admin"].get(t, ch.WebAuthn)}})
		if c, _ := f.call(aud, "POST", "/api/v1/step-up/approve", string(body), nil, nil); c != 404 {
			t.Fatalf("another user approves: %d", c)
		}
		if c, e := f.call(web, "POST", "/api/v1/step-up/approve", string(body), nil, nil); c != 204 {
			t.Fatalf("approve: %d %+v", c, e)
		}
		if c, _ := f.call(cli, "GET", "/api/v1/evidence-export", "", map[string]string{"X-Gaia-Step-Up": ch.ID}, nil); c != 200 {
			t.Fatalf("after the approval: %d", c)
		}
		admin = f.login(store.RoleAdmin) // three sessions: the fixture's own may have been the oldest
		f.tokens[store.RoleAdmin] = admin
	})

	t.Run("§4 the step-up challenge holds the action and its target", func(t *testing.T) {
		a := stepUpChallenge("id", "GET", "/api/v1/evidence-export")
		b := stepUpChallenge("id", "POST", "/api/v1/points/1/forget")
		if len(a) != 48 || string(a[16:]) == string(b[16:]) {
			t.Fatal("the challenge does not depend on the action")
		}
	})

	t.Run("§4 weakening a policy needs a step-up, tightening does not", func(t *testing.T) {
		p := fmt.Sprintf("/api/v1/policies/%d", f.ids["policies"])
		if c, e := f.call(op, "PATCH", p, `{"test_interval_s":172800}`, nil, nil); c != 403 || e.Code != "step_up_required" {
			t.Fatalf("a longer test interval: %d %+v", c, e)
		}
		if c, e := f.call(op, "PATCH", p, `{"keep_last":2}`, nil, nil); c != 403 || e.Code != "step_up_required" {
			t.Fatalf("fewer points kept: %d %+v", c, e)
		}
		if c, _ := f.call(op, "PATCH", p, `{"rpo_target_s":1800}`, nil, nil); c != 200 {
			t.Fatalf("a tighter RPO target: %d", c)
		}
		if c, _ := f.call(op, "PATCH", p, `{"rpo_target_s":3600}`, f.stepUp(op, "PATCH", p), nil); c != 200 {
			t.Fatalf("a looser RPO target with a step-up: %d", c)
		}
		v := fmt.Sprintf("/api/v1/vms/%d", f.ids["vms"])
		if c, e := f.call(op, "PATCH", v, `{"policy_id":0}`, nil, nil); c != 403 || e.Code != "step_up_required" {
			t.Fatalf("removing a VM's policy: %d %+v", c, e)
		}
	})

	t.Run("§5 idle timeout: background requests do not keep a session alive", func(t *testing.T) {
		tok := f.login(store.RoleOperator)
		bg := map[string]string{"X-Gaia-Background": "1"}
		clk.add(10 * time.Minute)
		if c, _ := f.call(tok, "GET", "/api/v1/views/fleet", "", bg, nil); c != 200 {
			t.Fatalf("a background request at 10 min: %d", c)
		}
		clk.add(5*time.Minute + time.Second)
		if c, e := f.call(tok, "GET", "/api/v1/views/fleet", "", bg, nil); c != 401 || e.Code != "session_ended" || e.Reason != store.EndIdle {
			t.Fatalf("15 min with only background requests: %d %+v", c, e)
		}
		tok = f.login(store.RoleOperator)
		for i := 0; i < 3; i++ {
			clk.add(14 * time.Minute)
			if c, _ := f.call(tok, "GET", "/api/v1/views/fleet", "", nil, nil); c != 200 {
				t.Fatalf("a user's request after 14 min: %d", c)
			}
		}
		op = f.login(store.RoleOperator)
		admin, aud = f.login(store.RoleAdmin), f.login(store.RoleAuditor)
		f.tokens[store.RoleOperator], f.tokens[store.RoleAdmin], f.tokens[store.RoleAuditor] = op, admin, aud
	})

	t.Run("§5 absolute timeout: 8 h whatever the activity", func(t *testing.T) {
		tok := f.login(store.RoleAuditor)
		for i := 0; i < 34; i++ {
			clk.add(14 * time.Minute)
			f.call(tok, "GET", "/api/v1/me", "", nil, nil)
			f.call(admin, "GET", "/api/v1/me", "", nil, nil)
			f.call(op, "GET", "/api/v1/me", "", nil, nil)
			f.call(aud, "GET", "/api/v1/me", "", nil, nil)
		}
		clk.add(5 * time.Minute) // 8 h 1 min
		if c, e := f.call(tok, "GET", "/api/v1/me", "", nil, nil); c != 401 || e.Reason != store.EndAbsolute {
			t.Fatalf("after 8 h: %d %+v", c, e)
		}
		op, admin, aud = f.login(store.RoleOperator), f.login(store.RoleAdmin), f.login(store.RoleAuditor)
		f.tokens[store.RoleOperator], f.tokens[store.RoleAdmin], f.tokens[store.RoleAuditor] = op, admin, aud
	})

	t.Run("§5 at most 3 sessions per user; a fourth ends the oldest", func(t *testing.T) {
		a, b, c := f.login(store.RoleAuditor), f.login(store.RoleAuditor), f.login(store.RoleAuditor)
		if st, e := f.call(aud, "GET", "/api/v1/me", "", nil, nil); st != 401 || e.Reason != store.EndReplaced {
			t.Fatalf("the oldest of four: %d %+v", st, e)
		}
		for _, tok := range []string{a, b, c} {
			if st, _ := f.call(tok, "GET", "/api/v1/me", "", nil, nil); st != 200 {
				t.Fatalf("a newer session: %d", st)
			}
		}
		aud = c
		f.tokens[store.RoleAuditor] = c
	})

	t.Run("§5 sign-out ends the session on the server", func(t *testing.T) {
		tok := f.login(store.RoleOperator)
		if st := f.do(tok, "POST", "/api/v1/logout", "", nil); st != 204 {
			t.Fatalf("logout: %d", st)
		}
		if c, e := f.call(tok, "GET", "/api/v1/me", "", nil, nil); c != 401 || e.Reason != store.EndSignedOut {
			t.Fatalf("after sign-out: %d %+v", c, e)
		}
	})

	t.Run("§5 a password, factor or role change ends the user's other sessions", func(t *testing.T) {
		u := f.newUser("chg1", store.RoleOperator)
		t1 := f.signIn("chg1")
		t2 := f.signIn("chg1")
		body := fmt.Sprintf(`{"current_password":%q,"new_password":"another long password for chg"}`, userPassword("chg1"))
		if c, e := f.call(t1, "PUT", "/api/v1/me/password", body, f.stepUp(t1, "PUT", "/api/v1/me/password"), nil); c != 204 {
			t.Fatalf("password change: %d %+v", c, e)
		}
		if c, e := f.call(t2, "GET", "/api/v1/me", "", nil, nil); c != 401 || e.Reason != store.EndPasswordChange {
			t.Fatalf("the other session after a password change: %d %+v", c, e)
		}
		if c, _ := f.call(t1, "GET", "/api/v1/me", "", nil, nil); c != 200 {
			t.Fatalf("the session that changed it: %d", c)
		}
		role := store.RoleAuditor
		if c, _ := f.call(admin, "PATCH", fmt.Sprintf("/api/v1/users/%d", u.ID), fmt.Sprintf(`{"role":%q}`, role), f.stepUp(admin, "PATCH", fmt.Sprintf("/api/v1/users/%d", u.ID)), nil); c != 200 {
			t.Fatal("role change")
		}
		if c, e := f.call(t1, "GET", "/api/v1/me", "", nil, nil); c != 401 || e.Reason != store.EndRoleChange {
			t.Fatalf("after a role change: %d %+v", c, e)
		}
	})

	t.Run("§5 a sign-in from a new device or address is marked and notifies admins", func(t *testing.T) {
		f.newUser("dev1", store.RoleOperator)
		f.signIn("dev1")
		_, _, st := f.password("dev1", userPassword("dev1"), `,"device_id":"another-cli-device-id-0123456789"`)
		body, _ := json.Marshal(FactorAnswer{SigninToken: st.SigninToken, Method: "webauthn", WebAuthn: f.keys["dev1"].get(t, st.WebAuthn)})
		if c, _ := f.call("", "POST", "/api/v1/login/factor", string(body), nil, &st); c != 200 || !st.NewDevice {
			t.Fatalf("a second device: %d %+v", c, st)
		}
		cs := f.stateChanges(t, "signin")
		if len(cs) == 0 || cs[len(cs)-1].To != evNewDevice {
			t.Fatalf("no new_device_signin state change: %+v", cs)
		}
		ev, err := f.s.classify(ctx, cs[len(cs)-1])
		if err != nil || ev == nil || ev.Event != evNewDevice {
			t.Fatalf("not an event: %+v %v", ev, err)
		}
	})

	t.Run("§6 password rules: 15-128 characters, the blocklist, the user and instance names", func(t *testing.T) {
		for _, pw := range []string{"fourteen chars", strings.Repeat("x1y2z3", 22), "1qaz2wsx3edc4rfv", "rules1 is my name ok", "the " + f.s.instance + " server!"} {
			c, e := f.call(admin, "POST", "/api/v1/users", fmt.Sprintf(`{"username":"rules1","role":"operator","password":%q}`, pw),
				f.stepUp(admin, "POST", "/api/v1/users"), nil)
			if c != 400 {
				t.Errorf("password %q: %d %+v", pw, c, e)
			}
		}
	})

	t.Run("§6 not one of the last 5 passwords", func(t *testing.T) {
		tok := f.signIn("dev1")
		cur := userPassword("dev1")
		for i := 0; i < 6; i++ {
			np := fmt.Sprintf("rotation password number %d!", i)
			if i == 5 {
				np = userPassword("dev1") // 6 changes ago: allowed again
			}
			body := fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, cur, np)
			if c, e := f.call(tok, "PUT", "/api/v1/me/password", body, f.stepUp(tok, "PUT", "/api/v1/me/password"), nil); c != 204 {
				t.Fatalf("change %d: %d %+v", i, c, e)
			}
			cur = np
		}
		body := fmt.Sprintf(`{"current_password":%q,"new_password":"rotation password number 3!"}`, cur)
		if c, _ := f.call(tok, "PUT", "/api/v1/me/password", body, f.stepUp(tok, "PUT", "/api/v1/me/password"), nil); c != 400 {
			t.Fatalf("one of the last 5: %d", c)
		}
	})

	t.Run("§6 periodic change: with an expiry, a reminder 14 days before and a new password first at expiry", func(t *testing.T) {
		set := func(age string) {
			body := fmt.Sprintf(`{"idle_timeout_min":15,"absolute_timeout_h":8,"password_max_age":%q}`, age)
			if c, e := f.call(admin, "PUT", "/api/v1/settings/signin", body, f.stepUp(admin, "PUT", "/api/v1/settings/signin"), nil); c != 200 {
				t.Fatalf("settings: %d %+v", c, e)
			}
		}
		set("90d")
		tok := f.signIn("dev1")
		clk.add(77 * 24 * time.Hour)
		tok = f.signIn("dev1")
		var me Me
		f.call(tok, "GET", "/api/v1/me", "", nil, &me)
		if !me.PasswordSoon || me.PasswordExpiresAt == "" {
			t.Fatalf("no reminder 13 days before: %+v", me)
		}
		clk.add(14 * 24 * time.Hour)
		if c, e := f.call(tok, "GET", "/api/v1/views/fleet", "", nil, nil); c != 401 && e.Code != "password_change_required" {
			t.Fatalf("with an expired password: %d %+v", c, e)
		}
		_, _, st := f.password("dev1", userPassword("dev1"), "")
		body, _ := json.Marshal(FactorAnswer{SigninToken: st.SigninToken, Method: "webauthn", WebAuthn: f.keys["dev1"].get(t, st.WebAuthn)})
		f.call("", "POST", "/api/v1/login/factor", string(body), nil, &st)
		if st.Stage != stagePassword || st.PasswordChange != "expired" {
			t.Fatalf("sign-in with an expired password: %+v", st)
		}
		if c, _ := f.call("", "POST", "/api/v1/login/password", fmt.Sprintf(`{"signin_token":%q,"new_password":"a fresh password after 90 days"}`, st.SigninToken), nil, &st); c != 200 || st.Stage != stageDone {
			t.Fatalf("the new password: %d %+v", c, st)
		}
		// Every fixture password is 91 days old now: back to no expiry before they sign in again.
		if err := f.s.db.SetSetting(ctx, "test", settingSignin, defaultSettings()); err != nil {
			t.Fatal(err)
		}
		f.s.db.SQL().Exec(`UPDATE users SET expires_at = ? WHERE role = 'auditor'`, clk.now().Add(store.AuditorMaxAge).Format(time.RFC3339Nano)) // and 91 days > 30
		admin, op, aud = f.login(store.RoleAdmin), f.login(store.RoleOperator), f.login(store.RoleAuditor)
		f.tokens[store.RoleOperator], f.tokens[store.RoleAdmin], f.tokens[store.RoleAuditor] = op, admin, aud
	})

	t.Run("§6 forced change after an admin's reset, after a blocklist update, and for a short password from before 0.2.0", func(t *testing.T) {
		u := f.newUser("forced1", store.RoleOperator)
		f.signIn("forced1")
		path := fmt.Sprintf("/api/v1/users/%d", u.ID)
		if c, _ := f.call(admin, "PATCH", path, `{"password":"an admin picked this password"}`, f.stepUp(admin, "PATCH", path), nil); c != 200 {
			t.Fatal("reset")
		}
		c, _, st := f.password("forced1", "an admin picked this password", "")
		body, _ := json.Marshal(FactorAnswer{SigninToken: st.SigninToken, Method: "webauthn", WebAuthn: f.keys["forced1"].get(t, st.WebAuthn)})
		f.call("", "POST", "/api/v1/login/factor", string(body), nil, &st)
		if c != 200 || st.Stage != stagePassword || st.PasswordChange != store.PasswordChangeAdminReset {
			t.Fatalf("after an admin's reset: %+v", st)
		}
		// A password set before the list had it: found at the next sign-in.
		h, _ := store.HashPassword("1qaz2wsx3edc4rfv")
		f.s.db.SQL().Exec(`UPDATE users SET password_hash = ?, password_change = '', blocklist_version = 'older' WHERE id = ?`, h, u.ID)
		_, _, st = f.password("forced1", "1qaz2wsx3edc4rfv", "")
		body, _ = json.Marshal(FactorAnswer{SigninToken: st.SigninToken, Method: "webauthn", WebAuthn: f.keys["forced1"].get(t, st.WebAuthn)})
		f.call("", "POST", "/api/v1/login/factor", string(body), nil, &st)
		if st.Stage != stagePassword || st.PasswordChange != store.PasswordChangeBlocklist {
			t.Fatalf("a password on a newer blocklist: %+v", st)
		}
		// A password shorter than 15 characters set before 0.2.0 (ADR-008 amendment 5): changed at the first sign-in
		// after the upgrade, the same way, even when the blocklist check was done already; the session needs the new one.
		h, _ = store.HashPassword("short-pw-0.1.2")
		f.s.db.SQL().Exec(`UPDATE users SET password_hash = ?, password_change = '' WHERE id = ?`, h, u.ID)
		f.s.db.NoteBlocklistChecked(context.Background(), u.ID)
		_, _, st = f.password("forced1", "short-pw-0.1.2", "")
		body, _ = json.Marshal(FactorAnswer{SigninToken: st.SigninToken, Method: "webauthn", WebAuthn: f.keys["forced1"].get(t, st.WebAuthn)})
		f.call("", "POST", "/api/v1/login/factor", string(body), nil, &st)
		if st.Stage != stagePassword || st.PasswordChange != store.PasswordChangeTooShort || st.Token != "" {
			t.Fatalf("a short password from before 0.2.0: %+v", st)
		}
		if cs, _ := f.s.db.StateChanges(context.Background(), "user_password", u.ID, 0, 100); len(cs) == 0 || cs[len(cs)-1].To != "must change: too_short" {
			t.Fatalf("recorded: %+v", cs)
		}
		var done SigninStep
		if c, _ := f.call("", "POST", "/api/v1/login/password", fmt.Sprintf(`{"signin_token":%q,"new_password":"a long enough new password"}`, st.SigninToken), nil, &done); c != 200 || done.Stage != stageDone {
			t.Fatalf("the new password: %d %+v", c, done)
		}
	})

	t.Run("§7 from the 3rd failure an increasing delay, per account and per address", func(t *testing.T) {
		f.s.db.SQL().Exec(`DELETE FROM signin_failures`)
		f.newUser("slow1", store.RoleOperator)
		for i := 0; i < 3; i++ {
			if c, _, _ := f.password("slow1", "a wrong password, again", ""); c != 401 {
				t.Fatalf("failure %d: %d", i+1, c)
			}
		}
		if c, e, _ := f.password("slow1", userPassword("slow1"), ""); c != 429 || !strings.Contains(e.Error, "3 failed attempts") || e.Reason != "delay" {
			t.Fatalf("right after the 3rd failure: %d %+v", c, e)
		}
		clk.add(time.Second)
		f.password("slow1", "a wrong password, again", "")
		clk.add(time.Second)
		if c, _, _ := f.password("slow1", userPassword("slow1"), ""); c != 429 {
			t.Fatalf("1 s after the 4th failure (the wait is 2 s): %d", c)
		}
		clk.add(time.Second)
		if c, _, _ := f.password("slow1", userPassword("slow1"), ""); c != 200 {
			t.Fatalf("2 s after the 4th failure: %d", c)
		}
		f.s.db.SQL().Exec(`DELETE FROM signin_failures`)
	})

	t.Run("§7 10 failures in 15 min pause the account from every address and notify admins", func(t *testing.T) {
		u := f.newUser("pause1", store.RoleOperator)
		for i := 0; i < 10; i++ {
			f.s.db.SQL().Exec(`DELETE FROM signin_failures WHERE remote_addr = '127.0.0.1' AND username != 'pause1'`)
			clk.add(time.Minute)
			if _, _, err := f.s.db.SigninFailed(ctx, "pause1", fmt.Sprintf("10.0.0.%d", i)); err != nil { // ten addresses
				t.Fatal(err)
			}
		}
		if c, e, _ := f.password("pause1", userPassword("pause1"), ""); c != 429 || !strings.Contains(e.Error, "paused") || e.Reason != "account_paused" {
			t.Fatalf("the right password while paused: %d %+v", c, e)
		}
		cs := f.stateChanges(t, "user_lockout")
		if len(cs) == 0 || cs[len(cs)-1].EntityID != u.ID || !strings.HasPrefix(cs[len(cs)-1].To, "locked until") {
			t.Fatalf("no lockout state change: %+v", cs)
		}
		if ev, _ := f.s.classify(ctx, cs[len(cs)-1]); ev == nil || ev.Event != evLocked {
			t.Fatalf("not an account_locked event: %+v", ev)
		}
		if a := lastAudit(t, f); a.Outcome != 429 || a.RemoteAddr != "127.0.0.1" || a.Target != "user:pause1" {
			t.Fatalf("the refused attempt in the audit log: %+v", a)
		}
		clk.add(store.PauseFor)
		if c, _, _ := f.password("pause1", userPassword("pause1"), ""); c != 200 {
			t.Fatalf("after the pause: %d", c)
		}
	})

	t.Run("§8 auditor accounts expire, at most 30 days ahead, renewable by an admin", func(t *testing.T) {
		relogin()
		far := clk.now().Add(31 * 24 * time.Hour).Format(time.RFC3339)
		if c, _ := f.call(admin, "POST", "/api/v1/users", fmt.Sprintf(`{"username":"aud2","role":"auditor","password":%q,"expires_at":%q}`, userPassword("aud2"), far),
			f.stepUp(admin, "POST", "/api/v1/users"), nil); c != 400 {
			t.Fatalf("31 days: %d", c)
		}
		u := f.newUser("aud2", store.RoleAuditor)
		tok := f.signIn("aud2")
		clk.add(store.AuditorMaxAge)
		if c, e := f.call(tok, "GET", "/api/v1/me", "", nil, nil); c != 401 || e.Reason != store.EndExpired {
			t.Fatalf("the session at expiry: %d %+v", c, e)
		}
		if c, e, _ := f.password("aud2", userPassword("aud2"), ""); c != 403 || e.Code != "signin_refused" {
			t.Fatalf("signing in expired: %d %+v", c, e)
		}
		admin = f.login(store.RoleAdmin)
		f.tokens[store.RoleAdmin] = admin
		path := fmt.Sprintf("/api/v1/users/%d", u.ID)
		if c, e := f.call(admin, "PATCH", path, `{"expires_at":""}`, f.stepUp(admin, "PATCH", path), nil); c != 200 {
			t.Fatalf("renew: %d %+v", c, e)
		}
		f.signIn("aud2")
		f.s.db.SQL().Exec(`UPDATE users SET expires_at = ? WHERE username = 'auditor1'`, clk.now().Add(store.AuditorMaxAge).Format(time.RFC3339Nano))
	})

	t.Run("§9 API tokens: admin-made, operator or auditor, at most 90 days, shown once, no step-up", func(t *testing.T) {
		relogin()
		if c, _ := f.call(admin, "POST", "/api/v1/api-tokens", `{"name":"ci1","role":"admin"}`, f.stepUp(admin, "POST", "/api/v1/api-tokens"), nil); c != 400 {
			t.Fatalf("an admin token: %d", c)
		}
		if c, _ := f.call(op, "POST", "/api/v1/api-tokens", `{"name":"ci1","role":"operator"}`, nil, nil); c != 403 {
			t.Fatalf("an operator makes a token: %d", c)
		}
		var nt NewToken
		if c, _ := f.call(admin, "POST", "/api/v1/api-tokens", `{"name":"ci1","role":"operator","allowed_addrs":["127.0.0.0/8"]}`, f.stepUp(admin, "POST", "/api/v1/api-tokens"), &nt); c != 201 || !strings.HasPrefix(nt.Token, "gaiat_") {
			t.Fatalf("make: %d %+v", c, nt)
		}
		var raw []byte
		f.doH(admin, "GET", "/api/v1/api-tokens", "", nil, &raw)
		var n int
		f.s.db.SQL().QueryRow(`SELECT count(*) FROM api_tokens WHERE token_sha256 = ?`, auth.SHA256Hex(nt.Token)).Scan(&n)
		if strings.Contains(string(raw), nt.Token) || n != 1 {
			t.Fatal("the token is shown again, or not stored as its sha256")
		}
		if c, _ := f.call(nt.Token, "GET", "/api/v1/views/fleet", "", nil, nil); c != 200 {
			t.Fatalf("the token reads: %d", c)
		}
		if c, _ := f.call(nt.Token, "POST", "/api/v1/step-up", `{"method":"POST","path":"/api/v1/hosts"}`, nil, nil); c != 403 {
			t.Fatalf("a token asks for a step-up: %d", c)
		}
		if c, e := f.call(nt.Token, "POST", "/api/v1/hosts", `{"name":"x","address":"x","ssh_user":"gaia"}`, map[string]string{"X-Gaia-Step-Up": "su_x"}, nil); c != 403 || e.Code != "step_up_failed" {
			t.Fatalf("a token does a step-up action: %d %+v", c, e)
		}
		if !hasAudit(f.audits(t), "api_token_use", "first use") {
			t.Fatal("the token's first use is not in the audit log")
		}
		far := clk.now().Add(91 * 24 * time.Hour).Format(time.RFC3339)
		if c, _ := f.call(admin, "POST", "/api/v1/api-tokens", fmt.Sprintf(`{"name":"ci2","role":"auditor","expires_at":%q}`, far), f.stepUp(admin, "POST", "/api/v1/api-tokens"), nil); c != 400 {
			t.Fatalf("91 days: %d", c)
		}
	})

	t.Run("§9 the CLI's sign-in through a browser gets a session with the same limits", func(t *testing.T) {
		relogin()
		var ch DeviceChallenge
		f.call("", "POST", "/api/v1/login/cli", `{"device_id":"cli-device-0123456789abcdef0123"}`, nil, &ch)
		var st SigninStep
		if f.call("", "POST", "/api/v1/login/cli/token", fmt.Sprintf(`{"handle":%q}`, ch.Handle), nil, &st); st.Stage != "pending" {
			t.Fatalf("before approval: %+v", st)
		}
		if c, e := f.call(admin, "POST", "/api/v1/login/cli/approve", fmt.Sprintf(`{"user_code":%q}`, ch.UserCode), nil, nil); c != 403 || e.Code != "step_up_required" {
			t.Fatalf("approval without a step-up: %d %+v", c, e)
		}
		if c, _ := f.call(admin, "POST", "/api/v1/login/cli/approve", fmt.Sprintf(`{"user_code":%q}`, ch.UserCode), f.stepUp(admin, "POST", "/api/v1/login/cli/approve"), nil); c != 204 {
			t.Fatalf("approval: %d", c)
		}
		if c, _ := f.call("", "POST", "/api/v1/login/cli/token", fmt.Sprintf(`{"handle":%q}`, ch.Handle), nil, &st); c != 200 || st.Stage != stageDone || st.Token == "" || st.User.Username != "admin" {
			t.Fatalf("after approval: %d %+v", c, st)
		}
		ss, _ := f.s.db.Sessions(ctx, st.User.ID)
		if len(ss) == 0 || ss[0].Kind != "cli" || ss[0].AbsoluteAt == "" {
			t.Fatalf("the CLI session %+v", ss)
		}
	})

	t.Run("§1 the last break-glass admin cannot be removed, demoted or disabled", func(t *testing.T) {
		relogin()
		var id int64
		f.s.db.SQL().QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&id)
		f.s.db.SQL().Exec(`UPDATE users SET break_glass = 0 WHERE username != 'admin'`)
		path := fmt.Sprintf("/api/v1/users/%d", id)
		for _, body := range []string{`{"break_glass":false}`, `{"disabled":true}`, `{"role":"operator"}`} {
			if c, e := f.call(admin, "PATCH", path, body, f.stepUp(admin, "PATCH", path), nil); c != 409 {
				t.Errorf("%s: %d %+v", body, c, e)
			}
		}
	})

	t.Run("§10 sign-in, sign-out, failure, step-up, session end, enrolment, reset, token and settings changes are audited", func(t *testing.T) {
		es := f.audits(t)
		for _, w := range [][2]string{
			{"POST /api/v1/login/factor", "signed in"}, {"POST /api/v1/logout", "signed out"}, {"POST /api/v1/login", "sign-in failed: wrong password"},
			{"step_up", "step-up passed"}, {"session_end", "ended: idle"}, {"session_end", "ended: replaced"}, {"POST /api/v1/login/enrol/finish", "enrolled totp"},
			{"POST /api/v1/users/{id}/factors/reset", "second factors reset"}, {"POST /api/v1/api-tokens", "API token ci1 made"},
			{"PUT /api/v1/settings/signin", "sign-in settings changed"}, {"api_token_use", "first use"},
		} {
			if !hasAudit(es, w[0], w[1]) {
				t.Errorf("no audit entry %s %q", w[0], w[1])
			}
		}
		if c, err := f.s.db.VerifyAudit(ctx); err != nil || !c.Intact {
			t.Fatalf("audit chain %+v %v", c, err)
		}
		// Settings changes go to the next daily report.
		cs, err := f.s.db.SecurityChanges(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range cs {
			found = found || (c.Entity == "setting" && strings.Contains(c.To, `"password_max_age":"90d"`))
		}
		if !found {
			t.Fatal("the settings change is not among the report's security changes")
		}
	})
}

func lastAudit(t *testing.T, f *fixture) store.AuditEntry {
	t.Helper()
	a := f.audits(t)
	if len(a) == 0 {
		t.Fatal("no audit entries")
	}
	return a[len(a)-1]
}

// An upgraded server whose host name has no domain (the default public_url) cannot be WebAuthn's relying party. It
// starts anyway (backups go on), says why in gaia system, refuses security keys with the reason, and lets a user in
// with another factor or a recovery code. A new installation refuses such a public_url at init (T26 upgrade drill).
func TestWebAuthnUnavailable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	cfg := f.s.cfg
	cfg.PublicURL = "https://gaia-t17-b:8443"
	f.ts.Close()
	f.s.Close()
	s, err := New(ctx, cfg, f.s.cfgPath, f.s.log)
	if err != nil {
		t.Fatalf("the server did not start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	f.s = s
	f.ts = httptest.NewServer(s.Handler())
	t.Cleanup(f.ts.Close)
	// The admin has a key and recovery codes (newFixture enrolled them): only the codes are offered.
	c, _, st := f.password("admin", adminPassword, "")
	if c != 200 || st.Stage != stageFactor || slices.Contains(st.Factors, "webauthn") || !slices.Contains(st.Factors, "recovery_code") {
		t.Fatalf("admin with keys unavailable: %d %+v", c, st)
	}
	// A new admin cannot enrol a key: 503 with the reason.
	if _, err := s.db.CreateUser(ctx, "test", store.NewUser{Username: "admin3", Role: store.RoleAdmin, Password: userPassword("admin3")}); err != nil {
		t.Fatal(err)
	}
	_, _, st = f.password("admin3", userPassword("admin3"), "")
	if c, e := f.call("", "POST", "/api/v1/login/enrol", fmt.Sprintf(`{"signin_token":%q,"kind":"webauthn"}`, st.SigninToken), nil, nil); c != 503 ||
		e.Code != "webauthn_unavailable" || !strings.Contains(e.Error, "public_url") {
		t.Fatalf("enrolment with keys unavailable: %d %+v", c, e)
	}
	var sys SystemInfo
	if _, err := s.db.CreateUser(ctx, "test", store.NewUser{Username: "appop", Role: store.RoleOperator, Password: userPassword("appop")}); err != nil {
		t.Fatal(err)
	}
	tok, _, _ := f.enrolTOTP("appop") // an authenticator app still works
	if f.call(tok, "GET", "/api/v1/system", "", nil, &sys); !strings.Contains(sys.SigninProblem, "public_url https://gaia-t17-b:8443") {
		t.Fatalf("gaia system does not say why: %q", sys.SigninProblem)
	}
	dir := t.TempDir()
	if _, err := Init(ctx, InitOptions{ConfigPath: dir + "/server.json", DataDir: dir + "/data", AdminUser: "admin", AdminPassword: adminPassword,
		KitPassphrase: kitPassphrase, PublicURL: "https://gaia-t17-b:8443"}); err == nil || !strings.Contains(err.Error(), "DNS name") {
		t.Fatalf("init with a public_url without a domain: %v", err)
	}
}

// gaia server tls: an installation from before 0.2.0 gets a certificate that names its public URL (security keys need
// the browser to trust the server by that name).
func TestNewTLSCertificate(t *testing.T) {
	f := newFixture(t)
	old, _ := CertFileFingerprint(f.s.cfg.TLSCert)
	r, err := NewTLSCertificate(f.s.cfgPath, []string{"extra.lab"}, "https://gaia-new.lab.test:8443")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := CertFileFingerprint(f.s.cfg.TLSCert)
	if now != r.CertSHA256 || now == old || !slices.Contains(r.CertNames, "gaia-new.lab.test") || !slices.Contains(r.CertNames, "extra.lab") {
		t.Fatalf("%+v (old %s, now %s)", r, old, now)
	}
	if fp, err := CertFileFingerprint(r.OldCert); err != nil || fp != old {
		t.Fatalf("the old certificate: %s %v", fp, err)
	}
	if _, err := NewTLSCertificate(f.s.cfgPath, nil, "https://nodomain:8443"); err == nil {
		t.Fatal("a public URL without a domain")
	}
}
