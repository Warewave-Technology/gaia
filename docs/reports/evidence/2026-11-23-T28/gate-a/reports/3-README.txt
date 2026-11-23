Migration readiness report: gaia-t8-broken on esxi (192.168.1.29), check 3, 2026-11-23T10:57:20.22219227Z.

Readiness: CRITICAL
  boot critical: no match for "(?m)login: *$" on the serial console within 300 s

Steps and checks
  source_vm          OK       [datastore1] gaia-t8-broken/gaia-t8-broken.vmx, guest debian12_64Guest, 2 vCPU, 1024 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t8-broken
  download           OK       3 files, 3221226474 bytes in 21.0 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://debian.org/debian/12 (x86_64, bios, 2 vCPU, 1024 MiB, NIC 00:50:56:3f:a8:02) in 74.3 s
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               CRITICAL no match for "(?m)login: *$" on the serial console within 300 s
  ssh                UNKNOWN  not run: the guest did not boot
  health             UNKNOWN  not run: the guest did not boot
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t8-broken-20261123T105213Z-18f376) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, 2265f9385124e81a98e8c2f986d959fc130577d1a235d6d21c003d086230bba0, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t8-broken-20261123T105213Z-18f376
       python3 -I verify.py bundle bundles/gaia-t8-broken-20261123T105213Z-18f376 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 4 of ledger.jsonl names bundle_sha256 237c9a21c31ab8752ef6d69b4dd8ddd96b213b1c6a5cc615a9d6fc353e7cf9e3, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
