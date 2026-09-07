#!/bin/bash
# install-figlet — cross-distro ASCII art renderer install (best effort).
# Detects the system package manager, handles root vs sudo, falls back to
# pip --user pyfiglet (with PEP 668 override), and finally degrades to plain
# text (the dashboard handles the plain fallback itself).
set -u

if command -v figlet >/dev/null 2>&1; then
  exit 0
fi

# macOS / Homebrew
if command -v brew >/dev/null 2>&1; then
  brew install figlet 2>/dev/null && exit 0
fi

# Linux package managers
pm_args=""
if command -v apt-get >/dev/null 2>&1; then
  pm_args="apt-get install -y"
elif command -v dnf >/dev/null 2>&1; then
  pm_args="dnf install -y"
elif command -v zypper >/dev/null 2>&1; then
  pm_args="zypper install -y"
elif command -v pacman >/dev/null 2>&1; then
  pm_args="pacman -S --noconfirm"
elif command -v apk >/dev/null 2>&1; then
  pm_args="apk add"
fi

if [ -n "$pm_args" ]; then
  if [ "$(id -u)" = "0" ]; then
    $pm_args figlet 2>/dev/null
  elif command -v sudo >/dev/null 2>&1; then
    sudo -n $pm_args figlet 2>/dev/null
  fi
fi

command -v figlet >/dev/null 2>&1 && exit 0

# pyfiglet fallback (python3 + pip required)
for pipcmd in pip3 pip; do
  command -v $pipcmd >/dev/null 2>&1 || continue
  $pipcmd install --user --quiet pyfiglet 2>/dev/null && exit 0
  $pipcmd install --user --quiet --break-system-packages pyfiglet 2>/dev/null && exit 0
done

# No renderer found — plain text fallback is fine, exit clean
exit 0
