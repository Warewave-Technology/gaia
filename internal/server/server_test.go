package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
)

var (
	updateOpenAPI = flag.Bool("update", false, "rewrite openapi.json")
	matrixOut     = flag.String("matrix", "", "write the role × endpoint matrix (markdown) to this file")
)

const (
	adminPassword = "the first password for tests"
	kitPassphrase = "the recovery kit passphrase for tests"
	testPublicURL = "https://gaia.test:8443"
)

// userPassword is a fixture user's password (ADR-008 §6: 15 characters, not the user name).
func userPassword(username string) string {
	if username == "admin" {
		return adminPassword
	}
	return "long test password, number " + fmt.Sprint(len(username))
}

func TestMain(m *testing.M) {
	recoverykit.SetWorkFactor(10) // age's default costs 256 MiB and a second per kit
	os.Exit(m.Run())
}

type fixture struct {
	t       *testing.T
	s       *Server
	ts      *httptest.Server
	dir     string
	tokens  map[string]string // role -> token ("" = anonymous)
	ids     map[string]int64  // path segment -> an existing ID
	userIDs map[string]int64
	s3      string              // testedVM's repository is S3 at this location (s3Location) instead of a directory
	keys    map[string]*softKey // username -> its security key (every fixture user has one)
	owners  map[string]string   // token -> username, for the step-ups f.do makes
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "etc", "server.json")
	if _, err := Init(ctx, InitOptions{ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), AdminUser: "admin", AdminPassword: adminPassword,
		KitPassphrase: kitPassphrase, PublicURL: testPublicURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(ctx, InitOptions{ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), AdminUser: "admin", AdminPassword: adminPassword, KitPassphrase: kitPassphrase}); err == nil {
		t.Fatal("Init ran twice on the same installation")
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, cfg, cfgPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, s: s, ts: httptest.NewServer(s.Handler()), dir: dir, tokens: map[string]string{"": ""}, ids: map[string]int64{},
		userIDs: map[string]int64{}, keys: map[string]*softKey{}, owners: map[string]string{}}
	t.Cleanup(func() { f.ts.Close(); s.Close() })
	for _, r := range []string{store.RoleOperator, store.RoleAuditor} {
		u, err := s.db.CreateUser(ctx, "admin", store.NewUser{Username: r + "1", Role: r, Password: userPassword(r + "1")})
		if err != nil {
			t.Fatal(err)
		}
		f.userIDs[r] = u.ID
	}
	// The target of the user routes in the role matrix (a reset ends its sessions; the fixture's users keep theirs).
	tu, err := s.db.CreateUser(ctx, "admin", store.NewUser{Username: "target1", Role: store.RoleOperator, Password: userPassword("target1")})
	if err != nil {
		t.Fatal(err)
	}
	f.userIDs["target"] = tu.ID
	for _, r := range []string{store.RoleAdmin, store.RoleOperator, store.RoleAuditor} {
		f.tokens[r] = f.login(r)
	}
	return f
}

// login signs a role's fixture user in (a CLI session) and returns the token.
func (f *fixture) login(role string) string {
	f.t.Helper()
	user := role + "1"
	if role == store.RoleAdmin {
		user = "admin"
	}
	return f.signIn(user)
}

// signIn signs a user in through every step, as the CLI does: the password, then its security key, or at the first
// sign-in the enrolment of one (a softKey).
func (f *fixture) signIn(user string) string {
	f.t.Helper()
	var st SigninStep
	if code := f.do("", "POST", "/api/v1/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, userPassword(user)), &st); code != http.StatusOK {
		f.t.Fatalf("sign-in of %s: %d", user, code)
	}
	switch st.Stage {
	case stageEnrol:
		var ch EnrolChallenge
		if code := f.do("", "POST", "/api/v1/login/enrol", fmt.Sprintf(`{"signin_token":%q,"kind":"webauthn"}`, st.SigninToken), &ch); code != http.StatusOK {
			f.t.Fatalf("enrol %s: %d", user, code)
		}
		k := newSoftKey(f.t, testPublicURL)
		body, _ := json.Marshal(EnrolFinish{SigninToken: st.SigninToken, Kind: "webauthn", Name: "test key", WebAuthn: k.create(f.t, ch.WebAuthn)})
		if code := f.do("", "POST", "/api/v1/login/enrol/finish", string(body), &st); code != http.StatusOK {
			f.t.Fatalf("enrol finish %s: %d", user, code)
		}
		f.keys[user] = k
	case stageFactor:
		k := f.keys[user]
		if k == nil {
			f.t.Fatalf("%s has a factor the fixture does not hold", user)
		}
		body, _ := json.Marshal(FactorAnswer{SigninToken: st.SigninToken, Method: "webauthn", WebAuthn: k.get(f.t, st.WebAuthn)})
		if code := f.do("", "POST", "/api/v1/login/factor", string(body), &st); code != http.StatusOK {
			f.t.Fatalf("second factor of %s: %d", user, code)
		}
	}
	if st.Stage != stageDone || st.Token == "" {
		f.t.Fatalf("sign-in of %s ended at %+v", user, st)
	}
	f.owners[st.Token] = user
	return st.Token
}

// stepUp makes a step-up for one action and returns its headers.
func (f *fixture) stepUp(token, method, path string) map[string]string {
	f.t.Helper()
	var ch StepUpChallenge
	if code := f.doH(token, "POST", "/api/v1/step-up", fmt.Sprintf(`{"method":%q,"path":%q}`, method, path), nil, &ch); code != http.StatusOK {
		return nil // a role not allowed: the request itself says so
	}
	k := f.keys[f.owners[token]]
	if k == nil {
		f.t.Fatalf("step-up for %s %s: no key for the token's user %q", method, path, f.owners[token])
	}
	proof, _ := json.Marshal(StepUpProof{Method: "webauthn", WebAuthn: k.get(f.t, ch.WebAuthn)})
	return map[string]string{"X-Gaia-Step-Up": ch.ID, "X-Gaia-Step-Up-Proof": b64.EncodeToString(proof)}
}

// do sends a request with a role's token; out (if set) receives the JSON body. An action that always needs a step-up
// (or may: a policy made looser) gets one first, made with the token's user's key.
func (f *fixture) do(token, method, path, body string, out any) int {
	f.t.Helper()
	var h map[string]string
	if p, _, _ := strings.Cut(path, "?"); token != "" && f.owners[token] != "" {
		if rt := f.s.stepUpRoute(method, p); rt != nil {
			h = f.stepUp(token, method, p)
		}
	}
	return f.doH(token, method, path, body, h, out)
}

// doH sends a request with extra headers.
func (f *fixture) doH(token, method, path, body string, headers map[string]string, out any) int {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.ts.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		if w, ok := out.(*[]byte); ok {
			*w = b
		} else if len(b) > 0 {
			if err := json.Unmarshal(b, out); err != nil {
				f.t.Fatalf("%s %s: %v: %s", method, path, err, b)
			}
		}
	}
	return resp.StatusCode
}

// seed creates one of everything, through the API where it exists and the store where only workers write.
func (f *fixture) seed() {
	t, ctx, admin := f.t, context.Background(), f.tokens[store.RoleAdmin]
	var repo store.Repository
	if st := f.do(admin, "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`, filepath.Join(f.dir, "repo"), kitPassphrase), &repo); st != http.StatusCreated {
		t.Fatalf("repository: %d", st)
	}
	var h store.Host
	if st := f.do(admin, "POST", "/api/v1/hosts", `{"name":"seed-host","address":"127.0.0.1","ssh_port":1,"ssh_user":"gaia"}` /* nothing listens: host calls fail fast with 502 */, &h); st != http.StatusCreated {
		t.Fatalf("host: %d", st)
	}
	var p store.Policy
	if st := f.do(admin, "POST", "/api/v1/policies", fmt.Sprintf(`{"name":"seed","repository_id":%d,"backup_interval_s":900,"test_interval_s":86400,"keep_last":8,"keep_daily":0,"keep_weekly":0,"rpo_target_s":3600,"rto_target_s":1800,"max_proof_age_s":172800,"check_template":"linux-basic"}`, repo.ID), &p); st != http.StatusCreated {
		t.Fatalf("policy: %d", st)
	}
	var v store.VM
	if st := f.do(admin, "POST", "/api/v1/vms", fmt.Sprintf(`{"host_id":%d,"name":"seed-vm","uuid":"","policy_id":%d}`, h.ID, p.ID), &v); st != http.StatusCreated {
		t.Fatalf("vm: %d", st)
	}
	j, err := f.s.db.CreateJob(ctx, "system", "backup", &v.ID, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := f.s.db.CreatePoint(ctx, "system", store.Point{VMID: v.ID, JobID: j.ID, RepositoryID: repo.ID, Kind: "full", TakenAt: "2026-11-16T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.s.db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: v.ID, PointID: pt.ID, JobID: j.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A real sealed bundle in the installation's ledger.
	key, err := evidence.LoadPrivateKey(f.s.cfg.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	bdir := filepath.Join(f.s.cfg.bundlesDir(), "seed-vm-test-1")
	os.MkdirAll(bdir, 0o755)
	os.WriteFile(filepath.Join(bdir, "result.json"), []byte(`{"verdict":"OK"}`), 0o644)
	sha, err := evidence.Seal(bdir, "seed-vm-test-1", map[string]string{"vm": "seed-vm"}, key)
	if err != nil {
		t.Fatal(err)
	}
	le, err := evidence.Append(f.s.cfg.ledgerPath(), evidence.Entry{BundleID: "seed-vm-test-1", BundleSHA256: sha, Subject: "seed-vm", Verdict: "OK"}, key)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := f.s.db.AddEvidence(ctx, store.EvidenceBundle{BundleID: "seed-vm-test-1", TestID: &rt.ID, VMID: &v.ID, LedgerSeq: le.Seq, BundleSHA256: sha, Verdict: "OK"})
	if err != nil {
		t.Fatal(err)
	}
	ver, err := f.s.db.StartVerification(ctx, "system", repo.ID, "sampled", "seed")
	if err != nil {
		t.Fatal(err)
	}
	// A notification channel (off, so it takes no events in other tests) and a daily report.
	if err := os.WriteFile(f.s.cfg.notifySecretFile("seed-hook"), []byte("seed secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch, err := f.s.db.AddChannel(ctx, "admin", store.Channel{Name: "seed-hook", Kind: "webhook", Config: []byte(`{"url":"http://127.0.0.1:1/"}`), Categories: allCategories, DailyReport: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetChannelEnabled(ctx, "admin", ch.ID, false, "seed"); err != nil {
		t.Fatal(err)
	}
	dr, err := f.s.db.AddDailyReport(ctx, store.DailyReport{ID: 1, GeneratedAt: "2026-11-16T07:00:00Z", Reason: "seed", LedgerHead: []byte("null"),
		Report: []byte(`{"format":"gaia-daily-report"}`)}, "seed", []byte(`{}`)) // (made by hand: a real one would compute the VM's status)
	if err != nil {
		t.Fatal(err)
	}
	// A migration source (no password: its inventory fails fast) and a finished check whose bundle is the seed bundle.
	src, err := f.s.db.CreateMigrationSource(ctx, "admin", store.MigrationSource{Name: "seed-esxi", Kind: "vsphere", URL: "https://127.0.0.1:9/sdk", Username: "root"})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := f.s.db.QueueMigrationCheck(ctx, "admin", src.ID, "seed-vm", nil, "seed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetJobState(ctx, "system", mc.JobID, "running", "seed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetJobState(ctx, "system", mc.JobID, "ok", "seed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.FinishMigrationCheck(ctx, "system", mc.ID, "OK", "seed", ev.BundleID, MigrationDetail{}); err != nil {
		t.Fatal(err)
	}
	f.ids = map[string]int64{"users": f.userIDs["target"], "hosts": h.ID, "vms": v.ID, "repositories": repo.ID, "policies": p.ID,
		"jobs": j.ID, "points": pt.ID, "restore-tests": rt.ID, "evidence": ev.ID, "verifications": ver.ID, "notification-channels": ch.ID,
		"daily-reports": dr.ID, "views": v.ID, "migration-sources": src.ID, "migration-checks": mc.ID}
}

func (f *fixture) path(rt route) string {
	seg := strings.Split(strings.TrimPrefix(rt.path, "/api/v1/"), "/")[0]
	return strings.ReplaceAll(rt.path, "{id}", fmt.Sprint(f.ids[seg]))
}

// Every endpoint, every role: anonymous -> 401, a role not allowed -> 403, an allowed role -> neither.
func TestRoleMatrix(t *testing.T) {
	f := newFixture(t)
	f.seed()
	roles := []string{"", store.RoleAuditor, store.RoleOperator, store.RoleAdmin}
	auditBefore, _ := f.s.db.Audit(context.Background(), 0, 100000)
	var md strings.Builder
	md.WriteString("| Endpoint | Allowed | anonymous | auditor | operator | admin |\n|---|---|---|---|---|---|\n")
	for _, rt := range f.s.routes {
		row := fmt.Sprintf("| `%s %s` | %s |", rt.method, rt.path, strings.Join(rolesOf(rt), ", "))
		if rt.roles == nil {
			row = fmt.Sprintf("| `%s %s` | (public) |", rt.method, rt.path)
		}
		for _, role := range roles {
			token := f.tokens[role]
			if rt.path == "/api/v1/logout" && role != "" {
				token = f.login(role) // a session of its own, so the others stay valid
			}
			if rt.path == "/api/v1/sessions/end-others" && role != "" {
				token = f.login(role) // it signs everyone else out: the fixture signs in again after it
			}
			st := f.do(token, rt.method, f.path(rt), rt.example, nil)
			if rt.path == "/api/v1/sessions/end-others" && st == http.StatusOK {
				for _, r := range []string{store.RoleAdmin, store.RoleOperator, store.RoleAuditor} {
					f.tokens[r] = f.login(r)
				}
			}
			// Every role is signed in from 127.0.0.1: the matrix's wrong examples would slow its own sign-ins (§7).
			if _, err := f.s.db.SQL().Exec(`DELETE FROM signin_failures`); err != nil {
				t.Fatal(err)
			}
			row += fmt.Sprintf(" %d |", st)
			switch {
			case rt.roles == nil:
				if st == http.StatusForbidden {
					t.Errorf("%s %s (public) as %q: 403", rt.method, rt.path, role)
				}
			case role == "":
				if st != http.StatusUnauthorized {
					t.Errorf("%s %s anonymous: %d, want 401", rt.method, rt.path, st)
				}
			case !slices.Contains(rt.roles, role):
				if st != http.StatusForbidden {
					t.Errorf("%s %s as %s: %d, want 403", rt.method, rt.path, role, st)
				}
			default:
				if st == http.StatusUnauthorized || st == http.StatusForbidden {
					t.Errorf("%s %s as %s (allowed): %d", rt.method, rt.path, role, st)
				}
			}
		}
		md.WriteString(row + "\n")
	}
	auditAfter, _ := f.s.db.Audit(context.Background(), 0, 100000)
	if len(auditAfter) <= len(auditBefore) {
		t.Fatal("the matrix left no audit entries")
	}
	if c, err := f.s.db.VerifyAudit(context.Background()); err != nil || !c.Intact {
		t.Fatalf("audit chain after the matrix: %+v %v", c, err)
	}
	if *matrixOut != "" {
		if err := os.WriteFile(*matrixOut, []byte(md.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d endpoints × %d roles", len(f.s.routes), len(roles))
}

func TestOpenAPIFile(t *testing.T) {
	f := newFixture(t)
	if *updateOpenAPI {
		if err := os.WriteFile("openapi.json", f.s.openapi, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile("openapi.json")
	if err != nil || !bytes.Equal(committed, f.s.openapi) {
		t.Fatalf("internal/server/openapi.json is not what routes.go generates (run go test ./internal/server -run TestOpenAPIFile -update): %v", err)
	}
	var served []byte
	if st := f.do("", "GET", "/api/v1/openapi.json", "", &served); st != 200 || !bytes.Equal(served, committed) {
		t.Fatalf("served openapi.json: %d", st)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Roles []string `json:"x-gaia-roles"`
		} `json:"paths"`
	}
	json.Unmarshal(committed, &doc)
	n := 0
	for _, ops := range doc.Paths {
		n += len(ops)
	}
	if n != len(f.s.routes) {
		t.Fatalf("openapi.json has %d operations, routes.go %d", n, len(f.s.routes))
	}
}

func TestSessionsAndAudit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if st := f.do("", "POST", "/api/v1/login", `{"username":"admin","password":"wrong password here"}`, nil); st != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", st)
	}
	es, _ := f.s.db.Audit(ctx, 0, 1000)
	last := es[len(es)-1]
	if last.Action != "POST /api/v1/login" || last.Outcome != 401 || last.Target != "user:admin" || last.UserID != nil {
		t.Fatalf("failed login in the audit log: %+v", last)
	}
	tok := f.login(store.RoleOperator)
	if st := f.do(tok, "POST", "/api/v1/logout", "", nil); st != http.StatusNoContent {
		t.Fatalf("logout: %d", st)
	}
	if st := f.do(tok, "GET", "/api/v1/me", "", nil); st != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", st)
	}
	var me store.User
	f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/me", "", &me)
	if st := f.do(f.tokens[store.RoleAdmin], "PATCH", fmt.Sprintf("/api/v1/users/%d", me.ID), `{"disabled":true}`, nil); st != 200 {
		t.Fatalf("disable: %d", st)
	}
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/me", "", nil); st != http.StatusUnauthorized {
		t.Fatalf("a disabled user's token: %d", st)
	}
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/hosts", `{"name":"x","address":"x","ssh_user":"gaia","extra":1}`, nil); st != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d", st)
	}
	// An action that cannot be recorded is not done.
	if _, err := f.s.db.SQL().ExecContext(ctx, `CREATE TRIGGER block BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/hosts", `{"name":"unaudited","address":"10.0.0.9","ssh_user":"gaia"}`, nil); st != http.StatusServiceUnavailable {
		t.Fatalf("an unauditable action: %d", st)
	}
	f.s.db.SQL().ExecContext(ctx, `DROP TRIGGER block`)
	hs, _ := f.s.db.Hosts(ctx)
	for _, h := range hs {
		if h.Name == "unaudited" {
			t.Fatal("the host was created although its audit entry was refused")
		}
	}
	var chk store.AuditCheck
	if st := f.do(f.tokens[store.RoleAdmin], "GET", "/api/v1/audit/verify", "", &chk); st != 200 || !chk.Intact {
		t.Fatalf("audit verify: %d %+v", st, chk)
	}
}

func TestStateBackupAndExport(t *testing.T) {
	f := newFixture(t)
	f.seed()
	ctx := context.Background()
	admin := f.tokens[store.RoleAdmin]
	var b store.StateBackup
	if st := f.do(admin, "POST", "/api/v1/state-backups", "", &b); st != http.StatusCreated || b.State != "ok" || b.SnapshotID == "" {
		t.Fatalf("state backup: %d %+v", st, b)
	}
	// Read it back from the repository: the database in it opens and holds the users.
	repo, _ := f.s.db.RepositoryByName(ctx, "main")
	st, err := f.s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)
	var arch bytes.Buffer
	a, err := st.ReadArchive(ctx, b.SnapshotID, &arch)
	if err != nil || a.SHA256 != b.SHA256 {
		t.Fatalf("archive %+v %v", a, err)
	}
	restored := filepath.Join(f.dir, "restored")
	os.MkdirAll(restored, 0o755)
	names := untar(t, &arch, restored)
	if !slices.Equal(names, []string{"gaia.db", "server.json", "keys/evidence-key.pub", "evidence/ledger.jsonl", "MANIFEST.json"}) {
		t.Fatalf("archive members %v", names)
	}
	db, err := store.Open(filepath.Join(restored, "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if us, err := db.Users(ctx); err != nil || len(us) != 4 {
		t.Fatalf("users in the backed-up database: %d %v", len(us), err)
	}
	if as, _ := st.Archives(ctx, StateArchiveKind); len(as) != 1 {
		t.Fatalf("archives %+v", as)
	}
	// The export verifies on its own, with the anchor it carries (which equals the installation's key_id).
	var tgz []byte
	if st := f.do(f.tokens[store.RoleAuditor], "GET", fmt.Sprintf("/api/v1/evidence/%d/export", f.ids["evidence"]), "", &tgz); st != 200 {
		t.Fatalf("export: %d", st)
	}
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.dir, "export")
	untar(t, zr, out)
	root := filepath.Join(out, "gaia-evidence-seed-vm-test-1")
	anchor, err := evidence.LoadPublicKey(filepath.Join(root, "anchor.pub"))
	if err != nil {
		t.Fatal(err)
	}
	var info SystemInfo
	f.do(admin, "GET", "/api/v1/system", "", &info)
	if evidence.KeyID(anchor) != info.EvidenceKeyID {
		t.Fatalf("export anchor %s, system key_id %s", evidence.KeyID(anchor), info.EvidenceKeyID)
	}
	r, err := evidence.VerifyLedger(filepath.Join(root, "ledger.jsonl"), filepath.Join(root, "bundles"), anchor)
	if err != nil || r.Verdict != evidence.OK {
		t.Fatalf("exported evidence: %+v %v", r.AllFindings(), err)
	}
	es, _ := f.s.db.Audit(ctx, 0, 10000)
	if last := es[len(es)-1]; last.Action != "GET /api/v1/evidence/{id}/export" || last.Username != "auditor1" || last.Outcome != 200 {
		t.Fatalf("the export is not in the audit log: %+v", last)
	}
	_ = diskstore.Splitter
}

func untar(t *testing.T, r io.Reader, dir string) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		p := filepath.Join(dir, h.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		b, _ := io.ReadAll(tr)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
