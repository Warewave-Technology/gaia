# ADR-007: MVP architecture

Status: accepted (2026-11-16). The feasibility study ended with **GO** (2026-11-16).

## Scope of the MVP

KVM/libvirt hosts only. Backups of running VMs (multi-disk), scheduled restore tests in an isolated sandbox,
signed evidence, and an operator screen that answers the core question: *which point can I return to right now,
and is that proven?* Migration check (ADR-003/005) is part of the MVP. Proxmox, KubeVirt, the Windows VSS agent
and the hot migration check are not.

**Design target (not a promise):** one Gaia server, up to ~50 VMs and ~10 TB of protected disk.

## Decisions

1. **One binary, one server.** `gaia server` runs API, UI, scheduler and workers in one process under systemd on
   Debian. No external services required.
2. **State in SQLite**, through a pure-Go driver (`modernc.org/sqlite`; no cgo, ADR-001), WAL mode. Gaia backs up
   its own state (config, catalog, keys' public parts, ledger) into its repository every day.
3. **Protected hosts are reached over SSH only.** One authenticated SSH connection per host carries both libvirt's
   RPC (go-libvirt over a forwarded unix socket) and the NBD export (libvirt pull-mode backup bound to a **unix
   socket** on the host, forwarded). No NBD over TCP, no unauthenticated channel. Pure-Go SSH client. If libvirt
   cannot bind the export to a unix socket in a supported version, fall back to TLS and report it.
4. **Repository:** a Kopia repository on a filesystem path or S3-compatible storage, with ADR-004 defaults.
5. **Sandbox runs on the Gaia server host** (it must have KVM). A remote sandbox host is a later step.
6. **API:** REST/JSON with an OpenAPI description. The CLI is a client of the same API.
7. **UI:** server-rendered Go `html/template` + htmx, vendored. No Node toolchain, no CDN: Gaia must work in
   air-gapped, regulated networks.
8. **Users and roles:** local users (argon2id), three roles — `admin`, `operator`, `auditor` (read-only, can
   export evidence). Every user action goes to an append-only audit log. OIDC is later.
9. **Keys:** one ed25519 signing key per installation; `key_id` per ADR-006. Rotation writes a ledger entry signed
   by the old key that endorses the new one.
10. **External anchoring, cheap:** the daily report (email and webhook) carries the ledger head hash, so copies of
    it exist outside Gaia. RFC 3161 timestamping is later.

## The status model (the product's heart)

For every protected VM Gaia computes, at every page load and on every event:

| Field | Meaning |
|---|---|
| Theoretical RPO | now − time of the newest backup point that passed its post-backup check |
| Proven RPO | now − time of the newest point whose restore test is `OK` |
| Proven RTO | ADR-004 A4, from that test |
| Proof age | now − time of that restore test |

A policy sets targets: RPO target, RTO target, maximum proof age. The VM's status is:

| Status | When |
|---|---|
| `OK` | Proven RPO ≤ RPO target **and** Proven RTO ≤ RTO target **and** proof age ≤ maximum |
| `WARNING` | Theoretical RPO is within target, but proof is missing a target (proven RPO, RTO or proof age) |
| `CRITICAL` | Theoretical RPO over target, **or** the latest restore test is `CRITICAL`, **or** chain/integrity verification failed |
| `UNKNOWN` | No restore test ever passed, or any input is missing |

Rules: `UNKNOWN` is never shown as healthy; when several rows apply, the worst wins (`CRITICAL` > `UNKNOWN` >
`WARNING` > `OK`). The truth table above is implemented as one function with exhaustive tests.

## Core-matrix items and where they live

| # | Question | MVP mechanism |
|---|---|---|
| 1 | Was the backup really taken? | Post-backup check: read back the point's manifest and block index, confirm every block is present and sizes match |
| 2 | Is the chain intact? | Scheduled repository verification (sampled daily, full weekly) and per-point index checks |
| 3 | Was a restore tested? | Scheduled `gaia verify` per policy |
| 4 | Does the operator know? | The status model above, on the operator screen |
| 5 | Can it be proven? | Evidence spec v1 (ADR-006), auditor export, ledger head in the daily report |
