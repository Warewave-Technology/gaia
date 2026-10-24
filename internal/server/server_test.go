package server

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

type secrets map[string]map[string]string

func (s secrets) Read(_ context.Context, ref string) (map[string]string, error) {
	if v, ok := s[ref]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("%s: no such secret", ref)
}
func (secrets) Addr() string  { return "http://bao:8200" }
func (secrets) Token() string { return "t" }

type archive struct{}

func (archive) Put(_ context.Context, key string, _ []byte, _ time.Time) (string, error) {
	return "mem://" + key, nil
}

type env struct {
	t     *testing.T
	store *store.Store
	repo  string
	queue *river.Client[pgx.Tx]
	busy  time.Duration
	repos *repo.Mirrors
	git   *gittest.Repo
	// finished is the workers' run-finished hook, when a test sets one.
	finished func(context.Context, queue.ApplyArgs, *catalog.Graph) error
	// failing makes the workers' systems fail to start.
	failing bool
}

// setup migrates a database and commits the lab example, with poc-web at
// zero replicas: a run that touches no system but still runs every step
// around them (lock, plan, stages, record).
func setup(t *testing.T) *env {
	t.Helper()
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	r := gittest.New(t)
	root := filepath.Join("..", "..", "examples", "lab")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		content := string(data)
		if rel == filepath.Join("contracts", "poc-web.yaml") {
			content = strings.Replace(content, "replicas: 1", "replicas: 0", 1)
		}
		if rel == filepath.Join("platform", "providers", "config-repo.yaml") {
			// The config repository is this repository.
			content = strings.Replace(content, "url: /home/labadm/gaia", "url: "+r.Dir, 1)
			content = strings.Replace(content, "ref: main", "ref: HEAD", 1)
		}
		r.Write(filepath.ToSlash(rel), content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Commit("lab")
	q, err := river.NewClient(riverpgxv5.New(s.Pool()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, store: s, repo: r.Dir, queue: q, repos: &repo.Mirrors{Dir: t.TempDir()}, git: r}
}

func (e *env) systems(*catalog.Graph) (apply.Deps, error) {
	if e.failing {
		return apply.Deps{}, fmt.Errorf("proxmox is down")
	}
	return apply.Deps{
		Store: e.store,
		Secrets: secrets{
			"bao://kv/platform/pulumi-state": {"username": "u", "password": "p"},
			"bao://kv/platform/ssh":          {"public": "ssh-ed25519 AAAA", "private": "k"},
			"bao://kv/platform/seaweedfs":    {"access_key": "a", "secret_key": "s"},
		},
		Archive: func(string, string, string, string) (runrecord.Archive, error) { return archive{}, nil },
		Out:     io.Discard,
	}, nil
}

// snapshot is the test repository at its head.
func (e *env) snapshot() repo.Snapshot {
	e.t.Helper()
	platform := repo.Rev{Name: "platform", URL: e.repo}
	commit, err := e.repos.Sync(context.Background(), platform.Repo())
	if err != nil {
		e.t.Fatal(err)
	}
	platform.Commit = commit
	return repo.Snapshot{Platform: platform}
}

func (e *env) enqueue() string {
	e.t.Helper()
	snap := e.snapshot()
	g, err := e.repos.Graph(context.Background(), snap)
	if err != nil {
		e.t.Fatal(err)
	}
	runs, err := queue.Enqueue(context.Background(), e.store, e.queue, g, snap, []string{"poc-web"}, "ops@test")
	if err != nil {
		e.t.Fatal(err)
	}
	return runs[0]
}

// start runs a worker server until the test ends.
func (e *env) start() *Server {
	e.t.Helper()
	srv, err := New(Config{Workers: 1, Repos: e.repos, Systems: e.systems, Finished: e.finished, StopTimeout: 5 * time.Second, BusyWait: e.busy}, e.store)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	e.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			e.t.Errorf("server: %v", err)
		}
	})
	return srv
}

func (e *env) waitFor(run string, statuses ...string) db.Run {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, err := e.store.Q().Run(context.Background(), run)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, st := range statuses {
			if r.Status == st {
				return r
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("run %s is %s, want %v", run, r.Status, statuses)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestQueuedRunIsApplied(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	run := e.enqueue()
	r, _ := e.store.Q().Run(ctx, run)
	if r.Status != "queued" || !r.JobID.Valid || r.RequestedBy.String != "ops@test" || r.StartedAt.Valid {
		t.Fatalf("queued run = %+v", r)
	}
	e.start()
	r = e.waitFor(run, "succeeded", "failed")
	if r.Status != "succeeded" || !r.StartedAt.Valid {
		t.Fatalf("run = %+v", r)
	}
	stages, _ := e.store.Q().RunStages(ctx, run)
	var got []string
	for _, st := range stages {
		got = append(got, st.Contract+"/"+st.Stage+":"+st.Status)
	}
	// A contract without servers has no stages of its own.
	if strings.Join(got, ",") != "/record:succeeded" {
		t.Errorf("stages = %v", got)
	}
	rec, err := e.store.Q().RunRecord(ctx, run)
	if err != nil || !strings.Contains(rec.Body, `"requestedBy":"ops@test"`) {
		t.Errorf("record = %+v, %v", rec, err)
	}
}

func TestAChangedPlanIsNotApplied(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	run := e.enqueue()
	// A host appears after the run was queued: the plan now deletes it.
	if err := e.store.CreateHost(ctx, store.NewHost{UID: store.NewUID(), Contract: "poc-web", Hostname: "labtpoc01", Site: "lab", Network: "lab-poc", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	e.start()
	r := e.waitFor(run, "succeeded", "failed")
	if r.Status != "failed" || !strings.Contains(r.Error.String, "the plan changed since the run was queued") {
		t.Errorf("run = %+v", r)
	}
}

func TestCancelBeforeItStarts(t *testing.T) {
	e := setup(t)
	run := e.enqueue()
	if err := queue.Cancel(context.Background(), e.store, e.queue, run, "ops"); err != nil {
		t.Fatal(err)
	}
	e.start()
	r := e.waitFor(run, "cancelled")
	if !strings.Contains(r.Error.String, "cancelled by ops before it started") {
		t.Errorf("run = %+v", r)
	}
	time.Sleep(500 * time.Millisecond)
	if r := e.waitFor(run, "cancelled", "running", "succeeded"); r.Status != "cancelled" {
		t.Errorf("the cancelled run ran: %+v", r)
	}
	if err := queue.Cancel(context.Background(), e.store, e.queue, run, "ops"); err == nil {
		t.Error("cancelled twice")
	}
}

func TestABusyContractWaits(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = e.store.WithContractLock(ctx, "poc-web", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	e.busy = 200 * time.Millisecond
	run := e.enqueue()
	e.start()
	time.Sleep(time.Second)
	if r, _ := q(e).Run(ctx, run); r.Status != "queued" {
		t.Fatalf("ran while the contract was busy: %+v", r)
	}
	close(release)
	if r := e.waitFor(run, "succeeded", "failed"); r.Status != "succeeded" {
		t.Errorf("run = %+v", r)
	}
}

func TestHealth(t *testing.T) {
	e := setup(t)
	srv, err := New(Config{Listen: "127.0.0.1:0"}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	for _, p := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(hs.URL + p)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %v %v", p, resp, err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}
}

func q(e *env) *db.Queries { return e.store.Q() }

func TestOldEventsArePruned(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	run := e.enqueue()
	e.start()
	e.waitFor(run, "succeeded")
	if _, err := e.store.Pool().Exec(ctx, "update run_events set created_at = now() - interval '100 days' where seq = 1"); err != nil {
		t.Fatal(err)
	}
	before, _ := e.store.Q().RunEvents(ctx, run)
	w := &queue.PruneEventsWorker{Store: e.store}
	if err := w.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := e.store.Q().RunEvents(ctx, run)
	if len(after) != len(before)-1 || after[0].Seq != 2 {
		t.Errorf("%d events before, %d after", len(before), len(after))
	}
}

func readFile(dir, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	return string(b), err
}
