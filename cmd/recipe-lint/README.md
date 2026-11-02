# recipe-lint

Static validator and linter for gogitops recipe YAML files.

Validates all `.yaml` recipe files in a gogitops repo's `recipes/` directory
against the schema (`recipe-schema.json`), cross-references, and secret
detection rules. Designed for CI integration and pre-commit use.

## Install

### `go install` (recommended)

```sh
go install github.com/bennbanks/gogitops/cmd/recipe-lint@latest
```

Requires Go 1.25+. The binary lands in `$GOBIN` (usually `~/go/bin`).

### Homebrew (tap)

```sh
brew install b3nnb/tap/recipe-lint
```

Formula template: [`packaging/homebrew/recipe-lint.rb`](../../packaging/homebrew/recipe-lint.rb)

### Build from source

```sh
git clone https://github.com/b3nnb/gogitops.git
cd gogitops
make build
# or: go build -ldflags "-X main.Version=1.0.0" -o bin/recipe-lint ./cmd/recipe-lint/
```

## Usage

```sh
# Lint all recipes in the current repo (defaults to ./recipes/)
recipe-lint -repo .

# JSON output for CI pipelines
recipe-lint -repo . --json

# Print suggested fixes (does not auto-apply)
recipe-lint -repo . --fix

# Lint specific files
recipe-lint recipes/new-node.yaml recipes/swarm-enroll.yaml

# Show version
recipe-lint --version
```

### Flags

| Flag           | Default | Description                                          |
|----------------|---------|------------------------------------------------------|
| `-repo`        | `.`     | Path to gogitops repo root                           |
| `-recipes-dir` | `<repo>/recipes` | Override recipes directory                  |
| `-schema`      | `<recipes>/recipe-schema.json` | Override schema path      |
| `-mesh-dir`    | `<repo>/mesh.d`  | Override mesh.d directory                   |
| `-json`        | off     | Output findings as JSON (for CI integration)         |
| `-fix`         | off     | Print suggested fixes (does not auto-apply)          |
| `-version`     | —       | Print version and exit                               |
| `-h`           | —       | Show usage                                           |

## Checks

1. **YAML format** — valid YAML; required fields present (`name`, `description`, `version`, `steps`); each step has a `name`.

2. **Schema compliance** — labels, `on_failure`, `parse`, `func`, `script_lang`, `os`, `arch` validated against the enum in `recipe-schema.json`. Each step must have at least one action field (`command`, `script`, `func`, `package`, `mount`). Duplicate step names flagged.

3. **Cross-reference validation** — `{{.step-name}}` template variables in `condition:`/`when:`/`command:` fields must reference a step that exists in the same recipe. `gogitops recipe run X` in a command must reference a recipe `X` that exists in `recipes/`.

4. **Secret detection** — scans for API keys, tokens, passwords, private SSH keys, AWS credentials, Nebula enrollment tokens, JWTs, Slack tokens, Google API keys, and private IPs in credential-assignment contexts.

5. **Label consistency** — labels used in recipes must be in the schema enum; labels in the schema enum are cross-checked against `mesh.d/` node configs (labels in schema but not in any node config produce a warning).

## Output Format

Default (text):

```
recipes/new-node.yaml:4: [ERROR] label 'new-node' is not in the schema enum
  → fix: use one of the valid labels: admin, compute, docker-host, ...
```

JSON mode (`--json`):

```json
[
  {
    "file": "recipes/new-node.yaml",
    "line": 4,
    "level": "error",
    "rule": "label-enum",
    "message": "label 'new-node' is not in the schema enum",
    "suggestion": "use one of the valid labels: ..."
  }
]
```

## Exit Codes

- `0` — no error-level findings (warnings/info may be present)
- `1` — one or more error-level findings
- `2` — internal error (schema/mesh.d load failure)

## Self-contained

The binary is statically linked with only Go stdlib + `gopkg.in/yaml.v3` — no external runtime dependencies. No shared libraries, no Python, no shell scripts required at runtime.