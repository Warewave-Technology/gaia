#!/usr/bin/env bash
# T28 gate (b), ADR-007 C7: an evidence export verifies with `gaia evidence verify` and with the independent Python
# verifier (T13/T19, written from the specification only), and its export bundle is in the ledger.
#
# Two exports are made one after the other. Export 1's record (an evidence bundle: who, scope, period, the SHA-256 of
# its SHA256SUMS) is sealed after export 1 is complete, so it is in the server's ledger and in export 2. The gate:
#   1. both exports: every file against SHA256SUMS (sha256sum -c), SHA256SUMS.sig, and the key_ids: the trust anchor,
#      the key that signed SHA256SUMS, the server's and the daily report's evidence key must be one key
#   2. gaia evidence verify -export on both, the independent verifier (ledger mode, with the daily report's head) on
#      both: OK; export 2 holds export 1's record, so v1 verification checks the export bundle unchanged
#   3. export 1's record, checked with jq and sha256sum only: in export 2's ledger (a bundle entry whose bundle_sha256
#      is the hash of its manifest.json), its manifest names the user, the scope, the period and the SHA-256 of
#      export 1's SHA256SUMS, it holds that SHA256SUMS byte for byte; the server's catalog has it at the same entry;
#      the server's ledger check after the exports is OK
#   4. negative controls on copies of export 2: a file and its SHA256SUMS line changed (only the signature catches
#      it: CRITICAL), SHA256SUMS.sig removed (UNKNOWN, never OK), SHA256SUMS.sig naming another key (CRITICAL),
#      export 1's record changed (CRITICAL in both verifiers)
#   5. for information, not in the verdict: SHA256SUMS.sig checked with openssl, when it has Ed25519 (OpenSSL 3 or later)
#   6. (T29, ADR-007 C18.6) a single-bundle export of a restore test's bundle: sha256sum -c, gaia evidence verify
#      -export OK with a valid signature and EXPORT.json's record audit_only; a copy with a bundle file changed CRITICAL
#
# Usage:
#   lab/lab-vm/t28-export.sh local [OUT]   any machine with Go, python3, jq, sha256sum (or shasum): a test server in
#                                          `go test` (fixture TestT28ExportFixture) makes the exports
#   lab/lab-vm/t28-export.sh lab           on gaia-lab, as root: server B (the upgrade-path server, upgraded to this
#                                          version with t17-drill.sh upgrade B_IP), as the drill's admin through
#                                          lab/tools/softkey (its key in software makes the step-ups)
# Writes OUT/gate.json (local: default $TMPDIR/t28-export-<time>; lab: /root/t28/out/export-<time>).
# Exit: 0 OK / 2 CRITICAL / 3 UNKNOWN (also when the harness failed).
set -euo pipefail

mode=${1:-}
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
PYV=$repo_root/docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py
stamp=$(date -u +%Y%m%dT%H%M%SZ)
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

for bin in jq python3 tar go; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin"; exit 3; }
done
if command -v sha256sum >/dev/null; then
	sha() { sha256sum "$@"; }
else
	sha() { shasum -a 256 "$@"; }
fi
hash_of() { sha "$1" | cut -d' ' -f1; }

case "$mode" in
local)
	OUT=${2:-${TMPDIR:-/tmp}/t28-export-$stamp}
	S=$OUT/server
	mkdir -p "$OUT" "$S" "$OUT/bin"
	exec > >(tee -a "$OUT/export.log") 2>&1
	echo "== local: this tree's gaia, a test server in go test (fixture)"
	go build -C "$repo_root" -o "$OUT/bin/gaia" ./cmd/gaia
	GAIA=$OUT/bin/gaia
	GAIA_T28_OUT=$S go test -C "$repo_root" ./internal/server -run '^TestT28ExportFixture$' -count=1
	;;
lab)
	((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
	D=/root/t17
	WS=$D/ws
	OUT=/root/t28/out/export-$stamp
	S=$OUT/server
	mkdir -p "$S"
	exec > >(tee -a "$OUT/export.log") 2>&1
	[[ -x $WS/bin/gaia ]] || { echo "UNKNOWN: no workstation CLI at $WS/bin/gaia (run t17-drill.sh upgrade first)"; exit 3; }
	GAIA=$WS/bin/gaia
	go build -C "$repo_root" -o "$repo_root/bin/softkey" ./lab/tools/softkey
	ws() { HOME=$WS "$WS/bin/gaia" "$@"; }
	sk() { "$repo_root/bin/softkey" call -cli-config "$WS/.config/gaia/cli.json" -key "$D/secrets/admin-key.json" "$@"; }
	if ! ws whoami >/dev/null 2>&1; then
		"$repo_root/bin/softkey" login -cli-config "$WS/.config/gaia/cli.json" -server "$(jq -r .server "$WS/.config/gaia/cli.json")" \
			-fingerprint "$(jq -r .result.tls_certificate_sha256 "$D/out/b-restore-state.json")" -user admin \
			-password-file "$D/secrets/admin.pw" -key "$D/secrets/admin-key.json"
	fi
	echo "== lab: server B ($(ws whoami -json | jq -r '"\(.username), \(.role)"'))"
	ws system -json >"$S/system.json"
	ws daily-reports list limit=1 -json >"$S/daily-reports.json"
	for n in 1 2; do
		sk -o "$S/export-$n.tar.gz" GET /api/v1/evidence-export
		# The record is named in the export's README (the server sends it in X-Gaia-Export-Bundle too).
		id=$(tar -xzOf "$S/export-$n.tar.gz" --wildcards '*/README.txt' | grep -o 'evidence bundle export-[0-9TZ]*-[0-9a-f]*' | head -1 | cut -d' ' -f3)
		jq -n --arg f "export-$n.tar.gz" --arg id "$id" --argjson bytes "$(wc -c <"$S/export-$n.tar.gz")" \
			'{command: "evidence", file: $f, bytes: $bytes, recorded_as: $id}' >"$S/export-$n.json"
	done
	ws ledger check -json >"$S/ledger-check.json" || true # its exit code is the ledger's verdict, judged below
	ws evidence list limit=10 -json >"$S/evidence-list.json"
	tid=$(jq -r '[.[] | select(.test_id != null)][0].id // empty' "$S/evidence-list.json")
	[[ -n $tid ]] || { echo "UNKNOWN: no restore test bundle among the newest 10 evidence bundles"; exit 3; }
	sk -o "$S/single-1.tar.gz" GET "/api/v1/evidence/$tid/export"
	;;
*)
	sed -n '2,34p' "$0"
	exit 3
	;;
esac
jq -c '{version, schema_version, evidence_key_id}' "$S/system.json"

# pyv ARGS: the independent verifier, isolated (-I); its exit code in $rc.
pyv() { rc=0; python3 -I "$PYV" "$@" || rc=$?; }
# gv ARGS: gaia evidence verify; its exit code in $rc.
gv() { rc=0; "$GAIA" evidence verify "$@" || rc=$?; }
# codes FILE: the finding codes of either verifier (gaia: objects with .code; verify.py: strings)
codes() { jq -c '[(.findings // [])[] | if type == "string" then . else .code end] | unique' "$1"; }
# anchor_id FILE: the key_id of a base64 raw key (spec §2: the SHA-256 of the 32 bytes)
anchor_id() { python3 -I -c 'import base64,hashlib,sys; print(hashlib.sha256(base64.b64decode(open(sys.argv[1]).read().strip(), validate=True)).hexdigest())' "$1"; }

for n in 1 2; do
	mkdir -p "$OUT/x$n" && tar -C "$OUT/x$n" -xzf "$S/export-$n.tar.gz"
done
X1=$(find "$OUT/x1" -mindepth 1 -maxdepth 1 -type d)
X2=$(find "$OUT/x2" -mindepth 1 -maxdepth 1 -type d)
ID1=$(jq -r .recorded_as "$S/export-1.json")
ID2=$(jq -r .recorded_as "$S/export-2.json")
[[ $ID1 == export-* && $ID2 == export-* ]] || { echo "UNKNOWN: the exports do not name their records ($ID1, $ID2)"; exit 3; }
(cd "$X2" && find . -type f | sort | sed -n '1,12p;$p') | tee "$OUT/contents-2.txt"

echo "== 1. SHA256SUMS and key_ids"
sums_ok=true
for x in "$X1" "$X2"; do
	(cd "$x" && sha -c --quiet SHA256SUMS) || sums_ok=false
done
server_id=$(jq -r .evidence_key_id "$S/system.json")
report_id=$(jq -r '.[0].report.evidence_key_id // empty' "$S/daily-reports.json")
anchor1=$(anchor_id "$X1/anchor.pub")
anchor2=$(anchor_id "$X2/anchor.pub")
sig1=$(jq -r .key_id "$X1/SHA256SUMS.sig")
sig2=$(jq -r .key_id "$X2/SHA256SUMS.sig")
echo "server $server_id, daily report $report_id, anchors $anchor1 $anchor2, SHA256SUMS.sig $sig1 $sig2"

echo "== 2. both verifiers on both exports"
head_of() { find "$1" -maxdepth 1 -name 'ledger-head-*.json' | sort | head -1; }
H1=$(head_of "$X1")
H2=$(head_of "$X2")
[[ -n $H1 && -n $H2 ]] || { echo "UNKNOWN: an export has no ledger head (no daily report on the server)"; exit 3; }
gv -pubkey "$X1/anchor.pub" -export "$X1"
gv -pubkey "$X1/anchor.pub" -export "$X1" -json >"$OUT/gaia-verify-1.json" || true
g1=$rc
gv -pubkey "$X2/anchor.pub" -export "$X2"
gv -pubkey "$X2/anchor.pub" -export "$X2" -json >"$OUT/gaia-verify-2.json" || true
g2=$rc
pyv ledger "$X1/ledger.jsonl" "$X1/bundles" --key "$X1/anchor.pub" --head "$H1" --json >"$OUT/python-verify-1.json"
p1=$rc
pyv ledger "$X2/ledger.jsonl" "$X2/bundles" --key "$X2/anchor.pub" --head "$H2" --json >"$OUT/python-verify-2.json"
p2=$rc
cat "$OUT/python-verify-2.json"
echo "gaia: exit $g1 $g2, verify.py: exit $p1 $p2"

echo "== 3. export 1's record ($ID1), with jq and sha256sum only"
R=$X2/bundles/$ID1
sums1=$(hash_of "$X1/SHA256SUMS")
man_sha=$(hash_of "$R/manifest.json")
entry=$(jq -c --arg id "$ID1" '.payload | fromjson | select(.type == "bundle" and .bundle_id == $id) | {seq, bundle_sha256, subject, verdict, key_id}' "$X2/ledger.jsonl")
echo "entry: $entry"
jq '{bundle_id, created, key_id, subject, export, files}' "$R/manifest.json" | tee "$OUT/record-manifest.json"
same_sums=false
cmp -s "$R/export/SHA256SUMS" "$X1/SHA256SUMS" && same_sums=true
catalog=$(jq -c --arg id "$ID1" '[.[] | select(.bundle_id == $id) | {ledger_seq, bundle_sha256, verdict, test_id, vm_id, migration_check_id}] | first // null' "$S/evidence-list.json")
listed=$(jq -c --arg id "$ID1" '[.report.recorded_exports[] | select(.bundle_id == $id)] | first // null' "$OUT/gaia-verify-2.json")
echo "catalog: $catalog"
echo "gaia evidence verify lists: $listed"

echo "== 4. negative controls on copies of export 2"
neg() { N=$OUT/neg-$1 && rm -rf "$N" && cp -a "$X2" "$N"; }
negrun() { # NAME: both verifiers on $N, results in neg-NAME-{gaia,python}.json
	gv -pubkey "$N/anchor.pub" -export "$N" -json >"$OUT/neg-$1-gaia.json" || true
	local g=$rc
	pyv ledger "$N/ledger.jsonl" "$N/bundles" --key "$N/anchor.pub" --head "$(head_of "$N")" --json >"$OUT/neg-$1-python.json"
	jq -n --argjson g "$g" --argjson p "$rc" --argjson gc "$(codes "$OUT/neg-$1-gaia.json")" --argjson pc "$(codes "$OUT/neg-$1-python.json")" \
		'{gaia: {exit: $g, codes: $gc}, python: {exit: $p, codes: $pc}}' >"$OUT/neg-$1.json"
	echo "$1: $(jq -c . "$OUT/neg-$1.json")"
}
neg readme # a file and its SHA256SUMS line changed: sha256sum -c and the ledger see nothing, the signature does
printf 'changed on the way\n' >>"$N/README.txt"
python3 -I -c 'import hashlib,sys
p,n=sys.argv[1],"README.txt"
h=hashlib.sha256(open(p+"/"+n,"rb").read()).hexdigest()
ls=open(p+"/SHA256SUMS").read().splitlines(True)
open(p+"/SHA256SUMS","w").write("".join(h+"  "+n+"\n" if l.endswith("  "+n+"\n") else l for l in ls))' "$N"
readme_sums_ok=true
(cd "$N" && sha -c --quiet SHA256SUMS) || readme_sums_ok=false
negrun readme
neg nosig
rm "$N/SHA256SUMS.sig"
negrun nosig
neg otherkey # SHA256SUMS.sig naming another key (a key made here: an attacker's)
"$GAIA" evidence keygen -key "$OUT/other.pem" -pub "$OUT/other.pub" >/dev/null
python3 -I -c 'import base64,hashlib,json,sys
pem=open(sys.argv[2]).read().split("-----")[2]
raw=base64.b64decode("".join(pem.split()))[-32:]
s=json.load(open(sys.argv[1]))
s["public_key"]=base64.b64encode(raw).decode(); s["key_id"]=hashlib.sha256(raw).hexdigest()
open(sys.argv[1],"w").write(json.dumps(s, indent=2)+"\n")' "$N/SHA256SUMS.sig" "$OUT/other.pub"
negrun otherkey
neg record # export 1's record changed in export 2: v1 itself catches it
printf 'x' >>"$N/bundles/$ID1/export/SHA256SUMS"
negrun record

echo "== 5. SHA256SUMS.sig with openssl (information)"
openssl_result="not available"
if command -v openssl >/dev/null && openssl version | grep -Eq '^OpenSSL ([3-9]|[1-9][0-9])'; then
	python3 -I -c 'import base64,json,sys
s=json.load(open(sys.argv[1]))
open(sys.argv[2],"wb").write(bytes.fromhex("302a300506032b6570032100")+base64.b64decode(s["public_key"]))
open(sys.argv[3],"wb").write(base64.b64decode(s["signature"]))' "$X2/SHA256SUMS.sig" "$OUT/sig-pub.der" "$OUT/sig.bin"
	if openssl pkeyutl -verify -pubin -inkey "$OUT/sig-pub.der" -keyform DER -rawin -in "$X2/SHA256SUMS" -sigfile "$OUT/sig.bin" >"$OUT/openssl.txt" 2>&1; then
		openssl_result=OK
	else
		openssl_result="FAILED: $(tr '\n' ' ' <"$OUT/openssl.txt")"
	fi
fi
echo "openssl: $openssl_result"

echo "== 6. the single-bundle export (ADR-007 C18.6)"
mkdir -p "$OUT/xs" && tar -C "$OUT/xs" -xzf "$S/single-1.tar.gz"
XS=$(find "$OUT/xs" -mindepth 1 -maxdepth 1 -type d)
single_sums_ok=true
(cd "$XS" && sha -c --quiet SHA256SUMS) || single_sums_ok=false
gv -pubkey "$XS/anchor.pub" -export "$XS"
gv -pubkey "$XS/anchor.pub" -export "$XS" -json >"$OUT/gaia-verify-single.json" || true
gs=$rc
N=$OUT/neg-single && rm -rf "$N" && cp -a "$XS" "$N"
printf 'x' >>"$(find "$N/bundles" -mindepth 2 -maxdepth 2 -name manifest.json | head -1)"
gv -pubkey "$N/anchor.pub" -export "$N" -json >"$OUT/neg-single-gaia.json" || true
jq -n --argjson g "$rc" --argjson gc "$(codes "$OUT/neg-single-gaia.json")" '{gaia: {exit: $g, codes: $gc}}' >"$OUT/neg-single.json"
echo "single: exit $gs; bundle changed: $(jq -c . "$OUT/neg-single.json")"

jq -n --arg mode "$mode" --slurpfile sys "$S/system.json" --slurpfile lc "$S/ledger-check.json" \
	--slurpfile gs "$OUT/gaia-verify-single.json" --slurpfile ns "$OUT/neg-single.json" --argjson gsrc "$gs" --argjson single_sums_ok "$single_sums_ok" \
	--slurpfile g1 "$OUT/gaia-verify-1.json" --slurpfile g2 "$OUT/gaia-verify-2.json" \
	--slurpfile p1 "$OUT/python-verify-1.json" --slurpfile p2 "$OUT/python-verify-2.json" \
	--slurpfile man "$OUT/record-manifest.json" \
	--slurpfile nr "$OUT/neg-readme.json" --slurpfile nn "$OUT/neg-nosig.json" --slurpfile nk "$OUT/neg-otherkey.json" --slurpfile nc "$OUT/neg-record.json" \
	--argjson sums_ok "$sums_ok" --argjson readme_sums_ok "$readme_sums_ok" --argjson same_sums "$same_sums" \
	--arg server_id "$server_id" --arg report_id "$report_id" --arg anchor1 "$anchor1" --arg anchor2 "$anchor2" --arg sig1 "$sig1" --arg sig2 "$sig2" \
	--argjson g1rc "$g1" --argjson g2rc "$g2" --argjson p1rc "$p1" --argjson p2rc "$p2" \
	--arg id1 "$ID1" --arg id2 "$ID2" --arg sums1 "$sums1" --arg man_sha "$man_sha" --argjson entry "${entry:-null}" \
	--argjson catalog "$catalog" --argjson listed "$listed" --arg openssl "$openssl_result" '
	{mode: $mode, server: ($sys[0] | {version, schema_version}),
	 key_ids: {server: $server_id, daily_report: $report_id, anchor: [$anchor1, $anchor2], sha256sums_sig: [$sig1, $sig2]},
	 sha256sums_ok: $sums_ok,
	 verify: {export_1: {gaia: {exit: $g1rc, verdict: $g1[0].verdict}, python: {exit: $p1rc, verdict: $p1[0].verdict, findings: $p1[0].findings}},
	          export_2: {gaia: {exit: $g2rc, verdict: $g2[0].verdict, sums: ($g2[0].report.sums | {listed, matching, signature_valid, key_id})},
	                     python: {exit: $p2rc, verdict: $p2[0].verdict, findings: $p2[0].findings}}},
	 record: {bundle_id: $id1, next_export_record: $id2, ledger_entry: $entry, manifest_sha256: $man_sha, export_1_sha256sums: $sums1,
	          manifest_subject: $man[0].subject, manifest_export: $man[0].export, holds_export_1_sha256sums: $same_sums,
	          server_catalog: $catalog, listed_by_gaia_verify: $listed},
	 server_ledger_check: ($lc[0] | {verdict, kind, entries, head_seq}),
	 negative: {readme_and_its_sum_changed: ($nr[0] + {sha256sum_c_ok: $readme_sums_ok}), signature_removed: $nn[0],
	            signature_names_other_key: $nk[0], record_changed: $nc[0]},
	 single_bundle: {sha256sums_ok: $single_sums_ok,
	                 gaia: {exit: $gsrc, verdict: $gs[0].verdict, record: ($gs[0].report.record | {scope, bundle, state}),
	                        sums: ($gs[0].report.sums | {listed, matching, signature_valid, key_id}), left_out: ($gs[0].report.ledger.left_out // 0)},
	                 bundle_changed: $ns[0]},
	 openssl_sha256sums_sig: $openssl}
	| . + {pass: (
	    .sha256sums_ok
	    and ([.key_ids.server, .key_ids.daily_report] + .key_ids.anchor + .key_ids.sha256sums_sig | unique | length == 1)
	    and .verify.export_1.gaia.exit == 0 and .verify.export_1.gaia.verdict == "OK"
	    and .verify.export_2.gaia.exit == 0 and .verify.export_2.gaia.verdict == "OK"
	    and .verify.export_1.python.exit == 0 and .verify.export_1.python.verdict == "OK"
	    and .verify.export_2.python.exit == 0 and .verify.export_2.python.verdict == "OK"
	    and .verify.export_2.gaia.sums.signature_valid
	    and .record.ledger_entry != null and .record.ledger_entry.bundle_sha256 == .record.manifest_sha256
	    and .record.ledger_entry.verdict == "OK"
	    and .record.manifest_subject.kind == "evidence_export" and .record.manifest_subject.user != ""
	    and .record.manifest_subject.sha256sums_sha256 == .record.export_1_sha256sums
	    and .record.manifest_export.sha256sums_sha256 == .record.export_1_sha256sums
	    and .record.manifest_export.scope.kind == "all" and .record.manifest_export.period.from != "" and .record.manifest_export.period.to != ""
	    and .record.holds_export_1_sha256sums
	    and .record.server_catalog != null and .record.server_catalog.ledger_seq == .record.ledger_entry.seq
	    and .record.listed_by_gaia_verify != null and .record.listed_by_gaia_verify.sha256sums_sha256 == .record.export_1_sha256sums
	    and .server_ledger_check.verdict == "OK"
	    and .negative.readme_and_its_sum_changed.sha256sum_c_ok
	    and .negative.readme_and_its_sum_changed.gaia.exit == 2
	    and (.negative.readme_and_its_sum_changed.gaia.codes | index("sums_signature_invalid")) != null
	    and .negative.signature_removed.gaia.exit == 3
	    and .negative.signature_removed.gaia.codes == ["sums_signature_missing"]
	    and .negative.signature_names_other_key.gaia.exit == 2
	    and (.negative.signature_names_other_key.gaia.codes | index("sums_key_mismatch")) != null
	    and .negative.record_changed.gaia.exit == 2 and .negative.record_changed.python.exit == 2
	    and (.negative.record_changed.python.codes | index("file_modified")) != null
	    and .single_bundle.sha256sums_ok
	    and .single_bundle.gaia.exit == 0 and .single_bundle.gaia.verdict == "OK"
	    and .single_bundle.gaia.record.scope == "bundle" and .single_bundle.gaia.record.state == "audit_only"
	    and .single_bundle.gaia.sums.signature_valid and .single_bundle.gaia.sums.key_id == .key_ids.server
	    and .single_bundle.bundle_changed.gaia.exit == 2
	    and (.single_bundle.bundle_changed.gaia.codes | index("sums_file_modified")) != null)}
	| . + {verdict: (if .pass then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json"
echo "out: $OUT"
[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
