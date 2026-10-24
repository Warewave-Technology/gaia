package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// SendArgs is the job that delivers one notification.
type SendArgs struct {
	ID int64 `json:"id"`
}

// Kind names the job.
func (SendArgs) Kind() string { return "notify" }

// maxAttempts is how often a delivery is tried: River backs off between
// attempts, so the last one is about a day after the first.
const maxAttempts = 12

// InsertOpts: deliveries are retried.
func (SendArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: maxAttempts}
}

// Secrets reads a bao:// reference at an OpenBao address.
type Secrets func(ctx context.Context, addr, ref string) (map[string]string, error)

// SendWorker delivers notifications.
type SendWorker struct {
	river.WorkerDefaults[SendArgs]
	Store   *store.Store
	Secrets Secrets
	HTTP    *http.Client
}

// Timeout bounds one delivery.
func (w *SendWorker) Timeout(*river.Job[SendArgs]) time.Duration { return time.Minute }

// Work sends the notification and records how it went.
func (w *SendWorker) Work(ctx context.Context, job *river.Job[SendArgs]) error {
	q := w.Store.Q()
	n, err := q.Notification(ctx, job.Args.ID)
	if err != nil {
		return err
	}
	if n.Status == "sent" {
		return nil
	}
	var m Message
	if err := json.Unmarshal(n.Message, &m); err != nil {
		return river.JobCancel(err)
	}
	err = w.send(ctx, m)
	if err == nil {
		return q.NotificationSent(ctx, n.ID)
	}
	status := "pending"
	if job.Attempt >= job.MaxAttempts {
		status = "failed"
	}
	if uerr := q.NotificationFailed(ctx, db.NotificationFailedParams{ID: n.ID, Status: status, LastError: text(err.Error())}); uerr != nil {
		return errors.Join(err, uerr)
	}
	return err
}

func (w *SendWorker) secret(ctx context.Context, ch Channel) (map[string]string, error) {
	if ch.Credentials == "" {
		return map[string]string{}, nil
	}
	if w.Secrets == nil {
		return nil, errors.New("no access to OpenBao for " + ch.Credentials)
	}
	return w.Secrets(ctx, ch.SecretsAddr, ch.Credentials)
}

func (w *SendWorker) send(ctx context.Context, m Message) error {
	ch := m.Channel
	sec, err := w.secret(ctx, ch)
	if err != nil {
		return err
	}
	switch ch.Type {
	case "slack":
		return w.post(ctx, sec["url"], slackBody(m.Event), nil)
	case "msteams":
		return w.post(ctx, sec["url"], teamsBody(m.Event), nil)
	case "webhook":
		body, _ := json.Marshal(m.Event)
		h := map[string]string{"X-Gaia-Event": m.Event.Trigger}
		if s := sec["secret"]; s != "" {
			mac := hmac.New(sha256.New, []byte(s))
			mac.Write(body)
			h["X-Gaia-Signature-256"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		return w.post(ctx, ch.Endpoint, body, h)
	case "smtp":
		return sendMail(ctx, ch, sec, m.Event)
	default:
		return river.JobCancel(fmt.Errorf("unknown channel type %q", ch.Type))
	}
}

func (w *SendWorker) post(ctx context.Context, url string, body []byte, headers map[string]string) error {
	if url == "" {
		return river.JobCancel(errors.New("the channel has no url"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return river.JobCancel(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gaia")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c := w.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// sendMail sends a plain text mail: TLS from the start on port 465, else
// STARTTLS when the server offers it. Credentials (username, password)
// are only sent over TLS.
func sendMail(ctx context.Context, ch Channel, sec map[string]string, ev Event) error {
	host, port, err := net.SplitHostPort(ch.Endpoint)
	if err != nil {
		return river.JobCancel(err)
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if port == "465" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", ch.Endpoint)
	} else {
		conn, err = d.DialContext(ctx, "tcp", ch.Endpoint)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	secure := port == "465"
	if ok, _ := c.Extension("STARTTLS"); ok && !secure {
		if err := c.StartTLS(cfg); err != nil {
			return err
		}
		secure = true
	}
	if u := sec["username"]; u != "" {
		if !secure {
			return river.JobCancel(errors.New("the mail server offers no TLS; Gaia does not send credentials in clear"))
		}
		if err := c.Auth(smtp.PlainAuth("", u, sec["password"], host)); err != nil {
			return err
		}
	}
	if err := c.Mail(address(ch.From)); err != nil {
		return err
	}
	for _, to := range ch.To {
		if err := c.Rcpt(address(to)); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	title, body := render(ev)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
		ch.From, strings.Join(ch.To, ", "), mime.QEncoding.Encode("utf-8", title), time.Now().Format(time.RFC1123Z))
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	if _, err := io.WriteString(wc, b.String()); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func address(s string) string {
	if i := strings.LastIndex(s, "<"); i >= 0 {
		return strings.TrimSuffix(s[i+1:], ">")
	}
	return s
}
