# How backup and DR products show recoverability, and what operators and auditors need

Research for T20. Reddit was not reachable; operator quotes come from vendor forums and knowledge bases. Compliance
clauses come partly from secondary write-ups: check the original texts before quoting them in an ADR.

## Products

| Product | Status, points and verification | Strengths | Weaknesses |
|---|---|---|---|
| Veeam ONE / SureBackup | "Protected VMs" = at least one valid point meeting RPO, "Last Backup Age"; Recovery Verification report: passed / warning / failed / **never tested**, per-point ping, heartbeat, script, antivirus; SLA heatmap ([1](https://helpcenter.veeam.com/docs/one/reporter/protected_vms.html), [2](https://helpcenter.veeam.com/docs/one/userguide/surebackup_jobs_overview.html), [3](https://www.veeam.com/blog/veeam-one-reports.html)) | Separates "never tested"; ties results to the point's date | Verification in a separate product/report, not in the VM's status; no screenshot; lab hard to set up ([forum](https://forums.veeam.com/vmware-vsphere-f24/surebackup-do-you-use-this-t19703.html)) |
| Rubrik RSC | SLA Domain; compliant / NON_COMPLIANT / UNPROTECTED; last snapshot, missed snapshots ([1](https://www.rubrik.com/community/blogs/watching-weather-observability-rubrik-security-cloud), [2](https://www.rubrik.com/community/blogs/finding-unprotected-workloads-rubrik-security-cloud-rkgetprotectionstatus)) | One policy object; finds unprotected workloads | "Compliant" = fresh snapshots, says nothing about restorability |
| Cohesity Helios | Protection Runs report: success / warning / failed, SLA met/missed ([doc](https://docs.cohesity.com/baas/Helios/reports/protection-runs.htm)) | Fleet view | Run-centred; "SLA" = run duration |
| Zerto | VPG "Meeting SLA" / "RPO Not Meeting SLA"; live RPO graph; RPO breach tables; Recovery Report PDF with user, point, RTO, result ([1](https://help.zerto.com/bundle/Admin.ZSSP.HTML.10.0_U3/page/VPG_Statuses.htm), [2](https://1111systems.com/?p=30203)) | RPO over time; measured RTO in tests | Test verdict "passed by user" (manual); report not tamper-evident |
| Commvault | % clients meeting SLA; recovery SLA for cleanroom recoveries ([doc](https://documentation.commvault.com/11.46/software/metrics_dashboard.html)) | Executive tile | "Met" = no missed backup; new servers excluded 30 days; percentages, not per-VM answers |
| Druva | Violations: RPO, success rate, consecutive failures; anomaly/IOC flags per point ([1](https://help.druva.com/en/articles/13933659-data-protection-violations)) | Suspicious vs clean points | No restore testing |
| Proxmox Backup Server | Per-snapshot Verify State OK / Failed / none; warning when the last verify is older than 30 days ([forum](https://forum.proxmox.com/threads/verify-state-error-old-in-yellow-what-to-do.162114/latest)) | Verification age per point | Chunk integrity only, never a boot; GUI can show OK while the verify job fails ([forum](https://forum.proxmox.com/threads/inconsistent-info-about-validation-status.182293/)) |
| Nakivo | Boots the backup, screenshot, "Last verification" with screenshot on dashboard and email; fails past an RTO limit ([help](https://helpcenter.nakivo.com/User-Guide/Content/Overview/VM-Verification.htm)) | Visual proof; RTO as a limit | Per job, reviewed by email |
| Datto | Screenshot of the login screen for chosen points; optional service/script checks ([KB](https://continuity.datto.com/help/Content/kb/siris-alto-nas/KB205050974.htm)) | Best-known pattern | Does not test non-boot volumes; false failures from custom login screens and timing |
| AWS Backup | Restore-testing plans; customer-posted validation status; Audit Manager "restore time meets target" ([doc](https://docs.aws.amazon.com/aws-backup/latest/devguide/restore-testing-validation.html)) | Measured restore time vs target in audit reports | Checks are do-it-yourself |
| Azure Backup | Grid: protection status, latest recovery point ([doc](https://learn.microsoft.com/he-il/azure/backup/backup-center-monitor-operate)) | Simple | No restore testing |

## Operator pain

- Green but not restorable: backups failing three years unnoticed ([Red Hat](https://redhat.com/sysadmin/backups-cautionary-tale)); "success rates are a dangerous metric" ([dev.to](https://dev.to/ntctech/backup-success-rates-are-a-dangerous-metric-15k6)); a 4-hour restore plan took 2.5 days ([blendbyte](https://www.blendbyte.com/blog/your-backups-are-useless-if-youve-never-tested-a-restore)).
- Noisy, badly structured alerts: "These aren't marketing emails, they're notifications" — status first, then time and object, resolved alarms marked resolved ([Veeam forum](https://forums.veeam.com/veeam-one-f28/email-alerts-are-still-hot-garbage-t62041.html)); Veeam ONE "too chatty" ([community](https://community.veeam.com/blogs-and-podcasts-57/from-the-lab-the-hidden-cost-of-set-it-and-forget-it-backups-even-with-veeam-13028)).
- Ambiguous verification: a failed screenshot can mean "doesn't boot" or "the check broke"; backup taken, replicated and verified bootable must be separate states ([runbook](https://docs.getthread.com/skill-library/vendor-runbooks/datto-bcdr-verification.md)).
- Verification abandoned because it is too hard to keep running.

## Auditors

- ISO 27001:2022 A.8.13: backups maintained and regularly tested; auditors ask for the policy, an asset-to-RPO/RTO register, restore-test records (date, scope, result, time vs RTO, who, failures); no tests for 12+ months is a major nonconformity ([UpGuard](https://www.upguard.com/compliance/iso-27001/8-13/)).
- SOC 2 A1.2/A1.3: recovery procedures tested; Type 2 evidence over the whole period.
- DORA Art. 12(2): "Testing of the backup procedures and restoration and recovery procedures and methods shall be undertaken periodically"; Art. 12(6) RTO/RPO per function.
- NIS2 Art. 21(2)(c),(f); Implementing Regulation 2024/2690 Annex §4.2.3 integrity checks, §4.2.6 recovery tests with documented results and corrective action ([nisd2](https://nisd2.eu/en/wiki/implementation/nis2-backup-strategy)).
- PCI DSS 4.0.1 12.10.2: plan incl. backup processes tested at least every 12 months.
- HIPAA §164.308(a)(7)(ii)(D): periodic testing of the contingency plan, documented.

Common to all: a dated, per-system test record with result and measured time against RTO, continuous over the audit
period, with corrective actions. None requires tamper evidence; no vendor offers it.

## Gaps Gaia fills

No product shows on its main screen, per VM, the newest proven point and the age of that proof; none separates tested
from untested points on the points timeline; none offers evidence that verifies offline.

## Implications

1. Status screen: one row per VM, worst first; status label, newest restorable point, Proven RPO beside Theoretical
   RPO, Proven RTO against target, proof age.
2. Verdict from proof, not job success.
3. The reason in one sentence, always in text.
4. A test that could not run is UNKNOWN, never OK and never a guest failure; a different reason from "did not boot".
5. "Never tested" as its own visible count, never healthy.
6. VM page: a points timeline where each point carries a test mark (proven / failed / untested / damaged); the newest
   proven point marked as "return here".
7. Test detail: screenshot, every check with its value, restore and boot times, sandbox context, what was checked.
8. Status timeline from state_changes with reasons and time in each state.
9. Auditor view: per system, test date, result, measured RTO vs target, corrective follow-ups; filter by period.
10. Evidence integrity explicit: signature state per bundle, ledger head and sequence, last external anchor (daily
    report); copy-pasteable verify commands.
11. Notifications status-first, resolutions marked.
12. Main screen short: no job percentages, throughput or capacity widgets.
