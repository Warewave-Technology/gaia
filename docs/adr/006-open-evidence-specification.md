# ADR-006: The evidence format is an open specification

Status: accepted (2026-11-16).

## Context

Automated restore testing exists elsewhere in open source (Xen Orchestra health checks, PBP for Proxmox), but none
of it produces cryptographic, tamper-evident evidence. Gaia's signed bundles and hash-chain ledger (T7) are a real
difference — and Gaia is not a vendor. An auditor is best served by one evidence format, whoever produced it.

## Decisions

1. Gaia publishes **"Restore Evidence Format" v1** as a standalone specification in `docs/spec/evidence-v1.md`.
   The specification text is released under **CC0 1.0**, so anyone — including other projects and proprietary
   products — can implement it. Gaia's code stays AGPL-3.0-or-later.
2. The specification covers: bundle layout, `manifest.json` schema, canonical bytes that are signed, ed25519
   signatures, ledger entry format and hash chain, **key identifiers**, **format versioning**, and the verification
   algorithm with its verdicts.
3. The specification ships with **conformance test vectors**: bundles and ledgers that must verify, and ones that
   must fail (each failure named). An implementation is conformant when it passes all vectors.
4. Key identifiers and a `format_version` field are added now, before the MVP has users, even though this changes
   the T7 format. After v1 is published, changes are versioned and v1 stays verifiable.
5. Once v1 is stable and the MVP uses it, we invite other projects (starting with PBP) to adopt it. No sooner.
