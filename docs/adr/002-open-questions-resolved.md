# ADR-002: Resolved open questions

Status: accepted (2026-11-13)

| # | Question | Decision |
|---|---|---|
| 1 | License | **AGPL-3.0-or-later, no CLA.** The FSF recommends the AGPL for software commonly run over a network. Without a CLA nobody, including us, can relicense Gaia as proprietary. VDDK is never bundled: users download it, Gaia calls it through nbdkit as a separate process. |
| 2 | VMware test license | Layered: (a) vcsim in CI; (b) free ESXi 8.0U3e — backup API limits to be verified by `vsphere-probe`; (c) evaluation-mode nested ESXi + vCenter, rebuilt automatically; (d) VMUG Advantage + VCP-VCF. Start with a + b/c. |
| 3 | Evidence | Fully automated "restore → screen capture → shut down → sign": sandbox boot, console screenshots (libvirt `virDomainScreenshot`, vSphere `CreateScreenshot_Task`), health-check results, shutdown; report + artefacts hashed, signed with ed25519, appended to a hash chain; optional RFC 3161 timestamp. |
| 4 | App-level health checks | Hybrid: built-in templates + user probes. NIC-less isolation blocks external probes, so the sandbox gets an isolated network with no uplink and an in-network probe runner. |
| 5 | Repository format | Kopia repository (Apache-2.0, AGPL-compatible). Velero's built-in data mover and OpenShift's VM backups already use it. |
