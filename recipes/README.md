# GoGitOps Recipe Specification

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
    action: <built-in>           # built-in action (see below)
    
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
    
    # Flow control
    only_if: "<condition>"       # skip step if condition is false
    on_failure: continue|abort   # default: abort
    retries: 3                   # retry on failure
    retry_delay: 10s             # wait between retries
    
    # Conditional execution
    when: "<shell test>"         # only run if this command exits 0
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
├── README.md           # this file
├── starship.yaml       # install starship + gogitops prompt
├── nebula-update.yaml  # update nebula binary
├── agent-self-update.yaml
├── container-restart.yaml
├── stack-migrate.yaml
└── full-migrate.yaml
```

Recipes are stored in the git repo and pulled by agents on every git sync.
Any node can execute any recipe it matches (by label filter).
