Migration readiness report: gaia-t11-win on esxi (192.168.1.29), check 1, 2026-11-23T12:05:13.156072164Z.

Readiness: UNKNOWN
  conversion unknown: a Windows guest needs virtio-win 0.1.302 (ADR-005 §2): no virtio-win ISO at /var/lib/gaia-convert/virtio-win-0.1.302.iso: download it from https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso

Steps and checks
  source_vm          OK       [datastore1] gaia-t11-win/gaia-t11-win.vmx, guest windows2019srvNext_64Guest, 2 vCPU, 4096 MiB, firmware bios, 1 disk(s), 1 NIC(s)
  cold               OK       poweredOff
  layout             OK       3 files to read, all in [datastore1] gaia-t11-win
  download           OK       3 files, 34359739281 bytes in 145.0 s (GET /folder, sha256 in downloads.json)
  conversion         UNKNOWN  a Windows guest needs virtio-win 0.1.302 (ADR-005 §2): no virtio-win ISO at /var/lib/gaia-convert/virtio-win-0.1.302.iso: download it from …
  firmware           UNKNOWN  not run: the conversion did not finish
  boot               UNKNOWN  not run: the conversion failed

Every step ran against the source with read-only calls only (ADR-003): 6 vSphere calls, 0 blocked.
The signed bundle (bundles/gaia-t11-win-20261123T120512Z-5d2c06) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, d859a0105b974b2e9c00950b750468b5f5e98c487d3dc2bc33a1345c25848556, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/gaia-t11-win-20261123T120512Z-5d2c06
       python3 -I verify.py bundle bundles/gaia-t11-win-20261123T120512Z-5d2c06 --key keys/evidence-key-current.pub
  3. Its ledger entry: line 2 of ledger.jsonl names bundle_sha256 21a100768fc7c5ff9135767e51a6474afa55579b369bee20e89ca70df02a5841, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
