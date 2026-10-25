#!/bin/sh
# Geliştirme AD'sinin içeriği: Teams OU'sunda takımlar ve onaycı grupları,
# iç içe bir grup, kişiler ve Gaia'nın okuma hesabı. Tekrar çalıştırılabilir.
set -u
# t runs samba-tool: "already exists" is fine (the script runs again),
# anything else is tried again a few times, then stops the script.
t() {
  for i in 1 2 3 4 5; do
    out=$(samba-tool "$@" 2>&1) && return 0
    case "$out" in *"already exist"*|*"already a member"*|*"ALREADY_EXISTS"*|*"is already"*) return 0 ;; esac
    sleep 2
  done
  echo "samba-tool $*: $out" >&2
  exit 1
}
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
# Gaia'nın yazma hesabı: sadece DNS zone'ları (DnsAdmins) ve OU=Servers'taki
# bilgisayar hesapları. Kullanıcı ve gruplara yazamaz.
t user create gaia-writer "Dev-gaia-writer-Password-1!" --userou="OU=People" --description="Gaia yazma hesabı"
t group addmembers DnsAdmins gaia-writer
t ou create "OU=Servers,$B"
# OU=Servers'ta bilgisayar hesabı açma ve silme, açtıklarında tam yetki.
SID=$(samba-tool user show gaia-writer --attributes=objectSid | sed -n 's/^objectSid: //p')
COMPUTER=bf967a86-0de6-11d0-a285-00aa003049e2
t dsacl set --objectdn="OU=Servers,$B" --sddl="(OA;CI;CCDC;$COMPUTER;;$SID)"
t dsacl set --objectdn="OU=Servers,$B" --sddl="(OA;CIIO;GA;;$COMPUTER;$SID)"
ADMIN="-U administrator --password=$(cat /run/secrets/samba-admin-password)"
t dns zonecreate 127.0.0.1 0.50.10.in-addr.arpa $ADMIN
echo "AD hazır: $(samba-tool group listmembers SG-Team-Web | tr '\n' ' ')"
