#!/usr/bin/env bash
# fixture: a gaia server for the PO's own sign-in on gaia-lab (T26): https://gaia-lab.test:8447, first admin "yigit",
# whose security key (Touch ID) is enrolled at the first sign-in in a browser. It shows the UI's WebAuthn with a real
# authenticator, which the gate's keys in software cannot. Not part of the product. The admin's password is generated
# into $DIR/yigit.pw (0600, root): the PO reads it there himself.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t26-smoke.sh up | down | status
set -euo pipefail
DIR=/root/t26-smoke
SRV_USER=gaia-t26s
SRV_HOME=/var/lib/gaia-t26s
SRV_BIN=/usr/local/sbin/gaia-t26s
UNIT=gaia-t26s-server
PUBLIC=https://gaia-lab.test:8447
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
((EUID == 0)) || { echo "run as root"; exit 3; }

down() {
	systemctl stop "$UNIT" 2>/dev/null || true
	userdel "$SRV_USER" 2>/dev/null || true
	rm -rf "$SRV_HOME" "$SRV_BIN" "$DIR"
}
status() {
	jq -n --arg u "$PUBLIC" --arg s "$(systemctl is-active "$UNIT" || true)" \
		--arg fp "$(openssl x509 -in "$SRV_HOME/data/tls/cert.pem" -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2)" \
		'{url: $u, service: $s, tls_certificate_sha256: $fp, admin: "yigit", password_file: "/root/t26-smoke/yigit.pw"}'
}
case "${1:-}" in
down) down; exit 0 ;;
status) status; exit 0 ;;
up) ;;
*) echo "usage: $0 up | down | status"; exit 3 ;;
esac

grep -q " gaia-lab.test\$" /etc/hosts || echo "$(hostname -I | awk '{ print $1 }') gaia-lab.test" >>/etc/hosts
down
install -d -m 0700 "$DIR"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then make -C "$repo_root" -s build; else runuser -u "$owner" -- env PATH="$PATH" make -C "$repo_root" -s build; fi
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data"
install -m 0755 "$repo_root/bin/gaia" "$SRV_BIN"
(umask 077 && printf 'po smoke %s\n' "$(head -c 18 /dev/urandom | base64 | tr -d '/+=')" >"$DIR/yigit.pw")
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$DIR/kit.pass"
install -m 0600 -o "$SRV_USER" "$DIR/yigit.pw" "$SRV_HOME/admin.pw"
install -m 0600 -o "$SRV_USER" "$DIR/kit.pass" "$SRV_HOME/kit.pass"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" -listen :8447 \
	-hostname gaia-lab.test -public-url "$PUBLIC" -admin yigit -password-file "$SRV_HOME/admin.pw" -kit-passphrase-file "$SRV_HOME/kit.pass" \
	-json >/dev/null
rm -f "$SRV_HOME/admin.pw" "$SRV_HOME/kit.pass"
systemd-run --unit="$UNIT" -p User="$SRV_USER" -p Group="$SRV_USER" -p NoNewPrivileges=yes "$SRV_BIN" server run -config "$SRV_HOME/server.json" >/dev/null
for ((i = 0; i < 30; i++)); do curl -fs --cacert "$SRV_HOME/data/tls/cert.pem" "$PUBLIC/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
cp "$SRV_HOME/data/tls/cert.pem" "$DIR/gaia-t26s.pem"
chmod 0644 "$DIR/gaia-t26s.pem"
status
