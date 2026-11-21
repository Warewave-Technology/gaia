#!/usr/bin/env bash
# The embedded password blocklist (ADR-008 §6): common and breached passwords from a pinned public list, kept only
# where they can matter (Gaia refuses passwords shorter than 15 characters anyway), lowercased, sorted, unique.
#
#   scripts/build-blocklist.sh [SOURCE_FILE]
#
# Without SOURCE_FILE the list is downloaded from the pinned commit. Either way its sha256 must be the pinned one.
# Writes internal/auth/blocklist.txt.gz and prints the sha256 of the uncompressed list, which internal/auth pins
# (blocklistSHA256): a release build that changes the list changes that constant too, in the same commit.
set -euo pipefail

# SecLists (MIT), Passwords/Common-Credentials/xato-net-10-million-passwords-1000000.txt.
commit=c205c36a445bff37f8e58a9ec829105cd4975c58
path=Passwords/Common-Credentials/xato-net-10-million-passwords-1000000.txt
want=424a3e03a17df0a2bc2b3ca749d81b04e79d59cb7aeec8876a5a3f308d0caf51
url="https://raw.githubusercontent.com/danielmiessler/SecLists/$commit/$path"

here="$(cd "$(dirname "$0")/.." && pwd)"
out="$here/internal/auth/blocklist.txt.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

src="${1:-}"
if [[ -z "$src" ]]; then
	src="$tmp/source.txt"
	curl -fsSL -o "$src" "$url"
fi
got="$( (sha256sum "$src" 2>/dev/null || shasum -a 256 "$src") | cut -d' ' -f1)"
[[ "$got" == "$want" ]] || { echo "CRITICAL: $src has sha256 $got, want $want" >&2; exit 2; }

LC_ALL=C tr -d '\r' <"$src" | LC_ALL=C tr '[:upper:]' '[:lower:]' | LC_ALL=C awk 'length($0) >= 15 && length($0) <= 512' |
	LC_ALL=C sort -u >"$tmp/list.txt"
gzip -n -9 <"$tmp/list.txt" >"$out"
echo "entries: $(wc -l <"$tmp/list.txt" | tr -d ' ')"
echo "blocklistSHA256: $( (sha256sum "$tmp/list.txt" 2>/dev/null || shasum -a 256 "$tmp/list.txt") | cut -d' ' -f1)"
