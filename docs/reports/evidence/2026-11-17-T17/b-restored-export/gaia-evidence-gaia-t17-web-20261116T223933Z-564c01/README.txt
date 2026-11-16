Evidence bundle gaia-t17-web-20261116T223933Z-564c01, exported 2026-11-16T22:47:45Z.

Verify it (Restore Evidence Format v1, docs/spec/evidence-v1.md) with:
  gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles

anchor.pub is this installation's trust anchor, key_id 2695ee2e41e8978c3a77d4a6d9e78d7284908d50053aa4e99dec669a1750c909. A verifier must not take the trust anchor from the
data it verifies: compare that key_id with the one you recorded when Gaia was installed.
The ledger lists every bundle; this export holds only gaia-t17-web-20261116T223933Z-564c01, so the other bundles show up as bundle_missing.
