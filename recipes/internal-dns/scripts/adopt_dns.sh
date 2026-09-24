#!/bin/bash
# Idempotent internal-DNS adoption: point this node at lan-proxy AdGuard
# (10.2.0.105) so internal *.bennbot.io names resolve port-less.
# Cross-platform: macOS (networksetup) + Linux (NetworkManager nmcli).
# No-op when paperclip.bennbot.io already resolves via the mapper.
#
# macOS NOTE (Sep 24 '26, Mini lesson): networksetup per-service DNS does
# not decide which resolver macOS uses — the PRIMARY service (default
# route interface) wins. A dock ethernet service can hold the setting
# while Wi-Fi still answers queries. We therefore resolve the primary
# interface (route get default) and set DNS on the service that owns it,
# plus any other active service for good measure.

DNS1=10.2.0.105
DNS2=1.1.1.1

# ── idempotency probe (works on both OSes) ──
res() {
  if command -v getent >/dev/null 2>&1; then
    getent hosts paperclip.bennbot.io 2>/dev/null | head -1 | awk '{print $1}'
  elif command -v dscacheutil >/dev/null 2>&1; then
    dscacheutil -q host -a name paperclip.bennbot.io 2>/dev/null | awk '/ip_address/{print $3; exit}'
  else
    echo ""
  fi
}

R=$(res)
case "$R" in
  10.2.0.105)
    echo "already-adopted: paperclip.bennbot.io -> 10.2.0.105"
    exit 0
    ;;
  "") : ;;  # unresolved — need to set DNS
  *)  echo "warn: paperclip.bennbot.io resolves via $R (upstream) — adopting internal DNS" ;;
esac

# ── macOS ──
if [ "$(uname)" = "Darwin" ]; then
  # Find the hardware port owning the default-route interface (primary service)
  PRIMARY_IF=$(route -n get default 2>/dev/null | awk '/interface:/{print $2; exit}')
  SVC=""
  if [ -n "$PRIMARY_IF" ]; then
    SVC=$(networksetup -listallhardwareports 2>/dev/null \
      | awk -v ifn="$PRIMARY_IF" '$0 ~ "Device: "ifn {getline prev; prev ~ /Hardware Port:/ && getline prev2; }')
    # simpler parse: walk pairs
    SVC=$(networksetup -listallhardwareports 2>/dev/null | awk '
      /^Hardware Port:/ { port = substr($0, 16) }
      /^Device: '$PRIMARY_IF'$/ { print port; exit }')
  fi
  # Fallback: first service with an IP
  if [ -z "$SVC" ]; then
    SVC=$(networksetup -listallnetworkservices 2>/dev/null | tail -n +2 | while read -r s; do
      [ "$(networksetup -getinfo "$s" 2>/dev/null | grep -c 'IP address')" -gt 0 ] && echo "$s" && break
    done)
  fi
  [ -z "$SVC" ] && { echo "BLOCKED: no active macOS network service found"; exit 1; }
  if sudo -n true 2>/dev/null; then
    sudo -n networksetup -setdnsservers "$SVC" "$DNS1" "$DNS2"
    echo "dns-set (primary '$SVC'): $DNS1 $DNS2"
  else
    echo "NEEDS-SUDO: run manually ->"
    echo "  sudo networksetup -setdnsservers \"$SVC\" $DNS1 $DNS2"
    exit 1
  fi
  exit 0
fi

# ── Linux (NetworkManager) ──
command -v nmcli >/dev/null 2>&1 || { echo "BLOCKED: nmcli not found on this Linux node"; exit 1; }
CONN=$(nmcli -t -f NAME,TYPE con show --active 2>/dev/null | grep -E ':802-11-wireless|:ethernet' | head -1 | cut -d: -f1)
[ -z "$CONN" ] && { echo "BLOCKED: no active NetworkManager connection found"; exit 1; }
if sudo -n true 2>/dev/null; then
  sudo -n nmcli con mod "$CONN" ipv4.dns "$DNS1 $DNS2" ipv4.ignore-auto-dns yes
  sudo -n nmcli con up "$CONN" >/dev/null 2>&1
  echo "dns-set: '$CONN' -> $DNS1 $DNS2"
else
  echo "NEEDS-SUDO: run manually ->"
  echo "  sudo nmcli con mod \"$CONN\" ipv4.dns '$DNS1 $DNS2' ipv4.ignore-auto-dns yes && sudo nmcli con up \"$CONN\""
  exit 1
fi
