#!/usr/bin/env bash
# fixture: T26's upgrade of server B (VM 9105, the upgrade path) found that a server whose host name has no domain did
# not start at schema 11 (the default public_url, https://gaia-t17-b:8443, cannot be WebAuthn's relying party). This
# brings B back the way install.md step 12 now says, with the fixed package: public_url set in server.json, the
# package installed, the service up. Then it checks that the 0.1.x-era CLI session the drill still holds answers 401
# (ADR-008 §9). Writes OUT/b-recover.json. Not part of the product.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t26-b-recover.sh B_IP OUT_DIR
#                               lab/lab-vm/t26-b-recover.sh B_IP drill   waits until B's VM is OK again after the
#                                 downtime (the drill wants no status change during an upgrade), then runs
#                                 t17-drill.sh upgrade (RUN=t26-schema11b)
set -euo pipefail
B=${1:?B_IP}
OUT=${2:?OUT_DIR}
D=/root/t17
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
if [[ "$OUT" == drill ]]; then
	go build -C "$repo_root" -o "$repo_root/bin/softkey" ./lab/tools/softkey
	"$repo_root/bin/softkey" login -cli-config "$D/ws/.config/gaia/cli.json" -server "https://$B:8443" \
		-fingerprint "$(jq -r .result.tls_certificate_sha256 "$D/out/b-restore-state.json")" -user admin \
		-password-file "$D/secrets/admin.pw" -key "$D/secrets/admin-key.json"
	for ((i = 0; i < 120; i++)); do
		s=$(HOME=$D/ws "$D/ws/bin/gaia" status -json | jq -r '.[0].status // .status // empty' 2>/dev/null || true)
		echo "$(date -u +%T) B status: $s"
		[[ "$s" == OK ]] && break
		sleep 30
	done
	RUN=t26-schema11b exec "$repo_root/lab/lab-vm/t17-drill.sh" upgrade "$B"
fi
sshopts=(-i "$D/ssh/id_ed25519" -o UserKnownHostsFile="$D/ssh/known_hosts" -o BatchMode=yes -o ConnectTimeout=10)
mkdir -p "$OUT"
make -C "$repo_root" -s dist | tail -1
deb=$(ls "$repo_root"/dist/gaia_*_amd64.deb)
scp -q "${sshopts[@]}" "$deb" "$repo_root/dist/SHA256SUMS" "gaia@$B:/tmp/"
ssh "${sshopts[@]}" "gaia@$B" sudo bash -s -- "$(basename "$deb")" <<'EOS' | tee "$OUT/b-recover-step12.txt"
set -euo pipefail
echo "gaia before: $(systemctl is-active gaia || true)"
cfg=/etc/gaia/server.json
# install.md step 12: public_url with a DNS name. A server has python3 (cloud-init), not always jq.
python3 -I - "$cfg" "https://$(hostname -s).lab.test:8443" <<'PY'
import json, os, re, shutil, sys
cfg, url = sys.argv[1], sys.argv[2]
c = json.load(open(cfg))
if not re.match(r"^https://[^/:]+\.[^/:]+", c.get("public_url") or ""):
    c["public_url"] = url
    tmp = cfg + ".new"
    json.dump(c, open(tmp, "w"), indent=2)
    shutil.copymode(cfg, tmp)
    st = os.stat(cfg)
    os.chown(tmp, st.st_uid, st.st_gid)
    os.replace(tmp, cfg)
print("public_url:", c["public_url"])
PY
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
