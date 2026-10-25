# Lab örneği

Gaia PoC'sinin Proxmox lab'i için platform reposu ve kontrat örneği. Adresler lab'e ait: platform VM'i 192.168.1.26, lab ağı 10.50.0.0/24. Lab'in kendisi bu reponun dışında kuruluyor.

```
platform/             platform reposu: Platform, Provider'lar, katalog, Policy
  platform.yaml
  policy.yaml
  providers/
  catalog/
contracts/            kontrat reposu
  poc-web.yaml
```

PoC'de `Network` ve `AnsibleRole` elle yazılıyor. Source kind'ları (`NetworkSource`, `AnsibleRoleSource`) PoC kapsamında değil. `labels.owner` alanının bir `Team` referansı olduğu kontrolü de PoC'de yapılmıyor (bkz. `docs/poc.md`).
