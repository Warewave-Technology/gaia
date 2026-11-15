# CLAUDE.md — Gaia, Phase 2 (closing)

You are the implementation engineer for **Gaia**, a free (AGPL-3.0-or-later) end-to-end backup and
recoverability platform. Read `README.md`, every file in `docs/adr/` (ADR-004 has new
**Amendments** that win over its original text) and the reports in `docs/reports/` before you change anything.

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
   boundaries. libvirt is reached through `github.com/digitalocean/go-libvirt` (pure Go, ADR-004 §5).
3. New third-party dependencies need a one-line justification in your report. Prefer the standard library.
4. **An unchecked state is never green.** Anything not verified is UNKNOWN, never OK. Verdicts use the four-level
   model: OK / WARNING / CRITICAL / UNKNOWN, exit codes 0 / 1 / 2 / 3.
5. Every tool prints a human-readable result **and** can emit JSON. Every measurement must be reproducible from a
   script in this repo — no numbers from manual one-off commands.
6. Never commit secrets, passwords, ISOs, disk images or license keys. **VDDK is never used** (ADR-003).
7. License: AGPL-3.0-or-later, no CLA. Do not add license headers that say otherwise.
8. Keep `go.sum` committed; CI must stay green (`make check`).
9. **Never call a write API against a VMware migration source** (ADR-003 §2). In our lab only, the ESXi host shell
   may create and delete test fixtures; label such scripts `fixture` and keep them out of `cmd/` and `internal/`.
10. Throughput gates use ADR-004 A1: hash-free read of the same bytes from the same source (the faster of single-
    and multi-stream), median of ≥ 3 runs.

## Where we are

| Task | Result | Evidence |
|---|---|---|
| T0–T3 (Phase 1) | T2 FAIL → ADR-003; T3 PASS with tuning | `2026-11-14-T*.md` |
| T4 Kopia gate | Re-scored `FAIL` under ADR-004 A1 (median 2.388 vs `plain_read_s`) | `2026-11-15-T4.md` |
| T5 restore | Cause found; became ADR-004 A2 | `2026-11-15-T5.md` |
| T6 incremental | PASS | `2026-11-15-T6.md` |
| T7 verification engine | PASS; Proven RTO defined in ADR-004 A4 | `2026-11-16-T7.md` |
| T8 migration check (cold, Linux) | PASS | `2026-11-16-T8.md` |

Phase 2 closes with T9–T11. After that the architect makes the go/no-go decision for the MVP.

## Phase 2 closing tasks (do them in this order)

Each task ends with a report (see "Reporting"). Do not start the next task before the report of the current one
exists. Keep the free-space checks, cache handling and `fstrim` from earlier runners in every new runner.

### T9 — Full backup throughput through the product path (ADR-004 A1)
- Measure a **full** backup with the product path from T6 (NBD reader → Kopia Go library), not the Kopia CLI.
  Source: the 30 GiB test image exported read-only over NBD (`qemu-nbd`), or a guest disk if you prefer; say which.
- Layout: source on one virtual disk, repository on the other (`scsi2` from T5). Also run once with both on the
  same disk, as context only.
- Reference (ADR-004 A1): hash-free read of exactly the byte ranges the backup reads, from the same NBD source;
  report single-stream and multi-stream, gate against the faster.
- Allowed tuning: number of NBD readers and Kopia writers, compression `s2-default` vs `none`. Not the splitter
  (that is T10). No Rust data path.
- **Output:** report `T9`. Gate: median `full_backup / plain_read` ≤ 2 over ≥ 3 runs, every restore byte-identical.
  If it fails after tuning, name the bottleneck **with a profile** (`pprof` CPU profile of the backup), not a hypothesis.

### T10 — Block size under real incremental write patterns (ADR-004 A3)
- Splitters: `FIXED-4M`, `FIXED-1M` and the smallest fixed splitter Kopia 0.23.1 offers at or above 256 KiB
  (check the list Kopia prints; do not assume names).
- Workloads inside the nested guest, 3 incremental cycles each: (a) T6's scattered 64 KiB random writes,
  (b) large sequential writes (file copy / log append), (c) a database workload (e.g. `pgbench` on PostgreSQL).
- Per splitter × workload, medians of: repository growth per incremental, growth ÷ dirty bytes, bytes read back
  from the repository during backup, incremental backup time, restore time of the latest point (ADR-004 A2 restore).
- **Output:** report `T10` with a recommendation table. Gate: `N/A` (decision input). Do not change the default
  yourself; the architect amends ADR-004.

### T11 — Migration check stage 1: Windows guest (ADR-003)
- Yiğit approved (2026-11-16): accepting the Microsoft Evaluation Center license and downloading a Windows Server
  evaluation ISO, and using the virtio-win ISO published by the Fedora project. Record the exact versions and
  download URLs in the report. Never commit either ISO.
- Fixture: an **unattended** Windows Server install that looks like a real VMware guest (VMware virtual hardware;
  VMware Tools if practical). Installing directly on the nested ESXi via the host shell is allowed (fixture rule 9).
- Gaia flow unchanged: read-only inventory → datastore HTTP download → `virt-v2v` (with virtio-win) → `gaia verify`.
- Windows has no serial `login:`. Boot detection must use something that proves Windows is up, e.g. the guest agent
  (if `virt-v2v` installs it) or a TCP probe (RDP 3389 / WinRM) from the isolated network, plus screenshots.
- **Output:** report `T11`. Gate: (a) Windows fixture → readiness `OK` with signed evidence, (b) zero write calls
  against ESXi, (c) the report says which check proved Windows was up and why that check cannot pass on a broken boot.

### Not in this phase
Control plane, scheduler, UI, Proxmox adapter, KubeVirt, Windows in-guest (VSS) agent, hot migration check.

Product backlog collected from your reports (do **not** start; the architect will schedule it for the MVP):
NBD over a unix socket or TLS; checkpoint retention; multiple disks per VM; disk resize handling; signing-key
management and rotation; anchoring the ledger outside (RFC 3161 or similar); UEFI and Windows boot detection;
vCenter and multiple datacenters; TLS verification for vSphere; VMs with snapshots as migration sources; thin-disk
zeros over the network; telling `virt-v2v` environment errors apart from guest rejections.

## Reporting

After each task write `docs/reports/YYYY-MM-DD-<task>.md` using `docs/reports/TEMPLATE.md`, commit it, and tell
Yiğit: "Report ready: <path>". He will paste the **"Mimara özet"** section to the architect. Write reports in
Turkish, with technical terms in English. Rules:

- Status is one of `DONE`, `PARTIAL`, `BLOCKED`. Gate is `PASS`, `FAIL` or `N/A`, each with a one-line reason.
- Measurements are pasted **verbatim** (JSON), never paraphrased or rounded.
- "Taslakta yapılan düzeltmeler" lists every change to code the architect drafted. In Phase 2 the architect drafts
  no code, so use this section for changes to **existing** behaviour that a reader of earlier reports would not expect.
- If you are blocked on Yiğit (a decision, an account, a command he must run), say exactly what you need.
- Keep "Mimara özet" to 15 lines. Put `ONAY GEREKİYOR` items there too.
