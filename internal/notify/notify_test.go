package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// inbox collects what the fake Slack, Teams and webhook receivers and
// the fake mail server got.
type inbox struct {
	mu   sync.Mutex
	got  map[string][]string
	fail map[string]int // path → how many requests fail first
}

func (b *inbox) add(k, v string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got[k] = append(b.got[k], v)
}

func (b *inbox) get(k string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got[k]...)
}

func (b *inbox) wait(t *testing.T, k string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := b.get(k); len(got) >= n {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: got %d, want %d", k, len(b.get(k)), n)
	return nil
}

func (b *inbox) http(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		failing := b.fail[r.URL.Path] > 0
		if failing {
			b.fail[r.URL.Path]--
		}
		b.mu.Unlock()
		if failing {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		b.add(r.URL.Path, r.Header.Get("X-Gaia-Signature-256")+"\n"+string(body))
	}))
}

// smtpServer is the least an SMTP server does: no TLS, no auth.
func (b *inbox) smtp(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				r := bufio.NewReader(c)
				say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
				say("220 test")
				var rcpt []string
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						say("250 test")
					case strings.HasPrefix(cmd, "RCPT TO:"):
						rcpt = append(rcpt, strings.Trim(strings.TrimSpace(line)[8:], "<>"))
						say("250 ok")
					case strings.HasPrefix(cmd, "DATA"):
						say("354 go")
						var msg strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							msg.WriteString(l)
						}
						b.add("mail", strings.Join(rcpt, ",")+"\n"+msg.String())
						say("250 ok")
					case strings.HasPrefix(cmd, "QUIT"):
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

// graph is the lab catalog with teams and channels pointing at the fakes.
func graph(t *testing.T, web, mail string) *catalog.Graph {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "lab")
	var files []catalog.File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		if rel == filepath.Join("platform", "platform.yaml") {
			data = []byte(strings.Replace(string(data), "  secrets: bao\n", "  secrets: bao\n  notifications: { email: mail }\n", 1))
		}
		files = append(files, catalog.File{Path: rel, Data: data})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, catalog.File{Path: "notify.yaml", Data: []byte(`apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: slack-platform }
spec: { type: slack, credentials: bao://kv/platform/notify/slack }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: teams-ops }
spec: { type: msteams, credentials: bao://kv/platform/notify/teams }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: hook }
spec: { type: webhook, endpoint: "` + web + `/hook", credentials: bao://kv/platform/notify/hook }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: mail }
spec: { type: smtp, endpoint: "` + mail + `", from: "Gaia <gaia@example.com>" }
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-platform }
spec:
  notifications:
    - { provider: slack-platform, triggers: [change.pending, change.failed] }
    - { provider: hook, triggers: [change.pending, change.applied] }
    - { provider: mail, triggers: [change.applied], to: [platform@example.com] }
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-ops }
spec:
  notifications:
    - { provider: teams-ops, triggers: [change.pending] }
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec:
  notifications:
    - { provider: teams-ops, triggers: [change.applied] }
`)})
	cat, err := catalog.Load("c0ffee", files)
	if err != nil {
		t.Fatal(err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestNotificationsReachSubscribersOnce(t *testing.T) {
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

	box := &inbox{got: map[string][]string{}, fail: map[string]int{"/teams": 1}}
	hs := box.http(t)
	defer hs.Close()
	mailAddr := box.smtp(t)
	g := graph(t, hs.URL, mailAddr)

	secrets := func(_ context.Context, addr, ref string) (map[string]string, error) {
		if addr != "http://192.168.1.25:8200" { // the Platform's OpenBao
			t.Errorf("secrets addr %q", addr)
		}
		switch ref {
		case "bao://kv/platform/notify/slack":
			return map[string]string{"url": hs.URL + "/slack"}, nil
		case "bao://kv/platform/notify/teams":
			return map[string]string{"url": hs.URL + "/teams"}, nil
		case "bao://kv/platform/notify/hook":
			return map[string]string{"secret": "hook-secret"}, nil
		}
		return nil, os.ErrNotExist
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &SendWorker{Store: s, Secrets: secrets})
	rc, err := river.NewClient(riverpgxv5.New(s.Pool()), &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Stop(context.Background()) })

	// People: ayse owns poc-web (team-platform) and subscribed; can is in
	// team-web, which sees poc-web's name only, and subscribed too.
	person := func(name, team, email string) {
		id := store.NewUserID()
		q := s.Q()
		if err := q.InsertUser(ctx, db.InsertUserParams{ID: id, Username: name, Provider: "local"}); err != nil {
			t.Fatal(err)
		}
		_ = q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: team, Source: "local"})
		_ = q.SetEmail(ctx, db.SetEmailParams{ID: id, Email: pgtype.Text{String: email, Valid: true}})
		_ = q.AddSubscription(ctx, db.AddSubscriptionParams{UserID: id, Trigger: "change.pending"})
	}
	person("ayse", "team-platform", "ayse@example.com")
	person("can", "team-web", "can@example.com")

	n := &Notifier{Store: s, Queue: rc}
	ev := Event{
		Trigger: "change.pending", Change: "chg-1", Contract: "poc-web", Status: "pending", PlanHash: "sha256:x",
		RequestedBy: []string{"mehmet"}, Summary: Summary{Create: 1},
		Approvers: []Approver{{Name: "owner", Teams: []string{"team-platform", "team-ops"}, Candidates: []string{"ayse"}, Why: "env test needs approval"}},
	}
	for range 2 { // the engine evaluates often: once is enough
		if err := n.Emit(ctx, g, ev); err != nil {
			t.Fatal(err)
		}
	}

	slack := box.wait(t, "/slack", 1)
	if !strings.Contains(slack[0], `poc-web: değişiklik onay bekliyor`) || !strings.Contains(slack[0], "ayse") {
		t.Errorf("slack = %s", slack[0])
	}
	hook := box.wait(t, "/hook", 1)
	sig, body, _ := strings.Cut(hook[0], "\n")
	mac := hmac.New(sha256.New, []byte("hook-secret"))
	mac.Write([]byte(body))
	if sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("webhook signature %q", sig)
	}
	var got Event
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Change != "chg-1" || got.URL != "http://localhost:8080/approve/chg-1" {
		t.Errorf("webhook body = %s (%v)", body, err)
	}
	// Teams failed once and was tried again.
	teams := box.wait(t, "/teams", 1)
	if !strings.Contains(teams[0], "AdaptiveCard") || !strings.Contains(teams[0], "Action.OpenUrl") {
		t.Errorf("teams = %s", teams[0])
	}
	mail := box.wait(t, "mail", 1)
	if !strings.HasPrefix(mail[0], "ayse@example.com\n") || !strings.Contains(mail[0], "Subject: =?utf-8?q?poc-web") {
		t.Errorf("mail = %s", mail[0])
	}

	time.Sleep(500 * time.Millisecond)
	for k, want := range map[string]int{"/slack": 1, "/hook": 1, "/teams": 1, "mail": 1} {
		if got := len(box.get(k)); got != want {
			t.Errorf("%s: %d messages, want %d", k, got, want)
		}
	}
	rows, err := s.Q().Notifications(ctx, db.NotificationsParams{ChangeID: "chg-1", MaxRows: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Status != "sent" {
			t.Errorf("%s → %s: %s (%s)", r.Channel, r.Recipient, r.Status, r.LastError.String)
		}
		if r.Channel == "teams-ops" && r.Attempts != 2 {
			t.Errorf("teams attempts = %d", r.Attempts)
		}
	}
	if len(rows) != 4 {
		t.Errorf("%d notifications, want 4", len(rows))
	}

	// applied: the team mail goes to the team's address; team-web's
	// channel is not told about another team's contract.
	ev.Trigger, ev.Status, ev.Approvers = "change.applied", "applied", nil
	if err := n.Emit(ctx, g, ev); err != nil {
		t.Fatal(err)
	}
	mail = box.wait(t, "mail", 2)
	if !strings.HasPrefix(mail[1], "platform@example.com\n") || !strings.Contains(mail[1], "From: Gaia <gaia@example.com>") {
		t.Errorf("team mail = %s", mail[1])
	}
	box.wait(t, "/hook", 2)
	time.Sleep(300 * time.Millisecond)
	if len(box.get("/teams")) != 1 {
		t.Error("team-web heard of poc-web")
	}
}
