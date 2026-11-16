# Gaia

Gaia is a free (AGPL-3.0-or-later) end-to-end backup and **recoverability** platform.

Most backup products tell you a job *succeeded*. Gaia's job is to tell the operator, at any moment,
**which restore point they can return to and prove that it will work**:

| # | Question Gaia answers continuously | Evidence |
|---|---|---|
| 1 | Was the backup really taken? | Integrity record per restore point |
| 2 | Is the backup chain intact? | Chain verification result + time |
| 3 | Was a restore actually tested? | Isolated boot + app-level health checks, verdict |
| 4 | Does the operator know? | Theoretical RPO vs **Proven RPO**, side by side |
| 5 | Can it be proven to an auditor? | Signed, timestamped, append-only evidence |

Design rule: **an unchecked state is never green.** Unknown is shown as UNKNOWN and never counts as success.

## Status

Phase 3: building the MVP on KVM/libvirt ([ADR-007](docs/adr/007-mvp-architecture.md)). The feasibility study
(Phases 1–2) ended with GO on 2026-11-16; its reports are in [`docs/reports/`](docs/reports). Not released yet.

VMware is a migration source only ([ADR-003](docs/adr/003-vmware-as-migration-source-and-platform-order.md)): the
**migration check** copies powered-off vSphere VMs with read-only API calls, converts them for KVM, boots them in an
isolated sandbox and issues a signed readiness report per VM.

| Path | What it is |
|---|---|
| [`docs/adr/`](docs/adr) | Architecture decision records |
| [`docs/spec/evidence-v1.md`](docs/spec/evidence-v1.md) | Restore Evidence Format v1: the open, CC0-licensed specification of Gaia's signed evidence bundles and ledger, with conformance vectors ([ADR-006](docs/adr/006-open-evidence-specification.md)) |
| [`CLAUDE.md`](CLAUDE.md) | Current tasks, rules and reporting format for the implementation agent |
| [`docs/reports/`](docs/reports) | Task reports (template: `TEMPLATE.md`) |
| [`lab/`](lab) | Fully automated lab: nested ESXi on Proxmox, Debian lab runner VM |
| [`cmd/vsphere-probe`](cmd/vsphere-probe) | Diagnostic: checks which backup APIs a vSphere host allows (feeds the migration check's pre-flight) |
| [`cmd/imgtool`](cmd/imgtool) | Test-image generation, mutation and verification (stdlib only) |
| [`cmd/gaia-backup`](cmd/gaia-backup) | Spike (T6): incremental KVM backup — libvirt checkpoint + pull-mode backup, changed extents over NBD, into Kopia; restore to a raw image |
| [`internal/nbd`](internal/nbd) | Minimal pure-Go NBD client (read, block status, dirty bitmaps), tested against `qemu-nbd` |
| [`cmd/gaia`](cmd/gaia) | Verification engine v0 (T7): `gaia verify` boots a restored image in an isolated sandbox and runs its checks; `gaia evidence seal`, `verify` and `rotate` implement the evidence format |
| [`spikes/kopia`](spikes/kopia) | Track B: Kopia as the repository for VM disk images |

## Freedom

Gaia is free software under the [GNU AGPL v3 or later](LICENSE). There is **no CLA**: every contributor keeps
their copyright, so nobody — including the original authors — can ever relicense Gaia as proprietary software.
We are not a vendor.
