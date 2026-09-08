# starship-terminal-setup

Fleet-generic deployment of the Friday terminal experience: Starship prompt
plus the login splash dashboard. Zero device-specific hardcoding — every
machine-specific value is auto-detected at render time or read from the
node's own files.

Run on any node:

    gogitops recipe run starship

**v2.2.0 is fully OS-neutral: the same recipe file runs unmodified on Ubuntu,
Fedora, Alpine, and macOS.** The agent on each node translates universal step
types to local reality — the recipe never changes per-OS.

## Universal step vocabulary

The recipe speaks one OS-neutral (Linux-leaning) vocabulary. The agent translates:

| Recipe says | Agent does |
|---|---|
| `command: <bash>` | runs it via `bash -c` (bash is a universal prereq) |
| `package: figlet` + `sources: "[pkg:figlet, pip:pyfiglet]"` | tries each source in order: `pkg:` -> local package manager (brew on macOS, apt/dnf/zypper/pacman/apk on Linux, `sudo -n` when non-root); `pip:` -> `python3 -m pip install --user` with PEP 668 retry. Best-effort: exits 0 when already installed or nothing works (graceful degradation) |
| `schedule: hourly` + `command: <bash>` | installs an idempotent crontab entry tagged `# gogitops:<step-name>` (presets hourly/daily/weekly, or raw 5-field cron). Re-runs replace, never duplicate; dedup also cleans legacy unmarked lines (expanded + literal `$HOME` forms) |
| `when: <bash guard>` | evaluated on every OS — detect capabilities (which rc files exist, which shell is in use), never assume them from the OS name |

Old agents (pre-v2.2.0 binaries) degrade gracefully on v2.2.0 recipes: unknown
fields are ignored, translated steps run as harmless one-shot commands.

## What it does

1. **Checks** whether starship is installed (`attr.starship_before` records
   `starship vX.Y.Z` or `not-installed`)
2. **Installs only if missing** — official installer, `~/.local/bin` target
   (no sudo required)
3. Deploys from the repo:
   - `configs/starship.toml` → `~/.config/starship.toml`
   - `configs/dashboard` → `~/bin/dashboard` (login splash)
   - `configs/gogitops_prompt.py` → `~/.local/bin/` (fleet status module helper)
4. Wires whichever shell rc files exist (`.bashrc` and/or `.zshrc`, detected
   via `when` guards — not assumed from the OS) — all greps guard against
   duplicates, so re-running is always safe:
   - `export PATH="$HOME/.local/bin:$PATH"`
   - `eval "$(starship init bash)"`
   - first-shell splash guard (`GOGITOPS_DASHBOARD_SHOWN`) + `dashboard` alias
5. Installs **figlet** via the `package:` universal step (agent picks the
   package manager; falls back to `pip --user pyfiglet`; plain hostname as
   last resort)
6. Caches the public IP and installs the hourly refresh via the `schedule:`
   universal step (agent writes the idempotent cron entry)

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
- Real deployment on Framework laptop (Sep 7 2026): 24 passed / 5 skipped — surfaced two gotchas fixed in v2.0.1: bashrc files without trailing newline get the PATH append concatenated onto the last line (now prepends `\n`), and Ubuntu 24.04 PEP 668 blocks `pip3 install --user` (pyfiglet fallback now retries with `--break-system-packages`).
- Cross-distro container tests (Sep 7 2026, v2.1.0): **Fedora (dnf)** — figlet via dnf, splash renders; **Alpine (apk/musl/busybox)** — 25 passed / 0 failed, figlet via apk, LAN IP via iproute2 fallback, splash renders with art. Ubuntu proven on Friday + Framework.
- v2.2.0 universal step types (Sep 7 2026), same recipe file on every OS: Friday (26 passed, legacy cron deduped to one marked line, rerun idempotent), Fedora (package: -> dnf installs figlet, schedule tolerated in cron-less container), Alpine (package: -> apk, splash renders), Mac mini (package: -> pyfiglet fallback, schedule: replaced legacy unmarked cron line with marked one, rerun idempotent).

## Prerequisites

- `bash` + `curl` (Alpine/minimal: `apk add bash curl`)
- Optional: `python3` + `pip` (pyfiglet fallback when no package manager figlet possible), `cron` (hourly public-IP refresh — skipped gracefully without it)
- User shell must be bash or zsh (starship init + splash hook are written to the matching rc file; fish not wired)
