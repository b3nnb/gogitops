# GoGitOps Use-Case Cookbook

Task-oriented how-tos for writing and running recipes. Each page answers one
question with a copy-pasteable example. Full field reference lives in
[recipes/README.md](../recipe-spec.md) (the spec) — this cookbook is
organized by *what you're trying to do*.

**Looking for something?** Every file here is plain markdown — search with
your editor's project search (or grep) across `docs/use-cases/`. The docs site
(http://docs.bennbot.io on LAN) also has a search box built from these files.

## The 60-second anatomy

A recipe is a YAML file in `recipes/` — name, version, labels, and ordered
steps. Steps are *references to capability*, not embedded bash:

```yaml
name: example
description: "What this recipe does"
version: "1.0.0"
labels: []          # empty = every node; otherwise node needs ALL labels

steps:
  - name: install-tool        # unique within recipe
    package: tool             # built-in step type — agent translates per-OS
  - name: run-it
    command: "tool --version"
    expect_regex: "tool .+"
```

## Task index

| I want to… | Page |
|---|---|
| Bring a brand-new node into the fleet | [bootstrap-a-new-node](bootstrap-a-new-node.md) |
| Install a package on any OS | [install-a-package](install-a-package.md) |
| Schedule a recurring job (cron) | [schedule-a-cron-job](schedule-a-cron-job.md) |
| Mount a drive / manage fstab | [mount-a-drive](mount-a-drive.md) |
| Collect info about a node (attributes) | [collect-attributes](collect-attributes.md) |
| Run a recipe only on certain nodes | [target-specific-nodes](target-specific-nodes.md) |
| Run a step only when something is true | [conditional-steps](conditional-steps.md) |
| Check a step worked (validation, retries) | [validate-and-retry](validate-and-retry.md) |
| Put real logic in a script/module | [write-a-script-step](write-a-script-step.md) |
| Call a typed native function (no shell) | [native-functions](native-functions.md) |
| Prove my recipe works (tests) | [test-your-recipe](test-your-recipe.md) |
| Run recipes / find drift / fix a wedged repo | [run-and-troubleshoot](run-and-troubleshoot.md) |
| Make the daemon apply recipes itself (convergence) | [auto-apply-recipes](auto-apply-recipes.md) |

Related: [tags & labels guide](../tags.md) · [recipe spec](../recipe-spec.md) ·
[capability placement (DESIGN.md)](../design.md)

## Where code lives (the placement ladder)

Every new function gets an explicit tier decision before implementation
(DESIGN.md — "Capability Placement"). Default to the lowest tier:

1. **Built-in step type** — `package:` / `schedule:` / `mount:`: universal,
   declarative, the YAML is data. Agent holds the logic (idempotent, sudo-aware).
2. **Agent-embedded module set** — reusable logic shipped in the binary
   (`storage/mount-info`, `system/disk-free`, `net/port-check`). Repo can override.
3. **Repo module library** — `modules/`: fleet-specific, instant delivery via git.
4. **Recipe-local script** — `recipes/<name>/scripts/`: one recipe's flow.
5. **`command:` glue** — one-liners only.

Rule of thumb: a `command:` that grows pipe chains or `|| echo` fallbacks is a
module misfiled as YAML.

## YAML quoting gotcha (read once, save an hour)

The parser strips outer quotes but processes **no escapes** — `\"` inside a
value passes literal backslashes to bash. House style: double-quoted YAML with
single quotes inside the command, or single-quoted YAML when the command needs
double quotes. Real example from `starship.yaml`:

```yaml
command: 'grep -q "starship init bash" ~/.bashrc || echo ''eval "$(starship init bash)"'' >> ~/.bashrc'
```
