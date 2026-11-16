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
