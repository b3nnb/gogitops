#!/bin/bash
# friday-mounts-doctor.sh — verify + self-heal Friday SSHFS mounts (runs ON the client).
# Checks each mount: exists? mounted? listable? Clears stale FUSE mounts, restarts
# the owning unit (systemd on Linux / launchd on macOS), re-probes, reports one line.
# Exit 0 = all healthy or healed. Exit 1 = unfixable (Friday down or remount failed).
# Silent-friendly: one compact line per mount, failures stand out. Fleet-health style:
# run from a timer; nothing to see when everything's fine.

REMOTE=benn@10.2.0.102
SPEC_FRIDAY_M2="friday-m2|$HOME/mnt/friday-m2|/media/benn/Drive_m2"
SPEC_FRIDAY_HOME="friday-home|$HOME/mnt/friday-home|/home/benn"
SPECS="$SPEC_FRIDAY_M2 $SPEC_FRIDAY_HOME"

OS=$(uname)
rc_total=0

friday_up() {
  ssh -o BatchMode=yes -o ConnectTimeout=5 "$REMOTE" true >/dev/null 2>&1
}

# portable listability probe — backgrounds ls, kills it after 8s (stale FUSE hangs forever)
probe() {
  ( ls "$1" >/dev/null 2>&1 ) &
  local p=$!
  ( sleep 8; kill -9 $p 2>/dev/null ) &
  local w=$!
  wait $p 2>/dev/null
  local rc=$?
  kill -9 $w 2>/dev/null
  wait $w 2>/dev/null 2>&1
  return $rc
}

is_mounted() {
  if [ "$OS" = "Darwin" ]; then
    mount | grep -qF "$1"
  else
    mountpoint -q "$1" 2>/dev/null || mount | grep -qF "$1"
  fi
}

clear_stale() {
  if [ "$OS" = "Darwin" ]; then
    umount -f "$1" >/dev/null 2>&1
  else
    fusermount -uz "$1" >/dev/null 2>&1
  fi
}

restart_unit() {  # $1 = mount name
  if [ "$OS" = "Darwin" ]; then
    launchctl kickstart -k "gui/$(id -u)/com.benn.$1" >/dev/null 2>&1
  else
    systemctl --user restart "$1.service" >/dev/null 2>&1
  fi
}

# ── pre-flight: is Friday even up? (host down ≠ mount failure)
if ! friday_up; then
  echo "FRIDAY HOST DOWN — mounts unreachable, not a mount problem (no action taken)"
  exit 1
fi

for spec in $SPECS; do
  IFS='|' read -r NAME MP REMOTE_PATH <<EOF
$spec
EOF
  if [ ! -d "$MP" ]; then
    mkdir -p "$MP"
  fi

  if is_mounted "$MP" && probe "$MP"; then
    echo "$NAME: OK"
    continue
  fi

  # broken: stale mount (probe hung / half-dead) or not mounted at all
  if is_mounted "$MP"; then
    clear_stale "$MP"
  fi
  restart_unit "$NAME"
  sleep 6
  if is_mounted "$MP" && probe "$MP"; then
    echo "$NAME: FIXED (remounted)"
  else
    echo "$NAME: FAIL — still not mounted after remount attempt"
    rc_total=1
  fi
done

exit $rc_total