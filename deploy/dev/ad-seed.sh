#!/bin/sh
# Geliştirme AD'sinin içeriği: Teams OU'sunda takımlar ve onaycı grupları,
# iç içe bir grup, kişiler ve Gaia'nın okuma hesabı. Tekrar çalıştırılabilir.
set -u
t() { samba-tool "$@" >/dev/null 2>&1 || true; }
B="DC=gaia,DC=dev"
t ou create "OU=Teams,$B"
t ou create "OU=People,$B"
for g in SG-Team-Platform SG-Team-Platform-Approvers SG-Team-Web SG-Team-Web-Approvers SG-Web-Contractors; do
  t group add "$g" --groupou="OU=Teams" --description="$g"
done
user() { # kullanıcı, ad, soyad
  t user create "$1" "Dev-$1-Password-1!" --userou="OU=People" --given-name="$2" --surname="$3" --mail-address="$1@gaia.dev"
}
user ayse Ayşe Kaya
user mehmet Mehmet Yılmaz
user can Can Demir
user zeynep Zeynep Ak
user ali Ali Veli
t user create gaia-reader "Dev-gaia-reader-Password-1!" --userou="OU=People" --description="Gaia okuma hesabı"
t group addmembers SG-Team-Platform ayse,mehmet
t group addmembers SG-Team-Platform-Approvers ayse
t group addmembers SG-Team-Web can,SG-Web-Contractors
t group addmembers SG-Team-Web-Approvers can
t group addmembers SG-Web-Contractors zeynep
t user disable ali
t group addmembers SG-Team-Web ali
echo "AD hazır: $(samba-tool group listmembers SG-Team-Web | tr '\n' ' ')"
