# ADR-003: VMware as a migration source; platform order

Status: accepted (2026-11-14). Supersedes ADR-000 "Platforms" and ADR-002 #2 (VMware test license).

## Context

- T2 (report `2026-11-14-T2.md`): free ESXi 8.0U3e refuses every write API an image backup needs
  (`CreateVM_Task`, `ReconfigVM_Task`/CBT, `CreateSnapshot_Task`, `ExportVm`) with
  "Current license or ESXi version prohibits execution of the requested operation". Read APIs work, including
  the datastore `/folder` HTTP read. Broadcom KB 399823: the free edition offers read-only API access.
- No licensed vSphere is available to us: the Broadcom portal shows no normal ESXi 8 / vCenter downloads for our
  account, no customer can lend a non-production environment, and a TAP application is pending but unlikely.
  VMUG is "Personal Use only" and is rejected for Gaia.
- Since 2026-08-25 VDDK is no longer publicly downloadable; Broadcom routes it through TAP partners only. An
  open-source user of Gaia could not obtain it.
- The market is leaving VMware (vSphere Standard / Enterprise Plus end of sale Oct 2026; vSphere 8 general
  support ends Oct 2027). Organisations that stay on VMware will not trust production backups to a young project;
  organisations that leave need proof that their VMs will run on the new platform.

## Decisions

1. **VMware is a migration source only.** Gaia does not back up VMware for production use. It offers a
   **migration check**: inventory a vSphere environment, copy VMs, convert them for KVM, boot them in Gaia's
   isolated sandbox, run health checks, and issue a signed readiness report per VM.
2. **Read-only vSphere API, no VDDK, ever.** Inventory and disk reads use only calls the free edition allows
   (the same path Proxmox's ESXi import uses). Gaia never calls a write API against a migration source.
3. **Two stages.**
   - Stage 1, *cold check*: powered-off VMs, or copies the customer cloned. Fully testable in our free ESXi lab.
   - Stage 2, *hot check*: running VMs via snapshots. Needs a licensed environment; first done in a real customer
     POC, in the customer's environment, with written consent.
4. **Gaia proves, it does not move.** Conversion reuses virt-v2v (KVM bootability, virtio drivers). Gaia's value is
   the evidence: boot, health checks, signed report. Gaia does not reimplement a migration tool.
5. **Platform order:** (1) KVM/libvirt — MVP core; (2) Proxmox VE — adapter on the same core; (3) KubeVirt
   (Harvester, OpenShift Virtualization). VMware stays a source only.
6. **No license circumvention.** Free-ESXi shell tricks (SSH + `vim-cmd` snapshots, ghettoVCB-style) are never a
   product path. In our own lab, the host shell may be used to **create test fixtures** only.

## Revisit triggers for VMware backup

A positive TAP answer, a licensed design partner, or a short paid test campaign once the migration check is mature.

## Consequences

- Phase 1 Track A closes as `FAIL` with T2 as evidence. The Phase 3 go criterion "a viable VMware test path" is
  replaced by "migration check (cold) proven in the lab" (task T8).
- `cmd/vsphere-probe` stays as a diagnostic tool; its findings feed the migration check's pre-flight.
