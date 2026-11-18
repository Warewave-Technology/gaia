# Installing gaia server

This guide installs `gaia server` (ADR-007) from a signed release on a clean Debian 12 machine and checks that it
works. It takes about 15 minutes. Every command is meant to be copied as it is; replace only what is in
`<angle brackets>`.

What this version does: users and roles, the API and the command line, the audit log, repositories, scheduled
backups of VMs on KVM hosts, restore tests with signed evidence, the status of every VM, the screens for operators
and auditors with the evidence export (step 10c), read-only migration checks of VMware VMs (step 10d), backups of
Gaia's own state, and a recovery kit to rebuild a lost Gaia server (step 13). Known limits are in
`docs/limitations.md` in the repository.

## 1. What you need

- **Server:** Debian 12 (bookworm), amd64, 2 vCPU, 4 GiB RAM, and enough disk for the repository (start with 50 GiB
  under `/srv/gaia`). It must be reachable on TCP port 8443 from wherever you will run the `gaia` command.
- **For restore tests** (step 10): the server boots restored VMs itself, so it needs KVM (`ls /dev/kvm` shows the
  device; in a VM, nested virtualisation must be on), 1–2 GiB RAM more per test VM, and free disk under
  `/var/lib/gaia-sandbox` for the largest VM's disks.
- **Workstation:** the machine you run the `gaia` command on and keep the recovery kit on (Linux or macOS), with
  OpenSSH (it checks the release's signature). Your Mac is fine.

## 2. Get the release and check its signature (on the workstation)

A release is five kinds of files, all with the same `<version>` (for example `0.1.0`):

| File | What it is |
|---|---|
| `gaia_<version>_amd64.deb` | The server package: the program, its three services, this guide |
| `gaia-<version>-<os>-<arch>.tar.gz` | The `gaia` command alone, for workstations (`linux` or `darwin`, `amd64` or `arm64`) |
| `BUILDINFO.txt` | The version, the source commit and the Go version it was built with |
| `SHA256SUMS` | The checksum of every file above |
| `SHA256SUMS.sig` | The signature of `SHA256SUMS` by Gaia's release key |

Download all of them into one empty directory, from the release page
(`https://github.com/Warewave-Technology/gaia/releases`) or from whoever gave you the release.

Gaia's release key is this one line. Copy it from this guide, not from the release directory, into a file
`allowed_signers` next to the release files:

```
release@gaia namespaces="gaia-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJnsHZ7oYaBuDknr1LKM2qIkVdO9dW6MwLNF0HwAxmrF
```

Its fingerprint is `SHA256:Nqb0MoKtSc+AZaFGJkfOpgELaRqyYLlkz9e2VjvKanc`. Then, in that directory, check the
signature and every checksum:

```bash
ssh-keygen -Y verify -f allowed_signers -I release@gaia -n gaia-release -s SHA256SUMS.sig < SHA256SUMS && (sha256sum -c SHA256SUMS || shasum -a 256 -c SHA256SUMS)
```

It must print
`Good "gaia-release" signature for release@gaia with ED25519 key SHA256:Nqb0MoKtSc+AZaFGJkfOpgELaRqyYLlkz9e2VjvKanc`
and then one line per file, each ending in `OK`. If not, **stop**: the files are not the release.

Copy the server package and the checksums to the server, as any user who can log in there:

```bash
scp gaia_<version>_amd64.deb SHA256SUMS <user>@<server>:/tmp/
```

(To build the packages yourself from the repository instead: `make dist` puts them, unsigned, in `dist/`.)

## 3. Install (on the server, as root)

Log in to the server and become root (`su -`, or `sudo -i`). Steps 3 to 5 and 8 run as root.

Check the package again on the server and install it:

```bash
cd /tmp && sha256sum -c --ignore-missing SHA256SUMS && apt-get update && apt-get install -y ./gaia_<version>_amd64.deb && gaia version
```

`sha256sum` must say `OK`, and the last line `gaia <version> (...)`.

The package installs `/usr/bin/gaia`, creates the user `gaia` and its directories (`/var/lib/gaia`, `/etc/gaia`,
`/srv/gaia`), and installs three services, none of them started yet:

| Service | What it does | Runs as |
|---|---|---|
| `gaia` | The server: the API, the screens, backups, restore tests, evidence | `gaia`, with no privileges at all |
| `gaia-sandbox` | Boots restored VMs for restore tests in an isolated network (step 10b). It only boots disks the server puts into `/var/lib/gaia-sandbox/images`, and only members of the group `gaia` can talk to it | root (the isolated network needs it) |
| `gaia-convert` | Converts VMware VMs for migration checks (step 10d, optional) | `gaia-convert` |

It also brings KVM, libvirt and dnsmasq, which restore tests need (the package's recommended packages). On a
server that will not run restore tests, add `--no-install-recommends` to `apt-get install`.

## 4. Set up Gaia (on the server, as root)

Replace `<names>` with the DNS name and IP address you will use to reach the server, separated by commas (for
example `gaia.lab,192.168.1.40`). They go into the server's TLS certificate.

```bash
runuser -u gaia -- gaia server init -config /etc/gaia/server.json -data-dir /var/lib/gaia -hostname <names> -admin admin
```

It asks twice for the password of the first admin user (`admin`); it must have at least 12 characters.

It then asks twice for the passphrase of the **recovery kit**, also at least 12 characters. The recovery kit is an
encrypted file with everything secret that a new server needs to take over if this one is lost: repository
passwords and the evidence signing key (step 8b). Gaia does not store the passphrase. **Put it in your password
manager now**; without it the kit cannot be opened.

It then prints two values. **Write both down now**, for example in your password manager:

| Value | What it is for |
|---|---|
| `TLS certificate sha256` | At the first `gaia login` you compare it with what the command shows. If they differ, someone is in between. |
| `evidence trust anchor` | The key that signs all evidence. An auditor checks exported evidence against this value. |

## 5. Start the service (on the server, as root)

```bash
systemctl daemon-reload && systemctl enable --now gaia-sandbox gaia && systemctl status gaia-sandbox gaia --no-pager
```

Both should say `active (running)`. Without restore tests, leave `gaia-sandbox` out of the first command.

Stopping `gaia` (`systemctl stop gaia`, a reboot, an upgrade) waits for running backups and restore tests to end,
up to 10 minutes (`stop_drain` in `/etc/gaia/server.json`); then it ends them. A job ended so is recorded as failed
with the reason and queued again when the service starts.

The log is in the journal:

```bash
journalctl -u gaia --no-pager -n 20
```

It should contain `gaia server is listening`, with `address=[::]:8443`.

If the server has a firewall, open TCP 8443 for the machines that run the `gaia` command.

## 6. Log in

On the workstation, unpack the `gaia` command for it from the release directory of step 2 and put it on your
`PATH` (here for a Mac with Apple silicon; `linux-amd64` on a Linux PC):

```bash
tar xzf gaia-<version>-darwin-arm64.tar.gz && sudo install -m 0755 gaia-<version>-darwin-arm64/gaia /usr/local/bin/gaia && gaia version
```

(The `gaia` command on the server works too.)

```bash
gaia login -server https://<server>:8443 -user admin
```

1. It shows the server certificate's sha256 and asks whether it is the one from step 4. Answer `y` only if it
   matches.
2. It asks for the password.
3. It prints `logged in to ... as admin (admin) until ...`.

The login is saved in `~/.config/gaia/cli.json` and lasts 12 hours.

Check it:

```bash
gaia whoami && gaia system
```

`gaia system` shows the version, the schema version (`8`), and the `evidence_key_id`. That key_id is the trust
anchor from step 4.

## 7. Create the repository

The repository is where backups (and Gaia's own state) go. It is a Kopia repository with Gaia's settings
(ADR-004).

```bash
gaia repositories add name=main location=/srv/gaia/main
```

It asks for the recovery kit's passphrase from step 4: a new repository means a new recovery kit that can open
it. The answer shows `splitter: FIXED-1M` and `hash: BLAKE2B-256-128`. The directory must be empty or not exist. If it
is not under `/srv/gaia`, the service may not write there. In that case, run `systemctl edit gaia`, add this
section, and restart:

```
[Service]
ReadWritePaths=<the directory>
```

### 7b. Or: a repository in an S3 bucket

The repository can instead be in a bucket of S3-compatible object storage (for example MinIO). You need: the
bucket (it must exist), an access key that may read, write, list and delete in it, and the endpoint's address.

```bash
gaia repositories add name=main kind=s3 location=https://<endpoint>:<port>/<bucket>/<folder> s3_access_key_id=<access key>
```

- It asks for the secret access key, then for the kit passphrase. Never put the secret key on the command line.
- `<folder>` is optional. Nothing may be stored under it yet.
- If the endpoint's TLS certificate is not signed by a CA the server trusts (a self-signed one), add
  `s3_ca_file=<CA certificate file>`: Gaia then checks the certificate against that file.
- `http://` instead of `https://` sends everything unencrypted on the network except the backup data itself, which
  Kopia encrypts. Use it only on a network you trust.
- Add `?region=<name>` to the location if the storage needs a region.
- The Gaia server must reach the endpoint (firewall).

The access key is kept on the server like the repository password and is part of the recovery kit (step 8b).
Everything else in this guide is the same for both kinds of repository.

## 8. Check the backup of Gaia's own state

Gaia backs up its database, the evidence ledger, the public evidence key and its config into the repository
every night at 02:30 (`state_backup_at` in `/etc/gaia/server.json`). Run one now and list them:

```bash
gaia state-backups run && gaia state-backups list
```

The new line must say `ok`.

Besides this nightly run, Gaia backs its state up a few minutes after backups and restore tests end, so a
rebuilt server (step 13) misses at most the last few minutes. The state backup holds no secrets: those are in
the recovery kit.

## 8b. Save the recovery kit off the server (do not skip)

If the server is lost, the repository can only be opened with the recovery kit and its passphrase. A kit that is
only on the server is lost with it. Run this on **your workstation** (where you ran `gaia login`), not on the
server:

```bash
gaia kit save
```

1. It downloads the newest kit, for example `gaia-recovery-kit-2.age`, into the current directory.
2. It asks for the kit's passphrase (step 4), opens the file with it, and tells the server that this kit is
   stored elsewhere.
3. It prints `Recovery kit 2 opens with the passphrase and is the server's newest: confirmed.`

Check it:

```bash
gaia kit status
```

The first line must start with `CONFIRMED`. Until it does, `gaia status` ends with `WARNING recovery kit: ...` and
`gaia system` shows the same.

Move the file to a safe place: a password manager that keeps files, an encrypted vault, or a USB stick in a
safe. Keep the passphrase separately (not in the same file or note).

**Every new repository writes a new kit** (and so does a new passphrase): `gaia kit status` then says
`NOT CONFIRMED` again, and you run `gaia kit save` again. Older kits cannot open repositories made after them.

If you only have a shell on the server: run `gaia kit save -o /root/gaia-recovery-kit.age` there, copy the file
off the server (`scp`), and delete it from the server.

The kit is a standard [age](https://age-encryption.org) file. To see what is inside without Gaia (it shows the
secrets, so do this only on a trusted machine): install `age` (`apt-get install age`, or `brew install age`), then
`age -d gaia-recovery-kit-2.age`. Gaia itself shows a summary without the secrets: `gaia kit show -kit
gaia-recovery-kit-2.age`.

## 8c. Notifications and the daily report

Gaia tells people when something changes: a VM's status (with the reason), a failed job, damage found in the
repository and its quarantine, a problem with the evidence ledger. Once a day it sends a **daily report**: every
VM's status, Proven RPO and RTO, and the evidence ledger's head, signed. Keep the reports: they are what proves
later that the evidence ledger was not cut short (step 13 and `gaia evidence verify -head`).

Add at least one channel. **Email**, through your mail server (STARTTLS on port 587 by default; `security` `tls`
for port 465). The password comes from the environment, never from the command line:

```bash
GAIA_SMTP_PASSWORD='the mail account password' gaia channels add name=ops-mail kind=smtp \
  smtp='{"host":"mail.example.org","username":"gaia@example.org","from":"Gaia <gaia@example.org>","to":["ops@example.org"]}'
```

Leave out `username` (and the password) if the mail server takes mail from the Gaia server without logging in. Add
`ca_file=/path/ca.pem` if the mail server's certificate is signed by your own CA.

A **webhook** (an HTTPS endpoint of your chat or ticket system, or your own receiver):

```bash
gaia channels add name=ops-hook kind=webhook webhook='{"url":"https://hooks.example.org/gaia"}'
```

The answer shows `webhook_secret` **once**: give it to the receiver. Every POST carries the event as JSON and the
header `X-Gaia-Signature: sha256=<HMAC-SHA256 of the body with that secret>`, so the receiver can check it came from
Gaia. `X-Gaia-Event` names the event and `X-Gaia-Delivery` numbers the message.

Check every channel now:

```bash
gaia channels list
gaia channels test <channel id>
```

`test` sends a message at once and answers with its `state`: `sent` is good. Otherwise `last_error` says why, and
Gaia keeps trying (after 1, 2, 4 ... minutes, at most hourly, for 24 hours). Until every enabled channel delivers,
`gaia status` ends with `WARNING notifications: ...`, and with no channel at all it says nobody is told.

`events=` limits a channel to some events, for example `events='["status","daily_report"]'`. The events are
`status`, `job_failed`, `integrity`, `quarantine`, `ledger` and `daily_report`. The daily report goes out at
`daily_report_at` (in `/etc/gaia/server.json`, default `07:00`, server time); `gaia daily-reports run` sends one now.
A channel is turned off with `gaia channels set <id> enabled=false` (and on again with `enabled=true`; it then
reports from that moment on). `gaia notifications list` shows every message and its state.

Gaia also checks the evidence ledger and every bundle with each daily report (and `gaia ledger check` does it now; its exit code is the verdict).
A ledger that does not verify, or is shorter than the last daily report says, is a `ledger` event and shows in
`gaia status` as `CRITICAL evidence ledger: ...`.

## 9. Users

There are three roles:

| Role | Can do |
|---|---|
| `admin` | Everything, including users, repositories and state backups. |
| `operator` | Manage hosts, VMs, disks and policies, and read everything except users and the audit log. |
| `auditor` | Read everything, the audit log included, and export evidence. Cannot change anything. |

Add a user. The command asks for the password; never put a password on the command line:

```bash
gaia users add username=<name> role=operator
```

Other user commands:

```bash
gaia users list
gaia users set <id> disabled=true
gaia users set <id> password=-
gaia password
```

- `disabled=true` disables a user and ends their sessions.
- `password=-` sets someone else's password; the command asks for it.
- `gaia password` changes your own password.

The last admin cannot be disabled or demoted.

**Wrong passwords.** After 5 wrong passwords in a row, a username is locked out for 15 minutes; `gaia login` then
answers `429 too many wrong passwords for this username` even for the right password. After 20 wrong passwords from
one address within 10 minutes, that address is refused the same way. Every attempt is in the audit log
(`gaia audit list`). An admin can end a lockout at once:

```bash
gaia users unlock <id>
```

The limits are `login_max_failures`, `login_lockout`, `login_address_failures` and `login_address_window` in
`/etc/gaia/server.json`.

## 10. Back up VMs on a KVM host

Gaia reaches a host only over SSH (ADR-007 §3): libvirt and the disk reads (NBD) both go through that one
connection to unix sockets on the host. Nothing on the host listens on a network port for Gaia.

What the host needs: Debian 12 or newer (libvirt ≥ 9.0, QEMU ≥ 7.2), OpenSSH, and VMs whose disks are qcow2 files.

**On the host, as root.** Create a user for Gaia. Membership in `libvirt` lets it control every VM on the host, so
treat its key like a root key. `libvirt-qemu` lets it open the disk export socket QEMU creates during a backup.
QEMU creates that socket in the VM's own directory, `/var/lib/libvirt/qemu/domain-<id>-<name>/`: libvirt's AppArmor
profile for the VM already allows it there, so AppArmor needs no change. Gaia backs up running VMs only.

```bash
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu gaia-backup && install -d -m 700 -o gaia-backup -g gaia-backup /home/gaia-backup/.ssh
```

**On the Gaia server.** Print the line for the host's `authorized_keys`:

```bash
gaia system -json | python3 -c 'import json,sys; print(json.load(sys.stdin)["ssh_authorized_keys_line"])'
```

It starts with `restrict,port-forwarding,command="/usr/bin/false"`: the key gets no shell, no terminal and no
TCP forwarding to other machines; it may only open unix sockets the user can open.

**On the host, as root.** Put that one line into the file (replace `<line>` with it, keep the single quotes):

```bash
echo '<line>' > /home/gaia-backup/.ssh/authorized_keys && chown gaia-backup: /home/gaia-backup/.ssh/authorized_keys && chmod 600 /home/gaia-backup/.ssh/authorized_keys && ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

The last command prints the host's key fingerprint (`SHA256:...`). Gaia trusts a host only after you confirm it.

**On the Gaia server.** Register the host, compare the fingerprint, trust it, and check the connection:

```bash
gaia hosts add name=<host> address=<address> ssh_user=gaia-backup
gaia hosts scan <host id>
gaia hosts trust <host id> fingerprint=SHA256:<the value printed on the host>
gaia hosts check <host id>
```

- `scan` shows the fingerprint the server sees. It must be the one printed on the host. If it is not, stop: something
  is between the server and the host.
- `trust` pins the key. From then on Gaia refuses the host if its key changes.
- `check` lists the VMs (libvirt domains) on the host.

Add a VM and a policy, and give the VM the policy. Gaia then backs it up every `backup_interval_s` seconds:

```bash
gaia policies add name=gold repository_id=1 backup_interval_s=900 test_interval_s=86400 keep_last=8 keep_daily=7 keep_weekly=4 rpo_target_s=3600 rto_target_s=1800 max_proof_age_s=172800 check_template=linux-basic
gaia vms add host_id=<host id> name=<libvirt domain name> policy_id=<policy id>
```

To back up now, and to follow it:

```bash
gaia vms backup <vm id>
gaia jobs get <job id>
gaia points list vm_id=<vm id>
```

- The first backup of a VM is full. Later ones are incremental: they read only what changed since the last point.
  Gaia makes a full backup again, and records why in the job, when the last point's checkpoint is no longer on the
  host, when a disk was added, removed or resized, or when the policy's repository changed.
- After each backup Gaia checks that every block of the new point is in the repository.
- Gaia keeps two checkpoints per VM on the host (the newest and the one before) and deletes older Gaia checkpoints.
  It does not touch checkpoints it did not make.
- Retention (`keep_last`, `keep_daily`, `keep_weekly`) removes old points after each backup. Their data is freed by
  the repository maintenance, at the earliest 24 hours later.

Repository verification reads the stored data back without the cache: every day at 03:30 a sample (2 % of the
blocks of every point), on Sundays all of it (`verify_at`, `full_verify_day`, `verify_sample` in
`/etc/gaia/server.json`). To run one now and see the result:

```bash
gaia repositories verify <repository id> mode=sampled
gaia verifications list
gaia verifications get <verification id>
```

A verification that finds damage is `critical` and lists the affected points; those points are marked failed. The
damaged data is also put in quarantine: Gaia stops reusing it, so the next backup of every affected VM is a full
backup that stores that data again (the job's reason says so). The quarantine is in the audit log. To see it:

```bash
gaia quarantine list
```

The damaged copy stays in the repository until maintenance frees it. Find out why it was damaged (the disk, the
storage) before you trust that storage again.

A failed point keeps its VM `CRITICAL` (`integrity check failed for point(s) [...]`) until retention prunes it,
even when newer points are proven: the damage is real until someone decides it no longer matters. Once the next
backup is proven (its restore test passed), an admin can remove the failed point, with the reason, which is
recorded on the point and in the audit log:

```bash
gaia points forget <point id> reason="<why it may go>"
```

Only a failed point can be forgotten; its data is deleted from the repository as retention would.

To prove a point restores, without touching the VM: `gaia points restore-check <point id>` restores each disk to a
scratch file on the Gaia server, prints its sha256, and deletes it. It needs free space for the largest disk under
`/var/lib/gaia`.

## 10b. Restore tests and the VM status

Gaia proves that backups can be restored: every `test_interval_s` seconds of a VM's policy it restores the newest
checked point into `/var/lib/gaia-sandbox/images`, boots it in an isolated network (no way out, and no way to the
server or the host) and runs the policy's checks. `check_template` names them:

| Template | Checks |
|---|---|
| `linux-basic` | The serial console shows a login prompt, and SSH (TCP 22) answers. |
| `linux-boot` | The serial console shows a login prompt. |
| `windows-basic` | Remote Desktop (3389) answers, and WinRM (5985) answers on `/wsman`. |

Every test is sealed as signed evidence. To run one now and see the result:

```bash
gaia vms test <vm id>
gaia tests list vm_id=<vm id>
```

The VM's **status** answers "which point can I go back to right now, and is that proven?":

```bash
gaia status
gaia status <vm id>
```

| Status | Meaning |
|---|---|
| `OK` | The newest proven point is within the RPO target, the proven restore time within the RTO target, and the proof is not older than `max_proof_age_s`. |
| `WARNING` | Backups are on time, but the proof misses a target (too old, or the restore was too slow). |
| `CRITICAL` | The newest checked backup is older than the RPO target, or the latest restore test failed, or a backup point was found damaged. |
| `UNKNOWN` | No restore test of the VM has passed yet, or something it needs is missing (no policy, no checked backup). |

`gaia status` exits with 0, 1, 2 or 3 for the worst status, so a monitoring system can run it. `gaia status <vm id>`
also lists every other reason. Every change of status is recorded with its time and reason:

```bash
gaia state-changes list entity=vm_status entity_id=<vm id>
```

## 10c. The screens and the auditor's export

Gaia's screens are on the same address as the API. Open it in a browser:

```
https://<server>:8443
```

1. The browser warns that the certificate is not trusted: it is the server's own certificate from step 4. Check
   its sha256 fingerprint in the browser's certificate view against the one from step 4, then continue.
2. Log in with a user from step 9. Each person has their own user; the login lasts 12 hours.

| Screen | Who sees it | What it shows |
|---|---|---|
| Overview | everyone | Every VM with its status, the point it can return to, Proven RPO, Proven RTO and proof age against their targets, the last 6 hours, and the reasons in words. |
| A VM (click its name) | everyone | The return point, the latest restore test with its console screenshots and checks, the evidence, the status history, the points and the restore tests. `Back up now` and `Test a restore now` only for `admin` and `operator`. |
| Evidence | `admin`, `auditor` | The ledger and its last check, the trust anchor, the newest daily report, every bundle, and **Download the evidence export**. |

Times on every screen are the server's local time; the zone is shown under the clock. The screens need
JavaScript; without it, use `gaia status`.

The **evidence export** is what an auditor takes away. It holds every bundle, the ledger, the trust anchor and
the current public key, the newest daily report with its signed ledger head, the evidence specification, a list of
every restore test, `SHA256SUMS`, and a `README.txt` that says how to verify it without Gaia's server. From the
command line (as `admin` or `auditor`):

```bash
gaia evidence export-all -o gaia-evidence.tar.gz
```

Verify it on any machine, after unpacking it:

```bash
gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles -head ledger-head-<n>.json
```

`<n>` is the daily report in the export (`ls ledger-head-*.json`). Compare the trust anchor's `key_id` in
`README.txt` with the one recorded in step 4 first. A daily report that reached you by email is a stronger anchor
than the one in the export: give it with `-head` as well.

## 10d. Migration checks (optional)

A migration check answers "will this VMware VM run on KVM?" with proof: Gaia reads a powered-off VM from vSphere
with **read-only calls only** (it never changes anything there), converts it with virt-v2v, boots the result in the
sandbox, runs the checks and seals a signed readiness report. Skip this step if you do not move VMs from VMware.

The conversion runs in a converter container under its own unprivileged user, `gaia-convert`, never as root and
never as gaia server. As root on the server:

1. Install podman and what rootless containers need, and OVMF for UEFI guests:

   ```bash
   apt-get install -y --no-install-recommends podman uidmap crun catatonit fuse-overlayfs ovmf
   ```

2. Create the converter's user. It needs `/dev/kvm` and its own range of user IDs; gaia server joins its group to
   reach it:

   ```bash
   useradd --system --home-dir /var/lib/gaia-convert --create-home --shell /usr/sbin/nologin gaia-convert
   usermod -aG kvm gaia-convert
   usermod --add-subuids 1000000-1065535 --add-subgids 1000000-1065535 gaia-convert
   usermod -aG gaia-convert gaia
   install -d -o gaia-convert -g gaia-convert -m 2770 /var/lib/gaia-convert/jobs
   ```

3. Build the converter image from the package's `Containerfile` (about 10 minutes; it downloads Fedora's
   packages), as `gaia-convert`, and print its ID:

   ```bash
   install -d -o gaia-convert -g gaia-convert /var/lib/gaia-convert/build /var/lib/gaia-convert/tmp
   install -m 0644 /usr/share/gaia/converter/Containerfile /var/lib/gaia-convert/build/
   systemd-run --wait --pipe --uid=gaia-convert --gid=gaia-convert -p RuntimeDirectory=gaia-convert \
     -p WorkingDirectory=/var/lib/gaia-convert/build -E HOME=/var/lib/gaia-convert \
     -E XDG_RUNTIME_DIR=/run/gaia-convert -E TMPDIR=/var/lib/gaia-convert/tmp \
     sh -c 'podman --cgroup-manager=cgroupfs build -q -t gaia-converter . | tail -1'
   ```

   The last line is the image's ID (64 hex digits). Pin it: the converter runs that image and no other.

   ```bash
   mkdir -p /etc/gaia
   echo '{"image": "sha256:<the 64 hex digits>"}' > /etc/gaia/convert.json
   ```

4. For Windows guests only: download virtio-win 0.1.302 (the drivers a Windows guest needs on KVM; Gaia does not
   ship them) to where the converter looks for it. It is checked against its sha256 on every use; without it,
   Windows guests are not converted and their check is `UNKNOWN`:

   ```bash
   curl -fL -o /var/lib/gaia-convert/virtio-win-0.1.302.iso \
     https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso
   echo "303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d  /var/lib/gaia-convert/virtio-win-0.1.302.iso" | sha256sum -c
   chown gaia-convert: /var/lib/gaia-convert/virtio-win-0.1.302.iso
   ```

5. Start the converter service, then restart gaia server (it joined a group), and check:

   ```bash
   systemctl enable --now gaia-convert
   systemctl restart gaia
   gaia convert info
   ```

   `gaia convert info` says `OK` (the image runs, virtio-win matches), `WARNING` (no virtio-win: Linux guests only) or
   `CRITICAL` (the image is missing or not the pinned one).

From the workstation, add the vSphere endpoint (an ESXi host or vCenter; a read-only user is enough). The command
asks for the password, or takes it from `GAIA_VSPHERE_PASSWORD`; it is stored in Gaia's secrets and never shown:

```bash
gaia sources add name=esxi url=https://<esxi>/sdk username=<user> [insecure=true]
gaia sources inventory <source id>
gaia sources check <source id> vm=<VM name>
gaia migration-checks list
gaia migration-checks report <check id> -o readiness.tar.gz
```

- `insecure=true` skips the check of the endpoint's certificate: only for a lab ESXi with its own certificate.
- The VM must be powered off (or a clone), have one disk and no snapshots; the inventory says which VMs can be
  checked and why not.
- The check picks its checks from the guest OS (`linux-basic`, or `windows-basic` for Windows: WinRM answers). Add
  `template=windows-rdp` when Remote Desktop is on, or `template=... checks=[...]` like a policy's.
- The **Migration** screen shows the same: the converter's state, every check with its steps and console, and, for
  admins and operators, each source's VMs with a **Check** button.
- The readiness report holds the signed bundle and says how to verify it offline.

## 11. What else the command line does

```bash
gaia vms set <vm id> policy_id=<policy id>
gaia repositories maintenance <repository id>
gaia audit list
gaia audit verify
gaia api GET /api/v1/hosts
```

- `gaia audit verify` checks that the audit log is unbroken.
- `gaia api` sends any API call. The full API is in `/api/v1/openapi.json`, also kept in the repository as
  `internal/server/openapi.json`.
- Every command takes `-json`.
- Exit code: 0 on success, 3 when something failed (the message says what).

## 12. Upgrading

1. Get the new release and check its signature (step 2), and copy its server package and `SHA256SUMS` to the
   server.
2. On the server, as root:

   ```bash
   cd /tmp && sha256sum -c --ignore-missing SHA256SUMS && apt-get install -y ./gaia_<new version>_amd64.deb
   ```

   It stops `gaia` first. The stop waits for running backups and restore tests to end (up to 10 minutes, step 5;
   it prints `stopping gaia; it waits for running jobs`), then the new version starts with the services that were
   running. The last line says `gaia: restarted; check with: gaia system`.
3. On the workstation, unpack the new `gaia` command (step 6) and check: `gaia system` shows the new version.

**From an install without the package** (before release 0.1.0, installed with `install` into `/usr/local/bin`):
the old service files in `/etc/systemd/system` would run in place of the package's, so the package refuses to
install over them. Remove them first, as root (add `gaia-convert` to the first and last command and its file to
the second if you set up migration checks):

```bash
systemctl disable --now gaia gaia-sandbox
rm -f /etc/systemd/system/gaia.service /etc/systemd/system/gaia-sandbox.service /etc/systemd/system/gaia-convert.service /usr/local/bin/gaia && systemctl daemon-reload
cd /tmp && sha256sum -c --ignore-missing SHA256SUMS && apt-get install -y ./gaia_<new version>_amd64.deb
systemctl enable --now gaia-sandbox gaia
```

Your settings stay: `/etc/gaia`, `/var/lib/gaia`, the repositories, and `systemctl edit` changes
(`/etc/systemd/system/gaia.service.d`).

A new version brings its database migrations with it. They run when the service starts, and `gaia system` shows the
new schema version. Gaia refuses to start on a database written by a newer version: do not downgrade.

From schema 5 to 6 (T18) there is nothing else to do. From schema 6 to 7 (T19): add a notification channel (step 8c).

**From a version without the recovery kit** (schema 4 or older): write the first kit and save it (step 8b):

```bash
gaia kit regenerate && gaia kit save
```

`gaia kit regenerate` asks twice for a new passphrase; put it in your password manager.

## 13. Rebuilding a lost server

When the Gaia server is lost (its disk died, the VM was deleted) but the repository survived, a new server takes
over from the recovery kit and the repository alone. Everything comes back as of Gaia's last state backup: users,
hosts, VMs, policies, points, restore tests, evidence, the audit log, and each VM's status.

You need: the recovery kit file from step 8b, its passphrase, the repository (the disk or share it was on), and a
new Debian 12 machine (step 1).

1. On the new machine, do step 3 (and step 2 for the package; the same Gaia version or a newer one). Do **not** run
   `gaia server init`.
2. Make the repository reachable on the new machine, for example by mounting its disk or share. The same path as
   before is simplest; `gaia kit show -kit <kit file>` shows where each repository was. Then, as root:

   ```bash
   chown -R gaia:gaia <repository path>
   ```

   An S3 repository needs nothing here: the kit holds its location and access key. The new server must reach the
   endpoint.

3. Copy the kit file to the new machine (`scp gaia-recovery-kit-<n>.age <user>@<server>:/tmp/`), then, as root:

   ```bash
   chown gaia /tmp/gaia-recovery-kit-<n>.age && runuser -u gaia -- gaia server restore-state -kit /tmp/gaia-recovery-kit-<n>.age -config /etc/gaia/server.json
   ```

   It asks for the kit's passphrase. Add `-repository <name>=<path>` if a repository is now at another path (for S3,
   `-repository <name>=https://<endpoint>/<bucket>/<folder>`), and
   `-hostname <names>` (as in step 4) if the new server has another name or address: it then gets a new TLS
   certificate, and `gaia login` asks you to confirm the new sha256 it prints.

   The first line says `OK` or `WARNING`, then what was restored. `WARNING` lists what could not be restored,
   usually the work of the last minutes before the loss (a point or a test after the last state backup). If it
   says `CRITICAL`, the restored evidence ledger does not verify: keep the output and do not delete anything.
4. Start the services (step 5), log in (step 6; the rebuild ended every login made on the lost server) and look:

   ```bash
   gaia status && gaia kit status
   ```

   Every VM is back with its status. The next backup of a VM continues from its last point if that point's
   checkpoint is still on the host; otherwise it is a full backup, and the job says why. Hosts need no change:
   the new server logs in with the same SSH key. (If a host's firewall allows SSH only from the old server's
   address, allow the new one.)
5. The rebuild wrote a new recovery kit. Save it (step 8b), then delete `/tmp/gaia-recovery-kit-<n>.age` on the
   server.
6. Notification channels come back, but not their secrets (SMTP passwords, webhook secrets are in neither the
   state backup nor the kit): the rebuild lists each such channel. Turn it off (`gaia channels set <id>
   enabled=false`) and add it again under a new name (step 8c). Then check the evidence ledger against the newest
   daily report you received before the loss (the attachment `gaia-daily-report-<n>.json` in your mailbox), on the
   server as root; the key's `key_id` printed first must be the `evidence_key_id` in that report:

   ```bash
   gaia evidence verify -pubkey /var/lib/gaia/keys/evidence-key.pub -ledger /var/lib/gaia/evidence/ledger.jsonl -evidence-dir /var/lib/gaia/evidence/bundles -head gaia-daily-report-<n>.json
   ```

   `ledger_truncated` means the rebuilt ledger lacks entries that report saw: the restore tests of the last
   minutes before the loss. Their bundles are listed by the rebuild; nothing else may be missing.

## 14. Removing Gaia

`apt-get purge gaia` removes the program and the services but keeps Gaia's data. This deletes everything Gaia
wrote too; the repository and its backups are deleted if they are under `/srv/gaia`:

```bash
apt-get purge -y gaia && rm -rf /etc/gaia /var/lib/gaia /var/lib/gaia-sandbox /srv/gaia && userdel gaia
```

With migration checks (step 10d), also: `rm -rf /var/lib/gaia-convert && userdel gaia-convert`.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `ssh-keygen -Y verify` does not say `Good "gaia-release" signature` | The files are not the release, or `allowed_signers` is not the line from step 2. Do not install them. |
| `apt-get install` stops with `... overrides the packaged unit` | An install without the package is still there: step 12, "From an install without the package". Then `dpkg --configure gaia`. |
| `apt-get install` waits after `stopping gaia` | It waits for running jobs (step 5), up to 10 minutes. |
| macOS: `"gaia" cannot be opened because the developer cannot be verified` | The browser marked the download. After the signature check of step 2: `sudo xattr -d com.apple.quarantine /usr/local/bin/gaia`. |
| `gaia login` cannot connect | The service is not running (step 5), or a firewall blocks 8443. |
| The certificate sha256 at login differs from step 4 | Do not log in. If the server was set up again, use the new value from that `init`. |
| `the server's certificate is not the one pinned at login` | The server's certificate changed. Log in again only if that was on purpose. |
| `401 unauthorized` | The login expired (12 hours) or the user was disabled: run `gaia login` again. |
| `429 ... too many wrong passwords` | Wait until the time it names, or an admin runs `gaia users unlock <id>` (step 9). The audit log shows the attempts. |
| `403 forbidden` | Your role may not do that (step 9). |
| `503 ... the audit log cannot be written` | The disk under `/var/lib/gaia` is full or read-only. Gaia does nothing it cannot record. |
| `state-backups run` says `failed` | The message says why; the journal has details (`journalctl -u gaia`). |
| `hosts scan` or `check` says `502 host: ...` | The server cannot reach the host's SSH (address, port, firewall), or the key is not in the host user's `authorized_keys`. |
| `host key ... is not the pinned one` | The host's SSH key changed. If that was on purpose (the host was reinstalled), run `scan` and `trust` again. |
| A backup job fails with `permission denied` on a socket | The host user is not in the groups `libvirt` and `libvirt-qemu` (step 10). |
| A backup job fails with `... is not running` | Gaia backs up running VMs only (libvirt pull-mode backup). Start the VM. |
| Restore tests stay `UNKNOWN` with `sandbox helper ... connect` | `gaia-sandbox` is not running: `systemctl status gaia-sandbox`. |
| A restore test is `CRITICAL` with `boot ... no login prompt` | The restored VM did not boot. The evidence (`gaia evidence export`) has its screenshots and serial log. |
| The service does not start | `journalctl -u gaia -n 50` shows why: a wrong config, missing files, or a database from a newer version. |
| `gaia status` ends with `WARNING recovery kit: ...` | Do what it says: `gaia kit save` (step 8b), or `gaia kit regenerate` and then `gaia kit save`. |
| `the passphrase does not open the current recovery kit` | Use the kit passphrase from step 4 (not the admin password). |
| `restore-state`: `... is not empty` | The data directory must be empty. A failed `restore-state` empties it again itself; anything else in it is not Gaia's. |
| `restore-state`: `the state repository ... cannot be opened` | Mount the repository, or give its path with `-repository <name>=<path>`; its files must belong to `gaia`. |
| `restore-state`: `... belong to different installations` | The kit is not the one of the server that wrote this repository. |
| S3: `x509: certificate signed by unknown authority` | The endpoint's certificate is self-signed: add `s3_ca_file=<CA file>` (step 7b). |
| S3: `The specified bucket does not exist` or `Access Denied` | Create the bucket, or check the access key and its permissions. |
| `gaia status` ends with `WARNING notifications: ...` | No channel, or the last message of one failed: `gaia notifications list` shows `last_error`; fix it and `gaia channels test <id>` (step 8c). |
| A channel's messages fail with `x509: certificate signed by unknown authority` | Add the channel again with `ca_file=` (turn the old one off first). |
| `CRITICAL evidence ledger: ... ledger_truncated` | The ledger is shorter than the last daily report or the catalog says: entries were cut off. Do not trust the evidence until you know why; the problems list names the entries. Keep the daily reports. |
| A verification is `critical` and `gaia quarantine list` shows contents | The repository's storage damaged data. The affected VMs back up full next; check the disk or storage (step 10). Once a newer point is proven, `gaia points forget` (step 10) clears the `CRITICAL`. |
