# Release notes

## 0.2.0 (in preparation: package M3)

### Strict sign-in (ADR-008, T26)

- **The command-line logins of 0.1.x stop working at the upgrade**, and every browser session ends: everyone signs in
  again. The 12-hour CLI token is gone: a CLI session has the browser's limits (15 minutes idle, 8 hours at most).
- **A second factor for every account**, enrolled at the next sign-in: admins a security key (WebAuthn: a USB/NFC
  key, Touch ID, Windows Hello), others a key or an authenticator app (TOTP); 10 recovery codes each. Security keys
  need the server opened by a name with its certificate trusted: set `public_url` (install.md step 12).
- Destructive and security actions ask for the factor again (step-up), every time.
- Passwords: at least 15 characters, checked against an embedded list of breached and common passwords, not one of
  the last 5. Optional expiry. A password shorter than 15 characters from 0.1.x must be changed at the first sign-in
  after the upgrade (after the second factor, before the session starts).
- Failed sign-ins slow down from the 3rd and pause the account after 10 in 15 minutes; admins are notified.
- Single sign-on with OIDC (optional), roles from IdP groups, break-glass local admins, `gaia login -sso`.
- API tokens for automation (operator or auditor, at most 90 days).
- `session_ttl` and `login_*` in `server.json` are ignored.
- Auditor accounts expire (at most 30 days, renewable).

### Scale, parallel tests (T24, T25)

- Reason codes, the status table, paged views and tags (T24); parallel restore tests in sandbox slots and the proof
  capacity view (T25).
- Reason codes version 2: `host_key_untrusted` (a host whose SSH key was never trusted is one cause for all its VMs).
- A policy says how much of the time its VMs can be OK at best (`max_ok_fraction`) and which test interval would make
  it always possible (`suggested_test_interval_s`). OK/WARNING changes caused only by the proof's age on such a policy
  are counted in the daily report instead of being notified one by one.
- A new event, `capacity_short`, when the proof capacity drops below 100 %; channels that take `job_failed` get it.
- The evidence ledger is checked every hour (incremental) and in full with the daily report.

### Evidence export (T28, ADR-007 C7)

- The export's `SHA256SUMS` is signed with the installation's evidence key: `SHA256SUMS.sig` (JSON: `key_id`, the
  public key in the specification's form, the Ed25519 signature of the file's bytes). It shows the files did not
  change on the way; it is not a trust anchor: `README.txt` still says to compare the key_ids with a daily report
  received by email first.
- Every export is recorded as an evidence bundle of its own (`export-<time>-<random>`), with its ledger entry: who
  made it, scope, period, and the SHA-256 of its `SHA256SUMS`. The record is made once the export is complete, so it
  is in the server's ledger and in every later export, not in the one it records. Any Restore Evidence Format v1
  verifier checks it unchanged (no new ledger entry type). An export that cannot be recorded (a ledger that is not
  intact) is refused. The Evidence screen lists the records as exports, by whom.
- `gaia evidence verify -pubkey anchor.pub -export DIR` checks an unpacked export: every file against `SHA256SUMS`,
  the signature against the key the ledger leads to from the anchor, the ledger with every bundle and the export's
  daily-report head, and lists the exports the ledger records. A missing `SHA256SUMS.sig` (an export made before
  0.2.0) is UNKNOWN, never OK.
