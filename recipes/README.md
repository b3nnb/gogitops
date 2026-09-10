# GoGitOps Recipe Specification

## Bootstrap Recipe — run this first on every new node

**`setup-ssh-access`** is the fleet's foundational recipe: installs the agent's
SSH public keys into `authorized_keys.d/<username>`, configures sshd, and
verifies sshd is running (process-level, init-system agnostic). Run it before
anything else so every node is reachable:

    gogitops recipe run setup-ssh-access

Verified end-to-end Sep 2026 (Docker ubuntu:24.04): 17 passed / 4 skipped,
real SSH login with the installed key succeeds. Fresh nodes without the repo
cloned can use the `GOGITOPS_SSH_PUBKEY` env var fallback.


Recipes are declarative YAML files that describe operational procedures —
installing software, updating configs, restarting services, migrating stacks.

## Schema

```yaml
name: <recipe-name>              # unique identifier
description: "<human description>"
version: "1.0.0"                 # recipe version (bump on breaking changes)
labels: []                       # which nodes can run this (empty = all)

# Optional parameters (for parametrized recipes like stack-migrate)
params:
  - name: container
    description: "Container to restart"
    required: true

steps:
  - name: <step-name>            # unique within recipe
    description: "<what this step does>"
    
    # Execution — one of:
    command: "<shell command>"   # run a shell command
    script: "<filename>"         # run a script (recipe-local scripts/, recipes/scripts/ fallback)
    action: <built-in>           # built-in action (see below)
    
    # Script options (only used with script:)
    script_args: "<args>"        # arguments passed to the script
    script_lang: bash|go|python3 # override language detection
    
    # Filters — skip step if conditions don't match
    os: linux|darwin|windows     # only run on this OS
    arch: amd64|arm64|arm        # only run on this arch
    labels_required: [docker-host]  # node must have these labels
    
    # Validation — check the step succeeded
    expect: "<expected output>"  # stdout must contain this
    expect_regex: "<pattern>"    # stdout must match this regex
    expect_exit: 0               # exit code (default 0)
    
    # Parsing — extract values from command output
    parse: regex                 # parse method
    pattern: "<regex>"           # capture groups become variables
    # captured groups: {{1}}, {{2}}, etc.
    
    # Attribute capture — store output for API/dashboard/other steps
    set_attr: <attr_name>        # store stdout as attr.<name>
    attr_prefix: <prefix>        # store regex captures as attr.<prefix>.1, .2, ...
    
    # Flow control
    only_if: "<condition>"       # skip step if condition is false
    on_failure: continue|abort   # default: abort
    retries: 3                   # retry on failure
    retry_delay: 10s             # wait between retries
    
    # Conditional execution
    when: "<shell test>"         # only run if this command exits 0
    when_attr: "<expr>"          # only run if attribute condition is true
    only_if_attr: "<expr>"       # skip if attribute condition is false
```

## Built-in Actions

| Action | Description |
|--------|-------------|
| `mesh-query` | Query the mesh for peer/label state |
| `service-check` | Check if a service is healthy |
| `git-commit` | Commit a change to the config repo |
| `mesh-broadcast` | Notify all peers of a state change |
| `wait-for-healthy` | Block until service responds |

## Variable Substitution

Variables come from three sources:

1. **Params** — `{{param_name}}`
2. **Step captures** — `{{1}}`, `{{2}}` (from `parse: regex`)
3. **Node context** — `{{hostname}}`, `{{os}}`, `{{arch}}`, `{{nebula_ip}}`

## Example

```yaml
name: install-starship
description: "Install Starship prompt and gogitops integration"
version: "1.0.0"
labels: []

steps:
  - name: detect-arch
    command: "uname -m"
    parse: regex
    pattern: "(.+)"

  - name: download
    command: "curl -sS https://starship.rs/install.sh | sh -s -- -y"
    only_if: "! which starship"
    on_failure: abort

  - name: drop-config
    command: "cp {{repo}}/configs/starship.toml ~/.config/starship.toml"
    on_failure: abort

  - name: drop-helper
    command: "cp {{repo}}/configs/gogitops_prompt.py ~/.local/bin/gogitops_prompt.py"
    on_failure: abort

  - name: init-bash
    command: 'grep -q "starship init bash" ~/.bashrc || echo ''eval "$(starship init bash)"'' >> ~/.bashrc'
    on_failure: continue

  - name: verify
    command: "starship --version"
    expect_regex: "starship .+"
```

## File Layout

```
recipes/
├── README.md                     # this file — recipe spec
├── setup-ssh-access/             # bootstrap: agent SSH keys + sshd (run first)
│   └── setup-ssh-access.yaml
├── starship/                     # starship prompt + login splash (fleet-generic)
│   ├── README.md
│   └── starship.yaml
├── script-demo/                  # demonstrates script execution + attributes
├── test-recipe/
└── scripts/                       # shared scripts referenced by recipes
    ├── install-docker.sh          # action script (installs Docker)
    ├── collect-system-info.sh     # info script (KEY=VALUE output)
    ├── collect-attrs.go           # info script (JSON output, Go)
    └── check-disk.sh              # info script (threshold check)
```

Recipes are stored in the git repo and pulled by agents on every git sync.
Any node can execute any recipe it matches (by label filter).

## Testing — YAML test suites (`gogitops test`)

Tests are YAML files with `test_module: true` — the same recipe vocabulary
(command/script/expect/assert/when/...), so tests are written by operators
and agents, not Go devs. Only `test_module: true` files run — the test runner
never executes normal recipes (they can install things).

    gogitops test list               # available test modules
    gogitops test run <name-or-path> # one module
    gogitops test run-all           # full suite; exits 1 on any failure (CI-able)

Locations:
- `test_modules/*.yaml` — fleet-wide suites (common, per-OS, docker, recipe-selftest)
- `recipes/<name>/tests/*.yaml` — recipe-scoped suites

Every step records pass/fail/skip (steps filtered by os/arch/when count as
skips). `{{self}}` substitutes the running binary's path — selftests always
exercise the CURRENT engine, never a stale PATH shadow. Reusable Go/shell/
Python modules plug in via `script:` from recipe-local `scripts/` dirs.

### Reading attributes & test results in recipes

Every recipe and test run **hydrates the device attribute store** first
(`~/.cache/gogitops/attrs.json` — plain keys, human/jq friendly; also served
at `GET /v1/attrs`). Anything the fleet tests collected — and the latest
suite results — is readable everywhere:

| What you want | Syntax |
|---|---|
| Substitute an attr into a command | `{{attr.docker_version}}` |
| Run a step only when a condition holds | `when_attr: "attr.tests.fail == 0"` |
| Run a step only when it doesn't | `when_attr: "attr.tests.fail != 0"` |
| Substring match | `when_attr: "attr.tests.failing contains docker"` |
| Truthy check (set, not "false"/"0") | `when_attr: "attr.docker_version"` |
| Inverse condition | `only_if_attr` (same syntax) |

Suite summary keys (written by `test run-all` / agent cycles / single-module
runs): `tests.pass`, `tests.fail`, `tests.skip`, `tests.total`,
`tests.failing` (comma-joined names), `tests.last_run`, and `tests.scope`
(`suite` or `module:<name>` — single-module runs describe THAT module only).
Collected module attrs (from `set_attr`/`attr_prefix` in test modules) persist
too: `docker_version`, `load.1`, `disk_root.1`, `kernel_version`, ...

Reference demo: `recipes/test-report/test-report.yaml` — reports suite
results, gates an ALL CLEAR step on `attr.tests.fail == 0`, and a FAILING
step on `!= 0`. Note: only attrs **set during a run** persist back to the
store — hydration is read-only, so merely-read attrs never leak in.

## Scripts

Scripts live in `recipes/<name>/scripts/` (recipe-local — the primary location,
so every recipe is self-contained) with `modules/` as the fleet library for
cross-recipe shared modules. They keep complex logic out of YAML and make
procedures independently testable. `gogitops recipe new <name>` scaffolds the
recipe-local `scripts/` dir automatically.

### Supported Languages

| Extension | Language | Execution |
|-----------|----------|-----------|
| `.sh` | Bash | `bash <script> <args>` |
| `.go` | Go | `go run <script> <args>` |
| `.py` | Python | `python3 <script> <args>` |
| (other) | Auto | Direct execution |

Override detection with `script_lang: bash|go|python3`.

### Script Resolution Order

1. `recipes/<recipe-name>/scripts/<name>` — recipe-local scripts (preferred: self-contained recipes)
2. `modules/<name>` — fleet library (cross-recipe shared modules)
3. As-is (absolute or relative path)

## Modules — the fleet library (`modules/`)

Reusable modules shared across recipes live in top-level `modules/` — the
fleet library. Any recipe or test module can reference them with `script:
<name>`; a same-named recipe-local script always wins (self-contained-first).

**Go modules are compiled + cached.** The agent builds each `.go` module once
to `~/.cache/gogitops/modules/<name>-<source-hash>` and reuses the binary —
recompiled automatically when the source changes. No `go run` recompile tax
on every step. (Verified: first run compiles, second run ~0s.)

Founding module: `modules/collect-attrs.go` — the general-purpose system
attribute collector (moved out of script-demo; any recipe can use it).

### Script Types

**Action scripts** — perform a task, exit code matters:
```yaml
- name: install-docker
  script: install-docker.sh
  on_failure: abort
```

**Info scripts** — collect data, output captured as attributes:
```yaml
- name: collect-info
  script: collect-system-info.sh
  set_attr: system_info       # full stdout → attr.system_info
```

**Go scripts** — structured JSON output for complex collection:
```yaml
- name: collect-attrs
  script: collect-attrs.go
  set_attr: system_attrs_json # JSON string → attr.system_attrs_json
```

**Script with args** — pass variables as arguments:
```yaml
- name: check-service
  script: check-service.sh
  script_args: "{{hostname}} {{attr.docker_version}}"
  set_attr: service_check
```

### Attribute Flow Between Steps

Attributes captured with `set_attr` are available to subsequent steps as `{{attr.<name>}}`:

```yaml
steps:
  - name: collect
    script: collect-system-info.sh
    set_attr: system_info

  - name: use-it
    command: "echo '{{attr.system_info}}' > /tmp/sysinfo.txt"
```

Use `when_attr` / `only_if_attr` for conditional execution based on attributes:
```yaml
  - name: docker-check
    command: "docker ps"
    only_if_attr: "attr.system_attrs_json contains docker_running"
```

Attributes persist for the duration of the recipe run. Future: attributes will be
exposed via the agent API (`/v1/attrs`) and dashboard for observability.
