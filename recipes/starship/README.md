# starship-terminal-setup

Fleet-generic deployment of the Friday terminal experience: Starship prompt
plus the login splash dashboard. Zero device-specific hardcoding — every
machine-specific value is auto-detected at render time or read from the
node's own files.

Run on any node:

    gogitops recipe run starship

## What it does

1. **Checks** whether starship is installed (`attr.starship_before` records
   `starship vX.Y.Z` or `not-installed`)
2. **Installs only if missing** — official installer, `~/.local/bin` target
   (no sudo required)
3. Deploys from the repo:
   - `configs/starship.toml` → `~/.config/starship.toml`
   - `configs/dashboard` → `~/bin/dashboard` (login splash)
   - `configs/gogitops_prompt.py` → `~/.local/bin/` (fleet status module helper)
4. Wires the shell rc (`.bashrc` on linux, `.zshrc` on macOS) — all greps
   guard against duplicates, so re-running is always safe:
   - `export PATH="$HOME/.local/bin:$PATH"`
   - `eval "$(starship init bash)"`
   - first-shell splash guard (`GOGITOPS_DASHBOARD_SHOWN`) + `dashboard` alias
5. Installs **figlet** best-effort (apt/brew, falls back to `pip --user
   pyfiglet`; plain hostname as last resort)
6. Caches the public IP and adds the hourly crontab refresh (guarded)

## Machine-specific values — where they come from

| Value | Source |
|---|---|
| Splash name | `~/.config/node-nickname` (create it per node), fallback: `hostname` |
| Mounts | auto-detected (`cifs/nfs/smbfs/sshfs/afpfs` from `mount`) |
| GPU | `nvidia-smi` (line hidden when absent) |
| Public/LAN/nebula IPs | `~/.cache/external_ip` / `hostname -I` or `ipconfig` / `ip addr` |
| Fleet failures | `~/.cache/gogitops/prompt.json` (down services only) |

## Attributes reported

`os`, `arch`, `starship_before`, `starship_version`, `ascii_renderer`,
`nickname`, `private_ip`, `public_ip`, `gpu`, `deploy_summary` — plus
asserts: `starship installed and callable`, `starship config deployed`,
`dashboard script syntax valid`, `dashboard renders without error`.

Example summary attribute:

    starship=starship 1.26.0 splash=FRIDAY gpu=NVIDIA GeForce RTX 4070 SUPER, 610.43.02 lan=10.2.0.102 wan=23.93.101.207 ascii=figlet

## Tested

- Full fresh install (clean `$HOME`, starship absent): 24 passed / 5 skipped / 0 failed
- Idempotent re-run on Friday (everything pre-existing): 23 passed / 6 skipped / 0 failed
- Splash render verified identical on Friday (mounts auto-detect finds Bifrost + MiddleEarth)
