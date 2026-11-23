Migration readiness report: gaia-t11-win on esxi (192.168.1.29), check 5, 2026-11-23T12:30:29.627558161Z.

Readiness: CRITICAL
  drivers critical: Windows restarts on every boot: virt-v2v's first-boot service cannot install virtio-win's smbus driver and retries it on the next boot: smbus failed to install on 9 of 9 boots (Adding the driver package failed : A certificate chain processed, but terminated in a root certificate which is not trusted by the trust provider.). Check again with exclude_drivers ["smbus"] if the VM does not need it

Steps and checks
  source_vm          OK       [datastore1] gaia-t11-win/gaia-t11-win.vmx, guest windows2019srvNext_64Guest, 2 vCPU, 4096 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t11-win
  download           OK       3 files, 34359739281 bytes in 143.4 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://microsoft.com/win/2k22 (x86_64, bios, 2 vCPU, 4096 MiB, NIC 00:50:56:3f:b1:01), virtio-win drivers in 72.2 s
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               OK       POST http://10.254.0.10:5985/wsman: unauthenticated Identify refused (401, as Windows Server's default), and the WinRM listener answered an …
  rdp                OK       10.254.0.10:3389: X.224 Connection Confirm, RDP_NEG_RSP selected protocol CredSSP (reply 03 00 00 13 0e d0 00 00 12 34 00 02 1f 08 00 02 00 …
  stable             CRITICAL boot did not keep answering for 1m0s without a break inside the 300 s boot timeout: interrupted 8 time(s), the last after 303 s (gave up aft…
  image_unchanged    OK       sha256 before and after are equal
  drivers            CRITICAL Windows restarts on every boot: virt-v2v's first-boot service cannot install virtio-win's smbus driver and retries it on the next boot: smbu…

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t11-win-20261123T122138Z-06fe41) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, d859a0105b974b2e9c00950b750468b5f5e98c487d3dc2bc33a1345c25848556, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t11-win-20261123T122138Z-06fe41
       python3 -I verify.py bundle bundles/gaia-t11-win-20261123T122138Z-06fe41 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 6 of ledger.jsonl names bundle_sha256 d53ed55ea6d11880415cd903c2ee0212de8fac4dad98f4acdc21b8547c3adca2, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
