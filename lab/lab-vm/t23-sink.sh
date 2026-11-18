#!/usr/bin/env bash
# fixture: where the T23 acceptance server sends its notifications: lab/tools/notify-sink on gaia-lab, a test SMTP
# server (STARTTLS and AUTH required) and an HTTPS webhook receiver on gaia-lab's LAN address, with a lab CA of their
# own. Not part of the product.
#
#   start ADDR    builds notify-sink, makes the CA and the server certificate for ADDR (gaia-lab's address, e.g.
#                 192.168.1.30), starts SMTP on ADDR:2587 and HTTPS on ADDR:2443 (unit gaia-t23-sink), and prints what
#                 install.md step 8c needs: the CA file to copy to the Gaia server and the SMTP user
#   password      prints the SMTP password (for GAIA_SMTP_PASSWORD on the Gaia server; only when asked)
#   list [N]      the last N (default 30) messages received: time, channel, event, subject, oldest first
#   stop          stops the sink (the messages and the CA stay in /root/t23-sink)
#
# Usage (on gaia-lab, as root): lab/lab-vm/t23-sink.sh start 192.168.1.30
set -euo pipefail

D=/root/t23-sink
UNIT=gaia-t23-sink
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }

case "${1:-}" in
start)
	addr=${2:?ADDR, gaia-lab LAN address}
	mkdir -p "$D/ca" "$D/msgs"
	go build -C "$repo_root" -o "$D/notify-sink" ./lab/tools/notify-sink
	ca=$D/ca
	if [[ ! -s "$ca/ca.crt" ]]; then
		openssl ecparam -genkey -name prime256v1 -noout -out "$ca/ca.key" 2>/dev/null
		openssl req -x509 -new -key "$ca/ca.key" -sha256 -days 30 -subj "/CN=gaia-t23 lab notification CA" -out "$ca/ca.crt"
	fi
	openssl ecparam -genkey -name prime256v1 -noout -out "$ca/server.key" 2>/dev/null
	openssl req -new -key "$ca/server.key" -subj "/CN=$addr" -out "$ca/server.csr"
	printf 'subjectAltName=IP:%s\n' "$addr" >"$ca/ext"
	openssl x509 -req -in "$ca/server.csr" -CA "$ca/ca.crt" -CAkey "$ca/ca.key" -CAcreateserial -days 30 -sha256 -extfile "$ca/ext" -out "$ca/server.crt" 2>/dev/null
	[[ -s "$ca/sink.env" ]] || printf 'SINK_SMTP_PASSWORD=%s\n' "$(head -c 18 /dev/urandom | base64)" | install -m 0600 /dev/stdin "$ca/sink.env"
	systemctl stop "$UNIT" 2>/dev/null || true
	systemd-run --unit="$UNIT" -p EnvironmentFile="$ca/sink.env" -p Restart=on-failure "$D/notify-sink" -dir "$D/msgs" \
		-smtp "$addr:2587" -https "$addr:2443" -cert "$ca/server.crt" -key "$ca/server.key" -smtp-user gaia
	sleep 2
	systemctl is-active "$UNIT"
	install -m 0644 "$ca/ca.crt" /tmp/t23-sink-ca.crt # public: for the operator to copy to the Gaia server
	cat <<EOF

The sink listens on $addr: SMTP port 2587 (user gaia), HTTPS port 2443 (any path).
CA certificate for the channels' ca_file (copy it to the Gaia server, e.g. /etc/gaia/t23-sink-ca.crt):
  /tmp/t23-sink-ca.crt (a copy of $ca/ca.crt)
SMTP password: lab/lab-vm/t23-sink.sh password
EOF
	;;
password)
	sed -n 's/^SINK_SMTP_PASSWORD=//p' "$D/ca/sink.env"
	;;
list)
	n=${2:-30}
	{
		for f in "$D"/msgs/mail/*.eml; do
			[[ -e "$f" ]] || continue
			printf '%s\tmail\t%s\t%s\n' "$(date -u -r "$f" +%FT%TZ)" "$(sed -n 's/^X-Gaia-Event: //p' "$f" | tr -d '\r' | head -1)" \
				"$(sed -n 's/^Subject: //p' "$f" | tr -d '\r' | head -1)"
		done
		for f in "$D"/msgs/hook/*.json; do
			[[ -e "$f" ]] || continue
			printf '%s\twebhook\t%s\n' "$(date -u -r "$f" +%FT%TZ)" "$(jq -r '.body | fromjson | "\(.event)\t\(.subject)"' "$f")"
		done
	} | sort | tail -n "$n"
	;;
stop)
	systemctl stop "$UNIT"
	;;
*)
	sed -n '2,15p' "$0"
	exit 3
	;;
esac
