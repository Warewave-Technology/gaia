#!/usr/bin/env bash
# Collects the T1 evidence from gaia-lab: ESXi reachability, `govc about`, the host build
# and license, and the lab VM's tool versions. Output goes verbatim into the T1 report.
#
# Usage (on gaia-lab, after setup.sh):  lab/lab-vm/t1-evidence.sh 192.0.2.10
# The ESXi root password is read from GOVC_PASSWORD or prompted for; it is never printed.
# Exit: 0 all checks passed, 2 a check failed, 3 a check could not run.
set -euo pipefail

ESXI_IP="${1:?ESXi host IP}"
export GOVC_URL="https://${ESXI_IP}/sdk" GOVC_USERNAME="${GOVC_USERNAME:-root}" GOVC_INSECURE=1
if [[ -z "${GOVC_PASSWORD:-}" ]]; then
	read -rsp "ESXi ${GOVC_USERNAME} password: " GOVC_PASSWORD
	echo
fi
export GOVC_PASSWORD

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
rc=0
section() { printf '\n== %s\n' "$*"; }
run() { # run CMD...: prints the output; a failure makes the whole run CRITICAL
	if ! "$@" 2>&1; then
		echo "FAILED: $*"
		rc=2
	fi
}

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"

section "reachability: ping ${ESXI_IP}"
run ping -c 3 -W 2 "$ESXI_IP"

section "reachability: HTTPS ${GOVC_URL}"
run curl -sk -o /dev/null -w 'HTTP %{http_code} in %{time_total}s\n' --max-time 10 "https://${ESXI_IP}/sdk/vimServiceVersions.xml"

# One login attempt first: ESXi locks root after 5 failed logins (Security.AccountLockFailures),
# so a wrong password must not be retried by every govc call below.
section "govc about"
if govc about 2>&1; then
	section "govc about -json"
	run govc about -json

	section "govc host.info"
	run govc host.info

	# The edition is evidence; the key is not and must not reach a public report.
	section "govc license.ls (keys masked)"
	if out=$(govc license.ls 2>&1); then
		sed -E 's/\b([A-Z0-9]{5})(-[A-Z0-9]{5}){3}-([A-Z0-9]{5})\b/\1-*****-*****-*****-\3/g' <<<"$out"
	else
		echo "$out"
		echo "FAILED: govc license.ls"
		rc=2
	fi
else
	echo "FAILED: govc about (login); remaining govc checks SKIPPED to avoid an ESXi account lockout"
	rc=2
fi

section "lab VM"
run uname -srm
run grep PRETTY_NAME /etc/os-release
run nproc
run free -h
run df -h /

section "tool versions"
for cmd in "go version" "kopia --version" "govc version" "qemu-img --version" "jq --version"; do
	read -ra argv <<<"$cmd"
	if command -v "${argv[0]}" >/dev/null; then
		# Not "run ... | head": a pipeline runs in a subshell and would lose rc.
		if out=$("${argv[@]}" 2>&1); then
			echo "${out%%$'\n'*}"
		else
			echo "FAILED: $cmd: $out"
			rc=2
		fi
	else
		echo "MISSING: ${argv[0]}"
		if ((rc == 0)); then rc=3; fi # a failed check (2) outranks a missing tool
	fi
done

section "result"
case $rc in
0) echo "OK" ;;
2) echo "CRITICAL: a check failed (see FAILED lines)" ;;
*) echo "UNKNOWN: a tool is missing" ;;
esac
exit "$rc"
