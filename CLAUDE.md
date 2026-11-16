# CLAUDE.md — Gaia, Phase 3 (MVP), package M2

You are the implementation engineer for **Gaia**, a free (AGPL-3.0-or-later) end-to-end backup and
recoverability platform. The feasibility study ended with **GO** on 2026-11-16. Read `README.md`, every file in
`docs/adr/` (new: **ADR-007 amendments B1–B5** and a note in ADR-006) and the reports in
`docs/reports/` before you change anything.

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
   boundaries. libvirt through `github.com/digitalocean/go-libvirt`; SQLite through `modernc.org/sqlite` (ADR-007).
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
10. Throughput gates use ADR-004 **A6** (reference = max(source read, repository write)); CPU budget per A7.
11. **No unauthenticated data or control channel** to a protected host: SSH only, NBD on unix sockets (ADR-007 §3).
12. Product code from now on is production code: errors are never swallowed, every state change of a job, point,
    test or VM status is recorded with a timestamp, and schema changes come with migrations.
13. **Privilege separation (ADR-007 B1):** only `gaia-sandbox.service` runs as root; it never listens on a network.
    Never give `gaia.service` capabilities.

## Where we are

M1 is done (reports `2026-11-16-T12.md` … `T16.md`, all `PASS`): Track B gate closed (1.249 under A6, CPU budget
4.292 CPU-s/GB), evidence spec v1 with 41 vectors and an independent verifier, `gaia server` with roles and an
append-only audit log, scheduled SSH backups with injected-failure tests, and the status model with exhaustive tests.

M2 turns this into something an operator and an auditor can use. Package M2 ends with the **MVP acceptance** (T23).

## Package M2 tasks (do them in this order)

Each task ends with a report (see "Reporting"). Do not start the next task before the report of the current one
exists. Keep the free-space checks, cache handling and `fstrim` in every lab runner. Keep Yiğit's T14 test VM: it is
the upgrade path test for every schema change in M2.

### T17 — Recovery kit and state restore (ADR-007 B5)
- `gaia server init` and every repository creation write a recovery kit in **age** format (passphrase/scrypt),
  holding repository passwords and the evidence signing key. The install guide makes storing it off the server
  an explicit, checked step.
- `gaia server restore-state` rebuilds a server from the kit and the repository alone.
- **Output:** report `T17`. Gate: **disaster-recovery drill** — destroy a server that protects one VM; on a fresh
  VM, from the guide, the kit and the repository only: Gaia comes back with its catalog, ledger and status, the next
  backup continues the chain (or is full with a recorded reason), and the ledger verifies across the rebuild. Also
  show the kit opens with the standalone `age` tool.

### T18 — Integrity quarantine, S3 and login protection (ADR-007 B3, §4)
- B3: damaged contents removed from the index, affected points `failed` with content IDs, next backup full with
  the reason, audit entry. Extend T15's corruption test to prove a new point no longer reuses the damaged content.
- S3-compatible repositories (MinIO in the lab), same defaults and same tests as filesystem repositories.
- Login rate limiting / lockout with audit entries.
- **Output:** report `T18`. Gate: (a) after a damaged content is found, the next backup's points restore
  byte-identical, (b) the T15 scheduled-backup gate passes on an S3 repository (≥ 4 cycles is enough),
  (c) repeated bad logins are throttled and audited.

### T19 — Notifications, daily report and external ledger anchoring (ADR-007 §10)
- Email (SMTP) and webhook. Events: status changes (with reasons), failed jobs, integrity quarantine, ledger
  problems. A daily report per installation: each VM's status, Proven RPO / RTO, and the **ledger head**
  (sequence and hash, signed).
- `gaia evidence verify` accepts a received daily report as an external anchor and detects ledger truncation
  (closes spec §11.1). Add conformance vectors for it.
- **Output:** report `T19`. Gate: (a) every listed event reaches a test SMTP sink and a webhook receiver in the lab,
  (b) truncating the ledger after a daily report is detected using that report.

### T20 — Operator and auditor UI (ADR-007 §7; core matrix #4)
- **Step 1, before wiring anything:** a static HTML mock-up with real lab data of (1) the status screen, (2) one
  VM's page. Give Yiğit the way to open it and **stop until he approves or changes it.** Record his decisions as
  "PO kararı" in the report.
- Status screen: every VM with status, Theoretical RPO, Proven RPO, Proven RTO, proof age, and the **reasons** in
  words. Status is never shown by colour alone (text label too). `UNKNOWN` never looks healthy.
- VM page: status timeline (`state_changes`), points, restore tests with screenshots and check results, signature
  state of each bundle.
- Auditor: read-only views and an **evidence export** (bundles, ledger, public keys, the latest daily report, the
  spec version, and how to verify offline).
- Go `html/template` + vendored htmx, no CDN, no Node toolchain; works without JavaScript for reading.
- **Output:** report `T20`. Gate: (a) Yiğit's approval of the mock-up and of the finished screens, (b) an auditor
  export verifies with `gaia evidence verify` **and** with the independent Python verifier from T13, (c) a role test
  for every page like T14's matrix.

### T21 — Migration check in the server (ADR-003, ADR-005)
- Converter container (Fedora-based, `Containerfile` in the repo, pinned by digest; virt-v2v + rhsrvany);
  virtio-win supplied by the operator, checked against a pinned sha256 (missing → `UNKNOWN`).
- `windows-basic` template: protocol-level **WinRM** probe first, RDP when enabled. UEFI boot detection.
- Migration checks as server jobs (API, CLI, UI page) with a per-VM readiness report export.
- **Output:** report `T21`. Gate: T8 and T11 pass again **through the server** (Linux and Windows, zero writes to
  ESXi), plus one UEFI guest; the WinRM check explains why it cannot pass on a broken boot.

### T22 — Hardening sweep
- Go through every report since T0 and list each known limitation that is still open; fix those that block real use
  of the MVP, and list the rest in `docs/limitations.md` with the reason they wait. Include the release build:
  versioned packages, checksums, and a signed release.
- **Output:** report `T22`. Gate: `docs/limitations.md` exists and every item in it names its reason; a signed
  release package installs from the guide.

### T23 — MVP acceptance
- Yiğit installs the release from T22 on a clean VM from the guide alone and protects at least: a 2-disk Linux VM,
  the Windows VM from T11's conversion (on KVM), and a UEFI VM, for at least 24 hours of schedules.
- Acceptance scenario (write it as a checklist Yiğit can follow): a guest made unbootable, a damaged repository
  content, the sandbox stopped, a deleted checkpoint, a lost server restored from the recovery kit — each must show
  up on the status screen and in notifications with the right status and reason.
- **Output:** report `T23` with Yiğit's answers to two questions, in his words: *"Ekrana baktığımda, o an hangi
  noktaya dönebileceğimi biliyor muyum?"* and *"Bunun çalışacağından emin miyim, ve bunu bir denetçiye kanıtlayabilir
  miyim?"* Gate: both answers yes, every scenario item shown correctly.

### Backlog (do **not** start)
Delta or binary block index (then `FIXED-256K`); OIDC; remote sandbox host; per-host SSH connection pool; RFC 3161
timestamps; vCenter, multiple datacenters, TLS verification and VMs with snapshots as migration sources; thin-disk
zeros over the network; `virt-v2v` error classification; Proxmox adapter; KubeVirt; hot migration check.

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
