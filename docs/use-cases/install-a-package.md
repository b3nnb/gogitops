# Install a Package (any OS)

**Goal:** install software on any node without caring whether it runs apt,
brew, dnf, zypper, pacman, or apk — and without failing when it's already
installed.

Use the built-in `package:` step type. The agent detects the local package
manager and translates; the recipe never changes across OSes.

## Basic

```yaml
steps:
  - name: install-figlet
    description: "Install figlet (agent picks the local package manager)"
    package: figlet
```

The agent runs the equivalent of `apt install figlet` / `brew install figlet`
/etc., with `sudo -n` only when not running as root. Already installed = exit 0.
Nothing works = exit 0 anyway (best-effort — check the next step if you need
to know).

## With fallback sources

`sources:` is tried in order. `pkg:` hits the system package manager;
`pip:` installs a Python package with `python3 -m pip install --user`
(including a PEP 668 `--break-system-packages` retry on Ubuntu 24.04+):

```yaml
  - name: install-ascii-renderer
    package: figlet
    sources: "[pkg:figlet, pip:pyfiglet]"
    when: "! command -v figlet >/dev/null 2>&1"
    on_failure: continue
```

Note `sources:` is a **quoted string** containing the list. Empty or omitted
`sources:` defaults to `pkg:<package>`.

## Report what actually happened

Pair the install with a detection step so the result lands in attributes:

```yaml
  - name: report-renderer
    command: "command -v figlet >/dev/null 2>&1 && echo figlet || echo plain"
    set_attr: ascii_renderer
```

## Real-world example

`recipes/starship/starship.yaml` (step `install-figlet`) uses exactly this
pattern on Ubuntu, Fedora, Alpine, and macOS with a byte-identical recipe.

## When NOT to use package:

- The package needs interactive prompts or unusual repo setup → write a
  module: [write-a-script-step](write-a-script-step.md).
- You need to verify the installed version → add an `expect_regex` step or a
  follow-up `command: "tool --version"` with `set_attr`.

## Many packages or version pins → the apt-packages recipe

`package:` handles one best-effort install. For multi-package or
version-pinned installs on apt nodes, use the parametrized recipe:

    gogitops recipe run apt-packages --var packages="htop nginx tree"
    gogitops recipe run apt-packages --var packages="nginx=1.18.0-6ubuntu14.4 jq=1.7.*"

Tokens: `name` (install if missing), `name=version` (exact dpkg version,
downgrades allowed), `name=prefix.*` (glob). Already-satisfied tokens are
skipped and the apt index refreshes only when something needs installing;
the result lands in the `apt_install_result` attribute.

Parametrized recipes (`params:` in the YAML) validate required params at
run time — invoking without `--var` prints the parameter table and exits,
and `recipe run-all` skips them (manual-invocation by design).
