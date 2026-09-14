# Run Recipes, Find Drift, Fix Wedged Repos

**Goal:** operational reality — run things, sweep the fleet, and recover when
a repo checkout goes bad.

## Running

```bash
gogitops recipe run <name>            # by name or YAML path
gogitops recipe run <name> --dry-run  # show translated steps, no exec
gogitops recipe run <name> --verbose  # full step output
gogitops recipe run <name> --pull     # git pull first (one-command fresh runs)
gogitops recipe run <name> --hostname framework   # when system hostname ≠ node name
gogitops recipe run <name> -repo /path/to/checkout
```

Agent commands (`status`, `fleet`, `info`, `config`, `logs`, `watch`) accept
`-addr <host:port>` (default `127.0.0.1:7780`) to query remote nodes.

## run-all: the drift sweep

```bash
gogitops recipe run-all          # pull + run EVERY recipe applicable here
gogitops recipe run-all --no-pull
```

- Recipe-level label gates decide applicability (not-applicable = skip)
- Test modules are excluded — run-all never executes tests
- Compact output by default: per-recipe one-liners, failures ALWAYS shown
  with the first failure line; exit 1 on any failure. `-v` for full steps
- **Recipe steps are expected to be idempotent — applied state shows as
  skipped.** So run-all doubles as a diagnostic: what applies to this node,
  and what drifted since last time

## Repo self-heal

```bash
# any command, fresh checkout first:
gogitops -reclone status      # wipe + fresh clone, then run the command
```

`-reclone` guards run BEFORE the wipe: needs `.git` + at least one gogitops
marker dir (recipes/nodes/mesh.d/modules/test_modules/install) + an origin
remote, and refuses `$HOME`. Nothing it can't re-download is ever wiped. On
clone failure it prints manual recovery. The agent daemon keeps its own
softer self-heal (`checkout -- .` + `clean -fd` + `reset --hard`); full
reclone stays CLI-side.

## Common failures

**Agent reports version `dev`** — a stale dev binary is shadowing the real
one (classic PATH trap: an old `/usr/local/bin/gogitops` or similar). Invoke
the intended binary by absolute path; on macs the CLI can be a symlink to the
self-updating daemon binary so it tracks every swap.

**"Not possible to fast-forward" every tick** — a diverged agent checkout
(mesh.yaml→mesh.d migration stranded some checkouts). The daemon's rebase
fallback usually heals it; if not, `gogitops -reclone` from CLI.

**Self-update not happening** — deb-installed nodes (root-owned binary) can't
self-swap; they log a write error each cycle and upgrade via deb instead.
Dev builds always adopt the published version (`dev` counts as oldest).

## Dashboard & API

- Fleet dashboard: `gogitops dashboard` (default :7781) — `/api/status`,
  `/api/node/<name>`
- Every agent serves: `GET /v1/health`, `GET /v1/logs`, `GET /v1/attrs`,
  `GET /v1/attrs/catalog`

## Editor autocomplete while editing recipes

`gogitops recipe schema` emits a JSON Schema from the binary itself —
`-repo` injects your fleet's real labels/hostnames as enums. VS Code is
pre-wired in this repo; refresh the committed copy after agent releases:

```bash
gogitops recipe schema -repo . -out recipes/recipe-schema.json
```
