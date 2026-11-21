-- Schema version 11: strict sign-in (ADR-008, T26).

-- Users. source: 'local' (a password and a second factor kept here) or 'oidc' (signed in at the IdP; no password here,
-- the role comes from the IdP's groups at every sign-in and session refresh). oidc_subject is issuer + " " + sub.
-- break_glass: a local account that may sign in while OIDC is on (§1); at least one enabled admin has it.
-- expires_at: an auditor's account ends then (§8, at most 30 days ahead; '' for the other roles).
-- password_change: why the user must set a new password before anything else ('' = no need): admin_reset, blocklist.
-- blocklist_version: the password blocklist the current password was checked against (a newer list checks it again
-- at the next sign-in).
ALTER TABLE users ADD COLUMN source TEXT NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'oidc'));
ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN break_glass INTEGER NOT NULL DEFAULT 0 CHECK (break_glass IN (0, 1));
ALTER TABLE users ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN password_change TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN blocklist_version TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX users_oidc_subject ON users (oidc_subject) WHERE oidc_subject != '';
-- Every admin of an installation from before ADR-008 is local: each is break-glass, so the rule "at least one" holds.
UPDATE users SET break_glass = 1 WHERE role = 'admin';
-- Auditors had no expiry: they get the longest one allowed, from the upgrade.
UPDATE users SET expires_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '+30 days') WHERE role = 'auditor';
-- T18's count of wrong passwords in a row is replaced by signin_failures (§7).
UPDATE users SET failed_logins = 0, locked_until = '';

-- Second factors (§3). webauthn: data is the credential (JSON, as go-webauthn keeps it), credential_id its id
-- (base64url). totp: data is the secret (base32), last_step the last time step whose code was accepted (a code is
-- never accepted twice).
CREATE TABLE user_factors (
	id            INTEGER PRIMARY KEY,
	user_id       INTEGER NOT NULL REFERENCES users (id),
	kind          TEXT    NOT NULL CHECK (kind IN ('webauthn', 'totp')),
	name          TEXT    NOT NULL,
	credential_id TEXT    NOT NULL DEFAULT '',
	data          TEXT    NOT NULL,
	last_step     INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT    NOT NULL,
	last_used_at  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX user_factors_user ON user_factors (user_id);
CREATE UNIQUE INDEX user_factors_credential ON user_factors (credential_id) WHERE credential_id != '';

-- Recovery codes (§3): 10 per user, single use, only their sha256 kept.
CREATE TABLE recovery_codes (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users (id),
	code_sha256 TEXT    NOT NULL,
	created_at  TEXT    NOT NULL,
	used_at     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX recovery_codes_user ON recovery_codes (user_id, code_sha256);

-- The user's earlier passwords (§6: not one of the last 5), argon2id like the current one.
CREATE TABLE password_history (
	id            INTEGER PRIMARY KEY,
	user_id       INTEGER NOT NULL REFERENCES users (id),
	password_hash TEXT    NOT NULL,
	set_at        TEXT    NOT NULL
);
CREATE INDEX password_history_user ON password_history (user_id, id);
INSERT INTO password_history (user_id, password_hash, set_at) SELECT id, password_hash, password_changed_at FROM users;

-- Sessions (§5) are kept after they end, with the reason, for the list of sessions and the audit log. Every session of
-- the earlier schema ends at the upgrade (§9: the 12 h CLI tokens of M2 stop working).
DROP TABLE sessions;
CREATE TABLE sessions (
	id                INTEGER PRIMARY KEY,
	token_sha256      TEXT    NOT NULL UNIQUE,      -- the token itself is never stored
	user_id           INTEGER NOT NULL REFERENCES users (id),
	kind              TEXT    NOT NULL CHECK (kind IN ('browser', 'cli')),
	method            TEXT    NOT NULL,             -- how it signed in: webauthn, totp, recovery_code, oidc
	break_glass       INTEGER NOT NULL DEFAULT 0,   -- a local sign-in while OIDC was on
	created_at        TEXT    NOT NULL,
	last_active_at    TEXT    NOT NULL,             -- the last request the user made (not X-Gaia-Background)
	last_seen_at      TEXT    NOT NULL,             -- the last request of any kind
	absolute_at       TEXT    NOT NULL,             -- it ends then, whatever the activity
	remote_addr       TEXT    NOT NULL,
	user_agent        TEXT    NOT NULL DEFAULT '',
	new_device        INTEGER NOT NULL DEFAULT 0,
	idp_state         TEXT    NOT NULL DEFAULT '',  -- OIDC: ok or idp_unreachable at the last refresh
	idp_refreshed_at  TEXT    NOT NULL DEFAULT '',
	idp_refresh_token TEXT    NOT NULL DEFAULT '',  -- OIDC: to read the user's groups again every 10 minutes
	ended_at          TEXT    NOT NULL DEFAULT '',
	end_reason        TEXT    NOT NULL DEFAULT ''   -- signed_out, idle, absolute, revoked, replaced, ...
);
CREATE INDEX sessions_live ON sessions (user_id) WHERE ended_at = '';

-- Devices and addresses a user signed in from (§5: one not seen in 30 days marks the session new_device).
CREATE TABLE signin_devices (
	user_id      INTEGER NOT NULL REFERENCES users (id),
	kind         TEXT    NOT NULL CHECK (kind IN ('device', 'address')),
	value        TEXT    NOT NULL,                  -- device: sha256 of the browser's or CLI's device id
	last_seen_at TEXT    NOT NULL,
	PRIMARY KEY (user_id, kind, value)
);

-- Failed sign-in steps (§7), for the delays and the pause: a wrong password, factor or recovery code. username is
-- what was typed (an unknown name is counted the same way, so a pause does not tell which names exist).
CREATE TABLE signin_failures (
	id          INTEGER PRIMARY KEY,
	username    TEXT NOT NULL,
	remote_addr TEXT NOT NULL,
	at          TEXT NOT NULL
);
CREATE INDEX signin_failures_user ON signin_failures (username, at);
CREATE INDEX signin_failures_addr ON signin_failures (remote_addr, at);

-- API tokens for automation (§9): operator or auditor only, at most 90 days, only the sha256 kept.
CREATE TABLE api_tokens (
	id            INTEGER PRIMARY KEY,
	name          TEXT    NOT NULL UNIQUE,
	role          TEXT    NOT NULL CHECK (role IN ('operator', 'auditor')),
	token_sha256  TEXT    NOT NULL UNIQUE,
	allowed_addrs TEXT    NOT NULL DEFAULT '[]',   -- JSON list of addresses and CIDR prefixes ([] = any)
	created_by    TEXT    NOT NULL,
	created_at    TEXT    NOT NULL,
	expires_at    TEXT    NOT NULL,
	revoked_at    TEXT    NOT NULL DEFAULT '',
	revoked_by    TEXT    NOT NULL DEFAULT '',
	first_used_at TEXT    NOT NULL DEFAULT '',
	last_used_at  TEXT    NOT NULL DEFAULT '',
	use_day       TEXT    NOT NULL DEFAULT '',     -- YYYY-MM-DD (UTC) of use_count
	use_count     INTEGER NOT NULL DEFAULT 0
);

-- Sign-in and security settings (§1-§6 where ADR-008 leaves a choice). value is JSON; every change is a state change
-- (entity setting) and an audit entry, and is listed in the next daily report. A secret (the OIDC client secret) is
-- never a setting: it is a file in secrets/.
CREATE TABLE settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	updated_by TEXT NOT NULL
);

-- The security events notify every channel that existed before them (admins' channels: §1, §5, §7).
UPDATE notification_channels SET events = (
	SELECT json_group_array(value) FROM (
		SELECT value FROM json_each(notification_channels.events)
		UNION SELECT 'account_locked' UNION SELECT 'breakglass_signin' UNION SELECT 'new_device_signin'
		ORDER BY value));
