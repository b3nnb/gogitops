# Tags Guide

Tags are GoGitOps' universal identity system. Every node has tags — short strings that describe what it is, what it runs, and how to target it with recipes.

## What Are Tags?

Tags are just strings. They can be plain (`gpu`, `laptop`) or namespaced with a colon (`stack:trader`, `service:n8n`). There's no schema, no registry, no validation — you decide what tags mean.

## Where Tags Come From

A node's effective tags merge from three sources:

```
1. Static tags  — defined in nodes/*.yaml under `tags:` (was `labels:`)
2. Nebula tags  — imported from dnclient cert groups (when Nebula integration is on)
3. NetEnv tags  — imported from secret namespace tags (when NetEnv integration is on)
```

At runtime, the agent reads all three and merges them into one list. Static tags are always present. The other two are optional integrations that enrich the tag set.

### Static Tags (node YAML)

```yaml
# nodes/friday.yaml
hostname: friday
lan_ip: 10.2.0.102
tags:
  - primary-compute
  - docker-host
  - gpu
  - stack:trader
  - stack:ctl
```

These are the source of truth. You set them, you change them, git tracks them.

### Nebula Tags (from cert)

When Nebula integration is enabled, cert groups import as tags:

| Cert group | Imported as |
|------------|-------------|
| `role:benn` | `dn-role:benn` |
| `t:service:n8n` | `service:n8n` (strips the `t:` prefix) |
| `t:access:unlimited` | `access:unlimited` |

The `dn-role:` prefix distinguishes network identity from your static tags. The `t:*:*` prefix is stripped because `service:n8n` is already namespaced.

### NetEnv Tags (from namespace)

When NetEnv integration is enabled, namespace membership imports as tags:

| Namespace | Imported as |
|-----------|-------------|
| `trader` | `nenv-ns:trader` |
| `hermes` | `nenv-ns:hermes` |

## How Tags Are Used

### Recipe Targeting

Recipes declare which nodes should run them:

```yaml
# recipes/update-nebula.yaml
targets:
  - tag:docker-host      # any node tagged docker-host
  - node:friday          # or specifically friday
  - group:exo            # or any node in the exo group
```

Multiple targets = OR (node runs if it matches ANY target).

### Status Display

`gogitops status` shows tags grouped:

```
  ╭─ Tags ──────────────────────
  │ Tags     primary-compute docker-host gpu
  │ Stacks   trader ctl
  ╰──────────────────────────────
```

Tags with the `stack:` prefix show separately as Stacks. Everything else shows as Tags.

### Group Membership

Groups are curated collections built from tags:

```yaml
# groups/exo.yaml
name: exo
type: node-group
members:
  - tag:gpu        # any node with the gpu tag
  - node:mini      # plus mini specifically
```

Groups answer "what breaks if X goes down?" — they're operational intent, not identity.

## What About Roles?

There are no "roles" in GoGitOps. What other systems call roles (`docker-host`, `compute`, `trader-host`) are just tags. The old code separated them into a "Roles" section in the status display, but that was a Kubernetes-ism. Tags are tags. If you want to conventionally use `role:` as a prefix for identity tags, that works — but GoGitOps treats them the same as any other tag.

## Naming Conventions

| Pattern | Meaning | Example |
|---------|---------|---------|
| `plain-word` | What the node is/does | `gpu`, `laptop`, `storage` |
| `stack:name` | A compose stack this node runs | `stack:trader`, `stack:netenv` |
| `service:name` | A network service this node provides | `service:n8n`, `service:jellyfin` |
| `dn-role:name` | Imported from Nebula cert (network identity) | `dn-role:benn` |
| `nenv-ns:name` | Imported from NetEnv namespace | `nenv-ns:trader` |

The `category:value` pattern is optional but recommended for anything that could be ambiguous. `gpu` is unambiguous. `service:n8n` is clearer than just `n8n`.

## Adding Tags

Edit `nodes/<hostname>.yaml`:

```yaml
tags:
  - compute
  - laptop
  - stack:netenv    # new!
```

Commit and push. The agent picks it up on the next git pull cycle (default: every 5 minutes).
