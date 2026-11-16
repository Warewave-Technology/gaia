# CLAUDE.md — Gaia, Phase 3 (MVP), package M1

You are the implementation engineer for **Gaia**, a free (AGPL-3.0-or-later) end-to-end backup and
recoverability platform. The feasibility study ended with **GO** on 2026-11-16. Read `README.md`, every file in
`docs/adr/` (new: ADR-004 amendments A6–A8, ADR-005, ADR-006, **ADR-007 — the MVP architecture**) and the reports in
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

## Where we are

Feasibility (Phases 1–2) is closed: see `docs/reports/2026-11-14-T0.md` … `2026-11-16-T11.md`. Proven so far on
KVM in the lab: incremental backups that read only dirty extents (T6), byte-identical restores, an isolated
verification sandbox with signed evidence (T7), and read-only migration checks for Linux and Windows guests
(T8, T11). Open: Track B gate under ADR-004 A6 (T12), and everything an operator needs to use this as a product.

## Package M1 tasks (do them in this order)

Each task ends with a report (see "Reporting"). Do not start the next task before the report of the current one
exists. Keep the free-space checks, cache handling and `fstrim` in every lab runner.

### T12 — Close the Track B gate (ADR-004 A6–A8)
- Make `FIXED-1M` / 1 MiB blocks the default (A8). Keep BLAKE2B.
- Add the repository-write reference to the T9 runner (A6): hash-free sequential write of `repository_bytes` to the
  repository's disk, same run.
- Run the best T9 configuration × 3 on the two-disk layout; run the same-disk NVMe context × 3 and record
  CPU-seconds per GB as the A7 baseline.
- **Output:** report `T12`. Gate: median `full_backup / max(source read, repository write)` ≤ 2, every restore
  byte-identical. If it fails, a CPU profile names the bottleneck.

### T13 — Restore Evidence Format v1 (ADR-006)
- Write `docs/spec/evidence-v1.md` (CC0 1.0 header): bundle layout, `manifest.json` schema, canonical signed bytes,
  ed25519, ledger entries and hash chain, `key_id`, `format_version`, key rotation (old key endorses new key), and
  the verification algorithm with its verdicts. Precise enough that someone can implement a verifier **without
  reading Gaia's code**.
- Implement v1 in `internal/evidence`. Bundles already committed under `docs/reports/evidence/` (pre-spec, "v0")
  must stay verifiable: keep a read-only v0 path and say so in the spec's appendix.
- Conformance vectors in `docs/spec/vectors/`: valid bundles and ledgers, and at least one invalid vector per
  failure class (each with the expected verdict and reason). `gaia evidence verify` passes all of them in CI.
- **Output:** report `T13`. Gate: all vectors give exactly their declared verdict; a key rotation is verified end to end.

### T14 — `gaia server` skeleton (ADR-007 §1, §2, §6, §8)
- One binary, `gaia server` under systemd; SQLite (WAL) with schema migrations from empty.
- Domain: hosts, VMs, disks, repositories, policies (schedules, retention, RPO/RTO targets, maximum proof age,
  check templates), jobs, points, restore tests, evidence bundles.
- REST/JSON API with an OpenAPI file; the CLI uses the API. Local users (argon2id), roles `admin` / `operator` /
  `auditor`, append-only audit log of user actions. Daily self-backup of Gaia's state into the repository.
- An install guide (`docs/install.md`) good enough for Yiğit to install on a clean Debian without asking you.
- **Output:** report `T14`. Gate: (a) role × endpoint test matrix (every endpoint, every role), (b) migrations from
  empty and from the previous schema, (c) audit log rejects updates and deletes, (d) Yiğit installs from the guide
  on a fresh VM and reports what was unclear.

### T15 — Scheduled backups of real VMs (ADR-007 §3, §4; core matrix #1, #2)
- Host registration with an SSH key; libvirt RPC and the NBD export over that SSH connection; NBD on a unix socket.
- Multi-disk VMs; checkpoint retention on the host; a full backup (with the reason recorded) when a checkpoint is
  missing or a disk was resized.
- Post-backup check per point (manifest and block index read back, every block present, sizes match) — matrix #1.
- Scheduled repository verification: sampled daily, full weekly; failures name the affected points — matrix #2.
- Retention prunes points and runs Kopia maintenance.
- **Output:** report `T15`. Gate on the lab: a 2-disk VM under write load, backed up every 15 minutes for at least
  8 cycles: (a) sampled points restore byte-identical to paused references (T6 method), (b) injected failures —
  checkpoint deleted on the host → full backup with reason; one repository content corrupted → verification
  `CRITICAL` naming the affected points; disk resized → full backup with reason, (c) no TCP listener for NBD during
  a backup (show `ss` output).

### T16 — Restore-test scheduling and the status model (ADR-007 "status model")
- Restore tests scheduled per policy; Proven RPO, Proven RTO, proof age and status computed exactly as ADR-007's
  tables say, in **one** function with exhaustive tests; every status change stored with time and reason.
- API: status per VM with its reasons; CLI: `gaia status` table.
- **Output:** report `T16`. Gate: (a) tests cover every combination of the truth table, (b) lab timeline on one VM:
  healthy → `OK`; guest made unbootable, next backup and test → `CRITICAL` with reason; proof older than the
  maximum → `WARNING`; a newly added VM → `UNKNOWN` until its first passing test; each transition with its time.

### Package M2 (not now — the architect writes it after M1's reports)
Operator UI (status screen, evidence viewer, auditor export), notifications with the ledger head (ADR-007 §10),
converter container and WinRM template (ADR-005), UEFI boot detection, MVP acceptance by Yiğit.

### Backlog (do **not** start)
Delta or binary block index (then re-evaluate `FIXED-256K`); OIDC; remote sandbox host; RFC 3161 timestamps;
vCenter, multiple datacenters, TLS verification and VMs with snapshots as migration sources; thin-disk zeros over
the network; telling `virt-v2v` environment errors apart from guest rejections; Proxmox adapter; KubeVirt.

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
