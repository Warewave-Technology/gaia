# ADR-004: Data path decisions after Phase 1

Status: accepted (2026-11-14), amended 2026-11-16 twice (see "Amendments" at the end; later amendments win over
original text). Amends ADR-001 (libvirt binding, benchmark gate) and ADR-002 #5 (Kopia defaults).
Evidence: report `2026-11-14-T3.md`.

## 1. Benchmark gate: compare like with like (answers T3 ONAY 1)

The Phase 1 gate compared Kopia (reads all 30 GiB) with `qemu-img convert` (very likely reads only the
~14.7 GiB allocated). From now on every throughput gate uses:

- **Reference:** a sequential read of the *same bytes* the tool under test reads (today: `sha256_full_read_s`).
- **Statistic:** the **median of at least 3 runs** per configuration, all in the same session.
- `qemu-img convert` stays in reports as context, not as the gate reference.

Under both the old and the new definition `s2-default` + `FIXED-4M` passes (old: 1.66×, 1.82×; new: 1.18×).
Task T4 records the formal median.

## 2. Repository defaults for disk images (answers T3 ONAY 2)

- Whole-image mode: `--object-splitter=FIXED-4M`, compression `s2-default`.
- Reason: restore is the weak point (3–5× slower than backup) and `FIXED-1M` made incremental restores 1.6–1.8×
  slower. Gaia sells recoverability; restore time wins over storage.
- This default is **provisional**: once incremental backups write only changed extents (section 4), chunking is
  re-evaluated against real write patterns, not the worst-case scattered 1 % churn.

## 3. Restore time is a product metric

Gaia already restores every tested point, so it can measure restore time. Next to Theoretical / Proven RPO, Gaia
reports **Proven RTO**: the measured time of the last successful restore test. Restore performance therefore gets
its own investigation (task T5).

## 4. Incremental backups read only changed extents (answers T3 open question 1)

- KVM/libvirt: the libvirt backup API (checkpoints + pull-mode backup) exposes the disk over NBD with a dirty
  bitmap. Gaia reads only the dirty extents and writes them through the **Kopia Go library** (no CLI).
- Proxmox and KubeVirt adapters reuse the same "changed extents in, Kopia out" interface.

## 5. libvirt binding: pure Go (fixes a contradiction in ADR-001)

ADR-001 named `libvirt.org/go/libvirt`, which uses cgo — against ADR-001's own "no cgo" rule. Gaia uses
**`github.com/digitalocean/go-libvirt`**, a pure-Go client of libvirt's RPC protocol. If a needed procedure is
missing there, fall back to `virsh` as a subprocess and report it.

## 6. NBD client: our own, pure Go

Gaia implements a minimal NBD client in `internal/nbd` (fixed newstyle handshake, structured replies,
`NBD_CMD_BLOCK_STATUS` with the `qemu:dirty-bitmap:*` meta context, `NBD_CMD_READ`). No cgo (rules out the
libnbd Go bindings), no new dependency. Tested against `qemu-nbd` in CI.

## Amendments (2026-11-16)

Product owner decisions of 2026-11-16 on the open `ONAY GEREKİYOR` items of reports T4–T7, written down here.

### A1. Gate reference is a hash-free read (replaces §1 "Reference")

- **Reference:** a hash-free read of the same bytes, from the same source, that the tool under test reads
  (`plain_read_s`). T4 showed `sha256_full_read_s` is bound by single-threaded hashing (1.85–2.51× slower than a
  plain read) and noisy (1.53× between runs), so it measured the hash, not the disk.
- If a multi-stream plain read is faster than a single stream, the gate uses the **faster** one.
- Statistic unchanged: median of ≥ 3 runs in one session.
- Consequence: T4 is re-scored `FAIL` (median `full_backup / plain_read_s` = 2.388). T4 measured the Kopia CLI
  with source and repository on one disk, which is neither the product path nor a production layout; T9
  re-measures through the product path with source and repository on separate disks.

### A2. Restore defaults (extends §2)

- A single image is restored with **parallel range reads** (default 4 readers until T9/T10 measure otherwise).
- The Kopia content cache has a **hard limit** (default 1 GiB).
- The cache lives **neither on the repository disk nor on the restore target disk**.
- Zero blocks are written as holes.

Evidence: T5 (restore was one ~1-core stream; default 172.6 s → 63.2 s, 2.73×).

### A3. Block size is measured, not assumed (extends §2)

`FIXED-4M` stays the default until T10 compares block sizes under real incremental write patterns.
Evidence that it matters: T6 rewrote ~772 MB and read back 646 MB from the repository for 147 MB of dirty data.

### A4. Proven RTO definition (refines §3)

**Proven RTO = restore time + `ready`**, where `ready` runs from the sandbox guest's vCPU start until every
declared check has passed. Sandbox setup, image hashing and shutdown are excluded. The total duration of
`gaia verify` is always recorded next to it (`timings_s.total`) so the overhead stays visible.

### A5. Screenshot fallback (note on §5)

go-libvirt's `DomainScreenshot` is broken (wrong MIME source, stream deadlock), so console screenshots use
`virsh screenshot` as a subprocess, as §5 allows. Every other libvirt call stays on go-libvirt.

## Amendments, second round (2026-11-16)

Decisions on the `ONAY GEREKİYOR` items of reports T9 and T10, approved by the product owner.

### A6. Gate reference includes the repository's write speed (replaces A1's reference)

- A backup cannot finish faster than its repository can be written. T9: the repository disk wrote at
  236–284 MB/s, which put a floor of ~2.9 under the A1 ratio no matter what the software did.
- **Reference = max(source read, repository write)**, both hash-free and measured in the same run:
  - source read: as in A1 (same byte ranges, same source, faster of single- and multi-stream);
  - repository write: a hash-free sequential write of `repository_bytes` to the repository's disk.
- Gate unchanged: median `full_backup / reference` ≤ 2 over ≥ 3 runs, every restore byte-identical.
- T9's estimate under this reference (~1.07) is **not** a result. Task T12 measures it.

### A7. CPU budget for the data path

On fast storage the backup is CPU-bound (T9 context run: ~5.1 CPU-seconds per GB processed). This number is
recorded as a **regression budget**: a change that raises CPU-seconds per GB by more than 10 % on the reference
run needs a justification in its report. It is a budget, not a target to optimise now.

### A8. Defaults: `FIXED-1M` splitter and 1 MiB blocks; BLAKE2B stays (supersedes the provisional §2 / A3)

- **`FIXED-1M`**, block size 1 MiB. T10: scattered writes' repository growth 4.85× → 2.24× of dirty bytes,
  read-back from the repository −66 %, no penalty on full backup or restore at 30 GiB.
- `FIXED-256K` was better still, but the block index lists the whole disk at every point (~155 MB per point for a
  1 TiB disk, T10 estimate). Re-evaluate 256K once the index is delta-encoded or binary (MVP backlog).
- **Hash stays `BLAKE2B-256-128`.** BLAKE3 and HMAC-SHA256 cut hashing CPU by 37–45 % but did not change
  end-to-end backup time (T10). The hash is part of the repository format and cannot change after creation.
