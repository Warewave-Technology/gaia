#!/usr/bin/env bash
# T28 gate (c), ADR-007 C8: a notification channel made in M2 receives a new event of its category after the upgrade.
#
# fixture: a Gaia server of release 0.1.2 (built from the tag v0.1.2) on gaia-lab, its own user gaia-t28c, the KVM
# host gaia-lab over SSH as gaia-t28c-backup (never gaia-backup: that one is server B's), and notify-sink
# (lab/tools/notify-sink) as an HTTPS webhook receiver on 127.0.0.1 with a CA made for this run.
#   M2 (0.1.2)  two webhook channels from install guide step 8c of 0.1.2: jobs-m2 takes ["job_failed"] (path /jobs),
#               status-m2 takes ["status"] (path /status). Six VMs (T25's Linux guests, shut off) get a policy that
#               tests every 10 minutes; their backups fail (a shut-off domain has no backup job): job_failed reaches
#               jobs-m2 and the status changes reach status-m2, as M2 sends them (positive control).
#   upgrade     the server takes this version in place (install guide step 12: stop, new binary, start); the sandbox
#               helper starts with one slot. Six VMs tested every 10 minutes need about two slots, so the proof capacity
#               is short and the server sends capacity_short: an event 0.1.2 did not have, in the category jobs.
# Gate: before the upgrade jobs-m2 got job_failed and status-m2 got status; after it, the server's database maps
# jobs-m2 to the category jobs and status-m2 to status (each with its M2 list kept and the mapping recorded as a state
# change), capacity_short reaches jobs-m2 with a valid signature and never status-m2. Writes OUT/gate.json.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t28-channel.sh [OUT]     (default /root/t28/channel-<UTC time>)
# Env: KEEP=1 keeps the server. Exit: 0 OK / 2 CRITICAL / 3 UNKNOWN (also when the harness failed).
set -euo pipefail

OUT=${1:-/root/t28/channel-$(date -u +%Y%m%dT%H%M%SZ)}
SRV_USER=gaia-t28c
SRV_HOME=/var/lib/gaia-t28c
UNIT=gaia-t28c-server
SB_UNIT=gaia-t28c-sandbox
SB_HOME=/var/lib/gaia-t28c-sandbox
SB_SOCK=/run/gaia-t28c-sandbox/sandbox.sock
SB_WORK=/var/lib/libvirt/images/gaia-t28c-sandbox
HOST_USER=gaia-t28c-backup
SINK_UNIT=gaia-t28c-sink
SINK=127.0.0.1:2453
API=https://127.0.0.1:8449
VMS=(gaia-t25-lin-1 gaia-t25-lin-2 gaia-t25-lin-3 gaia-t25-lin-4 gaia-t25-lin-5 gaia-t25-lin-6)
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
W=/root/t28c
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR
((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go jq python3 openssl curl virsh git; do command -v "$bin" >/dev/null || { echo "MISSING: $bin"; exit 3; }; done
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

cleanup() {
	for u in "$UNIT" "$SB_UNIT" "$SINK_UNIT"; do systemctl stop "$u" 2>/dev/null || true; done
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	pkill -u "$HOST_USER" 2>/dev/null || true
	userdel -r "$HOST_USER" 2>/dev/null || true
	userdel "$SRV_USER" 2>/dev/null || true
	git -C "$repo_root" worktree remove --force "$W/v0.1.2" 2>/dev/null || true
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$W"
	for vm in "${VMS[@]}"; do
		for c in $(virsh checkpoint-list "$vm" --name 2>/dev/null); do virsh -q checkpoint-delete "$vm" "$c" 2>/dev/null || true; done
	done
	fstrim -a 2>/dev/null || true
}
# shellcheck disable=SC2329 # invoked by the trap
on_exit() {
	for u in "$UNIT" "$SB_UNIT" "$SINK_UNIT"; do journalctl -u "$u" --no-pager -o short-iso-precise >"$OUT/${u#gaia-}.journal" 2>&1 || true; done
	[[ "${KEEP:-}" == 1 ]] || cleanup
}

for vm in "${VMS[@]}"; do
	[[ $(virsh domstate "$vm" 2>/dev/null) == "shut off" ]] || { echo "UNKNOWN: $vm must exist and be shut off (lab/lab-vm/t25-guests.sh)"; exit 3; }
done
free_gb=$(df --output=avail -BG /var/lib | tail -1 | tr -dc 0-9)
((free_gb >= 10)) || { echo "UNKNOWN: ${free_gb} GB free under /var/lib, wants 10"; exit 3; }
cleanup
mkdir -p "$OUT" "$W"
trap on_exit EXIT

log "builds: release 0.1.2 from its tag, and this version"
owner=$(stat -c %U "$repo_root")
asowner() { if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi; }
git -C "$repo_root" worktree add -q --detach "$W/v0.1.2" v0.1.2
asowner go build -C "$W/v0.1.2" -o "$W/gaia-0.1.2" ./cmd/gaia
asowner go build -C "$repo_root" -o "$W/gaia-new" ./cmd/gaia
asowner go build -C "$repo_root" -o "$W/notify-sink" ./lab/tools/notify-sink
{ "$W/gaia-0.1.2" version; "$W/gaia-new" version; git -C "$repo_root" rev-parse HEAD; } | tee "$OUT/versions.txt"

log "notify-sink: HTTPS $SINK"
ca=$W/ca
install -d -m 0700 "$ca"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/ca.key" 2>/dev/null
openssl req -x509 -new -key "$ca/ca.key" -sha256 -days 2 -subj "/CN=gaia-t28c lab CA" -out "$ca/ca.crt"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/server.key" 2>/dev/null
openssl req -new -key "$ca/server.key" -subj "/CN=127.0.0.1" -out "$ca/server.csr"
printf 'subjectAltName=IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >"$ca/ext"
openssl x509 -req -in "$ca/server.csr" -CA "$ca/ca.crt" -CAkey "$ca/ca.key" -CAcreateserial -days 2 -sha256 -extfile "$ca/ext" -out "$ca/server.crt" 2>/dev/null
printf 'SINK_SMTP_PASSWORD=%s\n' "$(head -c 18 /dev/urandom | base64 | tr -d '/+=')" | install -m 0600 /dev/stdin "$ca/sink.env"
mkdir -p "$OUT/sink"
systemd-run --unit="$SINK_UNIT" -p EnvironmentFile="$ca/sink.env" "$W/notify-sink" -dir "$OUT/sink" -smtp 127.0.0.1:2597 \
	-https "$SINK" -cert "$ca/server.crt" -key "$ca/server.key"
for ((i = 0; i < 30; i++)); do curl -fs --cacert "$ca/ca.crt" -X POST -d '{}' "https://$SINK/probe" -o /dev/null && break; sleep 1; done
rm -f "$OUT/sink/hook/"*.json

log "server 0.1.2 (user $SRV_USER, $API)"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$W/gaia-0.1.2" "$SRV_HOME/gaia"
head -c 24 /dev/urandom | base64 | sed 's/^/t28c m2 /' | install -m 0600 -o "$SRV_USER" /dev/stdin "$SRV_HOME/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" /dev/stdin "$SRV_HOME/kit.pass"
runuser -u "$SRV_USER" -- "$SRV_HOME/gaia" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" -listen 127.0.0.1:8449 \
	-hostname 127.0.0.1 -admin admin -password-file "$SRV_HOME/admin.pw" -kit-passphrase-file "$SRV_HOME/kit.pass" -json >"$W/server-init.json"
cert=$(jq -r '.result.tls_certificate_sha256 // .tls_certificate_sha256' "$W/server-init.json")
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$W/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$W/server.json" "$SRV_HOME/server.json"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_HOME/gaia server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" -e "s|gaia-sandbox.service|$SB_UNIT.service|" \
	"$W/v0.1.2/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
sed -e "s|^ExecStart=.*|ExecStart=$SRV_HOME/gaia sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles -slots 1 -memory-mib 2048|" \
	"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
systemctl daemon-reload
systemctl start "$UNIT"
for ((i = 0; i < 30; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
export HOME=$W
m2() { "$W/gaia-0.1.2" "$@" -json; }
m2 login -server "$API" -user admin -fingerprint "$cert" -password-file "$SRV_HOME/admin.pw" >/dev/null
m2 system | jq -c '{version, schema_version}' | tee "$OUT/system-before.json"
jq -n --arg l "$SRV_HOME/repo/main" --rawfile k "$SRV_HOME/kit.pass" '{name: "main", location: $l, kit_passphrase: ($k | rtrimstr("\n"))}' >"$W/repo.json"
m2 api POST /api/v1/repositories "$(cat "$W/repo.json")" >/dev/null
rm -f "$W/repo.json"

log "M2 channels (0.1.2 guide step 8c)"
for c in jobs:job_failed status:status; do
	name=${c%%:*}-m2 ev=${c#*:}
	m2 channels add name="$name" kind=webhook ca_file="$ca/ca.crt" webhook="{\"url\":\"https://$SINK/${c%%:*}\"}" events="[\"$ev\"]" >"$W/channel-$name.json"
	jq -r .webhook_secret "$W/channel-$name.json" | install -m 0600 /dev/stdin "$ca/$name.secret"
	jq -c 'del(.webhook_secret) | {id, name, kind, events, enabled}' "$W/channel-$name.json" | tee -a "$OUT/channels-before.jsonl"
done

log "host and VMs (shut off: their backups fail)"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
m2 system | jq -r .ssh_authorized_keys_line | install -m 0600 -o "$HOST_USER" -g "$HOST_USER" /dev/stdin "/home/$HOST_USER/.ssh/authorized_keys"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(m2 hosts add name=gaia-lab address=127.0.0.1 ssh_user="$HOST_USER" | jq .id)
[[ $(m2 hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
m2 hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
pol=$(m2 policies add name=t28c repository_id=1 backup_interval_s=3600 test_interval_s=600 keep_last=2 keep_daily=0 keep_weekly=0 \
	rpo_target_s=7200 rto_target_s=600 max_proof_age_s=3600 check_template=linux-boot | jq .id)
for vm in "${VMS[@]}"; do m2 vms add host_id="$host_id" name="$vm" policy_id="$pol" | jq -c '{id, name}'; done

hooks() { # hooks PATH EVENT: how many webhooks of EVENT reached PATH
	local n=0 f
	for f in "$OUT"/sink/hook/*.json; do
		[[ -e $f ]] || continue
		jq -e --arg p "$1" --arg e "$2" '.path == $p and .headers["X-Gaia-Event"] == $e' "$f" >/dev/null && n=$((n + 1))
	done
	echo "$n"
}
log "M2: waiting for job_failed at /jobs and status at /status"
for ((i = 0; i < 90; i++)); do
	(($(hooks /jobs job_failed) > 0 && $(hooks /status status) > 0)) && break
	sleep 10
done
before_jobs=$(hooks /jobs job_failed) before_status=$(hooks /status status)
log "before the upgrade: /jobs job_failed $before_jobs, /status status $before_status"
mkdir -p "$OUT/sink-before" && cp "$OUT"/sink/hook/*.json "$OUT/sink-before/" 2>/dev/null || true

log "upgrade in place (install guide step 12) and the sandbox helper with one slot"
systemctl stop "$UNIT"
install -m 0755 "$W/gaia-new" "$SRV_HOME/gaia"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_HOME/gaia server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" -e "s|gaia-sandbox.service gaia-convert.service|$SB_UNIT.service|" \
	-e "s|gaia-sandbox.service|$SB_UNIT.service|" "$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
systemctl daemon-reload
upgraded_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
systemctl start "$SB_UNIT"
for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK ]] && break; sleep 1; done
systemctl start "$UNIT"
for ((i = 0; i < 60; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
curl -fsk "$API/api/v1/health" | tee "$OUT/health-after.json"; echo

log "waiting for capacity_short at /jobs (the capacity is looked at every 5 minutes)"
for ((i = 0; i < 90; i++)); do (($(hooks /jobs capacity_short) > 0)) && break; sleep 10; done
sleep 30 # anything else queued at the same time reaches the sink too

db() { python3 -I - "$SRV_HOME/data/gaia.db" "$@" <<'EOF'
import json, sqlite3, sys
con = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
con.row_factory = sqlite3.Row
q = sys.argv[2]
print(json.dumps([dict(r) for r in con.execute(q, sys.argv[3:])]))
EOF
}
db "SELECT MAX(version) AS version FROM schema_migrations" >"$OUT/schema-after.json"
db "SELECT id, name, enabled, categories, m2_events, daily_report FROM notification_channels ORDER BY id" >"$OUT/channels-after.json"
db "SELECT seq, at, entity, entity_id, from_state, to_state, reason, actor FROM state_changes WHERE entity IN ('notification_channel_categories', 'capacity') ORDER BY seq" >"$OUT/state-changes.json"

# The capacity_short webhook: its signature, sha256=<HMAC-SHA256 of the body with jobs-m2's secret>
sig_ok=false
for f in "$OUT"/sink/hook/*.json; do
	jq -e '.path == "/jobs" and .headers["X-Gaia-Event"] == "capacity_short"' "$f" >/dev/null || continue
	cp "$f" "$OUT/capacity-short-hook.json"
	want=$(jq -r '.headers["X-Gaia-Signature"]' "$f")
	got=sha256=$(jq -j .body "$f" | openssl dgst -sha256 -hmac "$(cat "$ca/jobs-m2.secret")" -r | cut -d' ' -f1)
	[[ "$want" == "$got" ]] && sig_ok=true
	break
done
jq -n --argjson before_jobs "$before_jobs" --argjson before_status "$before_status" --arg upgraded "$upgraded_at" \
	--argjson short_jobs "$(hooks /jobs capacity_short)" --argjson short_status "$(hooks /status capacity_short)" --argjson sig "$sig_ok" \
	--slurpfile ch "$OUT/channels-after.json" --slurpfile sc "$OUT/state-changes.json" --slurpfile sys "$OUT/system-before.json" \
	--slurpfile health "$OUT/health-after.json" --slurpfile schema "$OUT/schema-after.json" '
	def chan(n): $ch[0] | map(select(.name == n)) | first | . + {categories: (.categories | fromjson), m2_events: (.m2_events | fromjson)};
	def mapped(n): [$sc[0][] | select(.entity == "notification_channel_categories" and .entity_id == chan(n).id)];
	{before: {release: $sys[0], jobs_m2_job_failed: $before_jobs, status_m2_status: $before_status, pass: ($before_jobs > 0 and $before_status > 0)},
	 after: {upgraded_at: $upgraded, health: $health[0], schema: $schema[0][0].version, jobs_m2: chan("jobs-m2"), status_m2: chan("status-m2"),
	         mapping_recorded: {jobs_m2: mapped("jobs-m2"), status_m2: mapped("status-m2")},
	         capacity: [$sc[0][] | select(.entity == "capacity")],
	         pass: ((chan("jobs-m2").categories | index("jobs")) != null and chan("jobs-m2").m2_events == ["job_failed"]
	                and (chan("status-m2").categories | index("status")) != null and (chan("status-m2").categories | index("jobs")) == null
	                and chan("status-m2").m2_events == ["status"] and (mapped("jobs-m2") | length) == 1 and (mapped("status-m2") | length) == 1)},
	 new_event: {event: "capacity_short", category: "jobs", reached_jobs_m2: $short_jobs, reached_status_m2: $short_status, signature_valid: $sig,
	             pass: ($short_jobs > 0 and $short_status == 0 and $sig)}}
	| . + {verdict: (if .before.pass and .after.pass and .new_event.pass then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json" |
	jq -c '{verdict, before: .before.pass, after: .after.pass, new_event: .new_event.pass}'
echo "out: $OUT"
[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
