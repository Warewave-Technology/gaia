package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// A browser logs in with a cookie it cannot read from script: the token is not in the answer, the cookie is HttpOnly,
// Secure, SameSite=Strict and __Host-; it authenticates the API; a request another site's page sends is refused;
// logging out ends the session and clears the cookie.
func TestBrowserSession(t *testing.T) {
	f := newFixture(t)
	f.seed()
	ts := httptest.NewTLSServer(f.s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	cl := ts.Client()
	cl.Jar = jar
	resp, err := cl.Post(ts.URL+"/api/v1/login", "application/json", strings.NewReader(`{"username":"auditor1","password":"password of auditor1","browser":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var lr LoginResponse
	json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if resp.StatusCode != 200 || lr.Token != "" || lr.User.Username != "auditor1" {
		t.Fatalf("browser login: %d %+v", resp.StatusCode, lr)
	}
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Domain != "" {
		t.Fatalf("session cookie: %+v", ck)
	}
	get := func(path string) int {
		resp, err := cl.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := get("/api/v1/views/overview"); st != 200 {
		t.Fatalf("overview with the cookie: %d", st)
	}
	if st := get("/api/v1/evidence-export"); st != 200 {
		t.Fatalf("an auditor's export: %d", st)
	}
	// Another site's page making the browser post: refused before the route.
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-site POST: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", ts.URL+"/api/v1/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if u, _ := url.Parse(ts.URL); len(jar.Cookies(u)) != 0 {
		t.Fatalf("cookie after logout: %v", jar.Cookies(u))
	}
	if st := get("/api/v1/views/overview"); st != http.StatusUnauthorized {
		t.Fatalf("overview after logout: %d", st)
	}
}

// The UI is served for every app route, with a policy that allows nothing but Gaia's own files; /api/ stays JSON.
func TestUIServing(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/", "/vms/7", "/evidence"} {
		req := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, req)
		if w.Code != 200 && w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d", p, w.Code)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Fatalf("%s: CSP %q", p, csp)
		}
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/assets/nothing-here.js", nil))
	if w.Code != 404 {
		t.Fatalf("a missing asset: %d", w.Code)
	}
	w = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nothing", nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"not_found"`) {
		t.Fatalf("an unknown API path: %d %s", w.Code, w.Body)
	}
}

// The views carry what the screens show: status, targets, the proven point and the history strip.
func TestViews(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	vmID := f.ids["vms"]
	if _, err := f.s.db.SetJobState(ctx, "system", f.ids["jobs"], "running", "worker", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetJobState(ctx, "system", f.ids["jobs"], "failed", "worker", "the host did not answer"); err != nil {
		t.Fatal(err)
	}
	var ov OverviewView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/overview", "", &ov); st != 200 || len(ov.VMs) != 1 {
		t.Fatalf("overview: %d %+v", st, ov)
	}
	row := ov.VMs[0]
	if row.VM != "seed-vm" || row.Targets == nil || row.Targets.RPOS != 3600 || row.Host != "seed-host" || row.Policy != "seed" || ov.Clock.Zone == "" {
		t.Fatalf("overview row: %+v %+v", row, ov.Clock)
	}
	var vv VMView
	if st := f.do(f.tokens[store.RoleOperator], "GET", "/api/v1/views/vms/"+itoa(vmID), "", &vv); st != 200 || vv.VM.ID != vmID || vv.Policy == nil || vv.Repository == nil {
		t.Fatalf("vm view: %d %+v", st, vv)
	}
	if len(vv.StatusHistory) == 0 || len(vv.Tests) != 1 {
		t.Fatalf("vm view history %d, tests %d", len(vv.StatusHistory), len(vv.Tests))
	}
	var bc BundleCheck
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/evidence/"+itoa(f.ids["evidence"])+"/verify", "", &bc); st != 200 || bc.Verdict != evidence.OK {
		t.Fatalf("bundle verify: %d %+v", st, bc)
	}
	var b []byte
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/evidence/"+itoa(f.ids["evidence"])+"/files/result.json", "", &b); st != 200 || !strings.Contains(string(b), "verdict") {
		t.Fatalf("bundle file: %d %s", st, b)
	}
	for _, bad := range []string{"%2E%2E", ".hidden", "..%2Fmanifest.json", "nothing"} { // (a plain ".." is cleaned away by the mux before any route)
		if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/evidence/"+itoa(f.ids["evidence"])+"/files/"+bad, "", nil); st == 200 {
			t.Fatalf("bundle file %q: %d", bad, st)
		}
	}
}

// The auditor's export verifies offline with the trust anchor, the ledger, the bundles and the daily report's head;
// its SHA256SUMS lists every file.
func TestAuditExport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	if _, err := f.s.makeDailyReport(ctx, "admin", "test"); err != nil {
		t.Fatal(err)
	}
	if st := f.do(f.tokens[store.RoleOperator], "GET", "/api/v1/evidence-export", "", nil); st != http.StatusForbidden {
		t.Fatalf("an operator's export: %d", st)
	}
	var b []byte
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/evidence-export", "", &b); st != 200 {
		t.Fatalf("export: %d %s", st, b)
	}
	dir := t.TempDir()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	root := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		root = strings.SplitN(h.Name, "/", 2)[0]
		p := filepath.Join(dir, h.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		body, _ := io.ReadAll(tr)
		os.WriteFile(p, body, 0o644)
	}
	r := filepath.Join(dir, root)
	anchor, err := evidence.LoadPublicKey(filepath.Join(r, "anchor.pub"))
	if err != nil {
		t.Fatal(err)
	}
	heads, _ := filepath.Glob(filepath.Join(r, "ledger-head-*.json"))
	if len(heads) != 1 {
		t.Fatalf("head statements in the export: %v", heads)
	}
	hb, _ := os.ReadFile(heads[0])
	rep, err := evidence.VerifyLedgerHeads(filepath.Join(r, "ledger.jsonl"), filepath.Join(r, "bundles"), anchor, [][]byte{hb})
	if err != nil || rep.Verdict != evidence.OK || len(rep.Heads) != 1 || rep.Heads[0].Result != evidence.OK {
		t.Fatalf("the export does not verify: %v %v %+v", err, rep.AllFindings(), rep.Heads)
	}
	sums, _ := os.ReadFile(filepath.Join(r, "SHA256SUMS"))
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		sum, name, _ := strings.Cut(l, "  ")
		body, err := os.ReadFile(filepath.Join(r, name))
		h := sha256.Sum256(body)
		if err != nil || hex.EncodeToString(h[:]) != sum {
			t.Fatalf("SHA256SUMS %s: %v", name, err)
		}
		n++
	}
	readme, _ := os.ReadFile(filepath.Join(r, "README.txt"))
	if n < 8 || !strings.Contains(string(readme), "gaia evidence verify -pubkey anchor.pub") || !strings.Contains(string(readme), "verify.py") {
		t.Fatalf("%d sums; README:\n%s", n, readme)
	}
	for _, name := range []string{"SPEC.md", "restore-tests.json", "keys/evidence-key-current.pub"} {
		if _, err := os.Stat(filepath.Join(r, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
