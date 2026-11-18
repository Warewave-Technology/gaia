Migration readiness report: gaia-t11-win on esxi (192.168.1.29), check 5, 2026-11-18T00:55:56.508614908Z.

Readiness: OK
  ready for KVM: converted in 74 s, booted in 41 s, all 9 checks passed, 6 vSphere calls, all read-only

Steps and checks
  source_vm          OK       [datastore1] gaia-t11-win/gaia-t11-win.vmx, guest windows2019srvNext_64Guest, 2 vCPU, 4096 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t11-win
  download           OK       3 files, 34359739281 bytes in 178.0 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://microsoft.com/win/2k22 (x86_64, bios, 2 vCPU, 4096 MiB, NIC 00:50:56:3f:b1:01), virtio-win drivers in 74.5 s
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               OK       POST http://10.254.0.77:5985/wsman: unauthenticated Identify refused (401, as Windows Server's default), and the WinRM listener answered an …
  rdp                OK       10.254.0.77:3389: X.224 Connection Confirm, RDP_NEG_RSP selected protocol CredSSP (reply 03 00 00 13 0e d0 00 00 12 34 00 02 1f 08 00 02 00 …
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t11-win-20261118T005350Z-6752a4) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, e5c66f784781cbd1fa5568bd6804f7cdb54f931d75235c884e0c18299d0f1324, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t11-win-20261118T005350Z-6752a4
       python3 -I verify.py bundle bundles/gaia-t11-win-20261118T005350Z-6752a4 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 6 of ledger.jsonl names bundle_sha256 a326e842923d7b9e98a9fa2b32feeb60df6936dfbcc88bd4a3fe94e401326f1a, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
