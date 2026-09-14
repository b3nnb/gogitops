# Validate Output and Retry on Failure

**Goal:** a step that fails loudly when it didn't do what you meant — with
retries for the flaky stuff (network installs, service starts).

## Expectations

```yaml
steps:
  - name: verify-starship
    command: "starship --version"
    expect_regex: "starship .+"        # stdout must match
```

| Field | Meaning |
|---|---|
| `expect: "text"` | stdout must contain this substring |
| `expect_regex: "pattern"` | stdout must match this regex |
| `expect_exit: 0` | exit code check (default 0) |

Omit all three = exit-code-only validation (default 0).

## Retries

```yaml
  - name: install-starship
    command: "curl -sS https://starship.rs/install.sh | sh -s -- -b $HOME/.local/bin -y"
    retries: 2
    retry_delay: 5s
    on_failure: abort
```

`retry_delay` takes a duration like `5s` or `10s`. Built-in step types
(`package:`, `mount:`, …) inherit retries too — everything flows through the
same runner.

## Failure policy

```yaml
  - name: optional-nice-to-have
    command: "curl -s --max-time 5 ifconfig.me > $HOME/.cache/external_ip"
    on_failure: continue   # or abort (default)
```

- `abort` (default): recipe stops, later steps don't run, run marks failed
- `continue`: log it, move on — for best-effort steps (caches, optional
  tooling, report-only)

Pattern worth copying: hard requirements `on_failure: abort`, diagnostics and
optional extras `on_failure: continue`. Every real recipe in `recipes/` mixes
both.

## Assertions (test modules)

Inside test modules (`test_module: true`), `assert:` checks attribute
conditions — equality, substring, truthy — same syntax as `when_attr:`. See
[test-your-recipe](test-your-recipe.md).

## Validate without executing

```bash
gogitops recipe validate recipes/my-thing/my-thing.yaml
```

Parses, checks structure and attribute references (unknown attrs → warning
with did-you-mean). Warnings never block. Plus a dry run:

```bash
gogitops recipe run my-thing --dry-run     # shows translated steps, no exec
```
