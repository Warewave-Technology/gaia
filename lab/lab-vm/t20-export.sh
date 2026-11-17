#!/usr/bin/env bash
# T20 gate (b): the auditor's evidence export of a running server verifies offline with `gaia evidence verify` and
# with the independent Python verifier (T13/T19), against the signed ledger head of the newest daily report. Runs on
# gaia-lab against server B (the M2 upgrade-path server of the T17 drill), through the T17 workstation's CLI.
#
#   1. gaia evidence export-all: GET /api/v1/evidence-export, as the workstation's user
#   2. SHA256SUMS of the export; the trust anchor's key_id against the server's and the daily report's
#   3. both verifiers on the export as it is: OK, with the head statement OK
#   4. negative controls on copies: the ledger cut below the head (ledger_truncated) and one bundle file changed
#      must be CRITICAL in both verifiers
#
# Usage (on gaia-lab, as root, after t17-drill.sh upgrade B_IP put this version's CLI on the workstation):
#   lab/lab-vm/t20-export.sh
# Writes /root/t20/out/export-<time>/gate.json. Exit: 0 OK / 2 CRITICAL / 3 UNKNOWN (also when the harness failed).
set -euo pipefail

D=/root/t17
WS=$D/ws
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
PYV=$repo_root/docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py
OUT=/root/t20/out/export-$(date -u +%Y%m%dT%H%M%SZ)
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in jq python3 sha256sum tar; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin"; exit 3; }
done
[[ -x $WS/bin/gaia ]] || { echo "UNKNOWN: no workstation CLI at $WS/bin/gaia (run t17-drill.sh upgrade first)"; exit 3; }
mkdir -p "$OUT"
exec > >(tee -a "$OUT/export.log") 2>&1

ws() { HOME=$WS "$WS/bin/gaia" "$@"; }
gaia() { "$WS/bin/gaia" "$@"; } # the local tool: no server, no HOME
# pyv ARGS: the independent verifier, isolated (-I), from its own directory; exit code kept in $rc.
pyv() { rc=0; python3 -I "$PYV" "$@" || rc=$?; }

echo "== server and export ($(ws whoami -json | jq -r '"\(.username), \(.role)"'))"
ws system -json >"$OUT/system.json"
jq -c '{version, schema_version, evidence_key_id}' "$OUT/system.json"
ws daily-reports list limit=1 -json >"$OUT/daily-reports.json"
ws evidence export-all -o "$OUT/export.tar.gz" -json | tee "$OUT/export-all.json"
mkdir "$OUT/x" && tar -C "$OUT/x" -xzf "$OUT/export.tar.gz"
X=$(find "$OUT/x" -mindepth 1 -maxdepth 1 -type d)
(cd "$X" && find . -type f | sort | sed -n '1,12p;$p') | tee "$OUT/contents.txt"

sums_ok=true
(cd "$X" && sha256sum --quiet -c SHA256SUMS) || sums_ok=false
head_file=$(find "$X" -maxdepth 1 -name 'ledger-head-*.json' -printf '%f\n' | head -1)
[[ -n "$head_file" ]] || { echo "UNKNOWN: the export has no ledger head (no daily report on the server)"; exit 3; }
report_id=$(jq -r '.[0].report.evidence_key_id // empty' "$OUT/daily-reports.json")
server_id=$(jq -r .evidence_key_id "$OUT/system.json")

echo "== verify the export as it is (head: $head_file)"
rc=0
gaia evidence verify -pubkey "$X/anchor.pub" -ledger "$X/ledger.jsonl" -evidence-dir "$X/bundles" -head "$X/$head_file" -json >"$OUT/gaia-verify.json" || rc=$?
echo "gaia evidence verify: exit $rc"
gaia_rc=$rc
anchor_id=$(jq -r '.anchor_key_id // empty' "$OUT/gaia-verify.json")
pyv ledger "$X/ledger.jsonl" "$X/bundles" --key "$X/anchor.pub" --head "$X/$head_file" --json >"$OUT/python-verify.json"
echo "verify.py: exit $rc"
py_rc=$rc

echo "== negative controls on copies"
neg() { # NAME: copies the export to $N for a change
	N=$OUT/neg-$1 && rm -rf "$N" && cp -a "$X" "$N"
}
neg truncated
seq=$(jq -r '.payload | fromjson | .seq' "$X/$head_file")
head -n $((seq - 10)) "$X/ledger.jsonl" >"$N/ledger.jsonl"
rc=0
gaia evidence verify -pubkey "$N/anchor.pub" -ledger "$N/ledger.jsonl" -evidence-dir "$N/bundles" -head "$N/$head_file" -json >"$OUT/neg-truncated-gaia.json" || rc=$?
t_gaia=$rc
pyv ledger "$N/ledger.jsonl" "$N/bundles" --key "$N/anchor.pub" --head "$N/$head_file" --json >"$OUT/neg-truncated-python.json"
t_py=$rc
neg tampered
victim=$(find "$N/bundles" -name result.json | sort | head -1)
printf ' ' >>"$victim" # one byte more: the manifest's hash no longer matches
rc=0
gaia evidence verify -pubkey "$N/anchor.pub" -ledger "$N/ledger.jsonl" -evidence-dir "$N/bundles" -head "$N/$head_file" -json >"$OUT/neg-tampered-gaia.json" || rc=$?
m_gaia=$rc
pyv ledger "$N/ledger.jsonl" "$N/bundles" --key "$N/anchor.pub" --head "$N/$head_file" --json >"$OUT/neg-tampered-python.json"
m_py=$rc

# codes FILE: the finding codes of either verifier (gaia: objects with .code; verify.py: strings)
codes() { jq -c '[(.findings // [])[] | if type == "string" then . else .code end] | unique' "$1"; }
jq -n --slurpfile sys "$OUT/system.json" --slurpfile ex "$OUT/export-all.json" --slurpfile g "$OUT/gaia-verify.json" --slurpfile p "$OUT/python-verify.json" \
	--argjson sums_ok "$sums_ok" --arg head "$head_file" --argjson head_seq "$seq" \
	--arg anchor_id "$anchor_id" --arg report_id "$report_id" --arg server_id "$server_id" \
	--argjson gaia_rc "$gaia_rc" --argjson py_rc "$py_rc" --argjson t_gaia "$t_gaia" --argjson t_py "$t_py" --argjson m_gaia "$m_gaia" --argjson m_py "$m_py" \
	--argjson t_gaia_codes "$(codes "$OUT/neg-truncated-gaia.json")" --argjson t_py_codes "$(codes "$OUT/neg-truncated-python.json")" \
	--argjson m_gaia_codes "$(codes "$OUT/neg-tampered-gaia.json")" --argjson m_py_codes "$(codes "$OUT/neg-tampered-python.json")" \
	--arg tampered "${victim#"$N"/}" '
	{server: ($sys[0] | {version, schema_version}), export: {bytes: $ex[0].bytes, sha256sums_ok: $sums_ok, head: $head, head_seq: $head_seq},
	 trust_anchor: {export: $anchor_id, server: $server_id, daily_report: $report_id},
	 as_exported: {gaia: {exit: $gaia_rc, verdict: $g[0].verdict}, python: {exit: $py_rc, verdict: $p[0].verdict}},
	 truncated_below_head: {gaia: {exit: $t_gaia, codes: $t_gaia_codes}, python: {exit: $t_py, codes: $t_py_codes}},
	 bundle_file_changed: {file: $tampered, gaia: {exit: $m_gaia, codes: $m_gaia_codes}, python: {exit: $m_py, codes: $m_py_codes}}}
	| . + {pass: (.export.sha256sums_ok and .as_exported.gaia.exit == 0 and .as_exported.python.exit == 0
	              and .as_exported.gaia.verdict == "OK" and .as_exported.python.verdict == "OK"
	              and (.trust_anchor.server == .trust_anchor.daily_report)
	              and .trust_anchor.export == .trust_anchor.server
	              and .truncated_below_head.gaia.exit == 2 and .truncated_below_head.python.exit == 2
	              and (.truncated_below_head.gaia.codes | index("ledger_truncated")) != null
	              and (.truncated_below_head.python.codes | index("ledger_truncated")) != null
	              and .bundle_file_changed.gaia.exit == 2 and .bundle_file_changed.python.exit == 2)}
	| . + {verdict: (if .pass then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json"
echo "out: $OUT"
[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
