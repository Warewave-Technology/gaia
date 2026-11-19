package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Notifications (ADR-007 §10, T19). Events come from state_changes: each channel keeps a cursor there, so an event is
// queued once per channel even across restarts, and a message that cannot be sent is tried again with a growing pause
// until giveUpAfter. Email goes through an SMTP server, webhooks are HTTPS POSTs signed with an HMAC.

// Event kinds a channel can take.
const (
	evStatus     = "status"       // a VM's status changed, with the reason
	evJobFailed  = "job_failed"   // a backup, restore test, verification or state backup failed
	evIntegrity  = "integrity"    // a repository verification found damage
	evQuarantine = "quarantine"   // damaged contents were quarantined (ADR-007 B3)
	evLedger     = "ledger"       // the evidence ledger does not verify, or verifies again
	evFullBackup = "full_backup"  // a backup was full although the VM had an earlier one (a checkpoint gone from the host, a disk resized, a quarantine)
	evDaily      = "daily_report" // the daily report (report.go)
	evTest       = "test"         // a test message sent on request
)

var allEvents = []string{evStatus, evJobFailed, evIntegrity, evQuarantine, evLedger, evFullBackup, evDaily}

const (
	notifyTick  = 5 * time.Second
	retryFirst  = time.Minute
	retryMax    = time.Hour
	giveUpAfter = 24 * time.Hour
	sendTimeout = time.Minute
)

// SMTPConfig is an email channel. Security: starttls (default, port 587), tls (implicit TLS, port 465), or none (no
// encryption; only where asked for in so many words). The password is not part of it (a file under secrets/).
type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"`
	Username string   `json:"username,omitempty"` // empty: no authentication
	From     string   `json:"from"`
	To       []string `json:"to"`
	CAPEM    string   `json:"ca_pem,omitempty"` // CA certificates to check the server's certificate against, when the system's do not sign it
}

// WebhookConfig is a webhook channel: an https:// URL (http:// only when written so). Every POST carries
// X-Gaia-Signature: sha256=<hex HMAC-SHA256 of the body with the channel's secret>.
type WebhookConfig struct {
	URL   string `json:"url"`
	CAPEM string `json:"ca_pem,omitempty"`
}

// ChannelCreate adds a notification channel.
type ChannelCreate struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`   // smtp or webhook
	Events        []string       `json:"events"` // default: all (status, job_failed, integrity, quarantine, ledger, full_backup, daily_report)
	SMTP          *SMTPConfig    `json:"smtp,omitempty"`
	SMTPPassword  string         `json:"smtp_password,omitempty"`
	Webhook       *WebhookConfig `json:"webhook,omitempty"`
	WebhookSecret string         `json:"webhook_secret,omitempty"` // default: a new random secret, returned once
}

// ChannelCreated is the new channel; a webhook's secret is shown here once.
type ChannelCreated struct {
	store.Channel
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// ChannelUpdate turns a channel on or off.
type ChannelUpdate struct {
	Enabled *bool  `json:"enabled"`
	Reason  string `json:"reason"`
}

// Event is the body of a notification: what a webhook receives, and what an email says.
type Event struct {
	Event         string             `json:"event"`
	Installation  string             `json:"installation"`
	EvidenceKeyID string             `json:"evidence_key_id"`
	At            string             `json:"at"`
	Subject       string             `json:"subject"`
	Text          string             `json:"text"`
	Change        *store.StateChange `json:"state_change,omitempty"`
	VM            string             `json:"vm,omitempty"`
	Job           *store.Job         `json:"job,omitempty"`
	// A status change: the deciding reason's code when it changed, and every reason of the VM's status now (when it
	// is still the status the change went to), with codes, scopes and parameters (ADR-007 C3).
	Code    string          `json:"code,omitempty"`
	Reasons []status.Reason `json:"reasons,omitempty"`
	// Daily report only: the report, and its signed ledger head on top, so the saved body works with
	// gaia evidence verify -head.
	LedgerHead json.RawMessage `json:"ledger_head,omitempty"`
	Report     json.RawMessage `json:"report,omitempty"`
}

func (c Config) notifySecretFile(name string) string {
	return filepath.Join(c.secretsDir(), "notify-"+name+".secret")
}

// secretMissing says that a channel needs a secret (a webhook's, or the SMTP password when it logs in) that is not on
// this server: after a rebuild from the recovery kit, which does not carry them, until the channel is added again.
func (c Config) secretMissing(ch store.Channel) bool {
	var smtpCfg SMTPConfig
	needs := ch.Kind == "webhook" || (json.Unmarshal(ch.Config, &smtpCfg) == nil && smtpCfg.Username != "")
	_, err := os.Stat(c.notifySecretFile(ch.Name))
	return needs && err != nil
}

// createChannel checks a channel, stores its secret and records it.
func (s *Server) createChannel(c *call) (int, any, error) {
	var in ChannelCreate
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	if !store.ValidName(in.Name) {
		return 0, nil, fmt.Errorf("%w: a channel name is 1-128 of A-Z a-z 0-9 . _ - and starts with a letter or digit", store.ErrInvalid)
	}
	if len(in.Events) == 0 {
		in.Events = slices.Clone(allEvents)
	}
	for _, e := range in.Events {
		if !slices.Contains(allEvents, e) {
			return 0, nil, fmt.Errorf("%w: event %q (one of %s)", store.ErrInvalid, e, strings.Join(allEvents, ", "))
		}
	}
	slices.Sort(in.Events)
	in.Events = slices.Compact(in.Events)
	var cfg any
	var secret string
	out := ChannelCreated{}
	switch in.Kind {
	case "smtp":
		if in.SMTP == nil || in.Webhook != nil || in.WebhookSecret != "" {
			return 0, nil, fmt.Errorf("%w: an smtp channel needs \"smtp\" and no webhook fields", store.ErrInvalid)
		}
		if err := checkSMTP(in.SMTP); err != nil {
			return 0, nil, err
		}
		if (in.SMTP.Username == "") != (in.SMTPPassword == "") {
			return 0, nil, fmt.Errorf("%w: smtp username and smtp_password go together", store.ErrInvalid)
		}
		cfg, secret = in.SMTP, in.SMTPPassword
	case "webhook":
		if in.Webhook == nil || in.SMTP != nil || in.SMTPPassword != "" {
			return 0, nil, fmt.Errorf("%w: a webhook channel needs \"webhook\" and no smtp fields", store.ErrInvalid)
		}
		if err := checkWebhook(in.Webhook); err != nil {
			return 0, nil, err
		}
		secret = in.WebhookSecret
		if secret == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return 0, nil, err
			}
			secret = base64.RawURLEncoding.EncodeToString(b)
			out.WebhookSecret = secret
		}
		cfg = in.Webhook
	default:
		return 0, nil, fmt.Errorf("%w: kind %q (smtp or webhook)", store.ErrInvalid, in.Kind)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	path := s.cfg.notifySecretFile(in.Name)
	if secret != "" {
		// A name is never used twice (channels are disabled, not deleted), so a file left here is from a failed attempt.
		if _, err := s.db.ChannelByName(ctx, in.Name); err == nil {
			return 0, nil, fmt.Errorf("%w: a channel named %s exists", store.ErrConflict, in.Name)
		}
		os.Remove(path)
		if err := writeExclusive(path, []byte(secret), 0o600); err != nil {
			return 0, nil, fmt.Errorf("storing the channel's secret: %w", err)
		}
	}
	ch, err := s.db.AddChannel(ctx, c.user.Username, store.Channel{Name: in.Name, Kind: in.Kind, Config: b, Events: in.Events})
	if err != nil {
		if secret != "" {
			os.Remove(path)
		}
		return 0, nil, err
	}
	out.Channel = ch
	return created(c, "notification channel "+ch.Name, out, ch.ID, nil)
}

func checkSMTP(c *SMTPConfig) error {
	bad := func(f string, a ...any) error {
		return fmt.Errorf("%w: smtp: %s", store.ErrInvalid, fmt.Sprintf(f, a...))
	}
	if c.Security == "" {
		c.Security = "starttls"
	}
	switch c.Security {
	case "starttls", "none":
		if c.Port == 0 {
			c.Port = 587
		}
	case "tls":
		if c.Port == 0 {
			c.Port = 465
		}
	default:
		return bad("security %q (starttls, tls or none)", c.Security)
	}
	if c.Host == "" || strings.ContainsAny(c.Host, "/ :") && net.ParseIP(c.Host) == nil {
		return bad("host %q", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return bad("port %d", c.Port)
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return bad("from %q: %v", c.From, err)
	}
	if len(c.To) == 0 {
		return bad("no \"to\" address")
	}
	for _, t := range c.To {
		if _, err := mail.ParseAddress(t); err != nil {
			return bad("to %q: %v", t, err)
		}
	}
	if c.CAPEM != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CAPEM)) {
		return bad("ca_pem holds no certificate")
	}
	return nil
}

func checkWebhook(c *WebhookConfig) error {
	u, err := url.Parse(c.URL)
	switch {
	case err != nil:
		return fmt.Errorf("%w: webhook url %q: %v", store.ErrInvalid, c.URL, err)
	case u.Scheme != "https" && u.Scheme != "http", u.Host == "", u.User != nil:
		return fmt.Errorf("%w: webhook url %q: https://host[:port]/path (or http://), without credentials", store.ErrInvalid, c.URL)
	case c.CAPEM != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CAPEM)):
		return fmt.Errorf("%w: webhook ca_pem holds no certificate", store.ErrInvalid)
	}
	return nil
}

// --- the notifier ---

// notifyLoop turns state changes into notifications, sends what is due, and makes the daily report when it is due,
// until ctx ends.
func (s *Server) notifyLoop(ctx context.Context) {
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		if err := s.notifyOnce(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Error("notifications", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) notifyOnce(ctx context.Context, now time.Time) error {
	var errs []error
	if err := s.dailyReportIfDue(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("daily report: %w", err))
	}
	chans, err := s.db.Channels(ctx)
	if err != nil {
		return err
	}
	for _, ch := range chans {
		if ch.Enabled {
			if err := s.queueEvents(ctx, ch); err != nil {
				errs = append(errs, fmt.Errorf("channel %s: %w", ch.Name, err))
			}
		}
	}
	due, err := s.db.DueNotifications(ctx, time.Now(), 50) // what was just queued too
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, n := range due {
		if ctx.Err() != nil {
			break
		}
		if _, err := s.attempt(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("notification %d: %w", n.ID, err))
		}
	}
	return errors.Join(errs...)
}

// queueEvents queues the channel's notifications for the state changes after its cursor.
func (s *Server) queueEvents(ctx context.Context, ch store.Channel) error {
	changes, err := s.db.StateChanges(ctx, "", 0, ch.Cursor, 500)
	if err != nil || len(changes) == 0 {
		return err
	}
	var ns []store.Notification
	for _, sc := range changes {
		ev, err := s.classify(ctx, sc)
		if err != nil {
			return fmt.Errorf("state change %d: %w", sc.Seq, err)
		}
		if ev == nil || !slices.Contains(ch.Events, ev.Event) {
			continue
		}
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		ns = append(ns, store.Notification{Event: ev.Event, Ref: sc.Seq, Subject: ev.Subject, Body: b})
	}
	return s.db.QueueEvents(ctx, ch.ID, ch.Cursor, changes[len(changes)-1].Seq, ns)
}

// classify says whether a state change is an event, and which.
func (s *Server) classify(ctx context.Context, sc store.StateChange) (*Event, error) {
	ev := s.event("", sc.At)
	sc2 := sc
	ev.Change = &sc2
	switch {
	case sc.Entity == "vm_status":
		vm, err := s.db.VM(ctx, sc.EntityID)
		if err != nil {
			return nil, err
		}
		from := sc.From
		if from == "" {
			from = "(new)"
		}
		ev.Event, ev.VM, ev.Code = evStatus, vm.Name, sc.Code
		ev.Subject = fmt.Sprintf("%s: %s -> %s", vm.Name, from, sc.To)
		ev.Text = fmt.Sprintf("VM %s is %s (was %s) since %s.\nWhy: %s", vm.Name, sc.To, from, sc.At, sc.Reason)
		if sc.Code != "" {
			ev.Text += "\nCode: " + sc.Code
		}
		if r, _, err := s.storedStatus(ctx, vm.ID); err != nil {
			return nil, err
		} else if r.Status == sc.To {
			ev.Reasons = r.Reasons
		}
	case sc.Entity == "job" && sc.To == "failed":
		j, err := s.db.Job(ctx, sc.EntityID)
		if err != nil {
			return nil, err
		}
		ev.Event, ev.Job = evJobFailed, &j
		what := fmt.Sprintf("%s job %d", j.Kind, j.ID)
		if j.VMID != nil {
			vm, err := s.db.VM(ctx, *j.VMID)
			if err != nil {
				return nil, err
			}
			ev.VM = vm.Name
			what += " of " + vm.Name
		}
		ev.Subject = what + " failed"
		ev.Text = fmt.Sprintf("The %s failed at %s.\nError: %s\nIt was started for: %s", what, sc.At, sc.Reason, j.Reason)
	case sc.Entity == "point" && sc.From == "" && sc.To == "unchecked":
		p, err := s.db.Point(ctx, sc.EntityID)
		if err != nil {
			return nil, err
		}
		why := unplannedFull(p)
		if why == "" {
			return nil, nil
		}
		vm, err := s.db.VM(ctx, p.VMID)
		if err != nil {
			return nil, err
		}
		ev.Event, ev.VM = evFullBackup, vm.Name
		ev.Subject = fmt.Sprintf("%s: the backup was full (%s)", vm.Name, why)
		ev.Text = fmt.Sprintf("Point %d of VM %s (job %d, %s) is a full backup, not an incremental, because %s.\n"+
			"Its data is protected; the full backup only takes longer and more space. If nobody changed the VM or its host, find out who did.",
			p.ID, vm.Name, p.JobID, sc.At, why)
	case sc.Entity == "state_backup" && sc.To == "failed":
		ev.Event = evJobFailed
		ev.Subject = fmt.Sprintf("state backup %d failed", sc.EntityID)
		ev.Text = fmt.Sprintf("Gaia's backup of its own state (catalog, ledger, evidence) failed at %s: a lost server could only be rebuilt from an older one.\nError: %s", sc.At, sc.Reason)
	case sc.Entity == "verification" && sc.To == "failed":
		ev.Event = evJobFailed
		ev.Subject = fmt.Sprintf("repository verification %d could not run", sc.EntityID)
		ev.Text = fmt.Sprintf("Repository verification %d could not read the repository at %s; nothing was marked damaged.\nError: %s", sc.EntityID, sc.At, sc.Reason)
	case sc.Entity == "verification" && sc.To == "critical":
		v, err := s.db.Verification(ctx, sc.EntityID)
		if err != nil {
			return nil, err
		}
		r, err := s.db.Repository(ctx, v.RepositoryID)
		if err != nil {
			return nil, err
		}
		var aff []Affected
		if len(v.Affected) > 0 {
			if err := json.Unmarshal(v.Affected, &aff); err != nil {
				return nil, fmt.Errorf("verification %d: affected: %w", v.ID, err)
			}
		}
		var lines []string
		for _, a := range aff {
			l := fmt.Sprintf("- point %d, disk %s", a.PointID, a.Disk)
			if len(a.Contents) > 0 {
				l += ": damaged contents " + strings.Join(a.Contents, ", ")
			} else if len(a.Problems) > 0 {
				l += ": " + a.Problems[0]
			}
			lines = append(lines, l)
		}
		ev.Event = evIntegrity
		ev.Subject = fmt.Sprintf("repository %s: verification %d found damage in %d point disk(s)", r.Name, v.ID, len(aff))
		ev.Text = fmt.Sprintf("The %s verification %d of repository %s found damage at %s. These points are marked failed and prove nothing any more:\n%s",
			v.Mode, v.ID, r.Name, sc.At, strings.Join(lines, "\n"))
	case sc.Entity == "quarantine":
		ev.Event = evQuarantine
		ev.Subject = fmt.Sprintf("damaged repository contents quarantined (verification %d)", sc.EntityID)
		ev.Text = fmt.Sprintf("At %s, %s.\nThe next backup of each VM named is full, so new points do not reuse the damaged data.", sc.At, sc.Reason)
	case sc.Entity == "ledger" && (sc.To != "OK" || (sc.From != "" && sc.From != "OK")):
		ev.Event = evLedger
		if sc.To == "OK" {
			ev.Subject = "the evidence ledger verifies again"
		} else {
			ev.Subject = "the evidence ledger is " + sc.To
		}
		ev.Text = fmt.Sprintf("Ledger check at %s: %s (was %s).\n%s", sc.At, sc.To, orDash(sc.From), sc.Reason)
	default:
		return nil, nil
	}
	ev.Subject = "Gaia " + ev.Installation + ": " + ev.Subject
	return &ev, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// event starts an event with what every one carries.
func (s *Server) event(kind, at string) Event {
	host, _ := os.Hostname()
	return Event{Event: kind, Installation: host, EvidenceKeyID: s.evidenceKeyID(), At: at}
}

func (s *Server) evidenceKeyID() string {
	if pub, err := loadPub(s.cfg); err == nil {
		return pub
	}
	return ""
}

// attempt sends one queued notification and records the outcome. The returned error is about recording it; a failed
// send is in the notification.
func (s *Server) attempt(ctx context.Context, n store.Notification) (store.Notification, error) {
	ch, err := s.db.Channel(ctx, n.ChannelID)
	if err != nil {
		return n, err
	}
	var sendErr error
	if !ch.Enabled {
		sendErr = errors.New("the channel is disabled")
	} else {
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		sendErr = s.deliver(sctx, ch, n)
		cancel()
	}
	var retry time.Time
	if sendErr != nil && ch.Enabled {
		created, err := time.Parse(time.RFC3339Nano, n.CreatedAt)
		if err != nil {
			return n, err
		}
		if wait := backoff(n.Attempts + 1); time.Since(created)+wait < giveUpAfter {
			retry = time.Now().Add(wait)
		}
		s.log.Warn("notification not sent", "notification", n.ID, "channel", ch.Name, "attempt", n.Attempts+1, "error", sendErr)
	}
	return s.db.NotificationAttempt(context.WithoutCancel(ctx), n.ID, sendErr, retry)
}

func backoff(attempt int) time.Duration {
	d := retryFirst
	for i := 1; i < attempt && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

func (s *Server) deliver(ctx context.Context, ch store.Channel, n store.Notification) error {
	var ev Event
	if err := json.Unmarshal(n.Body, &ev); err != nil {
		return fmt.Errorf("notification body: %w", err)
	}
	switch ch.Kind {
	case "smtp":
		var cfg SMTPConfig
		if err := json.Unmarshal(ch.Config, &cfg); err != nil {
			return err
		}
		var password string
		if cfg.Username != "" {
			b, err := os.ReadFile(s.cfg.notifySecretFile(ch.Name))
			if err != nil {
				return fmt.Errorf("the SMTP password: %w", err)
			}
			password = string(b)
		}
		msg, err := mailMessage(cfg, n, ev)
		if err != nil {
			return err
		}
		return sendMail(ctx, cfg, password, msg)
	case "webhook":
		var cfg WebhookConfig
		if err := json.Unmarshal(ch.Config, &cfg); err != nil {
			return err
		}
		secret, err := os.ReadFile(s.cfg.notifySecretFile(ch.Name))
		if err != nil {
			return fmt.Errorf("the webhook secret: %w", err)
		}
		return postWebhook(ctx, cfg, secret, n)
	}
	return fmt.Errorf("channel kind %q", ch.Kind)
}

func tlsConfig(host, caPEM string) (*tls.Config, error) {
	c := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if caPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("ca_pem holds no certificate")
		}
		c.RootCAs = pool
	}
	return c, nil
}

// postWebhook POSTs the notification's body, signed.
func postWebhook(ctx context.Context, cfg WebhookConfig, secret []byte, n store.Notification) error {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return err
	}
	tc, err := tlsConfig(u.Hostname(), cfg.CAPEM)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: sendTimeout, Transport: &http.Transport{TLSClientConfig: tc, Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(n.Body))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(n.Body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gaia")
	req.Header.Set("X-Gaia-Event", n.Event)
	req.Header.Set("X-Gaia-Delivery", strconv.FormatInt(n.ID, 10))
	req.Header.Set("X-Gaia-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the webhook answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// mailMessage is the email for a notification: text, and for a daily report the report as a JSON attachment.
func mailMessage(cfg SMTPConfig, n store.Notification, ev Event) ([]byte, error) {
	var b bytes.Buffer
	host, _ := os.Hostname()
	h := textproto.MIMEHeader{}
	h.Set("From", cfg.From)
	h.Set("To", strings.Join(cfg.To, ", "))
	h.Set("Subject", mime.QEncoding.Encode("utf-8", n.Subject))
	h.Set("Date", time.Now().Format(time.RFC1123Z))
	h.Set("Message-ID", fmt.Sprintf("<gaia-%d-%s-%d@%s>", n.ID, n.Event, n.Ref, host))
	h.Set("MIME-Version", "1.0")
	h.Set("X-Gaia-Event", n.Event)
	mw := multipart.NewWriter(&b)
	h.Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	var head bytes.Buffer
	for _, k := range []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "X-Gaia-Event", "Content-Type"} {
		fmt.Fprintf(&head, "%s: %s\r\n", k, h.Get(k))
	}
	head.WriteString("\r\n")
	text := ev.Text + "\n\n-- \nGaia, installation " + ev.Installation + " (evidence key " + ev.EvidenceKeyID + ")\n"
	pw, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return nil, err
	}
	qp := quotedprintable.NewWriter(pw)
	if _, err := qp.Write([]byte(strings.ReplaceAll(text, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	if len(ev.Report) > 0 {
		name := fmt.Sprintf("gaia-daily-report-%d.json", n.Ref)
		aw, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; name=\"" + name + "\""},
			"Content-Disposition": {"attachment; filename=\"" + name + "\""}, "Content-Transfer-Encoding": {"base64"}})
		if err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(ev.Report)
		for len(enc) > 76 {
			fmt.Fprintf(aw, "%s\r\n", enc[:76])
			enc = enc[76:]
		}
		fmt.Fprintf(aw, "%s\r\n", enc)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), b.Bytes()...), nil
}

// sendMail sends one message through the channel's SMTP server.
func sendMail(ctx context.Context, cfg SMTPConfig, password string, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tc, err := tlsConfig(cfg.Host, cfg.CAPEM)
	if err != nil {
		return err
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	if cfg.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tc}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	host, _ := os.Hostname()
	if err := c.Hello(host); err != nil {
		return err
	}
	if cfg.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s does not offer STARTTLS (security starttls)", addr)
		}
		if err := c.StartTLS(tc); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, password, cfg.Host)); err != nil {
			return fmt.Errorf("authentication: %w", err)
		}
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return err
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, t := range cfg.To {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return err
		}
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("recipient %s: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// testChannel queues a test message for a channel and sends it now.
func (s *Server) testChannel(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	ch, err := s.db.Channel(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	now := time.Now().UTC()
	ev := s.event(evTest, now.Format(time.RFC3339Nano))
	ev.Subject = "Gaia " + ev.Installation + ": test message (channel " + ch.Name + ")"
	ev.Text = fmt.Sprintf("A test message, sent at %s on the request of %s. If you read this, channel %s works.", ev.At, c.user.Username, ch.Name)
	b, err := json.Marshal(ev)
	if err != nil {
		return 0, nil, err
	}
	n, err := s.db.QueueNotification(ctx, store.Notification{ChannelID: ch.ID, Event: evTest, Ref: now.UnixNano(), Subject: ev.Subject, Body: b})
	if err != nil {
		return 0, nil, err
	}
	n, err = s.attempt(ctx, n)
	return http.StatusOK, n, err
}
