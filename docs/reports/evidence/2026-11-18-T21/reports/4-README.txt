Migration readiness report: gaia-t21-uefi on esxi (192.168.1.29), check 4, 2026-11-18T00:49:07.705039267Z.

Readiness: OK
  ready for KVM: converted in 75 s, booted in 34 s, all 10 checks passed, 6 vSphere calls, all read-only

Steps and checks
  source_vm          OK       [datastore1] gaia-t21-uefi/gaia-t21-uefi.vmx, guest debian12_64Guest, 2 vCPU, 1024 MiB, firmware efi, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t21-uefi
  download           OK       3 files, 3221226470 bytes in 21.5 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://debian.org/debian/12 (x86_64, efi, 2 vCPU, 1024 MiB, NIC 00:50:56:3f:a8:03) in 75.4 s
  firmware           OK       UEFI on the source and in the sandbox (OVMF)
  boot               OK       serial console matched "(?m)login: *$": "gaia-t21-uefi login:"
  ssh                OK       connected to 10.254.0.88:22
  health             OK       GET http://10.254.0.88:8080/health: 200
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t21-uefi-20261118T004822Z-ee68cf) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, e5c66f784781cbd1fa5568bd6804f7cdb54f931d75235c884e0c18299d0f1324, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t21-uefi-20261118T004822Z-ee68cf
       python3 -I verify.py bundle bundles/gaia-t21-uefi-20261118T004822Z-ee68cf --key keys/evidence-key-current.pub
  3. Its ledger entry: line 5 of ledger.jsonl names bundle_sha256 c69e6b14c5e6f14ea4493c98151c34b4e259ce4460ddb7f3e4cfef263a0591bd, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
