# ADR-001: Language and components

Status: accepted (2026-11-13)

**Decision: Go is the main language.** The Windows in-guest (VSS) agent is Rust. The data path starts in Go and
moves to Rust only if a measured bottleneck requires it.

| Criterion | Go | Rust | Winner |
|---|---|---|---|
| Memory safety (NSA/CISA list) | listed | listed | tie |
| VMware | govmomi, VMware's official SDK, ships vcsim | no official SDK | Go |
| KVM / libvirt | libvirt.org/go/libvirt, maintained by libvirt | `virt` crate, partial API | Go |
| Kubernetes / KubeVirt / Velero / Kanister | all written in Go | third-party clients | Go |
| VDDK (C library) | nbdkit as a subprocess | same | tie |
| Data path (chunking, hashing) | restic/Kopia prove it works | PBS: no GC, faster | Rust (narrow) |
| Windows VSS agent | no mature COM library | Microsoft's windows-rs | Rust |

Deciding argument: Gaia is integration and orchestration software (like libvirt), not a hypervisor (like QEMU).

## Component map

| Component | Language / tool |
|---|---|
| Control plane, scheduler, API | Go |
| Hypervisor adapters (libvirt, vSphere, KubeVirt) | Go |
| Verification engine | Go |
| Disk I/O (VMDK, NBD, qcow2) | Go + subprocess: nbdkit, libnbd, qemu-img |
| Repository / dedup | Kopia (ADR-002); benchmark gate in Phase 1 Track B |
| Windows in-guest agent | Rust, separate binary, gRPC |

Boundary rule: no cgo/FFI bridges between languages — only process and network boundaries.
