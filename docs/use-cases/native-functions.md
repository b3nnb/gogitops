# Call native functions

**You want to:** run a typed, in-process operation from a recipe — no shell, no stdout parsing, outputs flowing straight into later steps.

Use a `func:` step. GoGitOPS ships a native function library ("stdlib") compiled into the agent; the YAML is the menu and the variables, the Go functions are the instructions.

```yaml
name: disk-watch
steps:
  - name: check-bifrost
    func: storage.disk-free
    args: path=/media/benn/Bifrost
    set_attr: bifrost_space      # full key=value line becomes the attr

  - name: alert-if-full
    command: echo "bifrost {{func.used_pct}}% used ({{func.total_gb}}GB total)"
    # or branch on it:
    when_attr: "attr.bifrost_space contains used_pct=9"
```

## The model

- **`func: <set>.<name>`** picks a stdlib function; **`args:`** passes inputs as `key=value,key2=v2` (or a JSON object when values contain commas). `{{vars}}` substitute in args like everywhere else.
- **Outputs are typed.** Every output key `k` becomes `{{func.k}}` for later steps, and the step's pseudo-stdout is a `key=value` line — so `expect:`, `parse:`, `set_attr:`, and `attr_prefix:` all work unchanged.
- **No shell.** The function runs in-process; retries, os/arch/label filters, dry-run, and `on_failure:` apply exactly as for any other step.
- A **down result is a result**: `net.port-check` against a closed port returns `up=false` with exit 0 — your recipe decides via `expect:`/`when_attr:`, not a crash.

## What's in the stdlib

`gogitops functions` lists everything with params. Currently:

| Function | What it does |
|---|---|
| `storage.disk-free` | total/used/free GB + used pct, math in the outputs (`path=`) |
| `net.port-check` | TCP connect check: `up`, `latency_ms`, `error` (`host=`, `port=`, `timeout=`) |
| `system.info` | hostname/os/arch, typed — branching without `uname` |

## Stdlib vs user space — the separation rules

Three sets are **reserved**: `storage`, `net`, `system`. These are compiled into the agent, change only via release + version pin, carry Go tests, and **nothing in your repo can shadow them** — a repo file at `modules/storage/…` is ignored, and the run reports "function not available" rather than silently meaning something different.

Your own logic lives in **user space**: repo `modules/<your-set>/<name>.go`, called via `script:` steps (see [Write a script step](write-a-script-step.md)). Rules:

1. **Call, never shadow.** You may invoke any stdlib function; you may never define anything in `storage/`, `net/`, or `system/`.
2. **One-way dependency.** User code calls stdlib; stdlib never calls user code. Stdlib stays testable and version-pinned.
3. **Bad edits are cheap.** User functions live in the git repo — a broken one is one `git revert`, picked up on the next tick. No binary rebuild needed.

Proven a custom function fleet-worthy? Promote it into stdlib: open a PR with tests, it ships with the next agent release and gets a version pin through `versions.yaml` like everything else.

## Sums it up

`gogitops functions` — see the whole library. `recipes/test-funcs/` — the regression selftest exercising every stdlib function.
