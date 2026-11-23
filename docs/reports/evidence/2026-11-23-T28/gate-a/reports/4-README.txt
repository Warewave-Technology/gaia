Migration readiness report: gaia-t21-uefi on esxi (192.168.1.29), check 4, 2026-11-23T10:59:31.087135207Z.

Readiness: OK
  ready for KVM: converted in 73 s, booted in 24 s, all 10 checks passed, 6 vSphere calls, all read-only

Steps and checks
  source_vm          OK       [datastore1] gaia-t21-uefi/gaia-t21-uefi.vmx, guest debian12_64Guest, 2 vCPU, 1024 MiB, firmware efi, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t21-uefi
  download           OK       3 files, 3221226470 bytes in 21.3 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://debian.org/debian/12 (x86_64, efi, 2 vCPU, 1024 MiB, NIC 00:50:56:3f:a8:03) in 72.8 s
  firmware           OK       UEFI on the source and in the sandbox (OVMF)
  boot               OK       serial console matched "(?m)login: *$": "gaia-t21-uefi login:"
  ssh                OK       connected to 10.254.0.10:22
  health             OK       GET http://10.254.0.10:8080/health: 200
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t21-uefi-20261123T105857Z-01cf87) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, 2265f9385124e81a98e8c2f986d959fc130577d1a235d6d21c003d086230bba0, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t21-uefi-20261123T105857Z-01cf87
       python3 -I verify.py bundle bundles/gaia-t21-uefi-20261123T105857Z-01cf87 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 5 of ledger.jsonl names bundle_sha256 47035702ed940d1e75127012523d606b12139d4fa6fdb2f65b9da970f6ad3f99, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
