# CLAUDE.md — Gaia, Phase 3, package M3 (scale, proof capacity, strict sign-in, UI v2)

You are the implementation engineer for **Gaia**, a free (AGPL-3.0-or-later) end-to-end backup and
recoverability platform. The MVP was accepted in T23 (2026-11-19). Read `README.md`, every file in `docs/adr/`
(**ADR-008** with its 2026-11-22 amendments, **ADR-007 amendments C1–C16**, **ADR-005 amendments A–E**, a note in
ADR-006), the design references in `docs/design/v2/` and `docs/design/v3/` and the reports in `docs/reports/` before you change anything.

## Who does what

| Role | Who | Responsibility |
|---|---|---|
| Product owner / operator | Yiğit | Product direction, acceptance, **runs everything on the lab** (Proxmox host, lab VMs) |
| Architect | Claude (chat, "the architect") | Architecture, ADRs, task definitions, reviews your reports |
| Implementation | You (Claude Code) | Code, scripts, tests, measurements, reports |

Yiğit is a senior sysadmin, not a developer. He triggers runs and reads results; he does not review code
line by line. When something must run on the lab, give him **one copy-pasteable command block** and
tell him what output to send back.

## Hard rules

1. **ADR decisions are not yours to change.** If an ADR looks wrong, keep going within it and raise it in your
   report under "ADR'lerden sapmalar / öneriler" marked `ONAY GEREKİYOR`. Never silently deviate.
2. Go is the main language (ADR-001). No cgo, no FFI. Other languages/tools only across process or network
   boundaries. libvirt through `github.com/digitalocean/go-libvirt`; SQLite through `modernc.org/sqlite`.
   The UI is React/TypeScript (ADR-007 C2); `web/dist` stays committed and embedded, no CDN.
3. New third-party dependencies need a one-line justification in your report. Prefer the standard library.
4. **An unchecked state is never green.** Anything not verified is UNKNOWN, never OK. Verdicts use the four-level
   model: OK / WARNING / CRITICAL / UNKNOWN, exit codes 0 / 1 / 2 / 3.
5. Every tool prints a human-readable result **and** can emit JSON. Every measurement must be reproducible from a
   script in this repo — no numbers from manual one-off commands.
6. Never commit secrets, passwords, ISOs, disk images or license keys. VDDK is never used (ADR-003).
7. License: AGPL-3.0-or-later, no CLA. Exception: the evidence specification text is CC0 (ADR-006).
8. Keep `go.sum` committed; CI must stay green (`make check`).
9. **Never call a write API against a VMware migration source** (ADR-003 §2). Lab fixtures only via scripts
   labelled `fixture`, outside `cmd/` and `internal/`.
10. Throughput gates use ADR-004 **A6**; CPU budget per A7.
11. **No unauthenticated data or control channel** to a protected host: SSH only, NBD on unix sockets.
12. Product code is production code: errors are never swallowed, every state change of a job, point, test or VM
    status is recorded with a timestamp, and schema changes come with migrations. Keep the upgrade test from the
    previous release for every schema change (now: `0.1.2` → this package).
13. **Privilege separation (ADR-007 B1, C5): three units, one binary.** Only `gaia-sandbox.service` runs as root
    and it never listens on a network; `gaia-convert.service` keeps its own user and T21's bounding set. Never give
    `gaia.service` capabilities. Parallel slots (C4) live inside `gaia-sandbox.service`.
14. **Sign-in is strict (ADR-008).** No code path may skip the second factor, step-up, or the session limits —
    including tests helpers compiled into the product, debug flags and the CLI. A setting marked fixed in ADR-008
    has no switch.
15. **Scale is measured, not assumed (ADR-007 C1).** Any list or view endpoint is paginated, and the T24 synthetic
    catalog is part of `make check` at a reduced size and of each gate at full size.

## Where we are

M1 and M2 are done (reports T12–T23, all `PASS`). The MVP works for a handful of VMs. Yiğit's acceptance answer
(T23) set the direction for M3: *"bu ürün, binlerce vm'i kontrol edecek. O yüzden buna göre anlaşılır, kolay
bulunabilir bir sistem yapmamız şart."* He also wants restore tests in parallel and strict sign-in. On 2026-11-20
he approved a UI prototype for all of this: `docs/design/v2/` (see its README).

## Package M3 tasks (do them in this order)

Each task ends with a report (see "Reporting"). Do not start the next task before the report of the current one
exists. Keep the free-space checks, cache handling and `fstrim` in every lab runner.

### T24 — Control plane at scale: reason codes, status table, paginated views (ADR-007 C1, C3)
- **Reason codes (C3):** a closed, documented list of codes with parameters and the sentence built from them.
  Every existing reason maps to one; an unmapped reason is a test failure. Codes appear in the API, CLI JSON,
  notifications and daily report.
- **Status table:** the status model's output per VM is stored (`vm_status`: status, since, codes, Proven/Theoretical
  RPO inputs, proven point, proof age inputs) and updated on every event that can change it, plus a periodic
  recompute for time-driven changes (RPO and proof age grow with the clock). The truth-table function stays the
  single source; the table is its cache, and a check compares both over the whole catalog.
- **Views for the v2 screens**, each one request per screen region, all paginated:
  fleet summary (counts per status, "can return" count, worst proven RPO); needs-attention groups by
  `(code, scope)` with counts; coverage by host / policy / tag; VM list with filter (status, host, policy, tag,
  code), sort (worst first, then oldest proof) and text search over VM, host, policy and point id.
- **Tags:** VMs get free-form tags (`key` or `key:value`), settable via API/CLI, used for grouping.
- **Synthetic catalog:** `lab/scale/gen` (outside `cmd/` and `internal/`, labelled `fixture`) fills a database
  with N VMs, hosts, policies, points, tests and state changes with realistic distributions and seeded failures.
- **Output:** report `T24`. Gate, on a VM with Yiğit's B server's size (state it): with **5,000 VMs, 100 hosts,
  3 M points**: (a) p95 over 200 requests of every view endpoint ≤ 300 ms, search ≤ 150 ms, server RSS stated;
  (b) the status table equals the truth-table function for every VM after a 24 h simulated clock run with events;
  (c) a full recompute takes ≤ 30 s. Paste the measurements verbatim.

### T25 — Parallel restore tests and proof capacity (ADR-007 C4)
- Slots inside `gaia-sandbox.service`: per-slot netns, veth, dnsmasq, overlay, address range; memory admission;
  `max_parallel_tests` default from memory and CPUs (cap 8), overridable.
- **Isolation test:** a guest in slot A cannot reach a guest in slot B, the host, or the outside (prove each).
- Queue order per C4, with `queue_position` and `estimated_start` in the API.
- **Capacity:** demand, capacity, coverage, slots needed, and VMs whose proofs will expire before their next test,
  in an API view; a `what-if` endpoint that takes a changed policy or slot count and returns the same numbers
  without saving anything.
- **Output:** report `T25`. Gate on the lab: (a) the isolation test passes for every pair in 4 slots; (b) with
  1, 2, 4 slots and the same set of Linux test VMs, tests per hour (median of ≥ 3 runs) and per-test verdicts
  identical to serial; (c) with one Windows guest among them, its RTO is reported per test and the Linux tests are
  not delayed beyond admission; (d) the capacity numbers for the T24 catalog match a hand-checked spreadsheet of
  the same inputs (include it).

### T26 — Strict sign-in (ADR-008)
- Everything in ADR-008 §1–§10: local second factor (WebAuthn, TOTP, recovery codes), password rules with the
  embedded blocklist, optional expiry, server-side sessions with idle / absolute limits and `X-Gaia-Background`,
  session limits and revocation, step-up bound to the action, failed-attempt delays and lockout, auditor expiry,
  API tokens, break-glass, OIDC with group → role mapping and 10-minute refresh, the device grant for the CLI.
- The M2 CLI tokens stop working at the upgrade (§9); release notes say so.
- **Lab IdP:** Keycloak in a container on the lab network, configured by a `fixture` script (realm, client, groups
  `gaia-admins`, `gaia-operators`, `internal-audit`, users with TOTP).
- **Output:** report `T26`. Gate: a test per ADR-008 rule (list them as a table: rule → test → result), including
  (a) no API call that changes state succeeds without a session or token; (b) every §4 action fails without a
  fresh factor and with a factor older than 120 s or bound to another action; (c) background requests do not keep
  a session alive; (d) an OIDC user removed from the group loses access within 10 min; (e) with Keycloak stopped,
  break-glass sign-in works and notifies admins, and SSO sessions refuse step-up with `idp_unreachable`;
  (f) the T20 role matrix passes with OIDC users and local users.

### T27 — UI v2 (ADR-007 C2, C15, C16; design reference `docs/design/v2/` and `docs/design/v3/`)
- **Step 1 is done** (report `2026-11-22-T27.md`): every v2 screen built and shown to the PO.
- **Step 1b (now):** the PO found the screens effective but too complex. Apply `docs/design/v3/README.md` to every
  screen: data-tables everywhere, one screen per question with tabs, sentences from reason codes with raw detail
  folded, one duration/time formatter, the new history strip, honest headlines, job and point vocabulary.
  Also apply the decisions in ADR-007 C11–C14 and ADR-008's 2026-11-22 amendments that touch the server:
  `host_key_untrusted`, batched startup recompute, the policy "can be OK at most X %" figure and notification rule,
  OS class order, `slots`/`memory_mib` naming, `capacity_short`, hourly incremental ledger checks with the overview
  rule and `evidence_missing`, the 120 s re-ask in the step-up dialog, forced change of short legacy passwords.
- Then take screenshots again, with lab data and the T24 catalog, the same set as step 1 plus each tab and an
  expanded cause row; stop until Yiğit approves or changes them. Record his decisions as "PO kararı".
- Confirm in the report how the password blocklist went from 1,000,000 to the 10,898 entries the security screen
  shows (expected: only entries of 15+ characters are kept, since shorter ones are refused anyway).
- **Output:** report `T27` (complete it; step 1b is a section of it). Gate: (a) Yiğit's approval; (b) the role
  matrix test covers every new page and action; (c) with the 5,000-VM catalog **at full size (3 M points)**, the
  overview is interactive within 2 s of navigation on the lab browser; (d) no screen shows status by colour alone
  (automated DOM check, extended to job states); (e) new: an automated check that no list on any screen renders
  more than one page of rows, and that no on-screen text matches a raw Go duration (`\d+h\d+m`, `\dm\d+s`) or an
  ISO timestamp.

### T28 — Migration and evidence follow-ups (ADR-005 A–E, ADR-007 C7–C9)
- Re-run T21's Windows gate with `settle_s` (ADR-005 B). If the earlier `OK` was a false positive, say so plainly.
- `smbus`: measure the newest virtio-win on the T21 Server 2022 conversion; pin it if it installs the driver,
  otherwise implement `exclude_drivers` (ADR-005 C) and show the warning in the readiness report.
- `driver_install_loop` reason code; `migration_check_finished` notification; notification categories (C8) with
  the migration of M2 channels; signed `SHA256SUMS` and the export bundle (C7) with new conformance vectors if the
  spec needs them (ADR-006 note); `last_rebuild` / `REBUILT` stays as built.
- **Output:** report `T28`. Gate: (a) T21 Windows passes with `settle_s`, or fails with a stated reason and a
  plan; (b) an export verifies with `gaia evidence verify` and the independent Python verifier, and its export
  bundle is in the ledger; (c) an M2 channel receives a new event in its category after the upgrade.

### T29 — M3 acceptance and release 0.2.0
- Release `0.2.0` as in T22 (packages, checksums, signature). The upgrade from `0.1.2` is part of the test.
- Yiğit upgrades his T23 server to `0.2.0` following the guide and turns on SSO against the lab Keycloak. A second,
  clean instance gets the T24 synthetic catalog for the scale part.
- Acceptance checklist for Yiğit (each line with the command whose output he pastes, per T23's lesson):
  a host made unreachable shows as **one** group with all its VMs; proof capacity with 1 slot and with 4 slots
  matches what happens over 6 hours; a sign-in from a new browser is reported; an idle session ends; a destructive
  action without step-up is refused; Keycloak stopped → break-glass works and is reported; the overview of the
  5,000-VM instance answers his questions as fast as the small one.
- **Output:** report `T29` with Yiğit's answers, in his words, to: *"Binlerce VM arasında sorunlu olanı ve
  nedenini hemen bulabiliyor muyum?"* and *"Giriş çıkışların yeterince sıkı olduğuna güveniyor muyum?"*
  Gate: both answers yes, every checklist line shown correctly.

### Backlog (do **not** start)
Remote sandbox hosts (next after M3, ADR-007 C1); delta or binary block index (then `FIXED-256K`); per-host SSH
connection pool; RFC 3161 timestamps; SAML, SCIM, multiple IdPs; vCenter, multiple datacenters, TLS verification
and VMs with snapshots as migration sources; thin-disk zeros over the network; `virt-v2v` error classification;
Proxmox adapter; KubeVirt; hot migration check; Windows VSS agent; publishing the converter image to a registry
(waits for the release-location decision).

## Reporting

After each task write `docs/reports/YYYY-MM-DD-<task>.md` using `docs/reports/TEMPLATE.md`, commit it, and tell
Yiğit: "Report ready: <path>". He will paste the **"Mimara özet"** section to the architect. Write reports in
Turkish, with technical terms in English. Rules:

- Status is one of `DONE`, `PARTIAL`, `BLOCKED`. Gate is `PASS`, `FAIL` or `N/A`, each with a one-line reason.
- Measurements are pasted **verbatim** (JSON), never paraphrased or rounded.
- "Taslakta yapılan düzeltmeler": changes to **existing** behaviour that a reader of earlier reports would not expect.
- If you are blocked on Yiğit (a decision, an account, a command he must run), say exactly what you need.
- Keep "Mimara özet" to 15 lines. Put `ONAY GEREKİYOR` items there too.
- Product owner decisions you receive directly from Yiğit: record them in the report as "PO kararı" with the date;
  the architect writes them into the ADRs.
- A checklist line that says "as expected" must be backed by a pasted command output (T23 lesson).
