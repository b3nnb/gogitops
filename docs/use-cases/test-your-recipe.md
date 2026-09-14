# Test Your Recipe

**Goal:** prove a recipe (or the fleet) behaves — before and after changes,
from any checkout, without a Go dev environment.

## Three layers of checking

| Layer | Command | What it proves |
|---|---|---|
| Static | `gogitops recipe validate <file>` | parses, structure, attr refs (warnings, never blocks) |
| Dry run | `gogitops recipe run <name> --dry-run` | translation + filters resolve, nothing executes |
| Live | `gogitops recipe run <name>` | the real thing |

## YAML test suites (`gogitops test`)

Tests are YAML files with `test_module: true` — **the same recipe vocabulary**
(command/script/expect/assert/when/…), so tests are written by operators, not
Go devs. Only `test_module: true` files run — the test runner never executes
normal recipes (they can install things).

```bash
gogitops test list               # available test modules
gogitops test run <name-or-path> # one module
gogitops test run-all            # full suite; exit 1 on any failure (CI-able)
```

Locations:
- `test_modules/*.yaml` — fleet-wide suites (common, per-OS, docker, recipe-selftest)
- `recipes/<name>/tests/*.yaml` — recipe-scoped suites

`{{self}}` substitutes the running binary's path — selftests always exercise
the CURRENT engine, never a stale PATH shadow.

## Assertions

Inside test modules, `assert:` evaluates attribute conditions — same syntax
as `when_attr:`:

```yaml
name: docker-selftest
test_module: true
steps:
  - name: docker-version
    command: "docker --version"
    set_attr: docker_version
  - name: assert-version
    assert: "attr.docker_version contains Docker"
```

## Reading results everywhere

Suite runs write to the attribute store: `tests.pass`, `tests.fail`,
`tests.skip`, `tests.total`, `tests.failing`, `tests.last_run`, `tests.scope`
(`suite` or `module:<name>`). Any recipe can gate on them — reference recipe
`recipes/test-report/` reports suite results, gates an ALL CLEAR step on
`attr.tests.fail == 0` and a FAILING step on `!= 0`.

## Recipe-scoped tests ride along

`test_modules/` at repo root is the fleet suite; a recipe's own tests live in
`recipes/<name>/tests/` and travel with the recipe (self-contained-first).

## Container E2E pattern

Recipes can be proven in Docker without touching the host — the
setup-ssh-access recipe was verified this way (ubuntu:24.04, real sshd, real
key login). Mount the repo read-only, run `gogitops recipe run <name> -repo
/repo`, assert filesystem state. Recipe steps that need special safety
handling (`mkfs` etc.) are agent-blocked by design — structure container tests
around the allowed paths.

## Attribute catalog audit

```bash
gogitops attrs verify    # validate ALL recipes; exit 1 on fail (CI-able)
```
