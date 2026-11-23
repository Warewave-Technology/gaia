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
- Reason codes version 3: `host_key_changed` (a host that presents an SSH key other than the pinned one is its own
  cause, not "backups fail"). Gaia keeps the presented key and reads nothing from the host until an admin compares
  the fingerprints and accepts it on Inventory → Hosts (`POST /api/v1/hosts/{id}/accept-key`, with a step-up). It is
  never accepted automatically; `POST /api/v1/hosts/{id}/trust` no longer replaces a pinned key (409).
- While the sandbox helper does not answer, the proof capacity is modelled from the slot count it last reported
  (`slots_source: last_known`, `slots_at`), shown as "last known: 2 slots, at 14:02".
- A policy says how much of the time its VMs can be OK at best (`max_ok_fraction`) and which test interval would make
  it always possible (`suggested_test_interval_s`). OK/WARNING changes caused only by the proof's age on such a policy
  are counted in the daily report instead of being notified one by one.
- A new event, `capacity_short`, when the proof capacity drops below 100 % (category `jobs`).
- The evidence ledger is checked every hour (incremental) and in full with the daily report.

### Migration checks of Windows (ADR-005 B, C, T28)

- A converted Windows must keep answering for `settle_s` (60 s) after its checks pass. One that restarts on every
  boot because a virtio-win driver fails to install at first boot is `CRITICAL` with the reason code
  `driver_install_loop` and the driver's name, read from virt-v2v's first-boot log on the guest's disk (in the
  bundle: `migration/firstboot.json`).
- **T21's Windows `OK` was a false positive**: with virtio-win 0.1.302, Windows Server 2022 restarts for `smbus` on
  every boot; T21 saw only its first answer.
- New per-check option `exclude_drivers` (a list of virtio-win driver names): the guest is converted without them,
  recorded as a `WARNING` step (`drivers_excluded`) in the bundle and the readiness report, so the check is never
  `OK`. The Migration screen offers **Check again without <driver>**.
- Known limit (docs/limitations.md): a converted Windows is still `CRITICAL` on `stable`, even without the looping
  driver, because virt-v2v's own first-boot restarts fall inside the stability window (ADR-005 B, awaiting a decision).

### Notification categories (ADR-007 C8, T28)

- **Channels take categories, not a list of events**, so a new event of a category reaches every channel that takes
  the category:

  | Category | Events |
  |---|---|
  | `status` | `status` |
  | `jobs` | `job_failed`, `full_backup`, `capacity_short` |
  | `integrity` | `integrity`, `quarantine`, `ledger` |
  | `security` | `breakglass_signin`, `new_device_signin`, `account_locked`, `settings_changed`, `host_key_changed` |
  | `migration` | `migration_check_finished` |

  The daily report belongs to no category: a channel takes it when its `daily_report` is true. A test message
  (`gaia channels test ID`) goes to the channel it is asked for, whatever it takes.
- New events: `migration_check_finished` when a migration check ends `WARNING`, `CRITICAL` or `UNKNOWN` (not `OK`),
  with the VM, the source, the readiness and the reason; `settings_changed` when the sign-in and security settings
  change, naming the settings that changed (not their values); `host_key_changed` (ADR-007 C18.2) as soon as a host
  presents an SSH key other than the pinned one, with the host, both fingerprints (`host_key.pinned_fingerprint`,
  `host_key.presented_fingerprint`), when the pinned key was pinned and when the new one was first seen: Gaia reads
  nothing from that host, so its backups are paused, until an admin checks the key and accepts it on Inventory →
  Hosts (with a step-up).
- **Channels from 0.1.x are mapped at the upgrade:** each keeps its event list, readable as `m2_events`, and gets every
  category one of its events belongs to, whole (a channel that took only `quarantine` now also gets `integrity` and
  `ledger`); it takes the daily report when its list held `daily_report`. Every channel of 0.1.x also gets
  `security`, since the upgrade gives the sign-in events to every existing channel. A channel that took 0.1.2's
  default list, every event there was (`status`, `job_failed`, `full_backup`, `integrity`, `quarantine`, `ledger`,
  `daily_report`), gets all five categories, `migration` included (ADR-007 C18.3; schema 16, so also on a server
  already upgraded to an earlier 0.2.0 build). Any other channel does not get `migration`: add it with
  `gaia channels set ID categories='["status","jobs","integrity","security","migration"]'`. Each channel's mapping is
  in `gaia state-changes list` (entity `notification_channel_categories`, with the list before and the categories
  after; schema 16's addition of `migration` is a second entry).
- **Telling fewer people needs a step-up** (ADR-007 C18.4, ADR-008 §4): a `PATCH` of a channel that removes a
  category, turns the daily report off or turns the channel off asks for a fresh second factor (`step_up_required`
  without one); `gaia channels set` asks for it like the other step-up actions. Adding a category, or turning the
  daily report or the channel on, needs none. The daily report counts as weakening because its readers lose the
  day's summary and its signed ledger head.
- API: `categories` (default: all five) and `daily_report` (default true) on create; `PATCH` changes them as well as
  `enabled`. A channel shows `categories`, `daily_report` and `m2_events`; `events` is no longer shown. `events` is
  still accepted on create in 0.2.x (deprecated), mapped to categories as above; giving both is refused.

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
