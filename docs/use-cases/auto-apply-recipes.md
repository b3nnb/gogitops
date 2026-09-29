# Auto-Apply Recipes (Daemon Convergence)

**Goal:** recipes apply themselves. The daemon pulls the repo every
`git_pull_interval` but historically never *applied* anything — recipe
execution was CLI-only (`recipe run` / `run-all`). Convergence closes that
loop: the node converges on its own, no human poking the CLI.

## Enable per node (`nodes/<host>.yaml`)

```yaml
agent:
  recipes_interval: 10m   # "off"/"0" or unset = disabled (fleet default)
```

## How a cycle works

Every `recipes_interval`, the daemon sweeps `recipes/`:

- **Only `auto_apply: true` recipes converge** — opt-in per recipe. Manual
  `recipe run` / `run-all` is unaffected and still runs everything.
- Per-recipe content hash tracked in `~/.cache/gogitops/apply-state.json`
  (outside the repo — the daemon self-heals its checkout).
- **New/changed recipe** → applied. **Unchanged ok/not-applicable** → skipped
  silently (no churn). **Failed** → retried every cycle until it heals.
- **Full sweep every 24h** (or when `nodes/<host>.yaml` changes): everything
  re-verifies — idempotent steps show applied state as skipped, so the sweep
  is also the drift check.
- Recipes exec in a child of the agent binary (`recipe run <file>`), so step
  failures can never take the daemon down.

## Marking a recipe

```yaml
name: nas-bifrost-mount
labels: [laptop]
auto_apply: true
```

Label gates still apply (`labels:` + `labels_required`/`labels_exclude`) —
convergence respects the same applicability rules as manual runs.

## Observability

- `~/.cache/gogitops/agent.log` (also `/v1/logs` + dashboard log view):
  `auto-apply: <recipe> converged`, `auto-apply FAILED: ...`, retry lines.
- Fleet webhook alert **on transition into failure only** — a recipe that
  stays failed retraces silently each cycle, no alert spam.
- **Manual-step DM (BCR-19)** — when a recipe's failure is a privilege
  block (the `NEEDS-SUDO: run manually ->` convention, netmount's
  `reason=needs-sudo`, or sudo's own password denial), the daemon DMs the
  printed manual instructions to Benn's phone via the Discord bot API.
  Webhooks cannot DM a user, so this rides a bot token. Enable with the
  daemon flag:

  ```
  -dm-notify nenv:gogitops/DISCORD_DM     # value: <channel_id>|<bot_token>
  ```

  Same semantics as the webhook: fires once on first transition into the
  blocked state, silent on retries. Detection + notification only — the
  DM never grants privileges or runs anything; Benn does the step by hand.

## Gotchas

- A recipe with a root part (e.g. `mount:` unit writes) on a node without
  passwordless sudo will fail its root step unattended (sudo -n → pkexec →
  clean fail) and retry every cycle. The sudo-block DM surfaces the
  printed manual command (with `-dm-notify` configured); run it once
  interactively — after that, idempotent state checks need no privilege.
- `new-node`-style orchestration recipes stay manual: just don't mark them
  `auto_apply`. The flag is the boundary between "converges" and "ran by a
  human".
- Deleting a recipe doesn't run an uninstall — convergence applies state,
  it doesn't remove it. Prune `apply-state.json` entries manually if the
  state file bothers you.
