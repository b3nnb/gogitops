# Mount a Drive / Manage fstab

**Goal:** mount a filesystem at boot and now — declaratively, idempotently,
without hand-editing /etc/fstab.

Use the built-in `mount:` step type (v0.6.4+). Six lines of YAML; the agent
holds all the logic.

## Basic

```yaml
steps:
  - name: backup-drive
    mount: backup            # label used in fstab comment + attrs
    device: uuid=3f2a01c2-... # or label=NAME or /dev/sdb1
    at: /mnt/backup           # mountpoint (created if missing)
    options: defaults,noatime
    fstab: true               # idempotent /etc/fstab persistence
```

## What you get for free

- **Idempotent** — already-mounted = pass (not a failure)
- **fstab ensured every run** — append-only guard, never clobbers existing
  entries
- **sudo only when not root** — no gratuitous privilege
- **macOS**: clean gate message (mounting internal volumes via fstab is not
  a darwin concept — the step tells you instead of half-working)
- **Attributes reported**: `state` (`mounted` | `already-mounted` | `fail`),
  `device`, `point` — readable by later steps and asserts:

```yaml
  - name: verify-mount
    command: "test -d /mnt/backup/files"
    when_attr: "attr.state"
```

## Device specifiers

| Form | Meaning |
|---|---|
| `uuid=<uuid>` | stable across reboots & re-plug (preferred) |
| `label=<label>` | filesystem label |
| `/dev/sdb1` | raw path (breaks if enumeration changes) |

## Testing without mkfs

`mkfs`/format-filesystem is agent-blocked (safety hardline). To exercise a
`mount:` step in a container: `at: /` (already-mounted detection), a
nonexistent device (guard fires with device-not-found), and `fstab: true`
twice while counting /etc/fstab lines. The `[[:space:]]` vs `[[:space]]`
missing-colon bug (silent never-match → appended every run) was caught
exactly this way — always run mount recipes twice and count lines.

## Real-world examples

- `recipes/README.md` philosophy section — the canonical six-liner
- `recipes/friday-drive-mounts/` — SSHFS mounts handled via systemd user
  units + doctors (SSHFS needs its own flow; `mount:` is for local devices)
