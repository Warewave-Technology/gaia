Migration readiness report: gaia-t21-win-broken on esxi (192.168.1.29), check 6, 2026-11-23T12:41:06.138085226Z.

Readiness: CRITICAL
  boot critical: the guest took no DHCP lease within the 300 s boot timeout. WinRM answers only from a running Windows: the WinRM service listens on 5985 through HTTP.sys once Windows has started its network stack and taken a DHCP lease, so a guest stopped in firmware, the boot loader, a stop error or the Recovery Environment takes no lease or never answers on 5985

Steps and checks
  source_vm          OK       [datastore1] gaia-t21-win-broken/gaia-t21-win-broken.vmx, guest windows2019srvNext_64Guest, 2 vCPU, 4096 MiB, firmware bios, 1 disk(s), 1 NI…
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t21-win-broken
  download           OK       3 files, 34359739302 bytes in 187.0 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://microsoft.com/win/2k22 (x86_64, bios, 2 vCPU, 4096 MiB, NIC 00:50:56:3f:b1:02), virtio-win drivers in 72.7 s
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               CRITICAL the guest took no DHCP lease within the 300 s boot timeout. WinRM answers only from a running Windows: the WinRM service listens on 5985 thr…
  rdp                UNKNOWN  not run: the guest did not boot
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t21-win-broken-20261123T123513Z-845523) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, d859a0105b974b2e9c00950b750468b5f5e98c487d3dc2bc33a1345c25848556, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t21-win-broken-20261123T123513Z-845523
       python3 -I verify.py bundle bundles/gaia-t21-win-broken-20261123T123513Z-845523 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 7 of ledger.jsonl names bundle_sha256 0cf0bbac2fb0efbc35f5891ab5e138201860235da8973cfdb62225083e7e3f72, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
