# Write a Script Step (modules)

**Goal:** logic too big for one line — real code, called from YAML by name,
shared across recipes.

If a `command:` grows pipe chains or `|| echo` fallbacks, it's a module
misfiled as YAML. Scripts keep complex logic out of YAML and make procedures
independently testable.

## Calling a script

```yaml
steps:
  - name: collect-info
    script: collect-system-info.sh       # name, not a path
    set_attr: system_info                # info script: stdout → attribute

  - name: install-docker
    script: install-docker.sh            # action script: exit code matters
    on_failure: abort

  - name: check-service
    script: check-service.sh
    script_args: "{{hostname}} {{attr.docker_version}}"   # {{var}} substitution
    set_attr: service_check
```

## Languages

| Extension | Runs as | Notes |
|---|---|---|
| `.sh` | `bash <script> <args>` | runs anywhere |
| `.go` | compiled + cached | needs go on the node; cached to `~/.cache/gogitops/modules/<name>-<hash>`, recompiled only when source changes |
| `.py` | `python3 <script> <args>` | |
| other | direct execution | |

Override detection with `script_lang: bash|go|python3`.

## Resolution order (first hit wins)

1. `recipes/<recipe-name>/scripts/<name>` — recipe-local, self-contained
2. `modules/<name>` — fleet library (repo `modules/`)
3. `<set>/<name>` — agent-embedded module sets, extracted to
   `~/.cache/gogitops/agent-modules/`
4. As-is (absolute or relative path)

Same-name shadowing means admins can override any embedded module from the
repo without an agent release.

## What's already shipped (embedded sets)

| Module | What it does |
|---|---|
| `net/port-check` | TCP port reachability — no nc/telnet dependency. Args: `<host:port>` |
| `storage/mount-info` | Mount state for a mountpoint: device, options, fstab persistence |
| `system/disk-free` | Disk usage with the math shown: total, used, free, used pct |

Browse the catalog: `gogitops modules` (repo library + embedded sets, first
comment line = description).

## Where to put a new module — the ladder

Decided **before** implementation (DESIGN.md "Capability Placement"):

1. Built-in step type — universal declarative vocab (`mount:`, `package:`, `schedule:`)
2. Agent-embedded set — reusable, ships in binary, repo can override
3. Repo `modules/` — fleet-specific or fast-changing, instant git delivery
4. Recipe-local `scripts/` — one recipe's flow
5. `command:` — glue only

Default to the lowest tier. Record the tier call in the PR description.

## Founding repo module

`modules/collect-attrs.go` — general-purpose system attribute collector
(structured JSON output). Reference example for Go info-modules:

```yaml
  - name: collect-attrs
    script: collect-attrs.go
    set_attr: system_attrs_json
```

## Scaffold

`gogitops recipe new <name>` creates `recipes/<name>/` with a commented
template + `scripts/` dir ready for local scripts.
