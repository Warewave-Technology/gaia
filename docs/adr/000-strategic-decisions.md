# ADR-000: Strategic decisions

Status: accepted (2026-11-13)

| Decision | Choice | Why |
|---|---|---|
| Scope | End-to-end product: backup + verification + reporting | Gaia is an alternative, not just a layer on top of another vendor's backups |
| Quality bar | The strictest (regulated) customer, from day one | Small businesses deserve the same first-class, provable product |
| Platforms | Hybrid: KVM first, then VMware | KVM lab exists; the market is on VMware |
| License | AGPL-3.0-or-later, no CLA | See ADR-002 |

Roles: operations perspective, product direction and acceptance criteria — Yiğit. Architecture, component
choice and code — Claude.
