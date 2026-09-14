# Conditional Steps

**Goal:** run a step only when something is true — file exists, command
succeeds, attribute matches. This is what makes recipes idempotent.

## `when:` — shell test must pass

The step runs only if this command exits 0:

```yaml
  - name: install-starship
    command: "curl -sS https://starship.rs/install.sh | sh -s -- -y"
    when: "! command -v starship >/dev/null 2>&1"   # only if missing
    on_failure: abort
```

This is the idempotency workhorse: check-then-act, applied state = skip.

## `only_if:` — inverse form

Skip the step if the condition is false. Equivalent semantics to `when:`,
use whichever reads better:

```yaml
  - name: ensure-path
    command: "printf '\nexport PATH=\"$HOME/.local/bin:$PATH\"\n' >> $HOME/.bashrc"
    only_if: "test -f $HOME/.bashrc"
```

## `when_attr:` / `only_if_attr:` — condition on attributes

Same idea, but the condition is about captured attributes or the test store
([collecting attributes](collect-attributes.md)):

| Condition | Syntax |
|---|---|
| Equality | `when_attr: "attr.tests.fail == 0"` |
| Inequality | `when_attr: "attr.tests.fail != 0"` |
| Substring | `when_attr: "attr.tests.failing contains docker"` |
| Truthy (set, not "false"/"0") | `when_attr: "attr.docker_version"` |
| Inverse of any | `only_if_attr:` (same syntax) |

Real example — `recipes/test-report/` gates an ALL CLEAR step on
`attr.tests.fail == 0` and a FAILING step on `!= 0`.

## Runtime-detect, don't OS-assume

Capability checks beat OS filters (see [target-specific-nodes](target-specific-nodes.md)):

```yaml
  - name: init-zsh
    command: "grep -q 'starship init zsh' $HOME/.zshrc || printf '...' >> $HOME/.zshrc"
    when: "test -f $HOME/.zshrc || echo $SHELL | grep -q zsh"
```

## Filter interaction

Steps can combine filters — all must pass:

```yaml
  - name: mini-sshfs
    package: sshfs
    labels_required: [lora-training]   # label scope
    os: darwin                         # platform
    when: "[ -d /Library/Filesystems/macfuse.fs ]"   # runtime capability
```

Filtered-out steps count as **skips**, not failures — `run-all` treats
applied-state skips as its drift signal.

## YAML quoting reminder

`when:` values containing double quotes: use single-quoted YAML. The parser
strips outer quotes and processes no escapes — see the gotcha in the
[cookbook index](README.md).
