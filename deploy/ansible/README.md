# gaia.server

Installs Gaia on a VM (design 13.2): the package, `/etc/gaia`, the secret
files, the schema migrations, Pulumi's CLI and plugins, and the services.

```yaml
- hosts: gaia
  become: true
  roles:
    - role: gaia.server
      vars:
        gaia_package_file: dist/gaia-1.0.0-1.x86_64.rpm   # or a repository with the gaia package
        gaia_env:
          GAIA_DSN: postgres://gaia_app@db:5432/gaia?sslmode=verify-full
          GAIA_BAO_ADDR: https://bao:8200
          GAIA_BAO_ROLE_ID: "{{ vault_role_id }}"
          GAIA_SERVER_ARGS: --listen 0.0.0.0:8080 --repo https://github.com/example/platform.git --repo-credentials bao://kv/platform/git
        gaia_bao_secret_id: "{{ vault_secret_id }}"
        gaia_migrate_dsn: "{{ gaia_owner_dsn }}"
```

`defaults/main.yml` lists every variable. Secrets go to files of their own
(`/etc/gaia/bao-secret-id`, `/etc/gaia/ipam-token`, `root:gaia 0640`) and
are never logged; `gaia.env` holds none. The migration runs once per play,
with the schema owner, before the services start. Workers get podman for
their Ansible runs. `gaia pulumi prepare` runs as the service account, so
that runs download nothing.

Tested on Rocky 9 (systemd container) with a local package and a real
PostgreSQL; a second run changes nothing.
