# Known limitations of Gaia 0.1.0

Every limit below was found in a task report (`docs/reports/`, named in brackets) and is still open in release
0.1.0. Each one says **why it waits**: what it would take, and why that is not in the MVP. Limits that blocked real
use of the MVP were fixed in T22 instead (see the T22 report); limits of the lab itself (its hardware, its
fixtures) are in the reports, not here.

`scripts/check-limitations.sh` checks that every item has its reason (`make check` runs it).

## Backups of KVM VMs

- **Only running VMs are backed up.** A backup of a powered-off VM fails with `... is not running`. [T15]
  *Why it waits:* Gaia uses libvirt's pull-mode backup, which needs a running domain. A powered-off VM needs a second
  data path (reading the disk files on the host over SSH) with its own consistency rules; it is not in the MVP scope.
- **Backups are crash-consistent.** The guest is neither paused nor quiesced, so a backup is like a power cut; restore
  tests prove such a copy boots and passes its checks. [T7]
  *Why it waits:* quiescing needs the QEMU guest agent and freeze hooks in every guest, which Gaia does not install.
- **The backup's NBD socket lives in libvirt's directory of the VM** (`/var/lib/libvirt/qemu/domain-<id>-<name>/`), a
  workaround for libvirt 9.0 not adding the socket to the VM's AppArmor profile. A libvirt that names that directory
  differently breaks backups until Gaia follows it. Tested on Debian 12 (libvirt 9.0) hosts only. [T15]
  *Why it waits:* the alternative is a local AppArmor rule on every host, which is host configuration Gaia would have
  to own; the upstream fix is libvirt's.
- **The host user is in the groups `libvirt` and `libvirt-qemu`, which is root-equivalent on the host**, and OpenSSH
  cannot limit its socket forwarding to one path. [T15]
  *Why it waits:* a narrower access needs a helper on each host behind a forced command: new software on the
  protected hosts, outside the MVP.
- **A stop (an upgrade, a reboot) waits for running jobs up to 10 minutes** (`stop_drain`); a longer job, such as a
  first full backup of a large VM, is ended, recorded as failed and run again from its start after the stop. An
  install from before 0.1.0 does not wait at all (install.md step 12). [T18, T20, T22]
  *Why it waits:* resuming a backup half way needs Kopia to keep a partial point across processes; a longer
  `stop_drain` is possible (up to 1 hour, with `TimeoutStopSec` raised to match) but holds the upgrade.
- **One SSH connection per job** (ADR-007 B2); two parallel backups of one host open two. [T15]
  *Why it waits:* a per-host connection pool is in the backlog (CLAUDE.md).
- **Every point's block index lists the whole disk** (about 40 MiB of index per point for a 1 TiB disk at the default
  1 MiB blocks, about 155 MiB at 256 KiB). This is why the default block size is 1 MiB, not 256 KiB. [T10, T12]
  *Why it waits:* a delta or binary block index is in the backlog (CLAUDE.md); `FIXED-256K` is re-evaluated after it.
- **The block size and the hash are fixed when a repository is created.** Repositories made before T12 stay
  `FIXED-4M`. [T10]
  *Why it waits:* they are part of Kopia's repository format; changing them means a new repository.
- **The check after each backup reads the point's manifest and block index, not its data.** Data is read by the daily
  sampled and the weekly full verification. [T15]
  *Why it waits:* reading every backed-up byte again after each backup doubles the I/O; ADR-004 puts data reads in
  the verifications.
- **Space of pruned points comes back at Kopia's full maintenance, at least 24 hours later**, and this was not yet
  shown on a long run. [T15]
  *Why it waits:* the 24-hour delay is Kopia's safety margin against deleting data a running backup still uses; the
  first run long enough to show it is the 24-hour MVP acceptance (T23).
- **Garbage collection keeps the IDs of all reachable contents in memory** (about 100 MB per TB of repository). [T15]
  *Why it waits:* that fits the MVP's sizes; a disk-backed set is needed for repositories of many TB.
- **Throughput was measured on the lab's hardware only** (4 vCPU, SATA SSD repository). The ADR-004 gate passed there
  with a two-disk layout; one disk for source and repository did not (CPU-bound). [T9, T12]
  *Why it waits:* other hardware is not in the lab; the gate method (ADR-004 A6, A7) is in the repository to measure
  it again.

## Repositories on S3

- **S3 was tested only against MinIO on the same machine**, with no network latency and no real cloud region. [T18]
  *Why it waits:* the lab has no remote S3 endpoint; the MVP acceptance (T23) decides which S3 to use.
- **A pack the storage does not return after 10 tries, while it returns other data, counts as damage** and is
  quarantined; a long outage of one object can therefore look like damage. A full verification of a damaged pack is
  slow (about 35 s per read). [T18]
  *Why it waits:* it is the reading of ADR-007 B3 raised for approval (ONAY) in T18; fewer retries would make short
  outages look like damage more often.
- **The S3 secret key is also in Kopia's client configuration** under `/var/lib/gaia/kopia`, not only in Gaia's
  `secrets/` (both readable only by the user `gaia`). [T18]
  *Why it waits:* Kopia's connect writes it there; avoiding it needs changes in how Kopia is opened.
- **MinIO publishes no binaries any more**; Gaia's tests build it from source at a pinned commit. [T18]
  *Why it waits:* only the test setup depends on it; Gaia itself speaks S3 to any compatible storage.

## Restore tests and the sandbox

- **Migration checks run one at a time** in the server (restore tests run in parallel slots since T25). [T21, T25]
  *Why it waits:* a check runs for many minutes in the converter and is started by an operator, not a schedule; no
  fleet size in M3 needs two at once.
- **The sandbox runs on the Gaia server itself**; one server's sandbox tests at most 8 VMs at once. [T25]
  *Why it waits:* a remote sandbox host is the step after M3 (ADR-007 C4). `GET /api/v1/views/capacity` says how
  many slots a fleet needs; the T24 catalog (5,000 VMs) needs 17.
- **Parallel tests share one disk**: each test writes its restored image and reads it in full twice (its
  sha256 before and after the boot), so tests in several slots slow each other down on one disk (T25 run 3: removing the
  sandbox and hashing a 4 GiB image again took 36 s beside another slot's restore, 4 s alone). [T25]
  *Why it waits:* the full-image hash is ADR-002's proof that a test did not change the point; the sandbox images
  directory (`sandbox_images_dir`) can be put on a disk of its own.
- **The boot timeout is 300 s and the same for every VM** (a broken VM holds the sandbox that long). [T16]
  *Why it waits:* a per-policy `boot_timeout_s` needs a schema change and a UI field; nothing in the MVP needs it.
- **The sandbox's network card may sit at another PCI address than on the source host**, so a guest that names its
  interface by slot can come up without network and fail its network checks. [T16]
  *Why it waits:* copying the PCI addresses from the source's definition was proposed in T16 and is not built; no
  MVP guest needed it.
- **Only the newest checked point is tested**; older points are never tested again. [T16]
  *Why it waits:* ADR-007 tests the point you would restore first; testing older points costs sandbox time and is a
  policy option for later.
- **A restore test does not check free space first**; a full disk makes the test `UNKNOWN` with the error. [T16]
  *Why it waits:* the result is already honest (`UNKNOWN`, never `OK`); a check up front only changes the message.
- **The sandbox's isolation is checked by its configuration** (no forward, no route, its own namespace), not by a
  guest trying to reach out. [T7]
  *Why it waits:* an active egress probe needs a test guest that tries to reach out; it is not built.
- **A restore test or migration check proves that the guest boots and answers** (SSH, HTTP, WinRM, RDP, a port), not
  that its applications work or that a user can log in. The WinRM check proves Windows answers WinRM; it runs no
  command. [T11, T21]
  *Why it waits:* application checks need probes the operator defines per VM (and credentials for them); they are
  not in the MVP.
- **A policy's `test_interval_s` may not be longer than its `max_proof_age_s`**, and tests cannot be paused. [T16]
  *Why it waits:* the rule keeps a schedule from making its own proofs too old; whether it stays is an open question
  to the architect (T16).

## Status, evidence and audit

- **A failed point keeps its VM `CRITICAL` until it is pruned** (ADR-007 B4.3), even when newer points are proven.
  Since T22 an admin can remove it with a reason (`gaia points forget`), but only from the command line or the API:
  the screens have no button for it. [T19, T22]
  *Why it waits:* whether B4.3 should instead count an old failed point as `WARNING` is the architect's decision
  (ONAY since T19); a button follows that decision.
- **External anchoring of the ledger reaches the newest daily report**: entries after it can be cut off unseen until
  the next report. The server's own check uses its database's copy of the head, which catches only accidents. The
  head's time is the server's clock; a ledger shown with two different heads has no finding code of its own. [T19]
  *Why it waits:* stronger anchoring is RFC 3161 timestamps, in the backlog (CLAUDE.md).
- **The evidence key cannot be rotated from the server**, and a rotation is not a revocation: a stolen key can sign
  until the ledger rotates away from it. [T13, T19]
  *Why it waits:* `gaia evidence rotate` exists as a local tool; a server command needs the operator procedure for
  keeping the old key's genesis anchor, not written yet.
- **The auditor's export is not signed as a whole**: its bundles and ledger are, but `restore-tests.json` is an
  unsigned summary, and `anchor.pub` comes from inside the export, so the auditor must compare its key ID with one
  received elsewhere (the export's README says so). [T20]
  *Why it waits:* whether to sign the export itself is an open question to the architect (T20).
- **The record of an export given while the ledger could not record it waits in a file**
  (`<data_dir>/evidence/pending-exports/`, ADR-007 C18.5) until the next successful ledger write; it is not in Gaia's
  daily state backup, so a server lost before then loses the record (the audit log entry and the notification stay
  where they were sent). [T29]
  *Why it waits:* the state backup's members are fixed by T17's rebuild procedure; adding one needs that procedure
  and its drill updated.
- **Reading is not audited**; only changes and exports are. [T14]
  *Why it waits:* ADR-007 asks for an audit log of actions; logging every read needs its own retention policy.
- **`gaia status`'s exit code follows the VMs only**: warnings about the recovery kit, the ledger or notifications
  print but do not change it. [T17, T19]
  *Why it waits:* monitoring scripts read the exit code as the VMs' worst status; a separate code for the
  installation's own health is a design decision not taken yet.
- **The API returns at most 1000 items per list**; longer lists are read page by page with `after=`. [T18]
  *Why it waits:* it is by design, to bound responses; the screens and the command line page.

## Sign-in (ADR-008)

- **No SAML, no SCIM provisioning, one identity provider only, no client certificates, no risk-based sign-in.** [T26]
  *Why it waits:* ADR-008 leaves them out ("Not in this ADR"); OIDC covers the IdPs the lab and the PO use.
- **A security key needs a DNS name and a trusted certificate**: opened by an IP address, or past a certificate
  warning, browsers refuse WebAuthn, so an admin cannot sign in that way. [T26]
  *Why it waits:* it is the browsers' rule; Gaia's certificate is its own (T22), and a certificate from a CA or ACME
  is a deployment choice the PO has not made.
- **A terminal cannot use a security key**: an admin signs the command line in, and approves its destructive
  commands, in a browser. [T26]
  *Why it waits:* talking to a key from Go needs a native HID library (cgo), which ADR-001 rules out.
- **The authenticator app's key is shown as text and an `otpauth://` link, without a QR code.** [T26]
  *Why it waits:* a QR encoder is a new dependency for the UI; the v2 screens (T27) decide.
- **Sign-ins under way, step-ups and command-line approvals are kept in memory**: a restart in the middle of one
  asks the user to start again. [T26]
  *Why it waits:* they last 5 minutes at most; storing them would add short-lived secrets to the database.
- **The step-up list has two actions without an API yet**: restore over a running VM and signing-key rotation. [T26]
  *Why it waits:* neither exists in M3 (restore over a VM is not built; key rotation is a local tool, above); the rule
  is declared per route and holds when they come.
- **Delays after failed sign-ins count per address**, so people behind one NAT address slow each other down after
  someone's failures. [T26]
  *Why it waits:* ADR-008 §7 asks for per-address counting; an allow-list of trusted addresses is a decision of its own.
- **Passwords set before 0.2.0 that are shorter than 15 characters keep working** until they are changed. [T26]
  *Why it waits:* ADR-008 §6 checks a password when it is set; forcing a change at the upgrade needs the architect's
  decision (raised in T26).
- **TOTP seeds and the IdP's refresh tokens are in the database in clear** (mode 0600, like the password hashes). [T26]
  *Why it waits:* both must be read to be used; a key to encrypt them would live on the same disk.

## Losing and rebuilding a server

- **What the lost server did after its last state backup is not restored, only listed** (points and bundles by name),
  and such points are neither used nor pruned. A state backup runs a few minutes after jobs end, so this is minutes
  of work. [T17]
  *Why it waits:* importing them needs a way to trust data the catalog never recorded; it is a possible next step.
- **Notification secrets (SMTP passwords, webhook secrets) and vSphere source passwords are not in the recovery
  kit**; after a rebuild the channels and sources must be added again (install.md step 13). [T19, T21]
  *Why it waits:* the kit is written with the kit passphrase, so putting them in would ask for that passphrase at
  every channel or source change; raised for approval (ONAY) in T19 and T21.
- **"Confirmed" on the recovery kit means someone opened that kit with its passphrase** (`gaia kit save`), not that
  a copy is stored off the server. [T17]
  *Why it waits:* a server cannot prove where a file is kept; the guide makes storing it off the server a checked
  step instead.

## Migration checks (VMware to KVM)

- **Stage 1 only**: powered-off VMs with one disk and no snapshots, on an ESXi host or one datacenter; no vCenter
  with several datacenters, no running VMs. [T8, T21]
  *Why it waits:* vCenter, several datacenters, VMs with snapshots and hot checks are in the backlog (CLAUDE.md).
- **A vSphere endpoint's certificate is checked against the server's CAs only**; a self-signed ESXi needs
  `insecure=true`. [T8, T21]
  *Why it waits:* TLS verification options for sources (a CA file, a pinned certificate) are in the backlog.
- **Thin disks are read whole, zeros included** (34 GB downloaded for about 10 GB of Windows data). [T8, T21]
  *Why it waits:* free ESXi gives no allocation map over its read-only APIs; this is in the backlog (CLAUDE.md).
- **A failing `virt-v2v` is `CRITICAL`** whether the guest or the converter's environment caused it (a missing
  virtio-win is `UNKNOWN`). [T8, T21]
  *Why it waits:* classifying `virt-v2v`'s errors is in the backlog (CLAUDE.md).
- **The converter image is built on each server** from the package's `Containerfile` and pinned by its local image
  ID; two servers may build different IDs, and no published image digest exists to compare with. [T21]
  *Why it waits:* pinning by a published digest needs a registry to publish the image in (about 2 GB), which has not
  been chosen; raised for approval (ONAY) in T21.
- **The converter service cannot use `NoNewPrivileges`**, and its capability bounding set holds what `newuidmap`
  needs (`SETUID`, `SETGID`, `DAC_OVERRIDE`, `SYS_ADMIN`); the process itself has no capabilities. [T21]
  *Why it waits:* rootless podman maps its user namespace through the setuid `newuidmap`; the alternative is a root
  converter, which ADR-007 B1 rules out.
- **Secure Boot is recorded, not checked**: a UEFI guest boots with OVMF without Secure Boot. [T21]
  *Why it waits:* checking it needs OVMF's Secure Boot variables and enrolled keys in the sandbox; no MVP guest
  needs it.
- **A Windows Server 2022 converted with virtio-win 0.1.302 restarts on every boot**: virt-v2v's first-boot script
  cannot install the `smbus` driver ("a root certificate which is not trusted") and retries it on the next boot, for
  ever (T28: 17 boots in 8 minutes). The check says `CRITICAL` `driver_install_loop` with the driver's name; checked
  again with `exclude_drivers ["smbus"]` it is `WARNING` at best. Gaia does not change the converted disk itself.
  [T23, T28]
  *Why it waits:* 0.1.302 is the newest virtio-win release (read on 2026-11-23); a release whose `smbus` installs
  on Server 2022 would be measured and pinned (ADR-005 C step 1).
- **Windows guests need virtio-win 0.1.302, downloaded by the operator**; without it a Windows check is `UNKNOWN`.
  [T11, T21]
  *Why it waits:* ADR-005 has the operator supply it (Gaia does not redistribute it); its sha256 is pinned.
- **One migration check at a time**; one stopped by a restart of the server is `UNKNOWN` and is not run again. [T21]
  *Why it waits:* a check takes minutes to an hour of sandbox and converter time; rerunning is the operator's
  decision.
- **A migration check's result sends no notification**; only a failed job does. [T21]
  *Why it waits:* whether it should be an event is an open question to the architect (ADR-007 §10, T21).

## Platforms and scope

- **No backups of VMware VMs**: VMware is a read-only migration source. [T2]
  *Why it waits:* ADR-003; free ESXi refuses the backup APIs (T2).
- **Servers and hosts were tested on Debian 12 amd64 only**, and the package is for Debian 12 amd64. The command
  line runs on Linux and macOS (amd64 and arm64). [T14–T22]
  *Why it waits:* the lab and the MVP acceptance use Debian 12; other distributions need their own tests.
- **No Proxmox or KubeVirt hosts, no OIDC login, no remote sandbox host.** [CLAUDE.md backlog]
  *Why it waits:* they are in the backlog, after the MVP.
- **Reading the screens needs JavaScript**; without it they point to the command line and the export. [T20]
  *Why it waits:* the screens are a React application (PO decision 2026-11-17, raised for approval against ADR-007 §7
  in T20).
- **The overview lists every VM on one page**, with no search, filter or grouping, and the server builds it with a
  few queries per VM. [T23]
  *Why it waits:* the MVP protects a handful of VMs; the PO asked for screens that stay clear with thousands (T23,
  raised for approval), which is a design of its own.
- **The screens were reviewed in Chromium and Safari only.** [T20]
  *Why it waits:* other browsers were not in the review; nothing browser-specific is used.

## Releases

- **Releases are files to download; there is no apt repository**, so upgrades are manual (install.md step 12), and
  the release page is published by hand. [T22]
  *Why it waits:* an apt repository needs hosting and a repository signing key; the product owner decides where.
- **The release key is one SSH key held by the product owner.** There is no second signer and no revocation list.
  [T22]
  *Why it waits:* PO decision 2026-11-18; a key ceremony with more holders is for when there are more maintainers.
- **The macOS binaries are not notarized by Apple**, so a downloaded `gaia` must be released from quarantine
  (install.md, Troubleshooting). [T22]
  *Why it waits:* notarization needs an Apple Developer ID.
