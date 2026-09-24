#!/bin/bash
# Verify internal names resolve via lan-proxy AdGuard after adoption.
# Informational — reports, never fails the recipe.

if command -v getent >/dev/null 2>&1; then
  R=$(getent hosts paperclip.bennbot.io 2>/dev/null | head -1 | awk '{print $1}')
else
  R=$(dscacheutil -q host -a name paperclip.bennbot.io 2>/dev/null | awk '/ip_address/{print $3; exit}')
fi
[ -z "$R" ] && { echo "FAIL: paperclip.bennbot.io does not resolve — DNS not pointing at AdGuard?"; exit 0; }

echo "paperclip.bennbot.io -> $R"

if command -v curl >/dev/null 2>&1; then
  C=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://paperclip.bennbot.io/api/health 2>/dev/null)
  echo "paperclip http: $C (expect 200)"
fi
