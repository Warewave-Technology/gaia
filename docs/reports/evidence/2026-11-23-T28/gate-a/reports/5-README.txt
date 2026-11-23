Migration readiness report: gaia-t11-win on esxi (192.168.1.29), check 5, 2026-11-23T11:06:58.051881463Z.

Readiness: CRITICAL
  drivers critical: Windows restarts on every boot: virt-v2v's first-boot service cannot install virtio-win's smbus driver and retries it on the next boot: smbus failed to install on 3 of 3 boots (Adding the driver package failed : A certificate chain processed, but terminated in a root certificate which is not trusted by the trust provider.). Check again with exclude_drivers ["smbus"] if the VM does not need it

Steps and checks
  source_vm          OK       [datastore1] gaia-t11-win/gaia-t11-win.vmx, guest windows2019srvNext_64Guest, 2 vCPU, 4096 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t11-win
  download           OK       3 files, 34359739281 bytes in 144.4 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://microsoft.com/win/2k22 (x86_64, bios, 2 vCPU, 4096 MiB, NIC 00:50:56:3f:b1:01), virtio-win drivers in 71.1 s
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               OK       POST http://10.254.0.10:5985/wsman: unauthenticated Identify refused (401, as Windows Server's default), and the WinRM listener answered an …
  rdp                OK       10.254.0.10:3389: X.224 Connection Confirm, RDP_NEG_RSP selected protocol CredSSP (reply 03 00 00 13 0e d0 00 00 12 34 00 02 1f 08 00 02 00 …
  stable             CRITICAL boot stopped answering 10 s after it passed (probe 2, one every 5s for 1m0s): gave up after 5s: Post "http://10.254.0.10:5985/wsman-anon/ide…
  image_unchanged    OK       sha256 before and after are equal
  drivers            CRITICAL Windows restarts on every boot: virt-v2v's first-boot service cannot install virtio-win's smbus driver and retries it on the next boot: smbu…

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t11-win-20261123T110341Z-04b121) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, 2265f9385124e81a98e8c2f986d959fc130577d1a235d6d21c003d086230bba0, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t11-win-20261123T110341Z-04b121
       python3 -I verify.py bundle bundles/gaia-t11-win-20261123T110341Z-04b121 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 6 of ledger.jsonl names bundle_sha256 d497eafe7a017c8dff2a12b10139e4b4ca5f3db54affff601801293f09e4f1b6, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
