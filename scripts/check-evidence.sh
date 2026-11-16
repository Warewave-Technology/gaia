#!/usr/bin/env bash
# Conformance of `gaia evidence verify` with the Restore Evidence Format v1 (docs/spec/evidence-v1.md, ADR-006):
#   1. every vector in docs/spec/vectors gives exactly its declared verdict, finding codes and exit code;
#   2. the evidence committed before the specification (v0) still verifies;
#   3. a key rotation end to end through the CLI: keygen A and B, seal with A, rotate to B, seal with B, verify
#      against A; A can no longer seal; B is not a trust anchor.
# Usage: scripts/check-evidence.sh [path/to/gaia]   (make check runs it after the build)
set -euo pipefail

GAIA="${1:-bin/gaia}"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"
fails=0
fail() { echo "FAIL: $*"; fails=$((fails + 1)); }
exit_of() { case $1 in OK) echo 0 ;; WARNING) echo 1 ;; CRITICAL) echo 2 ;; *) echo 3 ;; esac }

n=0
for v in docs/spec/vectors/*/; do
	v=${v%/}
	mode=$(jq -r .mode "$v/expected.json")
	args=()
	[[ -f "$v/anchor.pub" ]] && args+=(-pubkey "$v/anchor.pub")
	if [[ "$mode" == bundle ]]; then
		args+=(-bundle "$v/bundle")
	else
		args+=(-ledger "$v/ledger.jsonl" -evidence-dir "$v/bundles")
	fi
	rc=0
	out=$("$GAIA" evidence verify "${args[@]}" -json) || rc=$?
	got=$(jq -c '{verdict, findings: ([.findings[].code] | unique)}' <<<"$out")
	want=$(jq -c '{verdict, findings}' "$v/expected.json")
	[[ "$got" == "$want" ]] || fail "$(basename "$v"): got $got, want $want"
	[[ "$rc" == "$(exit_of "$(jq -r .verdict "$v/expected.json")")" ]] || fail "$(basename "$v"): exit $rc"
	n=$((n + 1))
done
echo "vectors: $n checked"

for d in docs/reports/evidence/2026-11-16-T7 docs/reports/evidence/2026-11-16-T8 docs/reports/evidence/2026-11-16-T11 \
	docs/reports/evidence/2026-11-16-T11/first-run-20261116T072606Z; do
	got=$("$GAIA" evidence verify -pubkey "$d/evidence-pub.pem" -ledger "$d/ledger.jsonl" -evidence-dir "$d/evidence" -json |
		jq -c '{verdict, format: .report.format, entries: .report.entries}') || true
	[[ $(jq -r '.verdict + " " + .format' <<<"$got") == "OK v0" ]] || fail "v0 evidence $d: $got"
done
echo "v0 evidence: checked"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
a_id=$("$GAIA" evidence keygen -key "$tmp/a.pem" -pub "$tmp/a.pub" -json | jq -r .key_id)
b_id=$("$GAIA" evidence keygen -key "$tmp/b.pem" -pub "$tmp/b.pub" -json | jq -r .key_id)
mkdir -p "$tmp/ev/test-1" "$tmp/ev/test-2" "$tmp/ev/test-3"
echo "first" >"$tmp/ev/test-1/result.txt"
echo "second" >"$tmp/ev/test-2/result.txt"
echo "third" >"$tmp/ev/test-3/result.txt"
"$GAIA" evidence seal -dir "$tmp/ev/test-1" -key "$tmp/a.pem" -ledger "$tmp/ledger.jsonl" -subject vm1 -verdict OK >/dev/null
"$GAIA" evidence rotate -ledger "$tmp/ledger.jsonl" -key "$tmp/a.pem" -new-pub "$tmp/b.pub" >/dev/null
"$GAIA" evidence seal -dir "$tmp/ev/test-2" -key "$tmp/b.pem" -ledger "$tmp/ledger.jsonl" -subject vm1 -verdict OK >/dev/null
if "$GAIA" evidence seal -dir "$tmp/ev/test-3" -key "$tmp/a.pem" -ledger "$tmp/ledger.jsonl" -subject vm1 -verdict OK >/dev/null 2>&1; then
	fail "rotation: the old key appended after the rotation"
fi
rm -rf "$tmp/ev/test-3" # sealed by A, refused by the ledger
got=$("$GAIA" evidence verify -pubkey "$tmp/a.pub" -ledger "$tmp/ledger.jsonl" -evidence-dir "$tmp/ev" -json |
	jq -c '{verdict, entries: .report.entries, keys: .report.keys, bundle_keys: [.report.bundles[].key_id]}')
want=$(jq -nc --arg a "$a_id" --arg b "$b_id" '{verdict: "OK", entries: 4, keys: [$a, $b], bundle_keys: [$a, $b]}')
[[ "$got" == "$want" ]] || fail "rotation: got $got, want $want"
rc=0
"$GAIA" evidence verify -pubkey "$tmp/b.pub" -ledger "$tmp/ledger.jsonl" -evidence-dir "$tmp/ev" >/dev/null || rc=$?
((rc == 2)) || fail "rotation: the new key accepted as trust anchor (exit $rc)"
echo "key rotation: checked"

((fails == 0)) || { echo "$fails failure(s)"; exit 1; }
echo "OK"
