# GoGitOps — Agent Mesh Design

_Agent-controlled, GitOps-driven infrastructure mesh for Benn's homelab._

**Version:** 0.2.0 (design phase — container labels, Nebula integration, router agent)
**Date:** 2026-08-14
**Author:** Friday + Benn

---

## Philosophy

1. **No SPOF.** If any single device dies, the rest of the mesh stays operational and alerts.
2. **Git is truth.** All config, labels, recipes, and peer lists live in a git repo. Agents pull on boot + periodically.
3. **Labels, not hostnames.** Identity follows the role, not the hardware. Move a label to a new device → that device becomes the role.
4. **Recipes, not SSH sessions.** Operational procedures are declarative YAML, not ad-hoc commands.
5. **Self-updating.** Agents can update themselves and Nebula without human SSH.
6. **Minimal telemetry.** No CPU/RAM/VPN metrics by default. Just: is it alive? are services up? is disk full? Add perf monitoring on-demand when debugging.

---

## Architecture

### Mesh Topology

Every agent talks to every other agent over Nebula VPN. No central coordinator required.

```
friday (100.2.0.2) ←→ mini (TBD)
       ↕                    ↕
nas (100.2.0.4)  ←→ lighthouse (100.2.0.1)
       ↕
framework / gControl / pixel
```

**Peer discovery:** Agent reads `mesh.yaml` from git repo. No dynamic discovery needed at this scale (7 devices).

**Alert routing:** Every agent can alert directly to Discord. If agent A can't reach agent B after 3 attempts → agent A alerts. No need for a central alert processor.

**Aggregator (optional):** Any node can run `gogitops status` to collect mesh state from all peers for display. This is read-only convenience, not a dependency.

### Agent Binary

- **Name:** `gogitops`
- **Language:** Go (static binary, no dependencies, cross-compile for linux/amd64, linux/arm64, darwin/arm64, windows/amd64)
- **Size target:** <10MB
- **Runtime:** systemd (Linux), launchd (macOS), Windows Service (gControl), Termux (Android)
- **Config:** Pulled from git repo on boot + every 5 minutes
- **Heartbeat:** Every 60s, each agent pings all peers over Nebula
- **Service checks:** Every 60s, check ports/processes defined in node config
- **Reporting:** Post mesh state + service state to Discord on change (not every tick)

---

## Git Repo Structure

```
gogitops/
├── nodes/                    # Per-device config
│   ├── friday.yaml
│   ├── mini.yaml
│   ├── nas.yaml
│   ├── lighthouse.yaml
│   ├── framework.yaml
│   ├── gcontrol.yaml
│   └── pixel.yaml
├── groups/                   # Named collections with intent
│   ├── exo.yaml              # Which nodes form the exo cluster
│   ├── trader.yaml           # Which nodes/stacks run trading
│   ├── docker-hosts.yaml     # Which nodes run Docker
│   └── infra.yaml            # Core always-up nodes
├── stacks/                   # Compose stack definitions (for migration)
│   ├── trader.yaml           # compose dir, env, data dirs, containers
│   ├── ctl.yaml
│   ├── curation.yaml
│   ├── netenv.yaml
│   ├── honcho.yaml
│   └── media.yaml            # SABnzbd + Sonarr + Radarr + Seerr
├── recipes/                  # Operational procedures
│   ├── docker-health.yaml
│   ├── nebula-update.yaml
│   ├── stack-migrate.yaml
│   ├── container-restart.yaml
│   ├── agent-self-update.yaml
│   └── full-migrate.yaml
├── alerts/
│   └── routing.yaml          # who gets alerted for what
├── starship/
│   ├── base.toml             # Shared prompt config
│   ├── linux.toml            # Linux overrides
│   ├── darwin.toml           # macOS overrides
│   └── windows.toml          # Windows IoT overrides
├── mesh.yaml                 # Peer list, Nebula IPs
└── releases/
    └── latest.json           # current agent version + download URLs
```

### Node Config Schema

```yaml
# nodes/friday.yaml
hostname: friday
nebula_ip: 100.2.0.2
lan_ip: 10.2.0.102

labels:
  - primary-compute
  - docker-host
  - hermes-host
  - trader-host
  - gpu

services:
  - name: hermes-gateway
    type: systemd
    unit: hermes-gateway.service
  - name: docker
    type: docker
    check: daemon-alive
  - name: ctl-stack
    type: docker-compose
    path: /home/benn/Documents/code/Docker/ctl
    containers: [ctl-backend-1, ctl-frontend-1, ctl-trader-1, ctl-worker-1, ctl-db-1]
  - name: friday-voice
    type: systemd
    unit: friday-voice.service
  - name: curation-tool
    type: tcp
    port: 7842
  - name: ollama
    type: tcp
    port: 11434
  - name: nebula
    type: systemd
    unit: nebula.service

disk:
  - mount: /
    warn_pct: 85
    crit_pct: 95
  - mount: /media/benn/Bifrost
    warn_pct: 85
    crit_pct: 95

agent:
  update_url: https://releases.bennbot.com/gogitops/latest.json
  check_interval: 5m
  git_repo: git@github.com:bennbanks/gogitops.git
  git_branch: main
  git_pull_interval: 5m
```

### Mesh Config

```yaml
# mesh.yaml
peers:
  - hostname: friday
    nebula_ip: 100.2.0.2
    lan_ip: 10.2.0.102
    port: 7780           # gogitops agent port
  
  - hostname: mini
    nebula_ip: 100.2.0.3  # TBD — assign
    lan_ip: 10.0.0.251
    port: 7780
  
  - hostname: nas
    nebula_ip: 100.2.0.4
    lan_ip: 10.2.0.103
    port: 7780
  
  - hostname: lighthouse
    nebula_ip: 100.2.0.1
    lan_ip: 3.xxx.xxx.xxx  # AWS
    port: 7780
  
  - hostname: framework
    nebula_ip: 100.2.0.5  # TBD
    lan_ip: TBD
    port: 7780
  
  - hostname: gcontrol
    nebula_ip: 100.2.0.6  # TBD
    lan_ip: TBD
    port: 7780
  
  - hostname: pixel
    nebula_ip: 100.2.0.7  # TBD
    lan_ip: TBD
    port: 7780
```

### Recipe Schema

```yaml
# recipes/nebula-update.yaml
name: nebula-update
description: "Update Nebula VPN binary to latest version"
labels: []  # empty = available on all nodes

steps:
  - name: check-current-version
    command: "nebula -version"
    parse: regex
    pattern: "Nebula (.+)"
  
  - name: fetch-latest
    command: "curl -s https://github.com/slackhq/nebula/releases/latest/download/nebula-linux-amd64 -o /tmp/nebula-new"
    os: linux
  
  - name: fetch-latest-mac
    command: "curl -s https://github.com/slackhq/nebula/releases/latest/download/nebula-darwin-arm64 -o /tmp/nebula-new"
    os: darwin
  
  - name: verify-hash
    command: "sha256sum /tmp/nebula-new"
    compare: github-release-sha256
  
  - name: replace-binary
    command: "sudo mv /tmp/nebula-new /usr/local/bin/nebula && sudo chmod +x /usr/local/bin/nebula"
  
  - name: restart-service
    command: "sudo systemctl restart nebula"
    os: linux
  
  - name: verify-running
    command: "nebula -version"
    expect: "Nebula {{latest_version}}"
```

### Group Schema

Groups are named collections with intent. They answer "what breaks if X goes down?" and "is this service cluster healthy?"

**Two group types:**
- `node-group` — collection of nodes (e.g. exo cluster, core infra)
- `stack-group` — collection of compose stacks (e.g. trader ecosystem)

```yaml
# groups/exo.yaml
name: exo
description: "exo inference cluster — distributed LLM across GPU nodes"
type: node-group

members:
  - label: gpu           # any node with this label
  - node: mini           # explicit node (even without label)

config:
  min_members: 2         # alert if fewer than 2 are up
  check_port: 52415      # exo API port to verify
  coordinator: friday    # who typically runs --head
```

```yaml
# groups/trader.yaml
name: trader
description: "Autonomous options trading stack (GoDawg)"
type: stack-group

members:
  - stack: trader        # references stacks/trader.yaml

depends_on:
  - group: docker-hosts  # needs Docker
  - group: infra         # needs NAS + n8n

config:
  market_hours_only: true    # only alert during RTH
  alert_on_down: critical
```

```yaml
# groups/infra.yaml
name: infra
description: "Core infrastructure — must-always-be-up nodes"
type: node-group

members:
  - node: friday
  - node: nas
  - node: lighthouse

config:
  min_members: 2
  alert_on_down: critical
```

**Group-aware commands:**

```bash
gogitops group health exo        # Are enough exo nodes up?
gogitops group health trader     # Is the trading stack operational?
gogitops node impact mini        # What groups/stacks depend on Mini?
gogitops node impact nas         # What breaks if NAS goes down?
```

**Impact analysis example:**
```
$ gogitops node impact nas
Groups affected:
  - infra (critical, 1 of 3 members)
  - trader (depends on infra, via docker-hosts)

Stacks affected:
  - honcho (NAS-local, goes down)
  - media (NAS-local, goes down)

Services affected:
  - Caddy public HTTPS (NAS DMZ)
  - n8n (NAS container)
  - Nextcloud (NAS container)
  - QDrant (NAS container)
```

### Stack Schema

Stacks define what a compose deployment needs — containers, env, data dirs, health checks, and migration steps. This is what makes `gogitops label move stack:trader nas` actually work.

```yaml
# stacks/trader.yaml
name: trader
description: "Autonomous options trading stack (GoDawg)"

compose_dir: /home/benn/Documents/code/Docker/ctl
compose_file: docker-compose.yml

containers:
  - ctl-trader-1
  - ctl-backend-1
  - ctl-frontend-1
  - ctl-worker-1
  - ctl-db-1
  - ctl-searxng-1
  - ctl-studio-worker-1

env_files:
  - .env                    # relative to compose_dir

data_dirs:
  - /home/benn/Documents/code/Docker/ctl  # compose dir (has .env, configs)

health_checks:
  - type: tcp
    port: 8001              # trader API
  - type: tcp
    port: 3000              # frontend

migration:
  pre_migrate:
    - "docker compose down"                # stop on source
    - "rsync -av --exclude='*.log'"        # sync compose dir
  post_migrate:
    - "docker compose up -d"               # start on target
    - wait_for_healthy
  verify:
    - all containers running
    - API responding on 8001
```

```yaml
# stacks/curation.yaml
name: curation
description: "Friday curation tool for LoRA dataset management"

compose_dir: /home/benn/curation
compose_file: docker-compose.yml

containers:
  - friday-curation

env_files:
  - .env

data_dirs:
  - /home/benn/curation
  - /home/benn/lora-characters     # symlinked dataset root

health_checks:
  - type: tcp
    port: 7842

migration:
  pre_migrate:
    - "docker compose down"
    - "rsync -av /home/benn/curation/ target:/home/benn/curation/"
    - "rsync -av /home/benn/lora-characters/ target:/home/benn/lora-characters/"
  post_migrate:
    - "docker compose up -d --build"
    - wait_for_healthy
  verify:
    - container running
    - port 7842 responding
```

**Label → Stack → Group hierarchy:**

```
Node label: stack:trader     (tag on a node config)
     ↓ references
Stack:    stacks/trader.yaml  (what to migrate, how)
     ↓ belongs to
Group:    groups/trader.yaml  (operational status, dependencies, alerting)
```

### Alert Routing

```yaml
# alerts/routing.yaml
channels:
  - name: discord-benn
    type: discord-webhook
    url: nenv:ctl/ALERT_DISCORD_WEBHOOK  # pulled from netenv

rules:
  - match:
      label: primary-compute
      severity: critical
    route: discord-benn
    message: "🚨 {{hostname}}: {{alert_text}}"
  
  - match:
      any: true
      severity: warning
    route: discord-benn
    message: "⚠️ {{hostname}}: {{alert_text}}"
  
  - match:
      service: nebula
      severity: critical
    route: discord-benn
    message: "🕸️ {{hostname}}: Nebula VPN down — mesh connectivity at risk"
  
  - match:
      type: peer-unreachable
    route: discord-benn
    message: "👁️ {{source}} can't reach {{target}} — check both nodes"
```

---

## Agent Protocol

### Peer Health Check (every 60s)

```json
GET /v1/health HTTP/1.1
Host: 100.2.0.2:7780

Response:
{
  "hostname": "friday",
  "agent_version": "0.1.0",
  "uptime_seconds": 86400,
  "nebula_running": true,
  "nebula_version": "1.9.0",
  "labels": ["primary-compute", "docker-host", "hermes-host", "trader-host", "gpu"],
  "services": {
    "hermes-gateway": "running",
    "docker": "running",
    "ollama": "running",
    "nebula": "running"
  },
  "disk": {
    "/": { "used_pct": 42 },
    "/media/benn/Bifrost": { "used_pct": 61 }
  },
  "peers_reachable": ["mini", "nas", "lighthouse"],
  "peers_unreachable": [],
  "last_git_pull": "2026-08-14T12:00:00Z",
  "config_hash": "abc123"
}
```

### Remote Command (recipe execution)

```json
POST /v1/recipe HTTP/1.1
Host: 100.2.0.2:7780
Content-Type: application/json

{
  "recipe": "container-restart",
  "params": {
    "container": "ctl-trader-1"
  },
  "requested_by": "nas",
  "token": "shared-mesh-secret"  # from nenv or git config
}

Response:
{
  "status": "accepted",
  "job_id": "abc123",
  "steps_total": 3
}
```

### Self-Update

```json
GET /v1/update/check HTTP/1.1
Host: 100.2.0.2:7780

Response:
{
  "current_version": "0.1.0",
  "latest_version": "0.1.1",
  "download_url": "https://releases.bennbot.com/gogitops/v0.1.1/gogitops-linux-amd64",
  "sha256": "def456"
}

POST /v1/update/apply HTTP/1.1
{
  "version": "0.1.1",
  "token": "shared-mesh-secret"
}
```

---

## Device Migration Workflow

Moving "Friday" to a new box:

1. **Prep new device:** Install gogitops, Nebula, git access
2. **Update node config in git:** Change `friday.yaml` `hostname` field to new device's hostname (or create new node + reassign labels)
3. **New device pulls config:** Sees labels `primary-compute`, `docker-host`, etc.
4. **Run setup recipe:** `gogitops recipe run full-migrate`
   - Installs Docker
   - Deploys CTL stack, Hermes, Trader
   - Syncs curation data from NAS
   - Starts all services
   - Announces on mesh
5. **Old device sees labels gone:** Its config no longer matches → drains services, goes quiet
6. **Update mesh.yaml:** Remove or relabel old device
7. **Verify:** All peers confirm new device is reachable, services responding

**DNS angle:** Once we add internal DNS (later), step 2 just becomes "update the A record." Same workflow, cleaner.

---

## Internal DNS (Phase 2)

When we're ready:
- **CoreDNS** container on NAS (always-on)
- Resolves: `friday.lan`, `mini.lan`, `nas.lan`, `lighthouse.lan`
- Service records: `ollama.lan` → whichever node runs it
- Auto-config from the same git repo (CoreDNS reads zone file from git)

This replaces hardcoded IPs everywhere — configs, recipes, Caddy, n8n, everything.

---

## Build Plan

### Phase 1 — Minimum viable mesh
- [ ] Go binary: health endpoint, peer pings, service checks, disk checks
- [ ] Git repo with node configs + mesh.yaml
- [ ] Systemd unit for Friday
- [ ] launchd plist for Mini
- [ ] Discord alerts on peer loss + service down
- [ ] Deploy to Friday + Mini + NAS

### Phase 2 — Recipes & control
- [ ] Recipe execution engine
- [ ] Container restart recipe
- [ ] Agent self-update
- [ ] Nebula update recipe
- [ ] Remote command endpoint (authenticated)

### Phase 3 — Migration & DNS
- [ ] Container-level label migration (move single stacks)
- [ ] Device migration recipe (full-migrate)
- [ ] Label reassignment workflow
- [ ] CoreDNS on NAS
- [ ] Migrate configs from hardcoded IPs to DNS names

### Phase 4 — Extended nodes
- [ ] gControl agent (Windows IoT)
- [ ] Pixel agent (Termux)
- [ ] Framework laptop agent
- [ ] Lighthouse agent
- [ ] Router agent (ASUS GT-BE98 Pro, Entware)

---

## Container-Level Labels & Partial Migration

Labels aren't just device-level. They're hierarchical — device, stack, service:

```yaml
labels:
  # Device-level
  - compute
  - docker-host
  - gpu

  # Stack-level (compose stacks)
  - stack:trader
  - stack:ctl
  - stack:curation
  - stack:netenv

  # Service-level (individual units)
  - service:hermes-gateway
  - service:friday-voice
  - service:friday-calls
  - service:curation-server
```

**Why:** Move just the trader to NAS without touching CTL, Hermes, or curation.

```bash
# Move a single stack
gogitops label move stack:trader nas

# Move multiple related things
gogitops label move stack:trader,service:hermes-gateway nas

# Query what owns a label
gogitops label who stack:trader
# → friday (100.2.0.2)
```

**Label migration recipe** only handles what's tagged:

```yaml
# recipes/stack-migrate.yaml
name: stack-migrate
description: "Migrate a labeled compose stack to another docker-host"
params:
  - label        # e.g. "stack:trader"
  - target_host  # e.g. "nas"

steps:
  - name: find-owner
    action: mesh-query
    query: "who owns {{label}}"
  
  - name: rsync-data
    command: "rsync -av {{source_host}}:{{compose_dir}}/ {{target_host}}:{{compose_dir}}/"
  
  - name: rsync-env
    command: "rsync -av {{source_host}}:{{compose_dir}}/.env {{target_host}}:{{compose_dir}}/.env"
  
  - name: deploy-on-target
    command: "ssh {{target_host}} 'cd {{compose_dir}} && docker compose up -d'"
    labels_required: [docker-host]
  
  - name: verify-health
    action: service-check
    target: "{{target_host}}"
    retries: 5
    delay: 10s
  
  - name: teardown-source
    command: "ssh {{source_host}} 'cd {{compose_dir}} && docker compose down'"
    only_if: verify-health.success
  
  - name: reassign-label
    action: git-commit
    file: "nodes/{{target_host}}.yaml"
    change: "add label {{label}}"
  
  - name: announce-mesh
    action: mesh-broadcast
    message: "{{label}} now lives on {{target_host}}"
```

**Conflict resolution:** If two agents both think they own a label (race condition during migration), the Lighthouse can arbitrate. Or simpler: the git commit is the tiebreaker — whoever commits the label assignment last wins, and the loser sees the label gone on next git pull and drains gracefully.

---

## Nebula Integration

Nebula isn't just transport — it's agent infrastructure.

### Agent listens on Nebula only

Instead of opening port 7780 on all interfaces (LAN exposure), the agent only binds to the Nebula interface:

```go
// Only listen on Nebula IP — invisible to LAN
listener, _ := net.Listen("tcp", "100.2.0.2:7780")
```

Agents are only reachable from inside the VPN. No firewall rules needed, no LAN attack surface.

### Nebula cert as agent identity

Each agent has a Nebula cert with its hostname in the CN. When agent A calls agent B:

```
Agent A → Nebula tunnel → Agent B
Agent B sees: connection from cert CN="friday", group="gogitops"
Agent B trusts: cert is valid, group matches, hostname matches peer list
```

No shared secret needed. Nebula's PKI handles authentication. If a device's cert is revoked, it can't talk to any agent.

### Firewall group for agents

Add a `gogitops` group to Nebula's firewall config. Only agents in this group can reach port 7780. Other Nebula peers (if any) can't probe the agent API.

```yaml
# nebula config addition
firewall:
  inbound:
    - port: 7780
      proto: tcp
      group: gogitops
```

### Lighthouse as fallback coordinator

If two agents both claim a label, the Lighthouse (AWS, always up) can:
1. Query both agents for their state
2. Check git for the canonical label assignment
3. Tell the loser to drain

This is optional — git-based conflict resolution works for 7 devices. Lighthouse arbitration becomes useful at scale or when git is temporarily unavailable.

---

## Router Agent (ASUS GT-BE98 Pro)

The router is always-on, at the network edge, and on a different failure domain than any server. Perfect for a lightweight mesh node.

### What the router can run

ASUSWRT is Linux-based. The GT-BE98 Pro supports:
- **SSH access** ✅ (already configured)
- **Entware** (opkg package manager) — install userland binaries
- **JFFS partition** — persistent storage for custom scripts/binaries
- **Limited RAM/CPU** — router-grade, not server-grade

### Deployment

```bash
# Install Entware on router (one-time)
ssh router "entware-install.sh"  # via ASUS Merlin or custom

# Install gogitops via opkg or direct binary
scp gogitops-router-armv7 router:/jffs/gogitops/
ssh router "chmod +x /jffs/gogitops/gogitops"

# Add to cron (ASUSWRT has crontab)
ssh router "echo '*/1 * * * * /jffs/gogitops/gogitops heartbeat' >> /var/spool/cron/crontabs/admin"
```

### What the router agent monitors

| Check | Why | Feasible on router? |
|-------|-----|-------------------|
| WAN connectivity (ping 8.8.8.8) | Internet up? | ✅ Easy |
| DNS resolution | Can it resolve bennbot.io? | ✅ Easy |
| DHCP lease count | How many devices on LAN? | ✅ Read dnsmasq leases |
| Nebula daemon running? | VPN mesh health | ✅ Process check |
| Gateway latency | ISP quality | ✅ ping |
| NAT sessions | Connection table filling up? | ⚠️ May need root |
| Firmware version | Security updates | ✅ Read from system |

### What the router agent does NOT do

- No Docker monitoring (no Docker on router)
- No compose stack management
- No heavy telemetry (RAM is limited)
- No recipe execution (too risky on edge device)

### Router as WAN sentinel

The router is the **only device** that can detect WAN failures before they cascade. If the internet goes down, all servers lose external connectivity simultaneously — no server-side agent can distinguish "internet down" from "my NIC broken." The router knows the difference.

Alert pattern:
```
Router: WAN down (can't ping 8.8.8.8) → alert "🌐 Internet down"
Router: WAN up but DNS fails → alert "🔍 DNS resolution failing"
Router: WAN + DNS up, but bennbot.io unreachable → alert "🚨 Caddy/DMZ issue"
```

---

## Starship Integration — Terminal Heads-Up Display

Every node runs [Starship](https://starship.rs/) as the shell prompt. GoGitOps writes a local cache file every check cycle; Starship reads it and renders mesh health inline.

### Cache file (written by agent every 60s)

```json
// ~/.cache/gogitops/prompt.json
{
  "peers_total": 7,
  "peers_healthy": 6,
  "services_healthy": true,
  "services_down": [],
  "disk_warn": false,
  "disk_warn_mounts": [],
  "version": "0.1.0",
  "labels": ["compute", "docker-host", "gpu"],
  "hostname": "friday",
  "nebula_running": true
}
```

### Starship modules

```toml
# ~/.config/starship.toml

# Mesh peer health — 🟢 all peers up, 🔴 some down
[custom.gogitops_mesh]
command = """cat ~/.cache/gogitops/prompt.json 2>/dev/null | \
  python3 -c "import json,sys; d=json.load(sys.stdin); \
  h=d['peers_healthy']; t=d['peers_total']; \
  down=d.get('services_down',[]); \
  icon='🟢' if h==t and not down else '🔴'; \
  svcs=' ⚠️'+','.join(down) if down else ''; \
  print(f'{icon} {h}/{t}{svcs}')\""""
when = "test -f ~/.cache/gogitops/prompt.json"
format = '[$output]($style) '
style = 'bold green'
shell = ["sh"]
interval = 60

# Node labels — 🏷 count
[custom.gogitops_labels]
command = """cat ~/.cache/gogitops/prompt.json 2>/dev/null | \
  python3 -c "import json,sys; d=json.load(sys.stdin); \
  print('🏷'+str(len(d.get('labels',[]))))\""""
when = "test -f ~/.cache/gogitops/prompt.json"
format = '[$output]($style) '
style = 'bold purple'
shell = ["sh"]
interval = 60

# Disk warning — only shows when disk is >85%
[custom.gogitops_disk]
command = """cat ~/.cache/gogitops/prompt.json 2>/dev/null | \
  python3 -c "import json,sys; d=json.load(sys.stdin); \
  warn=d.get('disk_warn_mounts',[]); \
  print('💾 '+' '.join(warn)) if warn else None\""""
when = "test -f ~/.cache/gogitops/prompt.json"
format = '[$output]($style) '
style = 'bold red'
shell = ["sh"]
interval = 60
```

### Example prompts

**Healthy:**
```
benn@friday ~/code  🟢 7/7 🏷5  ±main ✓
```

**Problems:**
```
benn@friday ~/code  🔴 5/7 ⚠️ollama 💾 /media/benn/Bifrost  ±main ✓
```

### Cross-OS paths

| OS | Cache path | Starship config | Notes |
|----|-----------|----------------|-------|
| Linux | `~/.cache/gogitops/prompt.json` | `~/.config/starship.toml` | Friday, NAS, Framework |
| macOS | `~/.cache/gogitops/prompt.json` | `~/.config/starship.toml` | Mini |
| Windows IoT | `%LOCALAPPDATA%\gogitops\prompt.json` | `~\.config\starship.toml` | gControl |
| Android/Termux | `$PREFIX/var/cache/gogitops/prompt.json` | `~/.config/starship.toml` | Pixel |

**Consistency approach:** The starship.toml is nearly identical everywhere — only the `when` path and Python invocation differ on Windows. The git repo stores a base template + per-OS overrides. Agent deploys the right one based on OS.

### Shared starship config beyond GoGitOps

Since we're syncing starship across all machines, we should standardize the entire prompt:

```toml
# Base config — shared across all nodes
[username]
show_always = true
format = '[$user]($style)@'

[hostname]
ssh_only = false
format = '[$hostname]($style) '

[directory]
truncation_length = 2
truncate_to_repo = true

[git_branch]
format = '[$symbol$branch]($style)'

[character]
success_symbol = '[✓](bold green)'
error_symbol = '[✗](bold red)'
```

This goes in the git repo alongside node configs. Agent deploys it on first setup and keeps it synced.

---

## Naming

**Binary:** `gogitops`
**Repo:** `github.com/bennbanks/gogitops`
**Port:** 7780 (Nebula interface only — not LAN)
**Protocol:** HTTP JSON over Nebula (Nebula cert = agent auth)
**Config format:** YAML in git
**Router binary:** `gogitops-lite` (stripped-down, ~2MB, WAN/DNS/Nebula checks only)
**Config format:** YAML in git
