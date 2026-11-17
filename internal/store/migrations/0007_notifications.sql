-- Schema version 7: notifications, the daily report and ledger checks (ADR-007 §10, T19).

-- Where notifications go: an SMTP server (email) or an HTTPS endpoint (webhook). config is JSON without secrets (the
-- SMTP password and the webhook's signing secret are files under secrets/). events is a JSON list of the event kinds
-- the channel gets. cursor is the last state_changes seq the channel has turned into notifications: events come from
-- state_changes, so none is lost while the server is down. Each change of a channel is a row in state_changes
-- (entity notification_channel).
CREATE TABLE notification_channels (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL UNIQUE,
	kind       TEXT    NOT NULL CHECK (kind IN ('smtp', 'webhook')),
	config     TEXT    NOT NULL,
	events     TEXT    NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	cursor     INTEGER NOT NULL DEFAULT 0,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

-- One message to one channel. ref is the state_changes seq it reports (or the daily report's id for daily_report).
-- queued -> sent, or -> failed once retries end; every attempt counts, and each end state is a row in state_changes
-- (entity notification).
CREATE TABLE notifications (
	id              INTEGER PRIMARY KEY,
	channel_id      INTEGER NOT NULL REFERENCES notification_channels (id),
	event           TEXT    NOT NULL,
	ref             INTEGER NOT NULL,
	subject         TEXT    NOT NULL,
	body            TEXT    NOT NULL,             -- JSON: the event as sent
	state           TEXT    NOT NULL CHECK (state IN ('queued', 'sent', 'failed')),
	attempts        INTEGER NOT NULL DEFAULT 0,
	next_attempt_at TEXT    NOT NULL,
	last_error      TEXT    NOT NULL DEFAULT '',
	created_at      TEXT    NOT NULL,
	finished_at     TEXT    NOT NULL DEFAULT '',
	UNIQUE (channel_id, event, ref)
);
CREATE INDEX notifications_due ON notifications (state, next_attempt_at);

-- Every check of the evidence ledger and its bundles. Append-only; a change of verdict is also a row in state_changes
-- (entity ledger), which is what notifies.
CREATE TABLE ledger_checks (
	id           INTEGER PRIMARY KEY,
	checked_at   TEXT    NOT NULL,
	reason       TEXT    NOT NULL,
	verdict      TEXT    NOT NULL,
	entries      INTEGER NOT NULL,
	head_seq     INTEGER NOT NULL,
	head_sha256  TEXT    NOT NULL,
	problems     TEXT    NOT NULL DEFAULT '[]'    -- JSON list of findings as text
);
CREATE TRIGGER ledger_checks_no_update BEFORE UPDATE ON ledger_checks BEGIN SELECT RAISE(ABORT, 'ledger_checks is append-only'); END;
CREATE TRIGGER ledger_checks_no_delete BEFORE DELETE ON ledger_checks BEGIN SELECT RAISE(ABORT, 'ledger_checks is append-only'); END;

-- Every daily report, as sent. ledger_head is the signed head statement (evidence spec §6.8). Append-only.
CREATE TABLE daily_reports (
	id            INTEGER PRIMARY KEY,
	generated_at  TEXT    NOT NULL,
	reason        TEXT    NOT NULL,
	head_seq      INTEGER NOT NULL,
	head_sha256   TEXT    NOT NULL,
	ledger_head   TEXT    NOT NULL,
	report        TEXT    NOT NULL                -- JSON
);
CREATE TRIGGER daily_reports_no_update BEFORE UPDATE ON daily_reports BEGIN SELECT RAISE(ABORT, 'daily_reports is append-only'); END;
CREATE TRIGGER daily_reports_no_delete BEFORE DELETE ON daily_reports BEGIN SELECT RAISE(ABORT, 'daily_reports is append-only'); END;
