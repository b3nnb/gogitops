#!/usr/bin/env python3
"""Idempotently register an Obsidian vault in the local Obsidian config.

Usage: register_vault.py [vault_path]   (default: ~/Documents/Obsidian Vault)

- Detects the Obsidian config for this platform:
    1. ~/.var/app/md.obsidian.Obsidian/config/obsidian/obsidian.json  (flatpak, Linux)
    2. ~/Library/Application Support/obsidian/obsidian.json           (macOS)
    3. ~/.config/obsidian/obsidian.json                               (native, Linux)
- If the vault is already registered: prints it, exits 0 (no write).
- If Obsidian is running and a write is needed: exits 3 without writing
  (the app would clobber the file); agent retries on a later cycle.
- Otherwise: adds the vault (fresh random id, ts=now, open=true) with a
  tmp+rename atomic write. Never touches other vaults' entries.
"""
import json
import os
import subprocess
import sys
import time
import uuid

VAULT_DEFAULT = "~/Documents/Obsidian Vault"

CONFIG_CANDIDATES = [
    "~/.var/app/md.obsidian.Obsidian/config/obsidian/obsidian.json",
    "~/Library/Application Support/obsidian/obsidian.json",
    "~/.config/obsidian/obsidian.json",
]


def obsidian_running() -> bool:
    try:
        out = subprocess.run(
            ["pgrep", "-fl", "obsidian"], capture_output=True, text=True, timeout=5
        ).stdout.lower()
        # match the real app, not this script or unrelated processes
        return any(
            k in out for k in ("obsidian.app", "md.obsidian", "obsidian_")
        )
    except Exception:
        return False


def main() -> int:
    vault = os.path.abspath(os.path.expanduser(sys.argv[1] if len(sys.argv) > 1 else VAULT_DEFAULT))

    if not os.path.isdir(vault):
        print(f"WARN vault dir missing: {vault} (run the dir step first)")
        return 2

    # pick config: existing file, else first creatable path whose Obsidian install exists
    cfg = None
    for c in CONFIG_CANDIDATES:
        p = os.path.expanduser(c)
        if os.path.exists(p):
            cfg = p
            break
    if cfg is None:
        for c in CONFIG_CANDIDATES:
            p = os.path.expanduser(c)
            base = os.path.dirname(p)
            # flatpak: app sandbox dir; mac: app support; native: config dir
            if os.path.isdir(base) or os.path.isdir(os.path.dirname(base)):
                cfg = p
                os.makedirs(base, exist_ok=True)
                break
    if cfg is None:
        # no Obsidian config context at all (e.g. app never launched on a fresh
        # native install) — create the standard native path
        cfg = os.path.expanduser("~/.config/obsidian/obsidian.json")
        os.makedirs(os.path.dirname(cfg), exist_ok=True)

    data = {"vaults": {}}
    if os.path.exists(cfg):
        try:
            with open(cfg) as f:
                data = json.load(f)
        except Exception as e:
            print(f"WARN unreadable config {cfg}: {e} — refusing to overwrite")
            return 2

    for vid, v in (data.get("vaults") or {}).items():
        if isinstance(v, dict) and os.path.abspath(os.path.expanduser(str(v.get("path", "")))) == vault:
            print(f"registered: {vault} (id {vid}, config {cfg}) — no change")
            return 0

    if obsidian_running():
        print(f"vault not registered and Obsidian is running — deferring write to avoid clobber ({cfg})")
        return 3

    vid = uuid.uuid4().hex[:16]
    data.setdefault("vaults", {})[vid] = {
        "path": vault,
        "ts": int(time.time() * 1000),
        "open": True,
    }
    tmp = cfg + ".tmp-gogitops"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
    os.replace(tmp, cfg)
    print(f"registered: {vault} (new id {vid}, config {cfg})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
