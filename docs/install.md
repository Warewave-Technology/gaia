# Installing gaia server

This guide installs `gaia server` (ADR-007) on a clean Debian 12 machine and checks that it works. It takes about
15 minutes. Every command is meant to be copied as it is; replace only what is in `<angle brackets>`.

What this version does: users and roles, the API and the command line, the audit log, repositories, hosts, VMs
and policies as records, and a daily backup of Gaia's own state. Backing up and testing VMs comes in the next
versions.

## 1. What you need

- **Server:** Debian 12 (bookworm), amd64, 2 vCPU, 4 GiB RAM, and enough disk for the repository (start with 50 GiB
  under `/srv/gaia`). It must be reachable on TCP port 8443 from wherever you will run the `gaia` command.
- **Build machine:** any machine with this repository and Go (the version in `go.mod`). This is where you make the
  package. Your Mac is fine.

## 2. Make the package (on the build machine)

```bash
cd <path to the gaia repository> && make dist
```

It prints the package's name, for example `dist/gaia-b2c12e5-linux-amd64.tar.gz`; `<version>` below is the part
between `gaia-` and `-linux` (here `b2c12e5`). Copy it to the server, as any user who can log in there:

```bash
scp dist/gaia-<version>-linux-amd64.tar.gz <user>@<server>:/tmp/
```

## 3. Install (on the server, as root)

Log in to the server and become root (`su -`, or `sudo -i`). Steps 3 to 5 and 8 run as root.

Unpack the package and check it:

```bash
cd /tmp && tar xzf gaia-<version>-linux-amd64.tar.gz && cd gaia-<version>-linux-amd64 && sha256sum -c SHA256SUMS
```

Every line must end in `OK`.

Create the `gaia` user and its directories, and install the program:

```bash
useradd --system --user-group --home-dir /var/lib/gaia --shell /usr/sbin/nologin gaia
install -d -o gaia -g gaia -m 0750 /var/lib/gaia /etc/gaia /srv/gaia
install -m 0755 gaia /usr/local/bin/gaia
install -m 0644 gaia.service /etc/systemd/system/gaia.service
```

## 4. Set up Gaia (on the server, as root)

Replace `<names>` with the DNS name and IP address you will use to reach the server, separated by commas (for
example `gaia.lab,192.168.1.40`). They go into the server's TLS certificate.

```bash
runuser -u gaia -- gaia server init -config /etc/gaia/server.json -data-dir /var/lib/gaia -hostname <names> -admin admin
```

It asks twice for the password of the first admin user (`admin`); it must have at least 12 characters.

It then prints two values. **Write both down now**, for example in your password manager:

| Value | What it is for |
|---|---|
| `TLS certificate sha256` | At the first `gaia login` you compare it with what the command shows. If they differ, someone is in between. |
| `evidence trust anchor` | The key that signs all evidence. An auditor checks exported evidence against this value. |

## 5. Start the service (on the server, as root)

```bash
systemctl daemon-reload && systemctl enable --now gaia && systemctl status gaia --no-pager
```

The status should say `active (running)`. The log is in the journal:

```bash
journalctl -u gaia --no-pager -n 20
```

It should contain `gaia server is listening`, with `address=[::]:8443`.

If the server has a firewall, open TCP 8443 for the machines that run the `gaia` command.

## 6. Log in

Run the `gaia` command on the server itself, or copy `/usr/local/bin/gaia` to your workstation. On a Mac, build it
with `make build` and use `bin/gaia`.

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

`gaia system` shows the version, the schema version (`2`), and the `evidence_key_id`. That key_id is the trust
anchor from step 4.

## 7. Create the repository

The repository is where backups (and Gaia's own state) go. It is a Kopia repository with Gaia's settings
(ADR-004).

```bash
gaia repositories add name=main location=/srv/gaia/main
```

The answer shows `splitter: FIXED-1M` and `hash: BLAKE2B-256-128`. The directory must be empty or not exist. If it
is not under `/srv/gaia`, the service may not write there. In that case, run `systemctl edit gaia`, add this
section, and restart:

```
[Service]
ReadWritePaths=<the directory>
```

## 8. Check the backup of Gaia's own state

Gaia backs up its database, the evidence ledger, the public evidence key and its config into the repository
every night at 02:30 (`state_backup_at` in `/etc/gaia/server.json`). Run one now and list them:

```bash
gaia state-backups run && gaia state-backups list
```

The new line must say `ok`.

**Important — keep these files somewhere else.** On purpose, the state backup does not contain secrets:

- `/var/lib/gaia/secrets/repository-main.password`: the repository's password. Without it the repository,
  including the state backups, cannot be read.
- `/var/lib/gaia/keys/evidence-key.pem`: the private evidence signing key.

Copy both now to a safe place outside this server, for example an encrypted vault or a USB stick in a safe. If the
server is lost and these files are not saved elsewhere, the repository cannot be opened.

```bash
tar -C /var/lib/gaia -czf /root/gaia-secrets.tar.gz secrets keys/evidence-key.pem && chmod 600 /root/gaia-secrets.tar.gz
```

Then move `/root/gaia-secrets.tar.gz` off the server and delete it there.

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

## 10. What else the command line does

```bash
gaia hosts add name=<host> address=<address> ssh_user=<user>
gaia vms add host_id=<id> name=<libvirt domain name>
gaia policies add name=gold repository_id=1 backup_interval_s=900 test_interval_s=86400 keep_last=8 keep_daily=7 keep_weekly=4 rpo_target_s=3600 rto_target_s=1800 max_proof_age_s=172800 check_template=linux-basic
gaia vms set <vm id> policy_id=<policy id>
gaia audit list
gaia audit verify
gaia api GET /api/v1/hosts
```

- `gaia audit verify` checks that the audit log is unbroken.
- `gaia api` sends any API call. The full API is in `/api/v1/openapi.json`, also kept in the repository as
  `internal/server/openapi.json`.
- Every command takes `-json`.
- Exit code: 0 on success, 3 when something failed (the message says what).

## 11. Upgrading

1. Make and copy a new package (step 2) and unpack it (step 3, first command).
2. On the server, as root, replace the program and restart:

   ```bash
   install -m 0755 gaia /usr/local/bin/gaia && systemctl restart gaia && gaia system
   ```

A new version brings its database migrations with it. They run when the service starts, and `gaia system` shows the
new schema version. Gaia refuses to start on a database written by a newer version: do not downgrade.

## 12. Removing Gaia

This deletes everything Gaia wrote. The repository and its backups are deleted too if they are under `/srv/gaia`.

```bash
systemctl disable --now gaia && rm -f /etc/systemd/system/gaia.service /usr/local/bin/gaia && rm -rf /etc/gaia /var/lib/gaia /srv/gaia && userdel gaia
```

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `gaia login` cannot connect | The service is not running (step 5), or a firewall blocks 8443. |
| The certificate sha256 at login differs from step 4 | Do not log in. If the server was set up again, use the new value from that `init`. |
| `the server's certificate is not the one pinned at login` | The server's certificate changed. Log in again only if that was on purpose. |
| `401 unauthorized` | The login expired (12 hours) or the user was disabled: run `gaia login` again. |
| `403 forbidden` | Your role may not do that (step 9). |
| `503 ... the audit log cannot be written` | The disk under `/var/lib/gaia` is full or read-only. Gaia does nothing it cannot record. |
| `state-backups run` says `failed` | The message says why; the journal has details (`journalctl -u gaia`). |
| The service does not start | `journalctl -u gaia -n 50` shows why: a wrong config, missing files, or a database from a newer version. |
