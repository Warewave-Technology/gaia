package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// --- notification channels, notifications, ledger checks and daily reports (schema 7, ADR-007 §10) ---

// Channel is where notifications go. Config holds no secrets.
type Channel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"` // smtp or webhook
	Config json.RawMessage `json:"config"`
	// Categories are the kinds of event the channel takes (ADR-007 C8), of status, jobs, integrity, security and
	// migration, in that order. A new event of a category reaches the channels that take the category.
	Categories []string `json:"categories"`
	// DailyReport: the channel takes the daily report, which belongs to no category.
	DailyReport bool `json:"daily_report"`
	// M2Events is the event list of a channel added before 0.2.0, as it was when schema 14 mapped it to categories
	// ([] for a channel added since). It only says what the channel had; the categories decide what it gets.
	M2Events  []string `json:"m2_events"`
	Enabled   bool     `json:"enabled"`
	Cursor    int64    `json:"cursor"` // the last state change turned into notifications
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// ChannelCategoriesEntity is the state_changes entity of a change in what a channel takes. From and to are JSON,
// {"categories":[...],"daily_report":true|false}; schema 14's mapping of an M2 list has {"events":[...]} as from.
const ChannelCategoriesEntity = "notification_channel_categories"

const channelCols = `id, name, kind, config, categories, daily_report, m2_events, enabled, cursor, created_at, updated_at`

func scanChannel(s interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	var cfg, cats, m2 string
	if err := s.Scan(&c.ID, &c.Name, &c.Kind, &cfg, &cats, &c.DailyReport, &m2, &c.Enabled, &c.Cursor, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	c.Config = json.RawMessage(cfg)
	if err := json.Unmarshal([]byte(cats), &c.Categories); err != nil {
		return c, fmt.Errorf("channel %d: categories: %w", c.ID, err)
	}
	if err := json.Unmarshal([]byte(m2), &c.M2Events); err != nil {
		return c, fmt.Errorf("channel %d: m2_events: %w", c.ID, err)
	}
	return c, nil
}

// subscription is what a channel takes, as a state_changes value.
func subscription(categories []string, dailyReport bool) (string, error) {
	if categories == nil {
		categories = []string{}
	}
	b, err := json.Marshal(struct {
		Categories  []string `json:"categories"`
		DailyReport bool     `json:"daily_report"`
	}{categories, dailyReport})
	return string(b), err
}

// AddChannel records a channel with its categories (the caller checks them) and daily report. It starts at the newest
// state change: it reports what happens from now on.
func (d *DB) AddChannel(ctx context.Context, actor string, c Channel) (Channel, error) {
	if c.Categories == nil {
		c.Categories = []string{}
	}
	cats, err := json.Marshal(c.Categories)
	if err != nil {
		return c, err
	}
	sub, err := subscription(c.Categories, c.DailyReport)
	if err != nil {
		return c, err
	}
	var id int64
	err = d.tx(ctx, func(tx *sql.Tx) error {
		var cursor int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM state_changes`).Scan(&cursor); err != nil {
			return err
		}
		now := d.ts()
		r, err := tx.ExecContext(ctx, `INSERT INTO notification_channels (name, kind, config, categories, daily_report, m2_events, enabled, cursor, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, '[]', 1, ?, ?, ?)`, c.Name, c.Kind, string(c.Config), string(cats), c.DailyReport, cursor, now, now)
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		if err := d.recordState(ctx, tx, "notification_channel", id, "", "enabled", fmt.Sprintf("%s channel %s added", c.Kind, c.Name), actor); err != nil {
			return err
		}
		return d.recordState(ctx, tx, ChannelCategoriesEntity, id, "", sub,
			fmt.Sprintf("channel %s added: categories %s, daily report %t", c.Name, cats, c.DailyReport), actor)
	})
	if err != nil {
		return c, err
	}
	return d.Channel(ctx, id)
}

// ChannelChange changes what a channel takes and whether it is on; a nil field stays as it is.
type ChannelChange struct {
	Categories  []string // the whole new list (the caller checks it); nil keeps the list
	DailyReport *bool
	Enabled     *bool
}

// UpdateChannel changes a channel in one transaction, if it is still as was (its categories, daily report and
// enabled): the caller decided from was whether the change weakens who is told (a step-up, ADR-008 §4, ADR-007
// C18.4), so a channel changed in between is ErrConflict, to be asked again. Each part that changes is a state
// change: what it takes (ChannelCategoriesEntity; the cursor stays, so from the next state change on the new
// categories decide), and on or off (notification_channel; a channel turned on again starts at the newest state
// change: what happened while it was off is not sent).
func (d *DB) UpdateChannel(ctx context.Context, actor string, id int64, was Channel, ch ChannelChange, reason string) (Channel, error) {
	expect, err := subscription(was.Categories, was.DailyReport)
	if err != nil {
		return Channel{}, err
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		c, err := scanChannel(tx.QueryRowContext(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE id = ?`, id))
		if err != nil {
			return notFound("notification channel", id, err)
		}
		old, err := subscription(c.Categories, c.DailyReport)
		if err != nil {
			return err
		}
		if old != expect || c.Enabled != was.Enabled {
			return fmt.Errorf("%w: notification channel %s changed meanwhile (it takes %s, enabled %t): look again and repeat the change", ErrConflict, c.Name, old, c.Enabled)
		}
		if ch.Categories != nil || ch.DailyReport != nil {
			cats, daily := c.Categories, c.DailyReport
			if ch.Categories != nil {
				cats = ch.Categories
			}
			if ch.DailyReport != nil {
				daily = *ch.DailyReport
			}
			if err := d.setChannelCategories(ctx, tx, actor, id, old, cats, daily, reason); err != nil {
				return err
			}
		}
		if ch.Enabled != nil && *ch.Enabled != c.Enabled {
			return d.setChannelEnabled(ctx, tx, actor, id, *ch.Enabled, reason)
		}
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return d.Channel(ctx, id)
}

// setChannelCategories records what a channel takes now, when it is not old (its subscription value).
func (d *DB) setChannelCategories(ctx context.Context, tx *sql.Tx, actor string, id int64, old string, categories []string, dailyReport bool, reason string) error {
	if categories == nil {
		categories = []string{}
	}
	cats, err := json.Marshal(categories)
	if err != nil {
		return err
	}
	sub, err := subscription(categories, dailyReport)
	if err != nil || sub == old {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_channels SET categories = ?, daily_report = ?, updated_at = ? WHERE id = ?`,
		string(cats), dailyReport, d.ts(), id); err != nil {
		return err
	}
	return d.recordState(ctx, tx, ChannelCategoriesEntity, id, old, sub, reason, actor)
}

// ChannelByName loads a channel by name.
func (d *DB) ChannelByName(ctx context.Context, name string) (Channel, error) {
	c, err := scanChannel(d.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: notification channel %s", ErrNotFound, name)
	}
	return c, err
}

// Channel loads one channel.
func (d *DB) Channel(ctx context.Context, id int64) (Channel, error) {
	c, err := scanChannel(d.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE id = ?`, id))
	return c, notFound("notification channel", id, err)
}

// Channels lists every channel.
func (d *DB) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+channelCols+` FROM notification_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetChannelEnabled turns a channel on or off. A channel turned on again starts at the newest state change: what
// happened while it was off is not sent.
func (d *DB) SetChannelEnabled(ctx context.Context, actor string, id int64, enabled bool, reason string) (Channel, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var was bool
		if err := tx.QueryRowContext(ctx, `SELECT enabled FROM notification_channels WHERE id = ?`, id).Scan(&was); err != nil {
			return notFound("notification channel", id, err)
		}
		if was == enabled {
			return nil
		}
		return d.setChannelEnabled(ctx, tx, actor, id, enabled, reason)
	})
	if err != nil {
		return Channel{}, err
	}
	return d.Channel(ctx, id)
}

func (d *DB) setChannelEnabled(ctx context.Context, tx *sql.Tx, actor string, id int64, enabled bool, reason string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled = ?, updated_at = ?,
		cursor = CASE WHEN ? THEN (SELECT COALESCE(MAX(seq), 0) FROM state_changes) ELSE cursor END WHERE id = ?`, enabled, d.ts(), enabled, id); err != nil {
		return err
	}
	from, to := "enabled", "disabled"
	if enabled {
		from, to = to, from
	}
	return d.recordState(ctx, tx, "notification_channel", id, from, to, reason, actor)
}

// Notification is one message to one channel.
type Notification struct {
	ID            int64           `json:"id"`
	ChannelID     int64           `json:"channel_id"`
	Event         string          `json:"event"`
	Ref           int64           `json:"ref"` // the state change's seq, or the daily report's id
	Subject       string          `json:"subject"`
	Body          json.RawMessage `json:"body"`
	State         string          `json:"state"` // queued, sent, failed
	Attempts      int             `json:"attempts"`
	NextAttemptAt string          `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	CreatedAt     string          `json:"created_at"`
	FinishedAt    string          `json:"finished_at"`
}

const notificationCols = `id, channel_id, event, ref, subject, body, state, attempts, next_attempt_at, last_error, created_at, finished_at`

func scanNotification(s interface{ Scan(...any) error }) (Notification, error) {
	var n Notification
	var body string
	err := s.Scan(&n.ID, &n.ChannelID, &n.Event, &n.Ref, &n.Subject, &body, &n.State, &n.Attempts, &n.NextAttemptAt, &n.LastError,
		&n.CreatedAt, &n.FinishedAt)
	n.Body = json.RawMessage(body)
	return n, err
}

func insertNotification(ctx context.Context, d *DB, tx *sql.Tx, n Notification) error {
	now := d.ts()
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notifications (channel_id, event, ref, subject, body, state, next_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'queued', ?, ?)`, n.ChannelID, n.Event, n.Ref, n.Subject, string(n.Body), now, now)
	return err
}

// QueueEvents queues a channel's notifications for the state changes up to cursor and moves its cursor there, in one
// transaction; from must be the channel's cursor (one notifier at a time).
func (d *DB) QueueEvents(ctx context.Context, channelID, from, cursor int64, ns []Notification) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE notification_channels SET cursor = ? WHERE id = ? AND cursor = ? AND enabled = 1`, cursor, channelID, from)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("%w: notification channel %d moved on from %d, or is disabled", ErrConflict, channelID, from)
		}
		for _, n := range ns {
			n.ChannelID = channelID
			if err := insertNotification(ctx, d, tx, n); err != nil {
				return err
			}
		}
		return nil
	})
}

// QueueNotification queues one notification (a daily report, a test message) and returns it; one that exists already
// (same channel, event and ref) is returned as it is.
func (d *DB) QueueNotification(ctx context.Context, n Notification) (Notification, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error { return insertNotification(ctx, d, tx, n) })
	if err != nil {
		return n, err
	}
	out, err := scanNotification(d.db.QueryRowContext(ctx, `SELECT `+notificationCols+` FROM notifications WHERE channel_id = ? AND event = ? AND ref = ?`,
		n.ChannelID, n.Event, n.Ref))
	return out, err
}

// DueNotifications lists queued notifications whose next attempt is due, oldest first.
func (d *DB) DueNotifications(ctx context.Context, now time.Time, limit int) ([]Notification, error) {
	return d.notifications(ctx, `WHERE state = 'queued' AND next_attempt_at <= ? ORDER BY id LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
}

// Notifications lists a channel's notifications (every channel's for 0), newest first.
func (d *DB) Notifications(ctx context.Context, channelID int64, limit int) ([]Notification, error) {
	if channelID == 0 {
		return d.notifications(ctx, `ORDER BY id DESC LIMIT ?`, limit)
	}
	return d.notifications(ctx, `WHERE channel_id = ? ORDER BY id DESC LIMIT ?`, channelID, limit)
}

// Notification loads one notification.
func (d *DB) Notification(ctx context.Context, id int64) (Notification, error) {
	n, err := scanNotification(d.db.QueryRowContext(ctx, `SELECT `+notificationCols+` FROM notifications WHERE id = ?`, id))
	return n, notFound("notification", id, err)
}

func (d *DB) notifications(ctx context.Context, where string, args ...any) ([]Notification, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+notificationCols+` FROM notifications `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NotificationAttempt records one attempt to send a queued notification: sent (attemptErr nil); failed for now, to be
// tried again at retryAt; or failed for good (retryAt zero). An end state is a state change.
func (d *DB) NotificationAttempt(ctx context.Context, id int64, attemptErr error, retryAt time.Time) (Notification, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var state string
		var attempts int
		if err := tx.QueryRowContext(ctx, `SELECT state, attempts FROM notifications WHERE id = ?`, id).Scan(&state, &attempts); err != nil {
			return notFound("notification", id, err)
		}
		if state != "queued" {
			return fmt.Errorf("%w: notification %d is %s", ErrConflict, id, state)
		}
		attempts++
		now := d.ts()
		switch {
		case attemptErr == nil:
			if _, err := tx.ExecContext(ctx, `UPDATE notifications SET state = 'sent', attempts = ?, finished_at = ? WHERE id = ?`, attempts, now, id); err != nil {
				return err
			}
			return d.recordState(ctx, tx, "notification", id, "queued", "sent", fmt.Sprintf("attempt %d", attempts), "system")
		case retryAt.IsZero():
			if _, err := tx.ExecContext(ctx, `UPDATE notifications SET state = 'failed', attempts = ?, last_error = ?, finished_at = ? WHERE id = ?`,
				attempts, attemptErr.Error(), now, id); err != nil {
				return err
			}
			return d.recordState(ctx, tx, "notification", id, "queued", "failed", fmt.Sprintf("gave up after %d attempts: %v", attempts, attemptErr), "system")
		default:
			_, err := tx.ExecContext(ctx, `UPDATE notifications SET attempts = ?, last_error = ?, next_attempt_at = ? WHERE id = ?`,
				attempts, attemptErr.Error(), retryAt.UTC().Format(time.RFC3339Nano), id)
			return err
		}
	})
	if err != nil {
		return Notification{}, err
	}
	return d.Notification(ctx, id)
}

// LedgerCheck is one check of the evidence ledger and its bundles.
type LedgerCheck struct {
	ID         int64    `json:"id"`
	CheckedAt  string   `json:"checked_at"`
	Reason     string   `json:"reason"`
	Verdict    string   `json:"verdict"`
	Entries    int      `json:"entries"`
	HeadSeq    int      `json:"head_seq"`
	HeadSHA256 string   `json:"head_sha256"`
	Problems   []string `json:"problems"`
	// Kind (ADR-007 C14): incremental (the entries since the last check, and the link to the last checked entry) or
	// full (every entry and bundle).
	Kind string `json:"kind"`
}

// Ledger check kinds.
const (
	LedgerCheckIncremental = "incremental"
	LedgerCheckFull        = "full"
)

const ledgerCheckCols = `id, checked_at, reason, verdict, entries, head_seq, head_sha256, problems, kind`

func scanLedgerCheck(s interface{ Scan(...any) error }) (LedgerCheck, error) {
	var c LedgerCheck
	var problems string
	if err := s.Scan(&c.ID, &c.CheckedAt, &c.Reason, &c.Verdict, &c.Entries, &c.HeadSeq, &c.HeadSHA256, &problems, &c.Kind); err != nil {
		return c, err
	}
	return c, json.Unmarshal([]byte(problems), &c.Problems)
}

// AddLedgerCheck records a check; a verdict other than the last check's is a state change (entity ledger).
func (d *DB) AddLedgerCheck(ctx context.Context, actor string, c LedgerCheck) (LedgerCheck, error) {
	if c.Problems == nil {
		c.Problems = []string{}
	}
	if c.Kind != LedgerCheckIncremental && c.Kind != LedgerCheckFull {
		return c, fmt.Errorf("%w: ledger check kind %q (incremental or full)", ErrInvalid, c.Kind)
	}
	problems, err := json.Marshal(c.Problems)
	if err != nil {
		return c, err
	}
	var id int64
	err = d.tx(ctx, func(tx *sql.Tx) error {
		var last string
		if err := tx.QueryRowContext(ctx, `SELECT verdict FROM ledger_checks ORDER BY id DESC LIMIT 1`).Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO ledger_checks (checked_at, reason, verdict, entries, head_seq, head_sha256, problems, kind)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, d.ts(), c.Reason, c.Verdict, c.Entries, c.HeadSeq, c.HeadSHA256, string(problems), c.Kind)
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		if last == c.Verdict {
			return nil
		}
		why := fmt.Sprintf("%s: %d entries, head %d", c.Reason, c.Entries, c.HeadSeq)
		if len(c.Problems) > 0 {
			why = fmt.Sprintf("%s: %s", c.Reason, joinMax(c.Problems, 5))
		}
		return d.recordState(ctx, tx, "ledger", 1, last, c.Verdict, why, actor)
	})
	if err != nil {
		return c, err
	}
	return scanLedgerCheck(d.db.QueryRowContext(ctx, `SELECT `+ledgerCheckCols+` FROM ledger_checks WHERE id = ?`, id))
}

// LedgerChecks lists the newest ledger checks first.
// AnyTestPassed says whether any restore test ever passed (ADR-007 C14: then the ledger must have entries).
func (d *DB) AnyTestPassed(ctx context.Context) (bool, error) {
	var ok bool
	err := d.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM restore_tests WHERE verdict = 'OK' AND finished_at != '')`).Scan(&ok)
	return ok, err
}

// OldestBundleAfter is when the first evidence bundle after ledger entry seq was recorded.
func (d *DB) OldestBundleAfter(ctx context.Context, seq int) (string, error) {
	var at string
	err := d.db.QueryRowContext(ctx, `SELECT created_at FROM evidence_bundles WHERE ledger_seq > ? ORDER BY ledger_seq LIMIT 1`, seq).Scan(&at)
	return at, err
}

// LedgerHead is the newest ledger entry an evidence bundle recorded (0: none yet).
func (d *DB) LedgerHead(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(ledger_seq), 0) FROM evidence_bundles`).Scan(&n)
	return n, err
}

func (d *DB) LedgerChecks(ctx context.Context, limit int) ([]LedgerCheck, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+ledgerCheckCols+` FROM ledger_checks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LedgerCheck{}
	for rows.Next() {
		c, err := scanLedgerCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DailyReport is one daily report as it was sent.
type DailyReport struct {
	ID          int64           `json:"id"`
	GeneratedAt string          `json:"generated_at"`
	Reason      string          `json:"reason"`
	HeadSeq     int             `json:"head_seq"`
	HeadSHA256  string          `json:"head_sha256"`
	LedgerHead  json.RawMessage `json:"ledger_head"` // the head statement (null while the ledger is empty)
	Report      json.RawMessage `json:"report"`
}

const dailyReportCols = `id, generated_at, reason, head_seq, head_sha256, ledger_head, report`

func scanDailyReport(s interface{ Scan(...any) error }) (DailyReport, error) {
	var r DailyReport
	var head, report string
	err := s.Scan(&r.ID, &r.GeneratedAt, &r.Reason, &r.HeadSeq, &r.HeadSHA256, &head, &report)
	if head == "" {
		head = "null"
	}
	r.LedgerHead, r.Report = json.RawMessage(head), json.RawMessage(report)
	return r, err
}

// NextDailyReportID is the id the next daily report gets (the report carries it).
func (d *DB) NextDailyReportID(ctx context.Context) (int64, error) {
	var n int64
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM daily_reports`).Scan(&n)
	return n, err
}

// AddDailyReport records a daily report under the id NextDailyReportID gave, and queues it (as body) for every enabled
// channel that takes daily reports, in one transaction.
func (d *DB) AddDailyReport(ctx context.Context, r DailyReport, subject string, body json.RawMessage) (DailyReport, error) {
	head := string(r.LedgerHead)
	if head == "null" {
		head = ""
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO daily_reports (id, generated_at, reason, head_seq, head_sha256, ledger_head, report)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, r.ID, r.GeneratedAt, r.Reason, r.HeadSeq, r.HeadSHA256, head, string(r.Report)); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM notification_channels WHERE enabled = 1 AND daily_report = 1 ORDER BY id`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if err := insertNotification(ctx, d, tx, Notification{ChannelID: id, Event: "daily_report", Ref: r.ID, Subject: subject, Body: body}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	return d.DailyReport(ctx, r.ID)
}

// DailyReport loads one daily report.
func (d *DB) DailyReport(ctx context.Context, id int64) (DailyReport, error) {
	r, err := scanDailyReport(d.db.QueryRowContext(ctx, `SELECT `+dailyReportCols+` FROM daily_reports WHERE id = ?`, id))
	return r, notFound("daily report", id, err)
}

// DailyReports lists the newest daily reports first.
func (d *DB) DailyReports(ctx context.Context, limit int) ([]DailyReport, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+dailyReportCols+` FROM daily_reports ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DailyReport{}
	for rows.Next() {
		r, err := scanDailyReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func joinMax(s []string, n int) string {
	out := ""
	for i, x := range s {
		if i == n {
			return out + fmt.Sprintf("; and %d more", len(s)-n)
		}
		if i > 0 {
			out += "; "
		}
		out += x
	}
	return out
}

// MaxEvidenceLedgerSeq is the highest ledger entry the catalog records for a bundle (0: none): the ledger must have at
// least that many entries.
func (d *DB) MaxEvidenceLedgerSeq(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(ledger_seq), 0) FROM evidence_bundles`).Scan(&n)
	return n, err
}
