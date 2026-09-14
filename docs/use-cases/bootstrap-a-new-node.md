# Bootstrap a New Node

**Goal:** take a fresh machine from zero to a managed fleet member — agent
installed, pulling config from git, reachable over SSH. No NetEnv, no Nebula,
no dashboard dependency required. Standalone by design: the agent is fully
functional with nothing but a git repo.

## 1. Install the agent binary

Linux (deb or tarball from the latest release), or macOS (binary + sign it):

```bash
# macOS only — Apple Silicon SIGKILLs unsigned binaries:
codesign --force --sign - /path/to/gogitops
```

Verify: `gogitops version` prints version + platform. If it prints `dev`,
you're running a stale dev build — see
[run-and-troubleshoot](run-and-troubleshoot.md).

## 2. Point it at your config repo

```bash
gogitops set repo <git-url-or-local-path>
```

The repo is the single source of truth: node configs (`nodes/`), mesh
(`mesh.d/`), recipes (`recipes/`), modules (`modules/`), tests
(`test_modules/`). The agent pulls it every 5 minutes.

## 3. Run the daemon

Linux systemd user unit:

```ini
# ~/.config/systemd/user/gogitops-daemon.service
[Unit]
Description=GoGitOps agent daemon

[Service]
ExecStart=%h/.local/bin/gogitops daemon
Restart=always

[Install]
WantedBy=default.target
```

```bash
systemctl --user enable --now gogitops-daemon
```

macOS: launchd/cron keepalive wrapper (`~/bin/gogitops-launch.sh` pattern) —
the daemon self-updates and exec-restarts in place (v0.5.6+).

On first run the agent **auto-registers**: it writes its own
`mesh.d/<hostname>.yaml` file (and node yaml) so the rest of the fleet can see
it. Each agent owns exactly its own file — nodes never write each other's.

## 4. Make the node reachable: run the bootstrap recipe

`setup-ssh-access` is the fleet's foundational recipe — installs the agent's
SSH public keys into `authorized_keys.d/<user>`, configures sshd, verifies
it's running:

```bash
gogitops recipe run setup-ssh-access
```

Fresh nodes without the repo cloned can seed the key via the
`GOGITOPS_SSH_PUBKEY` env var fallback. Verified end-to-end in Docker
(ubuntu:24.04): 17 passed / 4 skipped, real SSH login with the installed key
succeeds.

## 5. Apply everything applicable

```bash
gogitops recipe run-all
```

Pulls, then runs every recipe this node matches. Idempotent recipes show
already-applied state as skips — so the output doubles as a drift report.
See [run-and-troubleshoot](run-and-troubleshoot.md).

## 6. Confirm fleet visibility

From any node (or the dashboard on :7781):

```bash
gogitops status       # this node — system, tags, services, peers, disk
gogitops fleet        # one line per node, parallel query
```

The new node should appear in both. Labels come from `nodes/<hostname>.yaml` —
add labels there (e.g. `docker-host`, `gpu`, `stack:trader`) and recipes start
targeting it: see [target-specific-nodes](target-specific-nodes.md).

## Pin the agent version (optional)

`versions.yaml` in the repo controls agent versions fleet-wide — global
default, per-label groups, per-node exact pins (downgrades included). Every
release is retained on the binaries branch, so any past version stays
fetchable. Omitted = track latest (upgrades only).
