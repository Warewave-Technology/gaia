#!/usr/bin/env bash
# fixture: the T26 gate on gaia-lab (CLAUDE.md T26; ADR-008). Not part of the product.
#
#   rules  one test per ADR-008 rule (go test -json: TestADR008Local, TestADR008OIDC with a mock IdP, and the store's and
#          auth's tests of the same rules), as a table rule -> test -> result (rules.md, rules.json)
#   lab    lab/tools/t26gate against a server of its own (gaia-t26, https://gaia-lab.test:8446) and the lab Keycloak
#          (lab/lab-vm/t26-keycloak.sh): the role matrix with local and OIDC users (f), removal from the group at the IdP
#          (d), Keycloak stopped (e), the CLI's device grant, a local TOTP sign-in
#
# Usage (on gaia-lab, as root): systemd-run --unit=gaia-t26 --collect -E HOME=/root lab/lab-vm/t26-gate.sh [OUT_DIR]
#                               lab/lab-vm/t26-gate.sh cleanup        (Keycloak stays: t26-keycloak.sh down)
# QUICK=1 skips d and e (about 20 minutes). Exit 0 OK, 2 CRITICAL, 3 UNKNOWN.
set -euo pipefail

OUT_DIR=${1:-/root/t26-$(date -u +%Y%m%dT%H%M%SZ)}
SRV_USER=gaia-t26
SRV_HOME=/var/lib/gaia-t26
SRV_BIN=/usr/local/sbin/gaia-t26
UNIT=gaia-t26-server
SINK_UNIT=gaia-t26-sink
SINK_HTTPS=127.0.0.1:2446
PUBLIC=https://gaia-lab.test:8446
WORK=/root/t26w
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR
((EUID == 0)) || { echo "run as root"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
section() { printf '\n== %s\n' "$*" >&2; }

cleanup() {
	for u in "$UNIT" "$SINK_UNIT"; do systemctl stop "$u" 2>/dev/null || true; done
	userdel "$SRV_USER" 2>/dev/null || true
	rm -rf "$SRV_HOME" "$SRV_BIN" "$WORK"
}
if [[ "${1:-}" == cleanup ]]; then
	cleanup
	exit 0
fi

section "build"
cleanup
mkdir -p "$OUT_DIR" "$WORK"
owner=$(stat -c %U "$repo_root")
asowner() { if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi; }
asowner make -C "$repo_root" -s build
asowner go build -C "$repo_root" -o "$BIN/notify-sink" ./lab/tools/notify-sink
asowner go build -C "$repo_root" -o "$BIN/t26gate" ./lab/tools/t26gate
git -C "$repo_root" rev-parse --short=12 HEAD >"$OUT_DIR/commit.txt"
free -m | tee "$OUT_DIR/memory.txt" >&2

section "rules: one test per ADR-008 rule"
asowner bash -c "cd '$repo_root' && go test -count=1 -json -run 'TestADR008Local|TestADR008OIDC|TestRoleMatrix|TestBrowserSession|TestPageRoles' ./internal/server
	go test -count=1 -json -run 'TestUsersAndSessions|TestSigninFailures|TestAPITokens|TestMigrationFromPreviousSchema' ./internal/store
	go test -count=1 -json ./internal/auth" >"$OUT_DIR/rules-test.jsonl" || true
python3 -I - "$OUT_DIR" <<'EOF'
import json, re, sys
out = sys.argv[1]
res = {}
for line in open(f"{out}/rules-test.jsonl"):
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("Action") in ("pass", "fail", "skip") and e.get("Test"):
        res[(e["Package"].split("/")[-1], e["Test"])] = (e["Action"], e.get("Elapsed", 0))
rows = []
for (pkg, test), (act, el) in sorted(res.items()):
    name = test.split("/", 1)[1].replace("_", " ") if "/" in test else ""
    m = re.match(r"§(\d+)", name)
    rows.append({"rule": f"§{m[1]}" if m else "", "test": f"{pkg}.{test.split('/')[0]}", "case": name, "result": act.upper(), "s": el})
ok = all(r["result"] == "PASS" for r in rows) and len(rows) > 40
json.dump({"pass": ok, "tests": len(rows), "failed": [r for r in rows if r["result"] != "PASS"], "rows": rows}, open(f"{out}/rules.json", "w"), indent=2)
with open(f"{out}/rules.md", "w") as f:
    f.write("| Rule | Test | Case | Result |\n|---|---|---|---|\n")
    for r in sorted(rows, key=lambda r: (int(r["rule"][1:]) if r["rule"] else 99, r["test"], r["case"])):
        f.write(f"| {r['rule'] or '-'} | `{r['test']}` | {r['case'] or '(whole test)'} | {r['result']} |\n")
print(json.dumps({"pass": ok, "tests": len(rows), "failed": len([r for r in rows if r["result"] != "PASS"])}))
EOF

section "lab IdP"
"$repo_root/lab/lab-vm/t26-keycloak.sh" up | tee "$OUT_DIR/keycloak.json" >&2

section "notify-sink (webhooks)"
ca=$WORK/ca
install -d -m 0700 "$ca"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/ca.key" 2>/dev/null
openssl req -x509 -new -key "$ca/ca.key" -sha256 -days 7 -subj "/CN=gaia-t26 lab CA" -out "$ca/ca.crt"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/server.key" 2>/dev/null
openssl req -new -key "$ca/server.key" -subj "/CN=127.0.0.1" -out "$ca/server.csr"
printf 'subjectAltName=IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >"$ca/ext"
openssl x509 -req -in "$ca/server.csr" -CA "$ca/ca.crt" -CAkey "$ca/ca.key" -CAcreateserial -days 7 -sha256 -extfile "$ca/ext" -out "$ca/server.crt" 2>/dev/null
printf 'SINK_SMTP_PASSWORD=%s\n' "$(head -c 18 /dev/urandom | base64 | tr -d '/+=')" | install -m 0600 /dev/stdin "$ca/sink.env"
mkdir -p "$OUT_DIR/sink"
systemd-run --unit="$SINK_UNIT" -p EnvironmentFile="$ca/sink.env" "$BIN/notify-sink" -dir "$OUT_DIR/sink" -smtp 127.0.0.1:2588 \
	-https "$SINK_HTTPS" -cert "$ca/server.crt" -key "$ca/server.key" -smtp-user gaia
for ((i = 0; i < 30; i++)); do
	curl -fs --cacert "$ca/ca.crt" -X POST -d '{}' "https://$SINK_HTTPS/probe" -o /dev/null && break
	sleep 1
done
rm -f "$OUT_DIR/sink/hook/"*.json

section "gaia server $PUBLIC"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/kit.pass"
install -m 0600 -o "$SRV_USER" "$WORK/admin.pw" "$SRV_HOME/admin.pw"
install -m 0600 -o "$SRV_USER" "$WORK/kit.pass" "$SRV_HOME/kit.pass"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" -listen :8446 \
	-hostname gaia-lab.test -public-url "$PUBLIC" -admin admin -password-file "$SRV_HOME/admin.pw" -kit-passphrase-file "$SRV_HOME/kit.pass" \
	-json >"$WORK/server-init.json"
rm -f "$SRV_HOME/admin.pw" "$SRV_HOME/kit.pass"
systemd-run --unit="$UNIT" -p User="$SRV_USER" -p Group="$SRV_USER" -p NoNewPrivileges=yes "$SRV_BIN" server run -config "$SRV_HOME/server.json"
for ((i = 0; i < 30; i++)); do curl -fs --cacert "$SRV_HOME/data/tls/cert.pem" "$PUBLIC/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
curl -fs --cacert "$SRV_HOME/data/tls/cert.pem" "$PUBLIC/api/v1/signin-options" | tee "$OUT_DIR/signin-options.json" >&2

section "lab checks (t26gate)"
quick=()
[[ "${QUICK:-}" == 1 ]] && quick=(-quick)
gate_rc=0
"$BIN/t26gate" -api "$PUBLIC" -cert "$SRV_HOME/data/tls/cert.pem" -kc https://gaia-lab.test:8444 -kc-script "$repo_root/lab/lab-vm/t26-keycloak.sh" \
	-admin-pw-file "$WORK/admin.pw" -out "$OUT_DIR" -sink-dir "$OUT_DIR/sink" -sink-url "https://$SINK_HTTPS/hook" -sink-ca "$ca/ca.crt" "${quick[@]}" \
	2> >(tee "$OUT_DIR/t26gate.log" >&2) || gate_rc=$?
journalctl -u "$UNIT" --no-pager -o short-iso >"$OUT_DIR/server.log" 2>/dev/null || true
podman logs gaia-t26-keycloak >"$OUT_DIR/keycloak.log" 2>&1 || true

section "verdict"
python3 -I - "$OUT_DIR" "$gate_rc" <<'EOF' | tee "$OUT_DIR/verdict.json"
import json, sys
out, rc = sys.argv[1], int(sys.argv[2])
rules = json.load(open(f"{out}/rules.json"))
gate = json.load(open(f"{out}/gate.json"))
checks = {k: v["pass"] for k, v in gate.items() if isinstance(v, dict) and "pass" in v}
ok = rules["pass"] and rc == 0 and all(checks.values())
print(json.dumps({"verdict": "OK" if ok else "CRITICAL", "rules": {"pass": rules["pass"], "tests": rules["tests"], "failed": len(rules["failed"])},
                  "lab": checks, "t26gate_exit": rc}, indent=2))
sys.exit(0 if ok else 2)
EOF
