#!/usr/bin/env bash
# T23 rehearsal: the MVP acceptance scenarios (CLAUDE.md T23) run once by script on gaia-lab, before Yiğit runs them
# by hand from docs/t23-acceptance.md on a clean server. Each scenario must show on the status (the API the screens
# use) and in notifications (an SMTP sink and a webhook receiver, lab/tools/notify-sink) with the right status and
# reason. The server runs on gaia-lab itself from the packaged units (as T15/T16/T19/T21), the VMs are the fixture's
# (lab/lab-vm/t23-guests.sh create), the host is gaia-lab over SSH as gaia-t23r-backup.
#
#   baseline    every VM OK (a backup and a passing restore test each)
#   unbootable  gaia-t23-linux2's boot code zeroed in the running guest -> its next test CRITICAL, the VM CRITICAL,
#               a status notification; boot code back -> OK again
#   damage      a byte of a repository pack flipped -> the full verification CRITICAL, integrity and quarantine
#               notifications, the affected VMs CRITICAL; their next backups full (full_backup), tested OK, the failed
#               points forgotten by the admin -> OK
#   sandbox     the sandbox helper stopped -> a test job fails (job_failed), "could not run" on the status, and the VM
#               WARNING once its proof is too old; helper started -> OK
#   checkpoint  gaia-t23-linux2's newest checkpoint deleted on the host -> its next backup full with the reason, a
#               note on the status, a full_backup notification; the status stays OK
#   rebuild     the server lost (stopped, its data and config moved away) and rebuilt with restore-state from the
#               recovery kit and the repository: every VM's status back, the ledger verifies against the last daily
#               report, the channels added again, the next daily report says REBUILT, the next backups continue
#
# Usage (on gaia-lab, as root):
#   systemd-run --unit=gaia-t23r --collect -E HOME=/root lab/lab-vm/t23-rehearsal.sh
#   lab/lab-vm/t23-rehearsal.sh cleanup        the server, its units, user, repository and the host user (not the VMs)
# Env: PHASES (default: all, in the order above), OUT_DIR (/root/t23r-<UTC>), PHASE_TIMEOUT_S (3600).
# Exit: 0 OK / 2 CRITICAL (a scenario showed wrong) / 3 UNKNOWN (not checked, also when the harness failed).
# shellcheck disable=SC2016 # jq programs in single quotes
set -euo pipefail

PHASES="${PHASES:-baseline unbootable damage sandbox checkpoint rebuild}"
OUT_DIR="${OUT_DIR:-/root/t23r-$(date -u +%Y%m%dT%H%M%SZ)}"
PHASE_TIMEOUT_S="${PHASE_TIMEOUT_S:-3600}"
SRV_USER=gaia-t23r
SRV_HOME=/var/lib/gaia-t23r
SRV_BIN=/usr/local/sbin/gaia-t23r
UNIT=gaia-t23r-server
SB_UNIT=gaia-t23r-sandbox
SB_HOME=/var/lib/gaia-t23r-sandbox
SB_SOCK=/run/gaia-t23r-sandbox/sandbox.sock
SB_WORK=/var/lib/libvirt/images/gaia-t23r-sandbox
SINK_UNIT=gaia-t23r-sink
SINK_SMTP=127.0.0.1:2587
SINK_HTTPS=127.0.0.1:2443
HOST_USER=gaia-t23r-backup
API=https://127.0.0.1:8443
WORK=/root/t23r
VMS=(gaia-t23-linux2 gaia-t23-uefi gaia-t23-win)
GUESTS="$(cd "$(dirname "$0")" && pwd)/t23-guests.sh"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; } # stderr: stdout carries JSON
section() { printf '\n== %s\n' "$*"; }
api() { HOME=$WORK "$BIN/gaia" "$@" -json; }

cleanup() {
	for u in "$UNIT" "$SB_UNIT" "$SINK_UNIT"; do systemctl stop "$u" 2>/dev/null || true; done
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	pkill -u "$HOST_USER" 2>/dev/null || true
	userdel -r "$HOST_USER" 2>/dev/null || true
	userdel "$SRV_USER" 2>/dev/null || true
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$SRV_BIN" "$WORK"
	for vm in "${VMS[@]}"; do # Gaia's checkpoints on the fixture VMs
		for c in $(virsh checkpoint-list "$vm" --name 2>/dev/null); do virsh -q checkpoint-delete "$vm" "$c" 2>/dev/null || true; done
	done
	fstrim -a 2>/dev/null || true
}
if [[ "${1:-}" == cleanup ]]; then
	cleanup
	exit 0
fi

mkdir -p "$OUT_DIR"
exec > >(tee -a "$OUT_DIR/t23r.log") 2>&1
log "phases: $PHASES; out $OUT_DIR; repository $(git -C "$repo_root" rev-parse --short=12 HEAD)"
for vm in "${VMS[@]}"; do
	[[ $(virsh domstate "$vm" 2>/dev/null) == running ]] || { echo "UNKNOWN: $vm is not running (lab/lab-vm/t23-guests.sh create)"; exit 3; }
done
avail=$(df --output=avail -B1G /var/lib/libvirt/images | tail -1 | tr -d ' ')
((avail >= 60)) || { echo "UNKNOWN: $avail GiB free, the rehearsal needs 60 (repository, sandbox copies)"; exit 3; }

# --- helpers -----------------------------------------------------------------------------------------------------
declare -A VM_ID
vm_status() { api vms status "${VM_ID[$1]}"; }
wait_status() { # VM STATUS [RULE]: until the VM's status is STATUS (and its first reason RULE); prints the status JSON
	local t0 s
	t0=$(date +%s)
	while (($(date +%s) - t0 < PHASE_TIMEOUT_S)); do
		s=$(vm_status "$1")
		if [[ $(jq -r .status <<<"$s") == "$2" && (-z "${3:-}" || $(jq -r '.reasons[0].rule' <<<"$s") == "$3") ]]; then
			log "$1 is $2: $(jq -r '.reasons[0].text' <<<"$s")"
			echo "$s"
			return 0
		fi
		sleep 15
	done
	echo "UNKNOWN: $1 did not become $2${3:+ ($3)} in $PHASE_TIMEOUT_S s: $(jq -c '{status, reasons, notes}' <<<"$s")" >&2
	exit 3
}
wait_job() { # ID: until the job ended; prints it
	local j
	for ((i = 0; i < PHASE_TIMEOUT_S / 10; i++)); do
		j=$(api jobs get "$1")
		[[ $(jq -r .state <<<"$j") == ok || $(jq -r .state <<<"$j") == failed ]] && { echo "$j"; return 0; }
		sleep 10
	done
	echo "UNKNOWN: job $1 did not end in $PHASE_TIMEOUT_S s" >&2
	exit 3
}
backup_now() { api vms backup "${VM_ID[$1]}" | jq -r .id; } # VM: queues a backup, prints the job's ID
test_now() { api vms test "${VM_ID[$1]}" | jq -r .id; }
# hook_mark: how many webhook deliveries the sink has; wait_event EVENT TEXT: until a webhook after the mark carries
# EVENT with TEXT in its subject, and a mail with the same subject arrived; prints {event, vm, subject}.
hook_mark() { find "$OUT_DIR/sink/hook" -name '*.json' | wc -l; }
wait_event() {
	local ev=$1 text=$2 t0 hit
	t0=$(date +%s)
	while (($(date +%s) - t0 < PHASE_TIMEOUT_S)); do
		hit=$(find "$OUT_DIR/sink/hook" -name '*.json' | sort | tail -n +"$((MARK + 1))" | xargs -r cat |
			jq -c --arg e "$ev" --arg t "$text" '.body | fromjson | select(.event == $e and (.subject | contains($t))) | {event, vm, subject}' | head -1)
		if [[ -n "$hit" ]] && grep -rlqF -- "$(jq -r .subject <<<"$hit" | cut -c1-60)" "$OUT_DIR/sink/mail"; then
			log "notified: $(jq -r .subject <<<"$hit")"
			echo "$hit"
			return 0
		fi
		sleep 10
	done
	echo "UNKNOWN: no $ev notification with \"$text\" after mark $MARK in $PHASE_TIMEOUT_S s" >&2
	exit 3
}
record() { # NAME JSON-FILTER ARGS...: OUT_DIR/NAME.json from jq -n
	local n=$1
	shift
	jq -n "$@" | tee "$OUT_DIR/$n.json"
}

# --- server, sink, channels, host, VMs ---------------------------------------------------------------------------
section "build and server (packaged units)"
cleanup
mkdir -p "$WORK" "$OUT_DIR/sink"
owner=$(stat -c %U "$repo_root")
asowner() { if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi; }
asowner make -C "$repo_root" -s build
asowner go build -C "$repo_root" -o "$BIN/notify-sink" ./lab/tools/notify-sink
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
sed -e "s|^ExecStart=.*|ExecStart=$SRV_BIN sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles|" \
	"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/kit.pass"
install -m 0600 -o "$SRV_USER" "$WORK/admin.pw" "$SRV_HOME/admin.pw"
install -m 0600 -o "$SRV_USER" "$WORK/kit.pass" "$SRV_HOME/kit.pass"
init_server() { # the server's config and the units from the packaged ones
	sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
		-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
		-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" \
		-e "s|gaia-sandbox.service gaia-convert.service|$SB_UNIT.service|" \
		"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
	systemctl daemon-reload
}
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen 127.0.0.1:8443 -hostname 127.0.0.1 -admin admin -password-file "$SRV_HOME/admin.pw" -kit-passphrase-file "$SRV_HOME/kit.pass" -json >"$OUT_DIR/server-init.json"
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$WORK/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$WORK/server.json" "$SRV_HOME/server.json"
init_server
start_server() {
	systemctl start "$SB_UNIT"
	for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK ]] && break; sleep 1; done
	systemctl start "$UNIT"
	for ((i = 0; i < 30; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
}
start_server
login() { HOME=$WORK "$BIN/gaia" login -server "$API" -user admin -fingerprint "$1" -password-file "$WORK/admin.pw" >/dev/null; }
login "$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json")"
api system >"$OUT_DIR/system.json"
jq -c '{version, schema_version}' "$OUT_DIR/system.json"

section "repository (step 7), recovery kit saved off the server (step 8b)"
GAIA_KIT_PASSPHRASE=$(cat "$WORK/kit.pass") api repositories add name=main location="$SRV_HOME/repo/main" | jq -c '{id, name, splitter}'
mkdir -p "$WORK/kits" && (cd "$WORK/kits" && HOME=$WORK "$BIN/gaia" kit save -passphrase-file "$WORK/kit.pass" | head -1)

section "notification channels (step 8c): notify-sink"
ca=$WORK/ca && mkdir -p "$ca"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/ca.key" 2>/dev/null
openssl req -x509 -new -key "$ca/ca.key" -sha256 -days 7 -subj "/CN=gaia-t23r lab CA" -out "$ca/ca.crt"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/server.key" 2>/dev/null
openssl req -new -key "$ca/server.key" -subj "/CN=127.0.0.1" -out "$ca/server.csr"
printf 'subjectAltName=IP:127.0.0.1\n' >"$ca/ext"
openssl x509 -req -in "$ca/server.csr" -CA "$ca/ca.crt" -CAkey "$ca/ca.key" -CAcreateserial -days 7 -sha256 -extfile "$ca/ext" -out "$ca/server.crt" 2>/dev/null
smtp_pw=$(head -c 18 /dev/urandom | base64)
printf 'SINK_SMTP_PASSWORD=%s\n' "$smtp_pw" | install -m 0600 /dev/stdin "$ca/sink.env"
systemd-run --unit="$SINK_UNIT" -p EnvironmentFile="$ca/sink.env" "$BIN/notify-sink" -dir "$OUT_DIR/sink" -smtp "$SINK_SMTP" \
	-https "$SINK_HTTPS" -cert "$ca/server.crt" -key "$ca/server.key" -smtp-user gaia
for ((i = 0; i < 30; i++)); do curl -fs --cacert "$ca/ca.crt" -X POST -d '{}' "https://$SINK_HTTPS/probe" -o /dev/null && break; sleep 1; done
rm -f "$OUT_DIR/sink/hook/"*.json
channels() { # SUFFIX: the mail and webhook channels, named mailSUFFIX and hookSUFFIX
	GAIA_SMTP_PASSWORD=$smtp_pw api channels add name="mail$1" kind=smtp ca_file="$ca/ca.crt" \
		smtp="{\"host\":\"127.0.0.1\",\"port\":${SINK_SMTP##*:},\"username\":\"gaia\",\"from\":\"Gaia T23 <gaia@lab.test>\",\"to\":[\"ops@lab.test\"]}" |
		jq -c '{id, name, events}'
	api channels add name="hook$1" kind=webhook ca_file="$ca/ca.crt" webhook="{\"url\":\"https://$SINK_HTTPS/gaia\"}" | jq -c '{id, name, events}'
}
channels ""

section "host (step 10): gaia-lab over SSH as $HOST_USER"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
jq -r .ssh_authorized_keys_line "$OUT_DIR/system.json" | install -m 0600 -o "$HOST_USER" -g "$HOST_USER" /dev/stdin "/home/$HOST_USER/.ssh/authorized_keys"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(api hosts add name=gaia-lab address=127.0.0.1 ssh_user="$HOST_USER" | jq .id)
[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
api hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
api hosts check "$host_id" | jq -c .

section "policies and VMs (steps 10, 10b)"
# Short intervals so a rehearsal fits in hours: backups every 5 minutes, tests every 10, proof at most 20 minutes old.
pol() { api policies add name="$1" repository_id=1 backup_interval_s=300 test_interval_s=600 keep_last=12 keep_daily=0 keep_weekly=0 \
	rpo_target_s=1800 rto_target_s=900 max_proof_age_s=1200 check_template="$2" | jq .id; }
linux_pol=$(pol t23-linux linux-boot)
win_pol=$(pol t23-windows windows-basic)
for vm in "${VMS[@]}"; do
	p=$linux_pol
	[[ $vm == gaia-t23-win ]] && p=$win_pol
	VM_ID[$vm]=$(api vms add host_id="$host_id" name="$vm" policy_id="$p" | jq .id)
	log "$vm is VM ${VM_ID[$vm]} (policy $p)"
done

# --- scenarios ---------------------------------------------------------------------------------------------------
phase_baseline() {
	section "baseline: every VM OK"
	for vm in "${VMS[@]}"; do wait_status "$vm" OK >"$OUT_DIR/baseline-$vm.json"; done
	record baseline --slurpfile a "$OUT_DIR/baseline-gaia-t23-linux2.json" --slurpfile b "$OUT_DIR/baseline-gaia-t23-uefi.json" \
		--slurpfile c "$OUT_DIR/baseline-gaia-t23-win.json" \
		'[$a[0], $b[0], $c[0]] | map({status, reason: .reasons[0].text}) | {vms: ., pass: all(.[]; .status == "OK")}'
}

phase_unbootable() {
	section "unbootable: gaia-t23-linux2's boot code zeroed in the running guest"
	MARK=$(hook_mark)
	"$GUESTS" break-boot gaia-t23-linux2
	wait_job "$(backup_now gaia-t23-linux2)" | jq -c '{id, kind, state}'
	t=$(wait_job "$(test_now gaia-t23-linux2)")
	wait_status gaia-t23-linux2 CRITICAL latest_test_critical >"$OUT_DIR/unbootable-status.json"
	wait_event status "gaia-t23-linux2: OK -> CRITICAL" >"$OUT_DIR/unbootable-event.json"
	section "unbootable: boot code back"
	MARK=$(hook_mark)
	"$GUESTS" fix-boot gaia-t23-linux2
	wait_job "$(backup_now gaia-t23-linux2)" | jq -c '{id, kind, state}'
	wait_job "$(test_now gaia-t23-linux2)" | jq -c '{id, kind, state}'
	wait_status gaia-t23-linux2 OK >"$OUT_DIR/unbootable-fixed.json"
	wait_event status "gaia-t23-linux2: CRITICAL -> OK" >"$OUT_DIR/unbootable-fixed-event.json"
	record unbootable --argjson test "$t" --slurpfile st "$OUT_DIR/unbootable-status.json" --slurpfile ev "$OUT_DIR/unbootable-event.json" \
		--slurpfile fixed "$OUT_DIR/unbootable-fixed.json" --slurpfile fev "$OUT_DIR/unbootable-fixed-event.json" \
		'{test_job: ($test | {id, state, reason}), status: ($st[0] | {status, rule: .reasons[0].rule, reason: .reasons[0].text}), notified: $ev[0],
		  after_fix: ($fixed[0] | {status, reason: .reasons[0].text}), notified_after_fix: $fev[0]}
		 | . + {pass: (.status.status == "CRITICAL" and (.status.reason | test("boot|login")) and .after_fix.status == "OK")}'
}

phase_damage() {
	section "damage: a byte of a repository pack flipped"
	MARK=$(hook_mark)
	pack=$(find "$SRV_HOME/repo/main" -path '*/p*' -type f -size +1M -printf '%s %p\n' | sort -n | tail -1 | cut -d' ' -f2)
	size=$(stat -c %s "$pack")
	printf '\xff' | dd of="$pack" bs=1 seek=$((size / 2)) conv=notrunc status=none
	log "flipped byte $((size / 2)) of $pack ($size bytes)"
	v=$(api repositories verify 1 mode=full | jq -r .id)
	for ((i = 0; i < PHASE_TIMEOUT_S / 10; i++)); do
		ver=$(api verifications get "$v")
		[[ $(jq -r .state <<<"$ver") != running ]] && break
		sleep 10
	done
	echo "$ver" | jq -c '{id, state, points_checked, affected: [.affected[] | {point_id, vm_id, disk, contents}]}' | tee "$OUT_DIR/damage-verification.json"
	[[ $(jq -r .state <<<"$ver") == critical ]] || { echo "CRITICAL: the verification is $(jq -r .state <<<"$ver"), not critical"; exit 2; }
	wait_event integrity "found damage" >"$OUT_DIR/damage-event-integrity.json"
	wait_event quarantine "quarantined" >"$OUT_DIR/damage-event-quarantine.json"
	mapfile -t hit < <(jq -r '[.affected[].vm_id] | unique | .[]' <<<"$ver")
	declare -A name_of
	for vm in "${VMS[@]}"; do name_of[${VM_ID[$vm]}]=$vm; done
	: >"$OUT_DIR/damage-vms.jsonl"
	for id in "${hit[@]}"; do
		vm=${name_of[$id]}
		wait_status "$vm" CRITICAL integrity_failed | jq -c --arg vm "$vm" '{vm: $vm, status, reason: .reasons[0].text}' >>"$OUT_DIR/damage-vms.jsonl"
		wait_event status "$vm: OK -> CRITICAL" >>"$OUT_DIR/damage-events-status.jsonl"
		b=$(wait_job "$(backup_now "$vm")")
		wait_event full_backup "$vm: the backup was full" >>"$OUT_DIR/damage-events-full.jsonl"
		wait_job "$(test_now "$vm")" | jq -c '{id, kind, state}'
		still=$(vm_status "$vm" | jq -c '{status, rule: .reasons[0].rule}')
		log "$vm after a new proven point: $still (ADR-007 B4.3)"
		for p in $(api points list vm_id="$id" limit=100 | jq -r '.[] | select(.state == "present" and .check_state == "failed") | .id'); do
			api points forget "$p" reason="T23 rehearsal: damaged by the flipped byte; the backup after job $(jq -r .id <<<"$b") is proven" | jq -c '{id, state}'
		done
		wait_status "$vm" OK | jq -c --arg vm "$vm" '{vm: $vm, status, reason: .reasons[0].text}' >>"$OUT_DIR/damage-vms-after.jsonl"
	done
	record damage --slurpfile ver "$OUT_DIR/damage-verification.json" --slurpfile ei "$OUT_DIR/damage-event-integrity.json" \
		--slurpfile eq "$OUT_DIR/damage-event-quarantine.json" --slurpfile vms "$OUT_DIR/damage-vms.jsonl" \
		--slurpfile st "$OUT_DIR/damage-events-status.jsonl" --slurpfile full "$OUT_DIR/damage-events-full.jsonl" --slurpfile after "$OUT_DIR/damage-vms-after.jsonl" \
		'{verification: $ver[0], notified: ([$ei[0], $eq[0]] + $st + $full), vms: $vms, after_forget: $after}
		 | . + {pass: (.verification.state == "critical" and (.vms | length > 0) and all(.vms[]; .status == "CRITICAL")
		              and (.after_forget | length) == (.vms | length) and all(.after_forget[]; .status == "OK"))}'
}

phase_sandbox() {
	section "sandbox: the helper stopped"
	MARK=$(hook_mark)
	systemctl stop "$SB_UNIT"
	j=$(wait_job "$(test_now gaia-t23-linux2)")
	echo "$j" | jq -c '{id, state, error}'
	wait_event job_failed "restore_test job $(jq -r .id <<<"$j") of gaia-t23-linux2 failed" >"$OUT_DIR/sandbox-event-job.json"
	vm_status gaia-t23-linux2 | jq -c '{status, notes}' | tee "$OUT_DIR/sandbox-note.json"
	wait_status gaia-t23-linux2 WARNING proof_too_old >"$OUT_DIR/sandbox-status.json"
	wait_event status "gaia-t23-linux2: OK -> WARNING" >"$OUT_DIR/sandbox-event-status.json"
	section "sandbox: the helper started again"
	MARK=$(hook_mark)
	systemctl start "$SB_UNIT"
	for vm in "${VMS[@]}"; do wait_status "$vm" OK >/dev/null; done
	wait_event status "gaia-t23-linux2: WARNING -> OK" >"$OUT_DIR/sandbox-event-ok.json"
	record sandbox --argjson job "$j" --slurpfile ej "$OUT_DIR/sandbox-event-job.json" --slurpfile note "$OUT_DIR/sandbox-note.json" \
		--slurpfile st "$OUT_DIR/sandbox-status.json" --slurpfile es "$OUT_DIR/sandbox-event-status.json" --slurpfile ok "$OUT_DIR/sandbox-event-ok.json" \
		'{test_job: ($job | {id, state, error}), notified: [$ej[0], $es[0], $ok[0]], note: $note[0].notes,
		  status: ($st[0] | {status, rule: .reasons[0].rule, reason: .reasons[0].text})}
		 | . + {pass: (.test_job.state == "failed" and (.test_job.error | test("sandbox")) and any(.note[]; test("could not run"))
		              and .status.status == "WARNING")}'
}

phase_checkpoint() {
	section "checkpoint: gaia-t23-linux2's newest checkpoint deleted on the host"
	MARK=$(hook_mark)
	"$GUESTS" delete-checkpoint gaia-t23-linux2 | tee "$OUT_DIR/checkpoint-deleted.txt"
	b=$(wait_job "$(backup_now gaia-t23-linux2)")
	p=$(api points list vm_id="${VM_ID[gaia-t23-linux2]}" limit=1 | jq -c '.[0] | {id, kind, reason, check_state}')
	echo "$p"
	wait_event full_backup "gaia-t23-linux2: the backup was full" >"$OUT_DIR/checkpoint-event.json"
	vm_status gaia-t23-linux2 | jq -c '{status, reason: .reasons[0].text, notes}' | tee "$OUT_DIR/checkpoint-status.json"
	record checkpoint --argjson job "$b" --argjson point "$p" --slurpfile ev "$OUT_DIR/checkpoint-event.json" --slurpfile st "$OUT_DIR/checkpoint-status.json" \
		'{backup_job: ($job | {id, state}), point: $point, notified: $ev[0], status: $st[0]}
		 | . + {pass: (.backup_job.state == "ok" and .point.kind == "full" and (.point.reason | test("not on the host"))
		              and .status.status == "OK" and any(.status.notes[]; test("full backup")))}'
}

phase_rebuild() {
	section "rebuild: a daily report (the external anchor), then the server is lost"
	MARK=$(hook_mark)
	api daily-reports run | jq -c '{id, head_seq}'
	wait_event daily_report "daily report" >/dev/null
	find "$OUT_DIR/sink/hook" -name '*.json' | sort | tail -n +"$((MARK + 1))" | xargs cat | jq -c '.body | fromjson | select(.event == "daily_report") | .report' |
		tail -1 | jq . >"$OUT_DIR/rebuild-last-report.json"
	# Gaia backs its state up a few minutes after jobs end; one now, so the rebuild has this very state to compare with.
	sb=$(api state-backups run | jq -r .id)
	for ((i = 0; i < 60; i++)); do [[ $(api state-backups list limit=5 | jq -r --argjson id "$sb" '.[] | select(.id == $id) | .state') == ok ]] && break; sleep 5; done
	before=$(for vm in "${VMS[@]}"; do vm_status "$vm" | jq -c --arg vm "$vm" '{vm: $vm, status}'; done | jq -sc .)
	systemctl stop "$UNIT" "$SB_UNIT"
	mv "$SRV_HOME/data" "$SRV_HOME/data.lost"
	mv "$SRV_HOME/server.json" "$SRV_HOME/server.json.lost"
	install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME/data"
	log "lost: data and config moved away"
	section "rebuild: restore-state from the kit and the repository (step 13)"
	kit=$(find "$WORK/kits" -name '*.age' | sort | tail -1)
	install -m 0600 -o "$SRV_USER" "$kit" "$SRV_HOME/kit.age"
	runuser -u "$SRV_USER" -- "$SRV_BIN" server restore-state -kit "$SRV_HOME/kit.age" -config "$SRV_HOME/server.json" \
		-passphrase-file "$SRV_HOME/kit.pass" -json >"$OUT_DIR/rebuild-restore-state.json" || true
	jq -c '{verdict, problems: .result.problems}' "$OUT_DIR/rebuild-restore-state.json"
	jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$WORK/server.json"
	install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$WORK/server.json" "$SRV_HOME/server.json"
	start_server
	rm -f "$WORK/.config/gaia/cli.json"
	fp=$(jq -r '.result.tls_certificate_sha256 // empty' "$OUT_DIR/rebuild-restore-state.json")
	[[ -n "$fp" ]] || fp=$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json") # the lost server's certificate, kept
	login "$fp"
	after=$(for vm in "${VMS[@]}"; do vm_status "$vm" | jq -c --arg vm "$vm" '{vm: $vm, status, reason: .reasons[0].text}'; done | jq -sc .)
	echo "$after"
	section "rebuild: the ledger against the last daily report, the channels again, a daily report"
	verify=$("$BIN/gaia" evidence verify -pubkey "$SRV_HOME/data/keys/evidence-key.pub" -ledger "$SRV_HOME/data/evidence/ledger.jsonl" \
		-evidence-dir "$SRV_HOME/data/evidence/bundles" -head "$OUT_DIR/rebuild-last-report.json" -json 2>&1 || true)
	echo "$verify" | jq -c '{verdict, findings: [.findings[]?.code]}' | tee "$OUT_DIR/rebuild-ledger.json"
	for c in $(api channels list | jq -r '.[] | select(.enabled) | .id'); do api channels set "$c" enabled=false reason="rebuilt: secrets are not in the kit" | jq -c '{id, name, enabled}'; done
	channels "-rebuilt"
	MARK=$(hook_mark)
	api daily-reports run | jq -c '{id}'
	rep=$(wait_event daily_report "daily report")
	find "$OUT_DIR/sink/mail" -name '*.eml' -newer "$OUT_DIR/rebuild-ledger.json" -exec grep -l "REBUILT: this server was rebuilt" {} + | head -1 >"$OUT_DIR/rebuild-mail.txt" || true
	section "rebuild: the next backups continue"
	: >"$OUT_DIR/rebuild-backups.jsonl"
	for vm in "${VMS[@]}"; do
		wait_job "$(backup_now "$vm")" >/dev/null
		api points list vm_id="${VM_ID[$vm]}" limit=1 | jq -c --arg vm "$vm" '.[0] | {vm: $vm, id, kind, reason}' | tee -a "$OUT_DIR/rebuild-backups.jsonl"
	done
	record rebuild --argjson before "$before" --argjson after "$after" --slurpfile rs "$OUT_DIR/rebuild-restore-state.json" \
		--slurpfile ledger "$OUT_DIR/rebuild-ledger.json" --argjson report "$rep" --arg mail "$(cat "$OUT_DIR/rebuild-mail.txt")" \
		--slurpfile backups "$OUT_DIR/rebuild-backups.jsonl" \
		'{restore_state: ($rs[0] | {verdict, problems: .result.problems}), status_before: $before, status_after: $after, ledger: $ledger[0],
		  daily_report: $report, rebuilt_line_mailed: ($mail != ""), next_backups: $backups}
		 | . + {pass: (.restore_state.verdict != "CRITICAL" and ([.status_before[].status] == [.status_after[].status])
		              and .ledger.verdict == "OK" and .rebuilt_line_mailed and all(.next_backups[]; .kind == "incremental" or (.reason | test("^full: "))))}'
}

MARK=0
for ph in $PHASES; do "phase_$ph"; done

section "gate (CLAUDE.md T23, rehearsal)"
gate='{}'
for ph in $PHASES; do gate=$(jq -c --arg p "$ph" --slurpfile r "$OUT_DIR/$ph.json" '. + {($p): $r[0].pass}' <<<"$gate"); done
jq '. + {verdict: (if all(.[]; .) then "OK" else "CRITICAL" end)}' <<<"$gate" | tee "$OUT_DIR/gate.json"
log "out: $OUT_DIR (the server stays until: lab/lab-vm/t23-rehearsal.sh cleanup)"
[[ $(jq -r .verdict "$OUT_DIR/gate.json") == OK ]] || exit 2
