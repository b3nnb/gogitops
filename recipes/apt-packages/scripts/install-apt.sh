#!/bin/bash
# install-apt.sh — idempotent apt install, single or many packages, with
# optional version pins.
#
# Tokens (argv): "htop" | "nginx=1.18.0-6ubuntu14.4" | "nginx=1.18.*"
#   - bare name      → install if missing (skip if installed at any version)
#   - name=version   → exact dpkg version match (installs/downgrades to it)
#   - name=prefix.*  → glob match against the installed version
# Already-satisfied tokens are skipped; the run reports each decision.
# apt index is refreshed only when something actually needs installing.
#
# Requires apt (linux); caller is expected to have sudo (recipe runs at the
# box or on a passwordless-sudo node — same convention as recipes/new-node).
set -u

[ $# -eq 0 ] && { echo "no packages requested"; exit 0; }

INSTALL=()
PINNED=0
for tok in "$@"; do
  name="${tok%%=*}"
  want="${tok#*=}"
  [ "$want" = "$tok" ] && want=""
  if [ -z "$name" ]; then
    echo "SKIP empty token"
    continue
  fi
  ver=$(dpkg-query -W -f='${Version}' "$name" 2>/dev/null || true)
  if [ -z "$ver" ] || [ "$ver" = "<none>" ]; then
    echo "$name: not installed -> will install"
    INSTALL+=("$tok")
    [ -n "$want" ] && PINNED=1
    continue
  fi
  if [ -z "$want" ]; then
    echo "$name: installed ($ver) -> converged"
    continue
  fi
  if [ "$want" = "$ver" ] || case "$want" in *'*'*) [[ "$ver" == $want ]] ;; esac; then
    echo "$name: $ver matches $want -> converged"
  else
    echo "$name: $ver != $want -> will install pinned"
    INSTALL+=("$tok")
    PINNED=1
  fi
done

if [ ${#INSTALL[@]} -eq 0 ]; then
  echo "all packages already at desired versions"
  exit 0
fi

echo "installing: ${INSTALL[*]}"
sudo apt-get update -qq
OPTS=(-y -q)
[ "$PINNED" = 1 ] && OPTS+=(--allow-downgrades)
sudo DEBIAN_FRONTEND=noninteractive apt-get install "${OPTS[@]}" "${INSTALL[@]}"

# report resulting versions
for tok in "${INSTALL[@]}"; do
  name="${tok%%=*}"
  echo "$name: now $(dpkg-query -W -f='${Version}' "$name" 2>/dev/null || echo '?')"
done
echo "installed: ${INSTALL[*]}"
