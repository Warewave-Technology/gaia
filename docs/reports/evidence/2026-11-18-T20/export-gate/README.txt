Evidence export of Gaia installation gaia-t17-b, 2026-11-17T21:50:57Z (server version e8e684818d7d).

Contents
  anchor.pub                 the ledger's first key: the trust anchor, key_id 2695ee2e41e8978c3a77d4a6d9e78d7284908d50053aa4e99dec669a1750c909
  keys/                      the installation's current public key
  ledger.jsonl               the evidence ledger, 82748 bytes
  bundles/                   138 evidence bundles (one per restore test)
  daily-report-1.json        the newest daily report (2026-11-17T18:59:15.042105816Z)
  ledger-head-1.json         its signed ledger head (SPEC.md §6.8): entry 123
  restore-tests.json         every restore test: VM, point, time, verdict, measured RTO against its target, bundle
  SPEC.md                    Restore Evidence Format, version 1 (CC0): what everything here means and how to verify it
  SHA256SUMS                 the hash of every file above

Verify, without Gaia's server
  1. Compare the trust anchor's key_id (above) with the one you recorded when Gaia was installed, or with the
     evidence_key_id in a daily report you received by email. A verifier must not take the anchor from the data it
     verifies (SPEC.md §3).
  2. With Gaia's command-line tool (any machine, no server):
       gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles -head ledger-head-1.json
  3. With any other implementation of SPEC.md, for example the independent Python verifier in Gaia's source
     (docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py, standard library only):
       python3 -I verify.py ledger ledger.jsonl bundles --key anchor.pub --head ledger-head-1.json
  OK means: every entry from the anchor on is signed and chained, every bundle is exactly as sealed, no bundle is
  missing or extra,
  and the ledger still holds the head the daily report recorded.
  A copy of the daily report that reached you by email is a stronger head than the one in this export: give that one
  with -head (--head) as well.
