# CLAUDE.md — Gaia, Phase 2

You are the implementation engineer for **Gaia**, a free (AGPL-3.0-or-later) end-to-end backup and
recoverability platform. Read `README.md`, every file in `docs/adr/` (ADR-003 and ADR-004 are new and change
earlier decisions) and the Phase 1 reports in `docs/reports/` before you change anything.

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
10. Throughput gates use ADR-004 §1: same-bytes reference, median of ≥ 3 runs.

## Where Phase 1 ended

| Track | Result | Evidence |
|---|---|---|
| T0 bootstrap | PASS | `2026-11-14-T0.md` |
| T1 lab | PASS | `2026-11-14-T1.md` |
| T2 free ESXi (Track A) | FAIL — closed by ADR-003, VMware becomes a migration source | `2026-11-14-T2.md` |
| T3 Kopia (Track B) | PASS with tuning — defaults fixed by ADR-004 §2 | `2026-11-14-T3.md` |

Update `README.md`'s Status section to say Phase 2 and to describe the migration check in one line.

## Phase 2 tasks (do them in this order)

Each task ends with a report (see "Reporting"). Do not start the next task before the report of the current one
exists. Lab capacity: `gaia-lab` disk is shared — keep the free-space check and cache handling from T3 in every
runner, and `fstrim` after each run.

### T4 — Close the Track B gate formally (small)
- Add a repeat count to `lab/lab-vm/t3-kopia.sh` and the ADR-004 §1 gate (median of ≥ 3, reference
  `sha256_full_read_s`) to `spikes/kopia/run.sh` / the runner summary. Keep the old `qemu-img` ratio as context.
- Run `s2-default/FIXED-4M` × 3.
- **Output:** report `T4`. Gate: median `full_backup / sha256_full_read` ≤ 2 and all restores byte-identical.

### T5 — Restore performance (groundwork for Proven RTO)
- Full restore was 3–5× slower than backup (T3, Yorum 5). Find the cause **with measurements**, not hypotheses.
  At least: Kopia cache on the same disk vs elsewhere (tmpfs or a second disk), restore to `/dev/null` vs to a
  file, Kopia's restore parallelism options (verify against `kopia restore --help` 0.23.1).
- If a second virtual disk for `gaia-lab` is needed, give Yiğit the exact `qm set` command.
- **Output:** report `T5`. Gate: `N/A` (investigation). Required: the dominant cause named with a measurement,
  and a recommended restore configuration with its measured median (≥ 3 runs).

### T6 — Incremental KVM backup: changed extents only (ADR-004 §4–6)
- Build `internal/nbd` (pure Go NBD client, ADR-004 §6) with tests against `qemu-nbd` in CI.
- Spike `cmd/gaia-backup` (name can change): libvirt checkpoint + pull-mode backup through go-libvirt, read dirty
  extents over NBD, write them into a Kopia repository **through the Kopia Go library** (ADR-004 §2 defaults).
  Restore produces a qcow2/raw image.
- Test VM: a small Linux guest inside `gaia-lab` (nested KVM; `gaia-lab` has `cpu: host`, the host has
  `kvm_amd nested=1`). If `/dev/kvm` is missing in `gaia-lab`, tell Yiğit what to change.
- Design the correctness check yourself, but it must compare the restored image with the disk state at the
  checkpoint (e.g. pause the guest and capture a reference), not just "it boots".
- **Output:** report `T6`. Gate: (a) incremental run reads ≈ only dirty bytes (report bytes read vs dirty bitmap
  size), (b) restored image byte-identical to the checkpoint reference, (c) restore of the incremental point works.

### T7 — Verification engine v0 (the product's core)
- `gaia verify` boots a restored image in an isolated libvirt sandbox: copy-on-write overlay (the restored image is
  never written), an isolated network with no uplink, probes run from inside that network.
- Health checks v0: boot reached (qemu-guest-agent ping or a serial-console marker), TCP port probe, one HTTP probe.
  Checks are declared per VM (YAML or JSON), templates first, user probes later (ADR-002 #4).
- Evidence (ADR-002 #3): console screenshot(s) via libvirt, check results, logs, timings (boot time and restore
  time feed Proven RTO, ADR-004 §3). Every artefact sha256-hashed; the bundle signed with ed25519; the bundle hash
  appended to an append-only hash-chain ledger. `gaia evidence verify` detects any tampering.
- **Output:** report `T7`. Gate: (a) healthy image → OK with a signed bundle, (b) deliberately broken image (e.g.
  damaged bootloader) → CRITICAL, (c) one modified byte in a bundle or ledger → `gaia evidence verify` fails,
  (d) the restored image's hash is unchanged after the test.

### T8 — Migration check, stage 1: cold (ADR-003)
- In the free ESXi lab: fixture script (host shell, lab only) creates a powered-off **Linux** guest with a real OS.
- Gaia, with **read-only** vSphere calls only: inventory → download the VM folder (vmx + disks) via the datastore
  HTTP API → `virt-v2v -i vmx` locally to qcow2 with virtio → `gaia verify` (T7) → readiness report per VM.
- Prove "read-only": wrap the vSphere client so only an allowlist of read methods can be called, and test it.
- Windows guest is a stretch goal: Microsoft Evaluation Center ISO + virtio-win. Do it only if Linux passes; report
  what blocks it if it does not work.
- **Output:** report `T8`. Gate: (a) Linux fixture → readiness OK with signed evidence, (b) a deliberately broken
  fixture → not OK, (c) zero write calls against ESXi (allowlist test + run log).

### Not in Phase 2
Control plane, scheduler, UI, Proxmox adapter, KubeVirt, Windows in-guest agent, hot migration check (stage 2).
Do not start them.

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
