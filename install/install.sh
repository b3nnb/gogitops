#!/bin/bash
# gogitops + nenv-agent installer
# Usage: bash install.sh [hostname] [nenv-server-url] [nenv-token]
# Defaults: hostname=$(hostname), server=http://10.2.0.102:7200, token from nenv
# Binaries go to /usr/local/bin (system-wide, no PATH shadowing)
# Config goes to /etc/gogitops (standard system config location)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOSTNAME="${1:-$(hostname)}"
NENV_SERVER="${2:-http://10.2.0.102:7200}"
NENV_TOKEN="${3:...BINDIR="/usr/local/bin"
CFGDIR="/etc/gogitops"

echo "=== GoGitOps + nenv-agent installer ==="
echo "  Host:    $HOSTNAME"
echo "  Bin:     $BINDIR"
echo "  Config:  $CFGDIR"
echo ""

# 1. Install binaries (system-wide)
echo "▸ Installing binaries to $BINDIR..."
if [ ! -w "$BINDIR" ]; then
    echo "  $BINDIR not writable — using sudo"
    SUDO="sudo"
else
    SUDO=""
fi

$SUDO mkdir -p "$BINDIR"
$SUDO cp "$SCRIPT_DIR/bin/gogitops" "$BINDIR/gogitops"
$SUDO cp "$SCRIPT_DIR/bin/nenv-agent" "$BINDIR/nenv-agent"
$SUDO chmod +x "$BINDIR/gogitops" "$BINDIR/nenv-agent"
echo "  ✓ gogitops $($BINDIR/gogitops version 2>/dev/null || echo 'installed')"
echo "  ✓ nenv-agent installed"

# Remove old ~/bin copies that shadow the system binary
for old in ~/bin/gogitops ~/bin/nenv-agent; do
    if [ -f "$old" ]; then
        echo "  Removing old $old (shadows $BINDIR/$(basename $old))"
        rm -f "$old"
    fi
done

# 2. Install config
echo "▸ Installing config to $CFGDIR..."
$SUDO mkdir -p "$CFGDIR"
if [ -f "$SCRIPT_DIR/config/mesh.yaml" ]; then
    $SUDO cp "$SCRIPT_DIR/config/mesh.yaml" "$CFGDIR/mesh.yaml"
    echo "  ✓ mesh.yaml (peers: $(grep -c 'hostname:' "$CFGDIR/mesh.yaml" 2>/dev/null || echo '?'))"
fi

# 3. Set up SSH authorized_keys.d
echo "▸ Setting up SSH authorized keys directory..."
mkdir -p ~/.ssh/authorized_keys.d
chmod 700 ~/.ssh
if [ -f "$SCRIPT_DIR/ssh/friday-automation.pub" ]; then
    cp "$SCRIPT_DIR/ssh/friday-automation.pub" ~/.ssh/authorized_keys.d/friday-automation
    chmod 600 ~/.ssh/authorized_keys.d/friday-automation
    echo "  ✓ ~/.ssh/authorized_keys.d/friday-automation"
else
    echo "  ⚠ No SSH key in package — skipping"
fi
touch ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
echo ""
echo "  ⚠ If using authorized_keys.d, add to /etc/ssh/sshd_config:"
echo "    AuthorizedKeysFile .ssh/authorized_keys.d/%u .ssh/authorized_keys"
echo "  Then: sudo systemctl restart sshd"

# 4. Fix shell init hang for non-interactive SSH
echo ""
echo "▸ Fixing non-interactive SSH shell hang..."
BASHRC=~/.bashrc
if ! head -5 "$BASHRC" 2>/dev/null | grep -q '!= \*i\*'; then
    TMP=$(mktemp)
    {
        echo '# Return early for non-interactive shells (fixes SSH hang)'
        echo '[[ $- != *i* ]] && return'
        cat "$BASHRC" 2>/dev/null || true
    } > "$TMP"
    mv "$TMP" "$BASHRC"
    echo "  ✓ Added non-interactive guard to .bashrc"
else
    echo "  ✓ .bashrc already has non-interactive guard"
fi

# 5. Resolve nenv token
if [ -z "$NENV_TOKEN" ]; then
    if command -v nenv &>/dev/null; then
        NENV_TOKEN=*** get gogitops AGENT_TOKEN 2>/dev/null || echo "")
    fi
    if [ -z "$NENV_TOKEN" ]; then
        echo ""
        echo "  ⚠ No nenv token provided. Pass it as the 3rd arg:"
        echo "    bash install.sh $HOSTNAME $NENV_SERVER <token>"
        echo "  Or set it up later in the systemd service file."
    fi
fi

# 6. Install systemd user services
echo "▸ Installing systemd services..."
mkdir -p ~/.config/systemd/user

# Render service files with hostname/token
sed "s|__HOSTNAME__|$HOSTNAME|g; s|__NENV_SERVER__|$NENV_SERVER|g; s|__NENV_TOKEN__|$NENV_TOKEN|g" \
    "$SCRIPT_DIR/systemd/gogitops-daemon.service.tmpl" > ~/.config/systemd/user/gogitops-daemon.service

sed "s|__HOSTNAME__|$HOSTNAME|g; s|__NENV_SERVER__|$NENV_SERVER|g; s|__NENV_TOKEN__|$NENV_TOKEN|g" \
    "$SCRIPT_DIR/systemd/nenv-agent.service.tmpl" > ~/.config/systemd/user/nenv-agent.service

systemctl --user daemon-reload
echo "  ✓ Services installed"

# 7. Enable and start
echo "▸ Starting services..."
systemctl --user enable --now gogitops-daemon.service 2>/dev/null \
    && echo "  ✓ gogitops-daemon started" \
    || echo "  ⚠ gogitops-daemon failed (check: systemctl --user status gogitops-daemon)"
systemctl --user enable --now nenv-agent.service 2>/dev/null \
    && echo "  ✓ nenv-agent started" \
    || echo "  ⚠ nenv-agent failed (check: systemctl --user status nenv-agent)"

echo ""
echo "=== Done! ==="
echo "Verify:"
echo "  $BINDIR/gogitops version"
echo "  systemctl --user status gogitops-daemon nenv-agent"
