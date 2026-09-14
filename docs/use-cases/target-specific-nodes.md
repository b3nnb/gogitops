# Target Specific Nodes

**Goal:** a recipe (or a single step) that only runs on certain machines —
by label, OS, or architecture — without maintaining per-node recipe copies.

Node labels live in `nodes/<hostname>.yaml`:

```yaml
hostname: friday
labels:
  - primary-compute
  - docker-host
  - gpu
  - stack:trader
```

## Recipe-level: run only where labels match

Recipe `labels:` = the node must have **ALL** of them. Empty = every node.

```yaml
name: gpu-node-setup
labels: [gpu, stack:trader]   # only nodes with both
steps: [...]
```

Nodes without the labels skip the whole recipe — reported as
not-applicable, not as failure.

## Step-level: scope inside one recipe

One recipe, per-node sections (real pattern from
`recipes/friday-drive-mounts/` — friday/laptop/mini sections in one file):

```yaml
steps:
  - name: install-share-doctor
    command: "cp {{repo}}/recipes/.../doctor.sh $HOME/.local/bin/"
    labels_required: [primary-compute]   # node must have ALL of these

  - name: laptop-install-sshfs
    package: sshfs
    labels_required: [laptop]

  - name: mini-report-fuse
    command: "echo macfuse status"
    labels_required: [lora-training]
    os: darwin                            # belt-and-suspenders, see below
```

`labels_exclude:` inverts: node must have **NONE** of the listed labels.

## OS / arch filters

```yaml
  - name: mac-only-step
    command: "brew list"
    os: darwin            # linux | darwin | windows
    arch: arm64           # amd64 | arm64 | arm
```

**Prefer `when:` guards over `os:`** — detect capability at runtime instead
of assuming from OS name (`when: "test -f $HOME/.zshrc"` beats
`os: darwin` for "is this a zsh box"). The starship recipe carries zero
`os:` filters. Use `os:` when the step is genuinely OS-defined (brew, launchd).

## Manual runs: hostname override

System hostname ≠ node name sometimes (laptop's hostname is a long Dell
string, node name is `framework`). Recipe runs on those nodes need:

```bash
gogitops recipe run friday-drive-mounts --hostname framework
```

Recipe runs never trigger auto-registration; `--hostname` just selects which
node yaml (and label set) applies. Labels come from `nodes/<hostname>.yaml`.

## Groups (fleet policy, not recipe targeting)

`groups/*.yaml` define node-groups for policy — e.g. `infra` with
`min_members: 2` and `alert_on_down: critical`. Agent version pins in
`versions.yaml` match node **labels** (groups key), lowest version wins.
Targeting in recipes uses labels directly; groups are for fleet-level rules.

## Per-node agent version overrides

Any recipe may carry `node_overrides:` — explicit, beats versions.yaml:

```yaml
node_overrides:
  framework:
    version: v0.6.8   # pin this node's agent regardless of this recipe's labels
```

Precedence: recipe override > versions.yaml nodes > groups > global > latest.
Several recipes overriding the same node → lowest wins (stay-back bias).
