#!/bin/bash
# Verify internal names resolve via AdGuard and serve through the mapper.
# Informational: also probes the lan-proxy smoke container on the NAS if up.

R=$(getent hosts ctl.bennbot.io 2>/dev/null | head -1)
if [ -z "$R" ]; then
  echo "FAIL: ctl.bennbot.io does not resolve — DNS not pointing at AdGuard?"
  exit 0
fi

C=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://ctl.bennbot.io 2>/dev/null)
echo "ctl.bennbot.io -> $R | http:$C"

# Optional probe: smoke-test internal caddy on the NAS (lan-proxy-smoke container)
S=$(curl -s -o /dev/null -w '%{http_code}' -H "Host: curation.bennbot.io" --max-time 5 http://10.2.0.103:8085 2>/dev/null)
echo "smoke-curation (10.2.0.103:8085, Host: curation.bennbot.io) -> http:$S"
