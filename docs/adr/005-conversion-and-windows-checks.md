# ADR-005: Conversion runtime and Windows checks for the migration check

Status: accepted (2026-11-16). Extends ADR-003. Evidence: report `2026-11-16-T11.md`.

## 1. Conversion runs in a pinned converter container

- virt-v2v converts Windows guests only with `rhsrvany.exe`, which Debian does not package (T11).
- Gaia runs every conversion in a **converter container image**: Fedora-based, pinned by digest, containing
  virt-v2v and `rhsrvany`. Gaia talks to it only as a separate process (ADR-001 boundary rule), with podman or
  docker on the Gaia host.
- The image is built reproducibly from a `Containerfile` in this repository; the digest Gaia uses is recorded in
  every migration-check bundle.

## 2. virtio-win is supplied by the operator

Gaia does not redistribute the virtio-win drivers until their redistribution terms are verified. The operator
downloads the ISO from the URL documented by Gaia; Gaia checks it against a pinned sha256 and records the version
in the bundle. A missing or mismatching ISO makes Windows conversions `UNKNOWN`, never `OK`.

## 3. Windows boot detection: WinRM first

- Windows Server enables WinRM and disables Remote Desktop by default (T11, measured from the fixture's registry).
- The `windows-basic` check template proves boot with a **protocol-level WinRM probe** from the isolated network
  (a response only Windows' WinRM / HTTP.sys stack gives, with the reply bytes stored in the bundle).
- The RDP handshake probe from T11 stays as an additional check, used when RDP is enabled.
- Both stay subject to T11's rule: the report must say why the check cannot pass on a broken boot.

## Amendments (2026-11-20, after M2)

### A. WinRM proof (amends §3; T21 item 1)

The protocol-level proof is **either** a WSMan Identify response **or**, when Identify is refused, the NTLM
CHALLENGE that Windows' own SSPI returns to an anonymous NEGOTIATE (computer name and Windows build, reply bytes
in the bundle). The report and the bundle state what it proves: a running Windows with a WinRM listener, not that
WinRM can run commands.

### B. Stability window (T23 item 3)

`windows-basic` waits `settle_s` (default 60 s) after the first passing probe and probes again; both must pass.
A guest that reboots inside the window fails with `guest_rebooted`. T21's Windows result is to be re-run with
the window (task T28).

### C. Drivers that cannot be installed (T23 item 4)

A converted guest that loops on a failing first-boot driver install is `CRITICAL` with `driver_install_loop` and
the driver's name. Decision:
1. First, measure whether a newer virtio-win release installs the `smbus` driver on Server 2022; if it does, pin
   that release (sha256) and record it.
2. Only if it does not: a per-check option `exclude_drivers` (a list of driver names). An excluded driver is
   recorded in the bundle and in the readiness report as a warning, so the result can be `WARNING`, never `OK`.

### D. Image pinning (amends §1; T21 item 3)

Accepted for now: the base image is pinned by digest and packages come from the frozen release repository; the
built image is pinned by its local ID, recorded in every bundle. Publishing the built image to a registry and
pinning by digest follows the release-location decision (ADR-007 C10).

### E. Source passwords (T21 item 4)

Not stored in the recovery kit. After `restore-state`, migration sources show `UNKNOWN: credentials missing`
until they are added again.
