Migration readiness report: gaia-t11-win on esxi (192.168.1.29), check 7, 2026-11-23T12:50:00.360193253Z.

Readiness: WARNING
  drivers_excluded warning: converted without virtio-win's smbus driver(s), as the check asked (exclude_drivers): the guest neither has nor installs them; left out of virtio-win: smbus, restarted 4 time(s) on its first boot (at 55 s, 84 s, 129 s, 161 s) before it stayed up

Steps and checks
  source_vm          OK       [datastore1] gaia-t11-win/gaia-t11-win.vmx, guest windows2019srvNext_64Guest, 2 vCPU, 4096 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t11-win
  download           OK       3 files, 34359739281 bytes in 152.2 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://microsoft.com/win/2k22 (x86_64, bios, 2 vCPU, 4096 MiB, NIC 00:50:56:3f:b1:01), virtio-win drivers in 70.8 s
  drivers_excluded   WARNING  converted without virtio-win's smbus driver(s), as the check asked (exclude_drivers): the guest neither has nor installs them; left out of v…
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               OK       POST http://10.254.0.10:5985/wsman: unauthenticated Identify refused (401, as Windows Server's default), and the WinRM listener answered an …
  rdp                OK       10.254.0.10:3389: X.224 Connection Confirm, RDP_NEG_RSP selected protocol CredSSP (reply 03 00 00 13 0e d0 00 00 12 34 00 02 1f 08 00 02 00 …
  stable             OK       boot kept answering for 1m0s after every check passed (13 probes, one every 5s); before that the guest restarted 4 time(s) on its first boot…
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t11-win-20261123T124528Z-63aed7) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, d859a0105b974b2e9c00950b750468b5f5e98c487d3dc2bc33a1345c25848556, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t11-win-20261123T124528Z-63aed7
       python3 -I verify.py bundle bundles/gaia-t11-win-20261123T124528Z-63aed7 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 8 of ledger.jsonl names bundle_sha256 c86ec91813626cfca5c876262d26d8208be3c15b064e7f3be8b04b7cf81e63cf, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
