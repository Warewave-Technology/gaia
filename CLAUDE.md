# CLAUDE.md — Gaia, Phase 1

You are the implementation engineer for **Gaia**, a free (AGPL-3.0-or-later) end-to-end backup and
recoverability platform. Read `README.md` and every file in `docs/adr/` before you change anything.

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
2. Go is the main language (ADR-001). No cgo, no FFI. Other languages/tools only across process or network boundaries.
3. New third-party dependencies need a one-line justification in your report. Prefer the standard library.
4. **An unchecked state is never green.** Anything not verified is UNKNOWN, never OK. Verdicts use the four-level
   model: OK / WARNING / CRITICAL / UNKNOWN, exit codes 0 / 1 / 2 / 3.
5. Every tool prints a human-readable result **and** can emit JSON. Every measurement must be reproducible from a
   script in this repo — no numbers from manual one-off commands.
6. Never commit secrets, passwords, ISOs, disk images or VDDK. VDDK is never bundled (ADR-002).
7. License: AGPL-3.0-or-later, no CLA. Do not add license headers that say otherwise.
8. Keep `go.sum` committed once generated; CI must stay green (`make check`).

## State of the code you are inheriting

The architect wrote these drafts in an environment **without access to the Go module proxy or the lab**.
Treat everything marked "not verified" as a draft that probably contains mistakes. Fix them, and **record every
fix** in your report (file, problem, fix, why).

| Path | What | State |
|---|---|---|
| `cmd/imgtool` | test-image gen / mutate / hash / cmp (stdlib) | built and unit-tested |
| `spikes/kopia/run.sh` | Track B spike | flow tested with mocks only; real Kopia CLI flags **not verified** |
| `scripts/make-test-image.sh` | realistic ext4 test image | **not run** |
| `internal/vsphereprobe`, `cmd/vsphere-probe` | Track A probe (govmomi) | **never compiled** (no module access) |
| `lab/esxi/*`, `lab/proxmox/*`, `lab/lab-vm/*` | lab automation | **never run** |
| `.github/workflows/ci.yml` | CI | **never run** |

## Phase 1 tasks (do them in this order)

Each task ends with a report (see "Reporting"). Do not start the next task before the report of the current one exists.

### T0 — Bootstrap the repository
- `go mod tidy`, commit `go.sum`. Make `make check` pass locally: fix compile errors in `vsphere-probe` first.
- `TestAgainstSimulator` (vcsim) must pass. If vcsim lacks a call, adjust the **test's expectations**, not the probe's logic.
- Run `shellcheck` on every script, fix findings. Get CI green on GitHub.
- **Output:** report `T0`. Gate: `make check` and CI green.

### T1 — Track A lab: nested ESXi + lab VM on Proxmox
- Lab placement decision (architect): both VMs on Yiğit's existing Proxmox host, beside the House lab, powered on
  only during tests. `gaia-esxi` 4 vCPU / 16 GiB / 200 GiB, `gaia-lab` 4 vCPU / 8 GiB / 200 GiB.
- Yiğit already has the free ESXi 8.0U3e ISO. Walk him through `lab/esxi/remaster-iso.sh`, `lab/proxmox/create-vms.sh`
  and `lab/lab-vm/setup.sh`. The ESXi install must be **unattended**; fix the scripts until it is.
- **Output:** report `T1` with: ESXi build, host IP reachability, `govc about` output, lab VM tool versions.
  Gate: ESXi installed without keyboard input, `govc about` works from `gaia-lab`.

### T2 — Track A gate: what does free ESXi allow?
- Run `vsphere-probe -insecure -json` from `gaia-lab` against the nested free ESXi.
- If a step fails, capture the **exact fault text**. Determine whether it is a license restriction or a bug in the
  probe (fix bugs, rerun). Do not guess about licensing — cite Broadcom documentation or KB articles by URL.
- **Output:** report `T2` with the full probe JSON. Gate: probe verdict (OK / WARNING / CRITICAL) and, if not OK,
  the evidence-based answer to "which path do we need: evaluation lab or VMUG?".

### T3 — Track B gate: Kopia as the repository for disk images
- On `gaia-lab`: `make build`, `scripts/make-test-image.sh` with `SIZE_GB=30`, then `spikes/kopia/run.sh`
  twice: `COMPRESSION=zstd` and `COMPRESSION=none`. Verify the real Kopia CLI flags (`repository create filesystem`,
  `policy set --compression`, `snapshot list --json`, `restore`) against `kopia --help` of the installed version.
- **Output:** report `T3` with both `report.json` files verbatim. Gate (ADR-001): both restores byte-identical and
  `full_backup <= 2 × qemu-img convert` time. If the speed gate fails, try at most Kopia tuning (splitter,
  compression, parallelism) and report what moved the number; do **not** start a Rust data path.

### Not in Phase 1
Track C (Bareos/PBS/Veeam CE comparison), the Gaia control plane, UI, KubeVirt, Windows agent. Do not start them.

## Reporting

After each task write `docs/reports/YYYY-MM-DD-<task>.md` using `docs/reports/TEMPLATE.md`, commit it, and tell
Yiğit: "Report ready: <path>". He will paste the **"Mimara özet"** section to the architect. Write reports in
Turkish, with technical terms in English. Rules:

- Status is one of `DONE`, `PARTIAL`, `BLOCKED`. Gate is `PASS`, `FAIL` or `N/A`, each with a one-line reason.
- Measurements are pasted **verbatim** (JSON), never paraphrased or rounded.
- "Taslakta yapılan düzeltmeler" lists **every** change to the inherited drafts, even one-character fixes.
- If you are blocked on Yiğit (a decision, an account, a command he must run), say exactly what you need.
