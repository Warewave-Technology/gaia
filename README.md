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

Phase 2: building the data path and verification core on KVM/libvirt. Phase 1 (feasibility) is closed; its
reports are in [`docs/reports/`](docs/reports). Nothing here is production software yet.

VMware is a migration source only ([ADR-003](docs/adr/003-vmware-as-migration-source-and-platform-order.md)): the
**migration check** copies powered-off vSphere VMs with read-only API calls, converts them for KVM, boots them in an
isolated sandbox and issues a signed readiness report per VM.

| Path | What it is |
|---|---|
| [`docs/adr/`](docs/adr) | Architecture decision records |
| [`CLAUDE.md`](CLAUDE.md) | Phase 2 tasks, rules and reporting format for the implementation agent |
| [`docs/reports/`](docs/reports) | Task reports (template: `TEMPLATE.md`) |
| [`lab/`](lab) | Fully automated lab: nested ESXi on Proxmox, Debian lab runner VM |
| [`cmd/vsphere-probe`](cmd/vsphere-probe) | Diagnostic: checks which backup APIs a vSphere host allows (feeds the migration check's pre-flight) |
| [`cmd/imgtool`](cmd/imgtool) | Test-image generation, mutation and verification (stdlib only) |
| [`spikes/kopia`](spikes/kopia) | Track B: Kopia as the repository for VM disk images |

## Freedom

Gaia is free software under the [GNU AGPL v3 or later](LICENSE). There is **no CLA**: every contributor keeps
their copyright, so nobody — including the original authors — can ever relicense Gaia as proprietary software.
We are not a vendor.
