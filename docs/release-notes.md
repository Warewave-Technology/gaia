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

### Notification categories (ADR-007 C8, T28)

- **Channels take categories, not a list of events**, so a new event of a category reaches every channel that takes
  the category:

  | Category | Events |
  |---|---|
  | `status` | `status` |
  | `jobs` | `job_failed`, `full_backup`, `capacity_short` |
  | `integrity` | `integrity`, `quarantine`, `ledger` |
  | `security` | `breakglass_signin`, `new_device_signin`, `account_locked`, `settings_changed` |
  | `migration` | `migration_check_finished` |

  The daily report belongs to no category: a channel takes it when its `daily_report` is true. A test message
  (`gaia channels test ID`) goes to the channel it is asked for, whatever it takes.
- New events: `migration_check_finished` when a migration check ends `WARNING`, `CRITICAL` or `UNKNOWN` (not `OK`),
  with the VM, the source, the readiness and the reason; `settings_changed` when the sign-in and security settings
  change, naming the settings that changed (not their values).
- **Channels from 0.1.x are mapped at the upgrade:** each keeps its event list, readable as `m2_events`, and gets every
  category one of its events belongs to, whole (a channel that took only `quarantine` now also gets `integrity` and
  `ledger`); it takes the daily report when its list held `daily_report`. Every channel of 0.1.x also gets
  `security`, since the upgrade gives the sign-in events to every existing channel. No 0.1.x event belongs to
  `migration`, so no existing channel gets it: add it with
  `gaia channels set ID categories='["status","jobs","integrity","security","migration"]'`. Each channel's mapping is
  in `gaia state-changes list` (entity `notification_channel_categories`, with the list before and the categories
  after).
- API: `categories` (default: all five) and `daily_report` (default true) on create; `PATCH` changes them as well as
  `enabled`. A channel shows `categories`, `daily_report` and `m2_events`; `events` is no longer shown. `events` is
  still accepted on create in 0.2.x (deprecated), mapped to categories as above; giving both is refused.
