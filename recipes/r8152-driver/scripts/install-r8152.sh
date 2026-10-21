#!/bin/bash
# install-r8152.sh — install Realtek r8152 DKMS driver for USB 10GbE adapters
#
# Idempotent: skips if the DKMS module is already built for the current kernel.
# Downloads the .deb from GitHub releases, installs dkms + build deps if needed,
# then installs the r8152 DKMS package which auto-builds for all kernels.
#
# After install, the user must physically plug in the USB 10G adapter and run:
#   sudo modprobe -r r8152 && sudo modprobe r8152
# to force the r8152-cfgselector to re-enumerate the device.
set -u

DEB_URL="https://github.com/awesometic/realtek-r8152-dkms/releases/download/2.22.1-1/realtek-r8152-dkms_2.22.1-1_amd64.deb"
DEB_FILE="/tmp/realtek-r8152-dkms_2.22.1-1_amd64.deb"
KVER="$(uname -r)"

echo "=== r8152 DKMS driver install ==="
echo "kernel: $KVER"

# Check if already installed for this kernel
if dkms status 2>/dev/null | grep -q "r8152.*$KVER.*installed"; then
  echo "r8152 DKMS already built for $KVER — skipping"
  exit 0
fi

# Install build dependencies if missing
echo "=== installing build deps ==="
NEEDS=()
dpkg -l dkms 2>/dev/null | grep -q '^ii' || NEEDS+=(dkms)
dpkg -l build-essential 2>/dev/null | grep -q '^ii' || NEEDS+=(build-essential)
dpkg -l "linux-headers-$KVER" 2>/dev/null | grep -q '^ii' || NEEDS+=("linux-headers-$KVER")
if [ ${#NEEDS[@]} -gt 0 ]; then
  apt-get update -qq
  apt-get install -y "${NEEDS[@]}"
  echo "installed: ${NEEDS[*]}"
else
  echo "build deps already present"
fi

# Download the .deb
echo "=== downloading r8152 DKMS deb ==="
if [ ! -s "$DEB_FILE" ]; then
  wget -q "$DEB_URL" -O "$DEB_FILE"
  echo "downloaded: $(ls -la "$DEB_FILE" 2>/dev/null)"
else
  echo "deb already downloaded: $DEB_FILE"
fi

# Install the DKMS package (auto-builds for all kernels)
echo "=== installing DKMS package ==="
dpkg -i "$DEB_FILE" 2>&1 || true

# Verify the module built
if dkms status 2>/dev/null | grep -q "r8152.*installed"; then
  echo "=== r8152 DKMS installed and built successfully ==="
  dkms status 2>/dev/null | grep r8152
  echo ""
  echo "NOTE: If a USB 10G adapter is plugged in, run:"
  echo "  sudo modprobe -r r8152 && sudo modprobe r8152"
  echo "to force the cfgselector to switch the device from NCM to vendor mode."
else
  echo "=== WARNING: dkms build may have failed ==="
  dkms status 2>/dev/null
  echo "Check /usr/src/r8152-2.22.1/ for build logs"
  exit 1
fi

exit 0