#!/usr/bin/env bash
# fixture: T26's upgrade of server B (VM 9105, the upgrade path) found that a server whose host name has no domain did
# not start at schema 11 (the default public_url, https://gaia-t17-b:8443, cannot be WebAuthn's relying party). This
# brings B back the way install.md step 12 now says, with the fixed package: public_url set in server.json, the
# package installed, the service up. Then it checks that the 0.1.x-era CLI session the drill still holds answers 401
# (ADR-008 §9). Writes OUT/b-recover.json. Not part of the product.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t26-b-recover.sh B_IP OUT_DIR
set -euo pipefail
B=${1:?B_IP}
OUT=${2:?OUT_DIR}
D=/root/t17
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
sshopts=(-i "$D/ssh/id_ed25519" -o UserKnownHostsFile="$D/ssh/known_hosts" -o BatchMode=yes -o ConnectTimeout=10)
mkdir -p "$OUT"
make -C "$repo_root" -s dist | tail -1
deb=$(ls "$repo_root"/dist/gaia_*_amd64.deb)
scp -q "${sshopts[@]}" "$deb" "$repo_root/dist/SHA256SUMS" "gaia@$B:/tmp/"
ssh "${sshopts[@]}" "gaia@$B" sudo bash -s -- "$(basename "$deb")" <<'EOS' | tee "$OUT/b-recover-step12.txt"
set -euo pipefail
echo "gaia before: $(systemctl is-active gaia || true)"
cfg=/etc/gaia/server.json
if ! jq -e '.public_url | select(. != null and test("^https://[^/:]+\\.[^/:]+"))' "$cfg" >/dev/null; then # install.md step 12
	jq --arg u "https://$(hostname -s).lab.test:8443" '. + {public_url: $u}' "$cfg" >"$cfg.new"
	chmod --reference="$cfg" "$cfg.new" && chown --reference="$cfg" "$cfg.new" && mv "$cfg.new" "$cfg"
fi
jq -r '"public_url: " + .public_url' "$cfg"
cd /tmp && sha256sum -c --ignore-missing SHA256SUMS
DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y --reinstall "./$1" >/dev/null
sleep 3 && systemctl is-active gaia gaia-sandbox && gaia version
EOS
for ((i = 0; i < 30; i++)); do curl -fsk -o /dev/null "https://$B:8443/api/v1/health" && break; sleep 2; done
old=$(jq -r .token "$D/ws/.config/gaia/cli.json")
code=$(curl -sk -o "$OUT/b-old-session.json" -w '%{http_code}' -H "Authorization: Bearer $old" "https://$B:8443/api/v1/me")
unset old
sys=$(curl -sk "https://$B:8443/api/v1/health")
jq -n --arg c "$code" --slurpfile a "$OUT/b-old-session.json" --argjson h "$sys" \
	'{old_session_status: ($c | tonumber), old_session_answer: $a[0], health: $h, pass: (($c | tonumber) == 401 and $h.status == "OK")}' | tee "$OUT/b-recover.json"
