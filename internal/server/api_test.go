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
	sort.Strings(spec)
	sort.Strings(code)
	if strings.Join(spec, "\n") != strings.Join(code, "\n") {
		t.Errorf("api/openapi.yaml and the routes differ:\nspec:\n%s\ncode:\n%s", strings.Join(spec, "\n"), strings.Join(code, "\n"))
	}
}
