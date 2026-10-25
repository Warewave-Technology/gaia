# Config reposu (PoC)

PoC'de Gaia'nın config reposu bu dizin (`config-repo` provider'ı, `path: ansible`). Gaia her run'da bu dizini ilgili commit'ten çıkarır ve ansible-runner'a proje olarak verir.

```
ansible.cfg
baseline/linux.yml        Baseline: linux-base
roles/gaia_linux_base/    baseline'ın rolü
```

## Gaia'nın verdiği değişkenler

Her sunucu envanterde UID'siyle durur (`inventory_hostname`); `ansible_host` IP adresidir.

| Değişken | Örnek |
|---|---|
| `gaia_uid` | `xfaoec2tq4ekespr` |
| `gaia_hostname` | `labtpoc01` |
| `gaia_contract` | `poc-web` |
| `gaia_site` | `lab` |
| `gaia_labels` | `{app: poc, env: test, owner: team-platform}` |
| `gaia_ntp` | Site'ın NTP sunucuları |
| `gaia_dns` | Network'ün DNS sunucuları |
| `gaia_app` | App playbook'larında: `{name, artifact: <Nexus adresi>, sha256: <Nexus'un bildirdiği; yoksa boş>}` |
| `gaia_domain_join` | Network'te `domain` varsa: `{domain: corp.local, password: <tek kullanımlık>}`. Gaia'nın `gaia_domain_join` rolü baseline'dan önce bununla domain'e katılır; parola run olaylarında maskelenir |
