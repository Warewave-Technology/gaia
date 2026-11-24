# Release notes

## 0.2.1 (package M3, T29 acceptance)

- **A restore test waits for room in the sandbox images directory instead of failing.** Every test reserves the full
  size of its VM's disks there (ADR-007 C13). In 0.2.0 a test that did not fit beside the running tests' started anyway
  and was `UNKNOWN` ("not tested: the sandbox images directory ... has ... bytes free"). With several slots, two large
  VMs due in the same minute then lost a test every time (T29: a 32 GiB Windows guest and a 6 GiB Linux guest in a
  36 GB directory, 4 slots). Now such a test stays queued until the running tests give the room back. Smaller tests
  may pass it for 10 minutes, then nothing passes it, as for memory. A VM whose disks do not fit even with nothing
  running is still tested at once and says why (`UNKNOWN`).
- **The proof capacity counts that room.** The simulation behind "Proofs that expire", the queue's estimated starts
  and the what-if hold back tests whose disks do not fit beside the running ones. `GET /api/v1/views/capacity` has
  `images` (`free_bytes`, `held_bytes`, `room_bytes`, `waiting`), `images_room_bytes` and `disks_too_large` (VMs never
  tested because their disks do not fit even alone). `GET /api/v1/views/test-queue` has `waiting_for_images`. The
  Proof capacity screen shows the room, the tests that wait for it, and the VMs that never fit.
- Schema 17: an index on a point's disks (the scheduler and the capacity read every VM's newest point's disk size).

## 0.2.0 (2026-11-23, package M3)

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
- Reason codes version 5 (ADR-007 C18): **a changed host key acts at once.** From the moment Gaia sees the different
  key (a backup or a host check), every VM of the host whose newest checked backup is still within its RPO target is
  at least `WARNING` with `host_key_changed` (new rule `backups_paused`: Gaia reads nothing from the host, so its
  backups are paused), and `CRITICAL` with the same code when its RPO target passes, as before. The host is a
  `CRITICAL` cause in "Needs attention" at once, even while its VMs are `WARNING`. `GET /api/v1/reason-codes` lists
  each code's other rows (`also`) and its group status (`group_status`).
- **A host-scoped cause shows its full reach** (ADR-007 C19): "Needs attention" still groups each VM by its deciding
  cause, but a group whose cause is a host (`host_unreachable`, `host_key_untrusted`, `host_key_changed`) says how many
  VMs the host has in all: "affects 41 VMs · 38 here, 3 under other causes" (and "N still OK"); the group's
  `reach` is `{host_vms, elsewhere, ok}`. Its **Show these VMs** lists every VM on the host
  (`GET /api/v1/views/vm-list?…&reach=true`).
- `GET /api/v1/migration-checks` is paged like the other lists: `before=` (the last id of the previous page) and
  `limit` (default 100, at most 1000), newest first (ADR-007 C19).
- **The migration view is paged on the server** (ADR-007 C18): `GET /api/v1/views/migration` answers one page of
  checks (`after`, `limit` default 50 and at most 200, `total`, `next`) instead of the newest 200, with `latest=true`
  (each VM's newest check, worst first), `result`, `source_id` and `vm` filters and the counts by result
  (`by_result`). It no longer reads every evidence bundle: only the page's. The Migration screen shows the checked
  VMs a page at a time and a read source's VMs in a table of their own.
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
- The stability window is the first uninterrupted `settle_s` that starts inside the boot timeout (ADR-005 F, H): the
  guest's own restarts before it (libvirt reboot events) are counted and listed, and after each one every guest check
  runs again. A converted Windows restarts a few times for virt-v2v's first-boot scripts and then passes (T29: 4
  restarts in 161 s without `smbus`); one that loops never gets a window and is `CRITICAL`. The check ends at the
  latest at the boot timeout + `settle_s` (+ the last probe's 5 s).
- Restore tests use the same window: a restored guest that restarts by itself while it boots is `WARNING` with the
  note code `guest_restarted_during_boot`, which does not change the VM's status by itself.

### Notification categories (ADR-007 C8, T28)

- **Channels take categories, not a list of events**, so a new event of a category reaches every channel that takes
  the category:

  | Category | Events |
  |---|---|
  | `status` | `status` |
  | `jobs` | `job_failed`, `full_backup`, `capacity_short` |
  | `integrity` | `integrity`, `quarantine`, `ledger` |
  | `security` | `breakglass_signin`, `new_device_signin`, `account_locked`, `settings_changed`, `host_key_changed`, `export_not_recorded` |
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
  verifier checks it unchanged (no new ledger entry type). The Evidence screen lists the records as exports, by whom.
- Every export holds `EXPORT.json`: who made it, its scope (`all`, or `bundle` for a single bundle), its period, and
  `ledger_record` (`sealed_after_export` with the record's bundle id, `not_recorded` with the reason, or `audit_only`).
- An export the ledger cannot record (a ledger that is not intact) is **given**, no longer refused (ADR-007 C18.5):
  its `README.txt` and `EXPORT.json` say `not recorded in the ledger: <reason>`, `SHA256SUMS` is still signed, it is
  an audit entry, and the new event `export_not_recorded` (category `security`) is sent. The server keeps the record
  under `<data_dir>/evidence/pending-exports/` and appends it with its next successful ledger write (a restore test's
  bundle, another export, or a ledger check that finds the ledger intact); that record's subject carries
  `recorded_late`, `not_recorded_reason` and `given`. Both steps are state changes (`entity=evidence_export`).
  `gaia evidence export-all` prints a `WARNING` and exits 1 for such an export (the file is written).
  The pending records are in Gaia's state backup (members `evidence/pending-exports/<bundle>.json`, counted in
  `MANIFEST.json` as `pending_export_records`), and `gaia server restore-state` re-queues them: its output and audit
  entry name them (`pending_exports_requeued` in `-json`), and the rebuilt server appends them with its next
  successful ledger write (ADR-007 C19.3). A state backup from an older version has no such member and restores as
  before. The Evidence screen lists such an export first among the entries, "not recorded in the ledger" (no entry,
  no hash, `UNKNOWN`), and says so next to the download, until its record is appended (ADR-007 C19.6;
  `GET /api/v1/views/evidence` rows carry `ledger_record`: `recorded`, `recorded_late` or `not_recorded`, and the
  view counts them in `not_recorded`).
- The single-bundle export (`gaia evidence export <id>`, `GET /api/v1/evidence/{id}/export`) now has `EXPORT.json`
  and a signed `SHA256SUMS` / `SHA256SUMS.sig` like the full export (ADR-007 C18.6). It is an audit entry, not a ledger
  bundle.
- `gaia evidence verify -pubkey anchor.pub -export DIR` checks an unpacked export: every file against `SHA256SUMS`,
  the signature against the key the ledger leads to from the anchor, the ledger with every bundle and the export's
  daily-report head, and lists the exports the ledger records (late ones with their reason). A missing
  `SHA256SUMS.sig` (an export made before 0.2.0) is UNKNOWN, never OK. An export whose `EXPORT.json` says
  `not_recorded` is UNKNOWN (`export_not_recorded`): no export holds its own record. A single-bundle export is
  verified with its whole ledger and its one bundle; the bundles it leaves out are counted (`left_out`), not
  `bundle_missing`.
