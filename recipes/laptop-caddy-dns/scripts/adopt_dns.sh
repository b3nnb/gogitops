#!/bin/bash
# Idempotent DNS adoption: point NetworkManager at AdGuard so internal
# *.bennbot.io names resolve. No-op when already resolving via the mapper.

if getent hosts ctl.bennbot.io 2>/dev/null | grep -qE '^10\.2\.0\.(102|105)'; then
  echo "already-adopted: ctl.bennbot.io resolves via the mapper"
  exit 0
fi

command -v nmcli >/dev/null 2>&1 || { echo "BLOCKED: NetworkManager (nmcli) not found on this node"; exit 1; }

CONN=$(nmcli -t -f NAME,TYPE con show --active 2>/dev/null | grep -E ':802-11-wireless|:ethernet' | head -1 | cut -d: -f1)
[ -z "$CONN" ] && { echo "BLOCKED: no active NetworkManager connection found"; exit 1; }

if sudo -n true 2>/dev/null; then
  sudo -n nmcli con mod "$CONN" ipv4.dns "10.2.0.103 1.1.1.1" ipv4.ignore-auto-dns yes
  sudo -n nmcli con up "$CONN" >/dev/null 2>&1
  echo "dns-set: '$CONN' -> 10.2.0.103 1.1.1.1"
else
  echo "NEEDS-SUDO: run manually ->"
  echo "  sudo nmcli con mod \"$CONN\" ipv4.dns '10.2.0.103 1.1.1.1' ipv4.ignore-auto-dns yes && sudo nmcli con up \"$CONN\""
  exit 1
fi
