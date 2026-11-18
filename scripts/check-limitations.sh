#!/usr/bin/env bash
# T22 gate (a): docs/limitations.md exists, and every item in it (a line starting "- **") has its reason, a line
# "*Why it waits:*" before the next item or heading. Prints the items without one; exit 0 when there are none.
set -euo pipefail

f="${1:-$(dirname "$0")/../docs/limitations.md}"
[[ -f "$f" ]] || { echo "CRITICAL: $f does not exist"; exit 2; }
awk '
	function close_item() { if (item != "" && !why) { print "no reason: " item; bad++ } item = ""; why = 0 }
	/^- \*\*/ { close_item(); item = $0; n++; next }
	/^#/ { close_item(); next }
	/\*Why it waits:\* [^ ]/ { why = 1 }
	END { close_item(); printf "%d items, %d without a reason\n", n, bad; exit (bad > 0 || n == 0) ? 2 : 0 }
' "$f"
