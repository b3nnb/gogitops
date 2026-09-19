# Collect Attributes (info about a node)

**Goal:** capture facts about a node — versions, disk usage, service state —
store them, and use them in later steps, conditions, or the dashboard.

Attributes are the node's shared vocabulary. Anything a step captures with
`set_attr` becomes readable everywhere **on that node** as `{{attr.<name>}}` —
and it outlives the run: values persist to the per-node attribute store
(gogitops v0.7.21+), so later recipes and tests hydrate them automatically.

## Capture stdout as one attribute

```yaml
steps:
  - name: detect-os
    description: "Record operating system"
    command: "uname -s | tr '[:upper:]' '[:lower:]'"
    set_attr: os
```

## Capture regex groups as numbered attributes

```yaml
  - name: detect-arch
    command: "uname -m"
    parse: regex
    pattern: "(.+)"
    # captured groups become {{1}}, {{2}}, ...

  - name: with-prefix
    command: "df -h / | tail -1"
    parse: regex
    pattern: "(\S+)\s+(\S+)\s+(\S+)"
    attr_prefix: disk_root   # → attr.disk_root.1, .2, .3
```

## Free attributes from asserts

Every step with an `assert:` records its verdict with no extra syntax —
`assert.<step-name>.status = pass|fail` lands in the store and is readable
by any later step or run:

```yaml
  - name: verify-nginx
    command: "systemctl is-active nginx"
    assert: "nginx is running"
    # → attr.assert.verify-nginx.status — gate on it:
  - name: report
    command: "echo nginx verdict: {{attr.assert.verify-nginx.status}}"
```

## Use them in later steps

```yaml
  - name: use-it
    command: "echo '{{attr.os}} / {{attr.arch}}' > /tmp/facts.txt"
```

## End-to-end: one recipe, submit then use

Submission and consumption together — capture a fact, then let later steps
(and conditions) act on it:

```yaml
steps:
  - name: detect-gpu
    description: "Capture GPU model from the info script"
    script: gpu-info.sh              # prints e.g. "NVIDIA GeForce RTX 4070"
    set_attr: gpu_model              # submission: stdout becomes attr.gpu_model

  - name: record
    command: "echo 'GPU: {{attr.gpu_model}}' >> $HOME/hw-notes.txt"
    only_if_attr: "attr.gpu_model"   # usage: run only if the attr was set
```

Conditionals on attributes ([syntax](conditional-steps.md)):

```yaml
  - name: docker-check
    command: "docker ps"
    only_if_attr: "attr.system_attrs_json contains docker_running"
```

## Naming rules

- `set_attr: <name>` takes anything matching `[A-Za-z0-9_.-]+`. The in-run
  reference adds the `attr.` prefix; the store keeps the plain name —
  `packet_state` on disk, `{{attr.packet_state}}` in YAML.
- Values are strings — trimmed stdout, possibly multi-line. Compare with
  `==` / `!=` exactly, or `contains` for substrings.
- Pick a style (underscore vs dot) and stay consistent; `recipe validate`
  warnings will catch typos with a did-you-mean.

## The attribute store

Every recipe/test run **hydrates the device attribute store first**
(`~/.cache/gogitops/attrs.json` — plain keys, jq-friendly). Suite results land
there too: `tests.pass`, `tests.fail`, `tests.skip`, `tests.total`,
`tests.failing` (comma-joined names), `tests.last_run`, `tests.scope`.
So a recipe can gate on fleet-test state:

```yaml
  - name: all-clear
    command: "echo fleet green"
    when_attr: "attr.tests.fail == 0"
```

Runs also **write back**: every attr explicitly set during a run (`set_attr`,
`attr_prefix` captures, assert verdicts) merges into the store when the run
ends — on every exit path, including aborted runs. That's the cross-run
story: a recipe can gate on state a *different* recipe recorded last week.

```yaml
# recipes/monitor-nginx/monitor-nginx.yaml — records state daily
  - name: probe
    command: "systemctl is-active nginx"
    set_attr: nginx_state

# recipes/after-upgrade/after-upgrade.yaml — different recipe, later run
  - name: confirm
    command: "nginx -t"
    when_attr: "attr.nginx_state == active"
```

Only attrs **set during a run** persist — hydration is read-only, so
merely-read attrs never leak in. Dry-runs write nothing.

**Per-node by design:** the store is decentralized — values stay on the node
that produced them. Cross-node state is what tags and the fleet table are
for; attributes are local facts.

## Viewing what a node recorded

- **Dashboard** — drill down into the node → **ATTRIBUTES** panel: list view,
  plus a copyable YAML view formatted for recipe reference.
- **API** — `GET /v1/attrs` on any agent (`curl localhost:7780/v1/attrs`).
- **CLI** — `gogitops info` (deep dive incl. attributes).

## Discover what exists

```bash
gogitops attrs scan          # grouped catalog: name, kind, source
gogitops attrs scan --json   # machine-readable
```

The scanner catalogs every attribute defined by `set_attr`/`attr_prefix`
across recipes + test modules, plus engine synthetics — attributes become
discoverable instead of tribal knowledge. `recipe validate` uses this
catalog: unknown attribute references get a **warning** with a did-you-mean
suggestion (warnings never block).

## Complex collection → a module

Don't build pipe-monsters in `command:`. The founding module
`modules/collect-attrs.go` collects system attributes as structured JSON:

```yaml
  - name: collect-attrs
    script: collect-attrs.go
    set_attr: system_attrs_json
```

See [write-a-script-step](write-a-script-step.md) for the module system.
