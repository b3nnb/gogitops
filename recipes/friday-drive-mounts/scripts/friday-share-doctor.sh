#!/bin/bash
# friday-share-doctor.sh — verify Friday host (10.2.0.102) is properly set up to
# serve SSHFS mounts to the Mini + laptop. Runs ON Friday.
# OK = healthy | WARN = setup incomplete (no alert) | FAIL = share broken (alerts to Discord webhook if configured)

EXPECTED_KEY_PREFIXES=(
  "AAAAC3NzaC1lZDI1NTE5AAAAILAE0vlfauKkKtxld2EDpGKtGf8zq0EvXExbaGG4Xy9D"   # Mini    id_gogitops_mini
  "AAAAC3NzaC1lZDI1NTE5AAAAINdZhT7k7QyrJq8PlTvYVZPC/q"                    # Laptop  id_gogitops
)
SHARE_DIRS=("/media/benn/Drive_m2" "/home/benn")
WEBHOOK_URL="$(nenv get global DISCORD_WEBHOOK 2>/dev/null || true)"

FAILS=0; WARNS=0; OUT=""

report() { OUT+="$1"$'\n'; case "$1" in FAIL:*) FAILS=$((FAILS+1));; WARN:*) WARNS=$((WARNS+1));; esac; }

# 1. sshd active + enabled on boot
if systemctl is-active --quiet ssh 2>/dev/null || systemctl is-active --quiet sshd 2>/dev/null; then
  report "OK: sshd active"
else
  report "FAIL: sshd not active — no SSH shares at all"
fi
if systemctl is-enabled --quiet ssh 2>/dev/null || systemctl is-enabled --quiet sshd 2>/dev/null; then
  report "OK: sshd enabled on boot"
else
  report "WARN: sshd not enabled — will not survive reboot"
fi

# 2. SFTP subsystem (the protocol sshfs speaks over SSH)
SUBSYS=$(grep -E '^[[:space:]]*Subsystem[[:space:]]+sftp' /etc/ssh/sshd_config 2>/dev/null | awk '{print $3}' | head -1)
if [ -z "$SUBSYS" ]; then
  report "FAIL: no 'Subsystem sftp' in sshd_config — sshfs cannot work"
elif [ "$SUBSYS" = "internal-sftp" ]; then
  report "OK: SFTP subsystem = internal-sftp"
elif [ -x "$SUBSYS" ]; then
  report "OK: SFTP subsystem binary present ($SUBSYS)"
else
  report "FAIL: SFTP binary missing at $SUBSYS"
fi

# 3. Share dirs readable + Drive_m2 actually MOUNTED (empty-mountpoint gotcha:
#    if the NVMe is not mounted, sshfs would happily serve the empty dir)
for d in "${SHARE_DIRS[@]}"; do
  if [ -r "$d" ] && [ -x "$d" ]; then
    report "OK: $d readable"
  else
    report "FAIL: $d not readable/traversable"
  fi
done
if findmnt --mountpoint /media/benn/Drive_m2 >/dev/null 2>&1; then
  report "OK: Drive_m2 mounted (1.8T data NVMe live)"
else
  report "FAIL: Drive_m2 not mounted — clients would see the empty mountpoint"
fi

# 4. Client keys present in authorized_keys
AK="$HOME/.ssh/authorized_keys"
for prefix in "${EXPECTED_KEY_PREFIXES[@]}"; do
  if grep -q "$prefix" "$AK" 2>/dev/null; then
    report "OK: client key ${prefix:0:16} authorized"
  else
    report "WARN: client key ${prefix:0:16} NOT in authorized_keys — that client cannot mount yet"
  fi
done

echo "$OUT"
echo "friday-share-doctor: $FAILS FAIL, $WARNS WARN"

# Alert on FAIL only (fleet convention: failures only, silent when healthy)
if [ "$FAILS" -gt 0 ] && [ -n "$WEBHOOK_URL" ]; then
  MSG="friday share doctor: $FAILS FAIL on $(hostname) — $(echo "$OUT" | grep '^FAIL:' | tr '\n' '; ')"
  curl -s -m 8 -X POST "$WEBHOOK_URL" -H 'Content-Type: application/json' -d "{\"content\":\"⚠️ $MSG\"}" >/dev/null 2>&1 || true
fi
[ "$FAILS" -eq 0 ]