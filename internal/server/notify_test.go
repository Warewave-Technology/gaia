package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// smtpSink is a minimal SMTP server for the tests: EHLO, STARTTLS (required before AUTH and MAIL), AUTH PLAIN, MAIL,
// RCPT, DATA, QUIT. It keeps every message it accepts.
type smtpSink struct {
	ln       net.Listener
	tls      *tls.Config
	user, pw string
	mu       sync.Mutex
	msgs     []*mail.Message
	raw      [][]byte
}

func newSMTPSink(t *testing.T, cert tls.Certificate, user, pw string) *smtpSink {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpSink{ln: ln, tls: &tls.Config{Certificates: []tls.Certificate{cert}}, user: user, pw: pw}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *smtpSink) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *smtpSink) serve(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(l string) { w.WriteString(l + "\r\n"); w.Flush() }
	say("220 sink ESMTP")
	secure, authed := false, false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.Fields(strings.TrimSpace(line) + " x")[0])
		switch {
		case cmd == "EHLO":
			if secure {
				say("250-sink\r\n250 AUTH PLAIN")
			} else {
				say("250-sink\r\n250 STARTTLS")
			}
		case cmd == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, s.tls)
			if tc.Handshake() != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(tc), bufio.NewWriter(tc)
		case cmd == "AUTH":
			f := strings.Fields(line)
			b, _ := base64.StdEncoding.DecodeString(f[len(f)-1])
			if !secure || string(b) != "\x00"+s.user+"\x00"+s.pw {
				say("535 bad credentials")
				continue
			}
			authed = true
			say("235 ok")
		case cmd == "MAIL" || cmd == "RCPT":
			if !secure || !authed {
				say("530 authenticate first")
				continue
			}
			say("250 ok")
		case cmd == "DATA":
			say("354 go on")
			var data []byte
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data = append(data, strings.TrimPrefix(l, ".")...)
			}
			m, err := mail.ReadMessage(strings.NewReader(string(data)))
			if err != nil {
				say("554 not a message")
				continue
			}
			s.mu.Lock()
			s.msgs, s.raw = append(s.msgs, m), append(s.raw, data)
			s.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// webhookSink records POSTs, checking their signature.
type webhookSink struct {
	srv    *httptest.Server
	secret string
	status int
	mu     sync.Mutex
	events []Event
	bodies [][]byte
	bad    []string
}

func newWebhookSink(t *testing.T) *webhookSink {
	w := &webhookSink{status: 200}
	w.srv = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.status != 200 {
			rw.WriteHeader(w.status)
			return
		}
		mac := hmac.New(sha256.New, []byte(w.secret))
		mac.Write(b)
		if r.Header.Get("X-Gaia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			w.bad = append(w.bad, "signature of delivery "+r.Header.Get("X-Gaia-Delivery"))
		}
		var ev Event
		if err := json.Unmarshal(b, &ev); err != nil || ev.Event != r.Header.Get("X-Gaia-Event") {
			w.bad = append(w.bad, fmt.Sprintf("body %s: %v", b, err))
		}
		w.events, w.bodies = append(w.events, ev), append(w.bodies, b)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *webhookSink) caPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: w.srv.Certificate().Raw}))
}

// channels adds a webhook and an smtp channel through the API and returns the sinks.
func notifyChannels(t *testing.T, f *fixture) (*webhookSink, *smtpSink) {
	hook := newWebhookSink(t)
	sink := newSMTPSink(t, hook.srv.TLS.Certificates[0], "gaia", "smtp password")
	admin := f.tokens[store.RoleAdmin]
	var wc ChannelCreated
	if st := f.do(admin, "POST", "/api/v1/notification-channels", fmt.Sprintf(`{"name":"hook","kind":"webhook","webhook":{"url":%q,"ca_pem":%q}}`,
		hook.srv.URL+"/gaia", hook.caPEM()), &wc); st != http.StatusCreated || wc.WebhookSecret == "" {
		t.Fatalf("webhook channel: %d %+v", st, wc)
	}
	hook.secret = wc.WebhookSecret
	var mc ChannelCreated
	if st := f.do(admin, "POST", "/api/v1/notification-channels", fmt.Sprintf(`{"name":"mail","kind":"smtp","smtp":{"host":"127.0.0.1","port":%d,
		"username":"gaia","from":"Gaia <gaia@lab.test>","to":["ops@lab.test"],"ca_pem":%q},"smtp_password":"smtp password"}`, sink.port(), hook.caPEM()), &mc); st != http.StatusCreated {
		t.Fatalf("smtp channel: %d %+v", st, mc)
	}
	if b, err := os.ReadFile(f.s.cfg.notifySecretFile("mail")); err != nil || string(b) != "smtp password" {
		t.Fatalf("smtp password file: %q %v", b, err)
	}
	if fi, err := os.Stat(f.s.cfg.notifySecretFile("hook")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("webhook secret file: %v %v", fi, err)
	}
	return hook, sink
}

// T19 gate (a) in miniature: every event kind reaches an SMTP server (STARTTLS, AUTH) and a webhook (HTTPS, signed),
// once; the daily report carries its signed ledger head, by mail as an attachment and in the webhook's body.
func TestNotificationsReachEveryChannel(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	hook, sink := notifyChannels(t, f)
	vmID, repoID, verID, jobID := f.ids["vms"], f.ids["repositories"], f.ids["verifications"], f.ids["jobs"]

	// status: the VM gets its first status
	if _, err := f.s.refreshStatus(ctx, vmID); err != nil {
		t.Fatal(err)
	}
	// job_failed
	if _, err := f.s.db.SetJobState(ctx, "system", jobID, "running", "worker", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetJobState(ctx, "system", jobID, "failed", "worker", "the host did not answer"); err != nil {
		t.Fatal(err)
	}
	// integrity, then quarantine
	aff, _ := json.Marshal([]Affected{{PointID: f.ids["points"], VMID: vmID, Disk: "vda", Problems: []string{"block 3 unreadable"}, Contents: []string{"c0ffee"}}})
	if _, err := f.s.db.FinishVerification(ctx, "system", store.Verification{ID: verID, State: "critical", PointsChecked: 1, Affected: aff}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.RecordQuarantine(ctx, "system", repoID, verID, "m1", []store.QuarantinedContent{{ContentID: "c0ffee", Pack: "p1",
		Reason: "stored bytes do not verify", Points: []int64{f.ids["points"]}}}, map[int64]string{vmID: "full: quarantine"}); err != nil {
		t.Fatal(err)
	}
	// full_backup: a backup is full because its checkpoint went from the host (a VM's first full backup is not one)
	for _, reason := range []string{firstBackupReason, "full: checkpoint gaia-1 of point 1 is not on the host"} {
		if _, err := f.s.db.CreateBackupPoint(ctx, "system", store.Point{VMID: vmID, JobID: jobID, RepositoryID: repoID, Kind: "full", Checkpoint: "c",
			TakenAt: time.Now().UTC().Format(time.RFC3339Nano), Reason: reason, Machine: json.RawMessage(`{}`)},
			[]store.PointDisk{{Target: "vda", SizeBytes: 1 << 20, BlockSize: 1 << 20, StoreID: "p-" + reason[:12]}}); err != nil {
			t.Fatal(err)
		}
	}
	// ledger: a bundle's file changes behind Gaia's back
	if c, err := f.s.checkLedger(ctx, "system", "test"); err != nil || c.Verdict != evidence.OK || c.Entries != 2 {
		t.Fatalf("first ledger check: %+v %v", c, err)
	}
	if err := os.WriteFile(filepath.Join(f.s.cfg.bundlesDir(), "seed-vm-test-1", "result.json"), []byte(`{"verdict":"CRITICAL"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := f.s.checkLedger(ctx, "system", "test"); err != nil || c.Verdict != evidence.Critical || !strings.Contains(strings.Join(c.Problems, ";"), "file_modified") {
		t.Fatalf("ledger check after the change: %+v %v", c, err)
	}
	// daily_report
	var rep store.DailyReport
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/daily-reports", "", &rep); st != http.StatusCreated || rep.HeadSeq != 2 {
		t.Fatalf("daily report: %d %+v", st, rep)
	}

	for range 2 { // the second round must send nothing new
		if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{evDaily, evFullBackup, evIntegrity, evJobFailed, evLedger, evQuarantine, evStatus}
	hook.mu.Lock()
	var got []string
	for _, e := range hook.events {
		got = append(got, e.Event)
	}
	hook.mu.Unlock()
	slices.Sort(got)
	if !slices.Equal(got, want) || len(hook.bad) > 0 {
		t.Fatalf("webhook got %v (problems %v), want %v", got, hook.bad, want)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var subjects, mailed []string
	var attachment []byte
	for _, m := range sink.msgs {
		subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
		subjects = append(subjects, subj)
		mailed = append(mailed, m.Header.Get("X-Gaia-Event"))
		_, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
		mr := multipart.NewReader(m.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FileName() != "" {
				b, _ := io.ReadAll(p)
				attachment, _ = base64.StdEncoding.DecodeString(strings.ReplaceAll(string(b), "\r\n", ""))
			}
		}
	}
	slices.Sort(mailed)
	if !slices.Equal(mailed, want) {
		t.Fatalf("mail got %v, want %v (subjects %q)", mailed, want, subjects)
	}
	if !slices.ContainsFunc(subjects, func(s string) bool { return strings.Contains(s, "seed-vm: (new) -> ") }) {
		t.Fatalf("no status subject among %q", subjects)
	}
	if !slices.ContainsFunc(subjects, func(s string) bool {
		return strings.HasSuffix(s, "seed-vm: the backup was full (checkpoint gaia-1 of point 1 is not on the host)")
	}) {
		t.Fatalf("no full_backup subject among %q", subjects)
	}
	// The mailed report and the posted body both hold the head statement, which verifies the installation's ledger.
	pub, _ := evidence.LoadPublicKey(f.s.cfg.pubPath())
	var posted []byte
	for i, e := range hook.events {
		if e.Event == evDaily {
			posted = hook.bodies[i]
		}
	}
	for name, doc := range map[string][]byte{"attachment": attachment, "webhook body": posted} {
		h := evidence.HeadFromDocument(doc)
		r, err := evidence.VerifyLedgerHeads(f.s.cfg.ledgerPath(), t.TempDir(), pub, [][]byte{h})
		if err != nil || len(r.Heads) != 1 || r.Heads[0].Result != evidence.OK || r.Heads[0].Seq != 2 {
			t.Fatalf("%s: head %+v %v", name, r.Heads, err)
		}
	}
	ns, err := f.s.db.Notifications(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if n.State != "sent" || n.Attempts != 1 {
			t.Fatalf("notification %d: %s after %d attempts: %s", n.ID, n.State, n.Attempts, n.LastError)
		}
	}
	if sum, err := f.s.notifySummary(ctx); err != nil || sum.State != "OK" || sum.Enabled != 2 {
		t.Fatalf("summary: %+v %v", sum, err)
	}
}

// A webhook that answers 503 keeps the message queued with a growing pause, shows up as failing, and gives up in the
// end; a test message reports its outcome at once.
func TestNotificationRetry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	hook, _ := notifyChannels(t, f)
	hook.mu.Lock()
	hook.status = 503
	hook.mu.Unlock()
	if _, err := f.s.refreshStatus(ctx, f.ids["vms"]); err != nil {
		t.Fatal(err)
	}
	if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, err := f.s.db.ChannelByName(ctx, "hook")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := f.s.db.Notifications(ctx, ch.ID, 10)
	var ns []store.Notification
	for _, n := range all {
		if n.Event == evStatus { // (the scheduled daily report is due too)
			ns = append(ns, n)
		}
	}
	if len(ns) != 1 || ns[0].State != "queued" || ns[0].Attempts != 1 || !strings.Contains(ns[0].LastError, "503") {
		t.Fatalf("after a 503: %+v", ns)
	}
	next, _ := time.Parse(time.RFC3339Nano, ns[0].NextAttemptAt)
	if d := time.Until(next); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("next attempt in %v, want about a minute", d)
	}
	if sum, _ := f.s.notifySummary(ctx); sum.State != "WARNING" || !strings.Contains(sum.Text, "hook") {
		t.Fatalf("summary while failing: %+v", sum)
	}
	if backoff(1) != time.Minute || backoff(3) != 4*time.Minute || backoff(20) != time.Hour {
		t.Fatalf("backoff %v %v %v", backoff(1), backoff(3), backoff(20))
	}
	n, err := f.s.db.NotificationAttempt(ctx, ns[0].ID, fmt.Errorf("still 503"), time.Time{})
	if err != nil || n.State != "failed" || n.Attempts != 2 {
		t.Fatalf("giving up: %+v %v", n, err)
	}
	if cs, _ := f.s.db.StateChanges(ctx, "notification", n.ID, 0, 10); len(cs) != 1 || cs[0].To != "failed" {
		t.Fatalf("state changes: %+v", cs)
	}
	var tn store.Notification
	if st := f.do(f.tokens[store.RoleAdmin], "POST", fmt.Sprintf("/api/v1/notification-channels/%d/test", ch.ID), "", &tn); st != 200 ||
		tn.State != "queued" || tn.Attempts != 1 || !strings.Contains(tn.LastError, "503") {
		t.Fatalf("test message: %d %+v", st, tn)
	}
	// Off, nothing more is queued for it; on again, it starts from now.
	if c, err := f.s.db.SetChannelEnabled(ctx, "admin", ch.ID, false, "test"); err != nil || c.Enabled {
		t.Fatal(c, err)
	}
	if _, err := f.s.db.SetJobState(ctx, "system", f.ids["jobs"], "running", "worker", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetJobState(ctx, "system", f.ids["jobs"], "failed", "worker", "x"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if c, err := f.s.db.SetChannelEnabled(ctx, "admin", ch.ID, true, "test"); err != nil || !c.Enabled {
		t.Fatal(c, err)
	}
	if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ns, _ := f.s.db.Notifications(ctx, ch.ID, 10); slices.ContainsFunc(ns, func(n store.Notification) bool { return n.Event == evJobFailed }) {
		t.Fatalf("an event of the time the channel was off was queued: %+v", ns)
	}
}

// T19 gate (b) in miniature: after a daily report, cutting the ledger back is detected by Gaia's own ledger check and
// by gaia evidence verify with the report as the head; the cut ledger alone verifies.
func TestDailyReportAnchorsLedger(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	key, err := evidence.LoadPrivateKey(f.s.cfg.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	bdir := filepath.Join(f.s.cfg.bundlesDir(), "seed-vm-test-2")
	os.MkdirAll(bdir, 0o755)
	os.WriteFile(filepath.Join(bdir, "result.json"), []byte(`{"verdict":"OK"}`), 0o644)
	sha, err := evidence.Seal(bdir, "seed-vm-test-2", map[string]string{"vm": "seed-vm"}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Append(f.s.cfg.ledgerPath(), evidence.Entry{BundleID: "seed-vm-test-2", BundleSHA256: sha, Subject: "seed-vm", Verdict: "OK"}, key); err != nil {
		t.Fatal(err)
	}
	rep, err := f.s.makeDailyReport(ctx, "admin", "test")
	if err != nil || rep.HeadSeq != 3 {
		t.Fatalf("report: %+v %v", rep, err)
	}
	var doc DailyReportDoc
	if err := json.Unmarshal(rep.Report, &doc); err != nil || doc.Format != DailyReportFormat || doc.LedgerCheck.Verdict != "OK" || len(doc.VMs) != 1 {
		t.Fatalf("report doc: %+v %v", doc, err)
	}
	// Cut the last entry and its bundle: the ledger alone is still OK.
	b, _ := os.ReadFile(f.s.cfg.ledgerPath())
	lines := strings.SplitAfter(string(b), "\n")
	if err := os.WriteFile(f.s.cfg.ledgerPath(), []byte(lines[0]+lines[1]), 0o644); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(bdir)
	pub, _ := evidence.LoadPublicKey(f.s.cfg.pubPath())
	if r, _ := evidence.VerifyLedger(f.s.cfg.ledgerPath(), f.s.cfg.bundlesDir(), pub); r.Verdict != evidence.OK {
		t.Fatalf("the cut ledger alone: %s %v", r.Verdict, r.AllFindings())
	}
	h := evidence.HeadFromDocument(rep.Report)
	r, _ := evidence.VerifyLedgerHeads(f.s.cfg.ledgerPath(), f.s.cfg.bundlesDir(), pub, [][]byte{h})
	if r.Verdict != evidence.Critical || len(r.Findings) != 1 || r.Findings[0].Code != evidence.CodeLedgerTruncated {
		t.Fatalf("with the report's head: %s %+v", r.Verdict, r.Findings)
	}
	c, err := f.s.checkLedger(ctx, "system", "test")
	if err != nil || c.Verdict != evidence.Critical || !strings.Contains(strings.Join(c.Problems, ";"), "ledger_truncated") {
		t.Fatalf("Gaia's own check: %+v %v", c, err)
	}
	if cs, _ := f.s.db.StateChanges(ctx, "ledger", 1, 0, 10); len(cs) == 0 || cs[len(cs)-1].To != evidence.Critical {
		t.Fatalf("ledger state changes: %+v", cs)
	}
	var sys SystemInfo
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/system", "", &sys); st != 200 || sys.Ledger.Verdict != evidence.Critical {
		t.Fatalf("system: %d %+v", st, sys.Ledger)
	}
}
