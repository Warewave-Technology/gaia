Migration readiness report: gaia-t8-linux on esxi (192.168.1.29), check 2, 2026-11-23T12:07:57.652902591Z.

Readiness: OK
  ready for KVM: converted in 97 s, booted in 16 s, all 10 checks passed, 6 vSphere calls, all read-only

Steps and checks
  source_vm          OK       [datastore1] gaia-t8-linux/gaia-t8-linux.vmx, guest debian12_64Guest, 2 vCPU, 1024 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t8-linux
  download           OK       3 files, 3221226471 bytes in 21.9 s (GET /folder, sha256 in downloads.json)
  conversion         OK       1 disk(s), http://debian.org/debian/12 (x86_64, bios, 2 vCPU, 1024 MiB, NIC 00:50:56:3f:a8:01) in 96.7 s
  firmware           OK       BIOS on the source and in the sandbox (SeaBIOS)
  boot               OK       serial console matched "(?m)login: *$": "gaia-t8-linux login:"
  ssh                OK       connected to 10.254.0.10:22
  health             OK       GET http://10.254.0.10:8080/health: 200
  image_unchanged    OK       sha256 before and after are equal

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t8-linux-20261123T120731Z-54812b) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, d859a0105b974b2e9c00950b750468b5f5e98c487d3dc2bc33a1345c25848556, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t8-linux-20261123T120731Z-54812b
       python3 -I verify.py bundle bundles/gaia-t8-linux-20261123T120731Z-54812b --key keys/evidence-key-current.pub
  3. Its ledger entry: line 3 of ledger.jsonl names bundle_sha256 fd6e8b85e6ba97a965b31cbf94292a98abd4de3a4530f69d1477edbfef997fb6, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
