#!/bin/bash
# Dry run of docs/install.md in a clean debian:bookworm container (no systemd: the service runs in the background).
set -euo pipefail
export GAIA_PASSWORD='dry run admin password'
echo "== step 3"
cd /tmp && tar xzf gaia-b2c12e5-dirty-linux-arm64.tar.gz && cd gaia-b2c12e5-dirty-linux-arm64 && sha256sum -c SHA256SUMS
useradd --system --user-group --home-dir /var/lib/gaia --shell /usr/sbin/nologin gaia
install -d -o gaia -g gaia -m 0750 /var/lib/gaia /etc/gaia /srv/gaia
install -m 0755 gaia /usr/local/bin/gaia
install -m 0644 gaia.service /etc/systemd/system/gaia.service
echo "== step 4"
runuser -u gaia -- gaia server init -config /etc/gaia/server.json -data-dir /var/lib/gaia -hostname gaia.dryrun,10.9.9.9 -admin admin | tee /tmp/init.txt
FP=$(grep 'TLS certificate sha256' /tmp/init.txt | awk '{print $4}')
echo "== step 5 (in the background instead of systemd)"
runuser -u gaia -- gaia server run -config /etc/gaia/server.json >/tmp/server.log 2>&1 &
sleep 3
grep 'gaia server is listening' /tmp/server.log
echo "== step 6"
gaia login -server https://localhost:8443 -user admin -fingerprint "$FP"
gaia whoami && gaia system
echo "== step 7"
gaia repositories add name=main location=/srv/gaia/main
echo "== step 8"
gaia state-backups run && gaia state-backups list
tar -C /var/lib/gaia -czf /root/gaia-secrets.tar.gz secrets keys/evidence-key.pem && chmod 600 /root/gaia-secrets.tar.gz && tar tzf /root/gaia-secrets.tar.gz
echo "== step 9"
GAIA_PASSWORD='an operator password' gaia users add username=op1 role=operator
gaia users list
echo "== step 10"
gaia hosts add name=kvm1 address=192.168.1.30 ssh_user=gaia
gaia vms add host_id=1 name=web1
gaia policies add name=gold repository_id=1 backup_interval_s=900 test_interval_s=86400 keep_last=8 keep_daily=7 keep_weekly=4 rpo_target_s=3600 rto_target_s=1800 max_proof_age_s=172800 check_template=linux-basic
gaia vms set 1 policy_id=1
gaia audit list limit=5
gaia audit verify
echo "== file modes"
ls -la /var/lib/gaia /var/lib/gaia/secrets /var/lib/gaia/keys /etc/gaia
echo "DRY RUN OK"
