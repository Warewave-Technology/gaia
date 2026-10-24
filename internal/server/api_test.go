package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

func (e *env) api() *httptest.Server {
	e.t.Helper()
	srv, err := New(Config{Listen: "127.0.0.1:0"}, e.store)
	if err != nil {
		e.t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	e.t.Cleanup(hs.Close)
	return hs
}

func get(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func post(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestRunAPI(t *testing.T) {
	e := setup(t)
	hs := e.api()
	run := e.enqueue()
	e.start()
	e.waitFor(run, "succeeded")

	var runs []Run
	if code := get(t, hs.URL+"/api/v1/runs?contract=poc-web", &runs); code != 200 || len(runs) != 1 || runs[0].ID != run || runs[0].Status != "succeeded" {
		t.Fatalf("runs: %d %+v", code, runs)
	}
	var r Run
	if get(t, hs.URL+"/api/v1/runs/"+run, &r); len(r.Stages) != 1 || r.Stages[0].Stage != "record" || r.Record == nil || r.Record.Seq != 1 {
		t.Errorf("run = %+v", r)
	}
	var evs []Event
	if get(t, hs.URL+"/api/v1/runs/"+run+"/events", &evs); len(evs) == 0 || evs[0].Seq != 1 || evs[len(evs)-1].Stage != "record" {
		t.Errorf("events = %+v", evs)
	}
	var rec map[string]any
	if get(t, hs.URL+"/api/v1/runs/"+run+"/record", &rec); rec["body"].(map[string]any)["run"] != run {
		t.Errorf("record = %v", rec)
	}
	var v map[string]any
	if get(t, hs.URL+"/api/v1/records/verify", &v); v["intact"] != true || v["records"].(float64) != 1 {
		t.Errorf("verify = %v", v)
	}
	var cs []ContractSummary
	if get(t, hs.URL+"/api/v1/contracts", &cs); len(cs) != 1 || cs[0].Name != "poc-web" || cs[0].LastRun != run || cs[0].LastStatus != "succeeded" {
		t.Errorf("contracts = %+v", cs)
	}
	var c Contract
	if get(t, hs.URL+"/api/v1/contracts/poc-web", &c); c.Name != "poc-web" || len(c.Runs) != 1 || len(c.Hosts) != 0 {
		t.Errorf("contract = %+v", c)
	}
	for _, p := range []string{"/api/v1/runs/run-nope", "/api/v1/contracts/nope", "/api/v1/runs/run-nope/events"} {
		var pr Problem
		if code := get(t, hs.URL+p, &pr); code != 404 || pr.Error == "" {
			t.Errorf("%s: %d %+v", p, code, pr)
		}
	}
	var pr Problem
	if code := get(t, hs.URL+"/api/v1/runs?limit=0", &pr); code != 400 {
		t.Errorf("limit=0: %d", code)
	}
	// A finished run cannot be cancelled.
	if code := post(t, hs.URL+"/api/v1/runs/"+run+"/cancel"); code != http.StatusConflict {
		t.Errorf("cancel finished: %d", code)
	}
}

func TestCancelThroughTheAPI(t *testing.T) {
	e := setup(t)
	hs := e.api()
	run := e.enqueue()
	if code := post(t, hs.URL+"/api/v1/runs/"+run+"/cancel"); code != http.StatusAccepted {
		t.Fatalf("cancel: %d", code)
	}
	if r := e.waitFor(run, "cancelled"); !strings.Contains(r.Error.String, "cancelled by api") {
		t.Errorf("run = %+v", r)
	}
	if code := post(t, hs.URL+"/api/v1/runs/run-nope/cancel"); code != http.StatusNotFound {
		t.Errorf("cancel missing: %d", code)
	}
}

// sse reads a stream into its events until it ends.
func sse(t *testing.T, url string, header http.Header) (ids []string, names []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %s", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			ids = append(ids, strings.TrimPrefix(line, "id: "))
		case strings.HasPrefix(line, "event: "):
			names = append(names, strings.TrimPrefix(line, "event: "))
		}
	}
	return ids, names
}

func TestStreamFollowsARunToItsEnd(t *testing.T) {
	e := setup(t)
	hs := e.api()
	run := e.enqueue()
	type result struct{ ids, names []string }
	done := make(chan result, 1)
	// The stream opens before the run starts and follows it live.
	go func() {
		ids, names := sse(t, hs.URL+"/api/v1/runs/"+run+"/events/stream", nil)
		done <- result{ids, names}
	}()
	time.Sleep(300 * time.Millisecond)
	e.start()
	res := <-done
	if len(res.ids) == 0 || res.ids[0] != "1" || res.names[len(res.names)-1] != "end" {
		t.Fatalf("stream: ids %v names %v", res.ids, res.names)
	}
	var evs []Event
	get(t, hs.URL+"/api/v1/runs/"+run+"/events", &evs)
	if len(res.ids) != len(evs) {
		t.Errorf("streamed %d events, the run has %d", len(res.ids), len(evs))
	}

	// Reconnecting with Last-Event-ID sends only what came after.
	ids, names := sse(t, hs.URL+"/api/v1/runs/"+run+"/events/stream", http.Header{"Last-Event-Id": {res.ids[0]}})
	if len(ids) != len(evs)-1 || names[len(names)-1] != "end" {
		t.Errorf("resumed stream: %v %v", ids, names)
	}
}

func TestOpenAPIDescribesEveryRoute(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var spec, code []string
	for path, ops := range doc.Paths {
		for method := range ops {
			spec = append(spec, strings.ToUpper(method)+" "+path)
		}
	}
	for _, rt := range (&Server{}).apiRoutes() {
		code = append(code, rt.method+" "+rt.path)
	}
	// Sign-in's API endpoints are in the same document; its pages are not.
	for _, rt := range (&auth.Service{}).Routes() {
		if strings.HasPrefix(rt.Path, "/api/") {
			code = append(code, rt.Method+" "+rt.Path)
		}
	}
	sort.Strings(spec)
	sort.Strings(code)
	if strings.Join(spec, "\n") != strings.Join(code, "\n") {
		t.Errorf("api/openapi.yaml and the routes differ:\nspec:\n%s\ncode:\n%s", strings.Join(spec, "\n"), strings.Join(code, "\n"))
	}
}

// person creates an account in teams and returns an access token for it.
func (e *env) person(a *auth.Service, name string, teams ...string) string {
	e.t.Helper()
	ctx := context.Background()
	id := store.NewUserID()
	if err := e.store.Q().InsertUser(ctx, db.InsertUserParams{ID: id, Username: name, Provider: "local"}); err != nil {
		e.t.Fatal(err)
	}
	for _, t := range teams {
		if err := e.store.Q().AddTeam(ctx, db.AddTeamParams{UserID: id, Team: t, Source: "local"}); err != nil {
			e.t.Fatal(err)
		}
	}
	tok, err := a.IssueAccessToken(ctx, id, time.Hour, false)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func call(t *testing.T, method, url, token string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestTheAPINeedsSignInAndHidesOthersContracts(t *testing.T) {
	e := setup(t)
	cat := &CatalogSource{Src: queue.Source{Repo: e.repo, Commit: "HEAD"}}
	if err := cat.Load(); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil)
	srv, err := New(Config{Listen: "127.0.0.1:0", Auth: a}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	run := e.enqueue()
	// poc-web belongs to team-platform, which the lab's Policy makes
	// platform-admin; team-web only sees its name.
	admin := e.person(a, "ayse", "team-platform")
	viewer := e.person(a, "mehmet", "team-web")

	if code := call(t, "GET", hs.URL+"/api/v1/runs", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	var runs []Run
	call(t, "GET", hs.URL+"/api/v1/runs", viewer, &runs)
	if len(runs) != 1 || !runs[0].Restricted || runs[0].PlanHash != "" || runs[0].RequestedBy != "" {
		t.Errorf("viewer's runs = %+v", runs)
	}
	var adminRuns []Run
	call(t, "GET", hs.URL+"/api/v1/runs", admin, &adminRuns)
	if len(adminRuns) != 1 || adminRuns[0].Restricted || adminRuns[0].PlanHash == "" {
		t.Errorf("admin's runs = %+v", adminRuns)
	}
	for _, p := range []string{"/api/v1/runs/" + run, "/api/v1/runs/" + run + "/events", "/api/v1/contracts/poc-web"} {
		if code := call(t, "GET", hs.URL+p, viewer, nil); code != 403 {
			t.Errorf("viewer %s: %d", p, code)
		}
		if code := call(t, "GET", hs.URL+p, admin, nil); code != 200 {
			t.Errorf("admin %s: %d", p, code)
		}
	}
	var cs []ContractSummary
	call(t, "GET", hs.URL+"/api/v1/contracts", viewer, &cs)
	if len(cs) != 1 || cs[0].Name != "poc-web" || !cs[0].Restricted || cs[0].LastRun != "" {
		t.Errorf("viewer's contracts = %+v", cs)
	}
	if code := call(t, "POST", hs.URL+"/api/v1/runs/"+run+"/cancel", viewer, nil); code != 403 {
		t.Errorf("viewer cancels: %d", code)
	}
	if code := call(t, "POST", hs.URL+"/api/v1/runs/"+run+"/cancel", admin, nil); code != 202 {
		t.Errorf("admin cancels: %d", code)
	}
	// Sign-in's own endpoints are mounted too.
	if code := call(t, "GET", hs.URL+"/api/v1/auth/config", "", nil); code != 200 {
		t.Errorf("auth config: %d", code)
	}
	var me map[string]any
	if call(t, "GET", hs.URL+"/api/v1/me", admin, &me); me["username"] != "ayse" {
		t.Errorf("me = %v", me)
	}
}
