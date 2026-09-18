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
| `//server/share` | SMB/CIFS network share (NAS etc.) |
| `server:/path` | NFS export |

## Network shares (v0.7.2+)

One step mounts a NAS share the way systemd does it natively — a `.mount`
unit backed by a lazy `.automount` unit, boot-safe (a down server never
blocks boot) and idempotent (already-mounted = pass; unchanged units are
left alone; foreign units are never clobbered):

```yaml
steps:
  - name: bifrost
    mount: Bifrost                 # volume name (path component + unit name)
    device: //10.2.0.103/Bifrost   # SMB share (//user@server/share also works)
    at: /media/benn/Bifrost        # optional — default /media/<user>/<name>
    credentials: nenv:global/NAS_USERNAME,nenv:global/NAS_PASSWORD  # or a file path; optional
    options: vers=3.0,soft,actimeo=30
```

- **Credentials from NetEnv** — `credentials: nenv:global/NAS_USERNAME,nenv:global/NAS_PASSWORD`
  makes the agent itself ensure `~/.smbcredentials`: it resolves both refs at
  runtime inside the step (secret values never appear in the translated
  script, dry-run, or logs), writes the file chmod 600, and falls back to an
  existing file with a warning if nenv is unreachable. No manual file, no
  secrets in git.
- **Linux** — the agent writes `media-benn-Bifrost.mount` +
  `media-benn-<name>`-style `.automount` units to `/etc/systemd/system`,
  installs `cifs-utils` when missing, daemon-reloads, enables the automount,
  and verifies with a real `ls` (which forces the lazy mount).
- **macOS** — `mount_smbfs` at `/Volumes/<name>`; the recipe's `at:` path is
  ignored (volumes live where Finder expects). Auth comes from the user's
  Keychain — store the server password there for agent-run mounts.
- **Root** — unit writes need privilege: `sudo -n` (NOPASSWD/cached) →
  `pkexec` (desktop password dialog, 120s) → clean fail with a hint. The
  step never hangs on a password prompt.
- **NFS** — same flow with `device: nas:/export/path`, `Type=nfs`.
- States emitted: `state=mounted | already-mounted | units-armed |
  units-active | mounted-fallback | fail reason=…` (match with
  `expect_regex`).

Real recipes: `recipes/nas-bifrost-mount/`, `recipes/nas-middleearth-mount/`.
End-to-end self-test (real share, then cleanup): `recipes/test-netmount/`.

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
