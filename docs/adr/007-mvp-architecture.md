# ADR-007: MVP architecture

Status: accepted (2026-11-16), amended 2026-11-16 (B1–B5), 2026-11-20 (C1–C10), 2026-11-22 (C11–C16) and 2026-11-23 (C17–C19) and 2026-11-27 (C20); amendments win over the text above them.
The feasibility study ended with **GO** (2026-11-16).

## Scope of the MVP

KVM/libvirt hosts only. Backups of running VMs (multi-disk), scheduled restore tests in an isolated sandbox,
signed evidence, and an operator screen that answers the core question: *which point can I return to right now,
and is that proven?* Migration check (ADR-003/005) is part of the MVP. Proxmox, KubeVirt, the Windows VSS agent
and the hot migration check are not.

**Design target (not a promise):** one Gaia server, up to ~50 VMs and ~10 TB of protected disk.

## Decisions

1. **One binary, one server.** `gaia server` runs API, UI, scheduler and workers in one process under systemd on
   Debian. No external services required.
2. **State in SQLite**, through a pure-Go driver (`modernc.org/sqlite`; no cgo, ADR-001), WAL mode. Gaia backs up
   its own state (config, catalog, keys' public parts, ledger) into its repository every day.
3. **Protected hosts are reached over SSH only.** One authenticated SSH connection per host carries both libvirt's
   RPC (go-libvirt over a forwarded unix socket) and the NBD export (libvirt pull-mode backup bound to a **unix
   socket** on the host, forwarded). No NBD over TCP, no unauthenticated channel. Pure-Go SSH client. If libvirt
   cannot bind the export to a unix socket in a supported version, fall back to TLS and report it.
4. **Repository:** a Kopia repository on a filesystem path or S3-compatible storage, with ADR-004 defaults.
5. **Sandbox runs on the Gaia server host** (it must have KVM). A remote sandbox host is a later step.
6. **API:** REST/JSON with an OpenAPI description. The CLI is a client of the same API.
7. **UI:** server-rendered Go `html/template` + htmx, vendored. No Node toolchain, no CDN: Gaia must work in
   air-gapped, regulated networks.
8. **Users and roles:** local users (argon2id), three roles — `admin`, `operator`, `auditor` (read-only, can
   export evidence). Every user action goes to an append-only audit log. OIDC is later.
9. **Keys:** one ed25519 signing key per installation; `key_id` per ADR-006. Rotation writes a ledger entry signed
   by the old key that endorses the new one.
10. **External anchoring, cheap:** the daily report (email and webhook) carries the ledger head hash, so copies of
    it exist outside Gaia. RFC 3161 timestamping is later.

## The status model (the product's heart)

For every protected VM Gaia computes, at every page load and on every event:

| Field | Meaning |
|---|---|
| Theoretical RPO | now − time of the newest backup point that passed its post-backup check |
| Proven RPO | now − time of the newest point whose restore test is `OK` |
| Proven RTO | ADR-004 A4, from that test |
| Proof age | now − time of that restore test |

A policy sets targets: RPO target, RTO target, maximum proof age. The VM's status is:

| Status | When |
|---|---|
| `OK` | Proven RPO ≤ RPO target **and** Proven RTO ≤ RTO target **and** proof age ≤ maximum |
| `WARNING` | Theoretical RPO is within target, but proof is missing a target (proven RPO, RTO or proof age) |
| `CRITICAL` | Theoretical RPO over target, **or** the latest restore test is `CRITICAL`, **or** chain/integrity verification failed |
| `UNKNOWN` | No restore test ever passed, or any input is missing |

Rules: `UNKNOWN` is never shown as healthy; when several rows apply, the worst wins (`CRITICAL` > `UNKNOWN` >
`WARNING` > `OK`). The truth table above is implemented as one function with exhaustive tests.

## Core-matrix items and where they live

| # | Question | MVP mechanism |
|---|---|---|
| 1 | Was the backup really taken? | Post-backup check: read back the point's manifest and block index, confirm every block is present and sizes match |
| 2 | Is the chain intact? | Scheduled repository verification (sampled daily, full weekly) and per-point index checks |
| 3 | Was a restore tested? | Scheduled `gaia verify` per policy |
| 4 | Does the operator know? | The status model above, on the operator screen |
| 5 | Can it be proven? | Evidence spec v1 (ADR-006), auditor export, ledger head in the daily report |

## Amendments (2026-11-16, after M1)

Decisions on the `ONAY GEREKİYOR` items of reports T14–T16, approved by the product owner.

### B1. Two units, one binary (amends §1 and §5)

The sandbox needs root (network namespace, veth, dnsmasq). Gaia runs as **two systemd units of the same binary**:
`gaia.service` (API, UI, scheduler, workers; unprivileged, no capabilities) and `gaia-sandbox.service`
(root; no network listener; one unix socket `0660 root:gaia`; one endpoint; opens only plain files in its image
directory; holds no keys; returns unsigned bundles, the server signs). The process that faces the network never
has root, and the root process never faces the network. Giving `gaia.service` capabilities is rejected.

### B2. One SSH connection per job (amends §3)

"One authenticated SSH connection per host" becomes: **every job uses one authenticated SSH connection** that
carries both its libvirt RPC and all its NBD streams. Parallel jobs on a host use separate connections. A
connection pool per host is a later optimisation, not a requirement.

### B3. Damaged repository contents are quarantined (core matrix #1, #2)

T15 showed a damaged content stays in Kopia's index and is silently reused by deduplication, so new points can
pass the post-backup check and still be unreadable. When repository verification finds damage:
1. the damaged content IDs are removed from the index (the pack stays until GC, for inspection);
2. every affected point is marked `failed` with the content IDs as the reason;
3. the next backup of every affected VM is a full backup, recording the reason;
4. the event is an audit entry and, from M2, a notification.

### B4. Status model: three readings made explicit (refines "The status model")

1. "The latest restore test" means the latest test that reached a verdict (`OK`, `WARNING`, `CRITICAL`). A test
   that could not run (`UNKNOWN`, e.g. the sandbox was down) is shown as a note; it never clears `CRITICAL` and
   never counts as a pass. Otherwise breaking the sandbox would turn a red VM green.
2. A passed test counts toward Proven RPO only while its point still exists and is not `failed`.
   A pruned or damaged point proves nothing.
3. "Chain/integrity verification failed" means the VM has an existing point whose check `failed`
   (post-backup check or repository verification). Pruning that point clears it.

### B5. Recovery kit and state restore (amends §2)

Gaia's self-backup deliberately contains no secrets, so it is useless without the repository password and the
evidence signing key. Therefore:
- `gaia server init` (and every repository creation) writes a **recovery kit**: repository passwords and the
  evidence signing key, encrypted with a passphrase the operator chooses, in the **age** format
  (`filippo.io/age`, scrypt recipient). An operator can open it with the standalone `age` tool, without Gaia.
- `gaia server restore-state` rebuilds a server from a recovery kit and the repository alone.
- The install guide makes storing the kit off the server a step that cannot be skipped silently.

## Amendments (2026-11-20, after M2)

Decisions on the `ONAY GEREKİYOR` items of reports T20–T23, approved by the product owner. The UI prototype
approved on 2026-11-20 is in `docs/design/v2/`.

### C1. Design target: thousands of VMs (amends "Scope of the MVP")

The PO's T23 answer: the product will watch thousands of VMs. The design target becomes:
- **Control plane** (catalog, status model, API, UI, scheduler): **5,000 VMs, 100 hosts, 3 million points** on one
  Gaia server, measured with a synthetic catalog (T24 gate).
- **Data path and proof** scale with hosts and sandbox slots, not with the server. Proof capacity is shown, never
  assumed (C4). A remote sandbox host is the next step after M3, not part of it.

### C2. UI stack (amends §7; T20 item 1)

Accepted: React 19 + TypeScript (strict) + Vite, as T20 built it. Kept from §7: no CDN, fonts vendored, `web/dist`
committed and embedded in the binary, `go build` and installs need no Node, CI checks that `dist` matches the
source. Changed: reading in the browser needs JavaScript; without it the page says so and points to `gaia status`
and the API, which stay complete.

### C3. Reason codes (refines "The status model")

Every reason that can make a VM non-OK has a **stable machine code** (e.g. `test_failed_boot`,
`repo_content_damaged`, `no_backup_in_rpo`, `host_unreachable`, `never_proven`, `proof_too_old`, `rto_over_target`,
`test_not_run`), plus parameters (host, repository, test id) and a human sentence built from them. The overview's
"Needs attention" groups VMs by `(code, cause scope)`, so one host failure is one row, not 41. Codes are part of
the API contract and versioned with it.

### C4. Parallel restore tests and proof capacity (amends §5; T23 item 5)

- The sandbox runs up to `max_parallel_tests` tests at once (default: computed from the host's memory and CPUs,
  capped at 8). Each slot has its own network namespace, veth, dnsmasq, overlay and IP range; slots cannot reach
  each other (tested). Admission is by memory: a test starts only if guest RAM + a fixed overhead fits under the
  sandbox's memory limit.
- Queue order: retests of `CRITICAL` VMs, then never-proven VMs, then by time to proof expiry, then by policy.
  The order is part of the API (`queue_position`, `estimated_start`).
- **Proof capacity** is computed, not guessed: demand = Σ over VMs of (tests per day from the policy × that VM's
  median test duration over its last 10 tests, or the OS-class median if it has none); capacity = slots × 24 h.
  The API and the UI show coverage, the VMs whose proofs will expire before their next test, and the slots needed.
  Policy edits show their effect on capacity before saving.
- The B1 privilege split is unchanged: slots live inside `gaia-sandbox.service`.

### C5. Third unit for conversion (amends B1; T21 item 2)

Accepted: `gaia-convert.service`, own user, no network listener, a socket shared only with `gaia.service`,
rootless podman with the bounding set T21 measured. B1 now reads "three units, one binary"; only
`gaia-sandbox.service` runs as root. Rule 13 in CLAUDE.md is updated.

### C6. Forgetting damaged points (B4.3; T20 item 4, T22 item 1)

Accepted as built in T22 (option a): `gaia points forget`, admin only, `failed` points only, reason required.
From M3 the UI offers it on a damaged point, behind step-up (ADR-008 §4).

### C7. Evidence export (T20 item 3)

Accepted as built, plus: the export's `SHA256SUMS` is signed by the installation's evidence key, and every
export is recorded as an **evidence bundle** (its manifest names who, scope, period and the SUMS hash) with its
ledger entry, so v1 verifiers check it unchanged. The signature is a convenience for integrity in
transit, not a trust anchor; the README keeps the key_id comparison against an externally received daily report
as the first step.

### C8. Notifications (T23 item 1, T21 open question)

- `full_backup` (with its reason) is accepted as an event.
- New: `migration_check_finished` for results other than `OK`, plus the ADR-008 events `breakglass_signin`,
  `account_locked`, `new_device_signin`, `settings_changed`.
- Channels subscribe to **categories** (status, jobs, integrity, security, migration), not to a fixed event list,
  so new events in a category reach existing channels. The migration from M2's lists keeps every channel's current
  events and maps it to the categories it covered.

### C9. Daily report after a rebuild (T23 item 2)

Accepted: `last_rebuild` field and a `REBUILT:` line for 48 hours after `restore-state`.

### C10. Packaging and accounts (T22 item 2, open questions)

Accepted: the `.deb` layout from T22. The T20 review users (`ui-review`, `ui-auditor`) are disabled before any
further acceptance run. The release location is a PO decision that is still open; until then releases are
published by hand as T22 documented.

## Amendments (2026-11-22, after T24–T27 step 1)

Decisions on the `ONAY GEREKİYOR` items of reports T24, T25 and T27, approved by the product owner. The UX rules
and the v3 screens approved on 2026-11-22 are in `docs/design/v3/`.

### C11. Reason codes and the status table (T24 items 1–3)

- Accepted: `rule` (the truth-table row) and `code` (the cause) are separate fields; `notes` (text) and
  `note_reasons` (codes) are both kept in API v1. The UI uses codes and builds its own sentences (C15); the text
  fields are for M2 scripts and are marked deprecated in the OpenAPI description.
- Accepted: a status changed by the clock alone is recorded at most one scheduler tick (15 s) late.
- The startup full recompute is split into batches so that requests are not slowed for its whole duration
  (T24: 29.4 s cold, limit 30 s).
- New code `host_key_untrusted`, scoped to the host. A host whose key is not pinned is one group in "Needs
  attention", not part of "backups fail".

### C12. Policies that cannot be OK (T24 item 4)

A policy whose test interval is longer than its RPO target can only be `OK` part of the time. The status model is
right about this and does not change. Instead:
- The policy API and the policy editor show **how much of the time a VM on this policy can be OK at best**
  (e.g. "at most 17 % of the day") and suggest the test interval that would make it possible. Saving is allowed.
- `OK ↔ WARNING` changes whose only reason is `proven_rpo_over_target` or `proof_too_old` on such a policy are not
  sent as immediate notifications; they are counted in the daily report.

### C13. Parallel tests, settled (T25 items 1–6)

- Accepted: admission may let a small test pass a waiting large one; after 10 minutes of waiting, nobody passes it.
- Accepted: the setting is the sandbox unit's `slots` (and `memory_mib`); C4's `max_parallel_tests` is renamed.
- Accepted: expected durations default to Linux 180 s, Windows 600 s. The OS class comes first from the VM's
  `os:` tag, then from the guest information libvirt reports, and only last from the check template's name.
- Accepted: a restore reserves the full size of the VM's disks.
- Accepted: what-if is not audited and every role may call it; it is rate-limited per session.
- New fleet-level event `capacity_short` (category jobs) when coverage drops below 100 %, with the slots needed.
  It is a notice on the overview and in the daily report, not a VM status: a VM turns `WARNING` only when its
  proof actually expires.

### C14. Ledger checks (T27 K4)

- The ledger is checked **every hour**, after every daily report, and on "Check now". Each check verifies the new
  entries since the last check and the link to the last checked entry; a full check runs daily.
- On the overview, the ledger is `OK` while unchecked entries are younger than 2 hours (the fact says how many
  wait); it becomes `UNKNOWN` when a check is overdue (no successful check for 2 hours) or failed to run.
- A ledger with no entries while restore tests have passed is `CRITICAL` (`evidence_missing`): tests that leave
  no evidence are a fault, not an empty state.

### C15. UI language and structure (T27 review; replaces nothing, adds rules to C2)

The rules in `docs/design/v3/README.md` are binding for every screen: data-tables for every list, one screen per
question with details in tabs or expanding rows, human sentences built from reason codes with raw detail folded,
one duration and time format, charts that explain themselves with at most four legend items.

### C16. Job and point vocabulary (T27 K1, K3)

- **Job states are not VM statuses** and never use the status marks: `● RUNNING` (blue, animated unless reduced
  motion is set), `QUEUED` (neutral, with queue position and estimated start), `✓ DONE`, `▲ DONE, WARNING`,
  `◆ FAILED`, `CANCELLED`.
- **A point that was backed up and checked but not tested is neutral:** `BACKED UP` with "not tested", no ✓ and no
  hatching. `✓ PROVEN` is only for a point a passing restore test proved; `? UNKNOWN` only for something that
  should have been checked and could not be.

## Amendment (2026-11-23, after T27)

### C17. Decisions on T27's `ONAY GEREKİYOR` items

1. **Recompute batching (C11):** back to T24's setting (250 per transaction, no pause): T27 measured no gain from
   50 + 5 ms on the gate VM. C11's aim is restated as measurable: *while a full recompute runs, view requests keep
   T24's limit (p95 ≤ 300 ms), and a cold-start recompute of the T24 catalog finishes within 60 s on the gate VM.*
   T27's 31.4 s cold start and p95 90–102 ms meet it.
2. **`host_key_changed` (new code, CRITICAL, host scope):** a host that presents a key different from the pinned
   one is its own cause, not `backup_failing`. Sentence: "kvm-07 presents a different SSH host key than the one
   pinned on <date>. Gaia reads nothing from it until an admin checks and accepts the new key." Accepting it is a
   step-up action (ADR-008 §4) and an audit entry. Never accepted automatically.
3. **`max_ok_fraction` (C12):** accepted as an upper bound; the UI already says "at most".
4. **OS class (C13):** the libvirt guest step is dropped (Gaia does not keep it); the order is the `os:` tag, then
   the check template's name, then the default. `capacity_short` moves to the jobs category in T28.
   When the sandbox does not answer, the server models from the **last slot count the sandbox reported**, shown as
   "last known: 2 slots, at 14:02", not from 1 and not from a second copy of the setting in `server.json`.
5. **Incremental ledger checks (C14):** accepted: the hourly check verifies the chain links, sequence, key and the
   last checked entry's hash; signatures and bundles of older entries are re-verified by the daily full check.
6. **Durations (C15 rule 4):** accepted with the PO's approval of the screens: seconds are shown below 2 min for
   any duration, and below 10 min for measured durations of a restore, a test or a test step ("4 min 22 s").

### C18. Decisions on T28's `ONAY GEREKİYOR` items (2026-11-23)

1. **A changed host key acts at once (refines C17.2).** Gaia stops reading from the host, so its VMs' backups are
   paused. New status-table row: a VM whose host is `host_key_changed` is at least `WARNING` (`host_key_changed`,
   "backups paused") from the moment the change is seen, and `CRITICAL` when its RPO target passes, as before.
   The host is a `CRITICAL` cause in "Needs attention" at once.
2. **Notification:** event `host_key_changed` in the `security` category, sent immediately.
3. **M2 channels and categories (C8):** a 0.1.x channel that carried 0.1.2's default "all events" list gets all
   five categories, `migration` included; other channels keep the mapping as built. Every old channel keeping
   `security` (from schema 11) is accepted: security events should reach someone.
4. **Removing a category from a channel, or disabling a channel, is a step-up action** (it weakens who is told,
   ADR-008 §4 "weakening"). Adding a category is not.
5. **Export when the ledger cannot record it (C7):** the export is **given**, not refused: the auditor may need it
   exactly then. Its manifest and README say "not recorded in the ledger: <reason>", `SHA256SUMS` is still signed,
   the export is an audit entry and a `security` notification is sent. The next successful ledger write records it.
6. **Single-bundle export:** its `SHA256SUMS` is signed too; it is an audit entry, not a ledger bundle. An unsigned
   `SHA256SUMS` (exports from before 0.2.0) verifies as `UNKNOWN` for the signature, as built.
7. **Migration view:** paginated on the server before T29's acceptance (CLAUDE.md rule 15).

### C19. Decisions on T29 preparation's `ONAY GEREKİYOR` items (2026-11-23)

1. **Turning the daily report off** for a channel is a step-up action, as built (C18.4).
2. **Host-scoped causes show their full reach.** "Needs attention" keeps grouping each VM by its determining
   cause, but a host-scoped cause (`host_key_changed`, `host_key_untrusted`, `host_unreachable`) states every VM
   on the host: "affects 41 VMs · 38 here, 3 under other causes", and its "Show these VMs" lists all 41.
3. **Pending export records go into the daily state backup** (they are small); `restore-state` re-queues them.
   Remove the entry in `docs/limitations.md`.
4. **`GET /api/v1/migration-checks` is paginated** like the other lists. The live vSphere inventory stays the
   source's own list; the screen pages it on the client.
5. **0016 only adds `migration`**, as built: an admin's earlier removal of a category stands.
6. **The Evidence screen shows "not recorded in the ledger"** on an export that was given without a ledger record
   (C18.5), next to the download and in its row. Nothing about evidence may look more finished than it is.

## Amendment (2026-11-27, M3 closed)

M3 was accepted in T29 (0.2.1, signed). The PO's answers, in his words: *"Evet, needs attention kısmı muhteşem
çalışıyor."* and *"Evet, bence epeyce güzel olmuş."*

### C20. Decisions on T29's `ONAY GEREKİYOR` items

1. **Disk admission for restore tests (extends C4 and C13; built in 0.2.1).** A test starts only when the restored
   disks of the VM's newest checked point fit in the sandbox images directory next to what running tests hold.
   Otherwise it waits in the queue; smaller tests may pass it for 10 minutes, then nobody passes it (C13's memory
   rule, now for memory and disk alike). A VM whose disks do not fit even alone is `UNKNOWN` at once with the reason
   and listed as `disks_too_large`. Proof capacity simulates the images room as well as the slots; the formulas for
   demand, capacity, coverage and slots needed are unchanged.
2. **Reach of host-scoped causes (C19.2):** "affects N VMs" counts every VM on the host, OK ones included, and says
   how many are still OK: they will be affected soon (an unreachable host's VMs are OK only until their RPO passes).
