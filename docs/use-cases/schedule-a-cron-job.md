# Schedule a Recurring Job (cron)

**Goal:** install a cron entry that runs a command periodically — without
duplicating the entry on every recipe re-run.

Use the built-in `schedule:` step type. `command:` is the job payload; the
agent installs an idempotent crontab entry tagged `# gogitops:<step-name>`.
Re-runs **replace** the tagged entry instead of duplicating, and the dedup
sweep also removes legacy unmarked lines from older recipe versions.

## Presets

```yaml
steps:
  - name: refresh-ip-cache
    description: "Refresh cached public IP hourly"
    command: "curl -s --max-time 5 ifconfig.me > $HOME/.cache/external_ip"
    schedule: hourly
```

Presets: `hourly`, `daily`, `weekly`.

## Raw cron

Any 5-field cron expression works:

```yaml
  - name: mounts-doctor
    description: "Run mounts doctor every 5 minutes"
    command: "$HOME/.local/bin/friday-mounts-doctor.sh"
    schedule: "*/5 * * * *"
```

(Real example: `recipes/friday-drive-mounts/` runs doctors at `*/15` and `*/5`.)

## Gotchas

- **Use absolute paths / `$HOME`** in the payload — cron has no login
  environment. Prefer `$HOME/...` over `~` inside command strings.
- The entry is keyed by step name: renaming the step orphans the old cron
  line (the dedup only knows current + legacy-unmarked lines).
- `schedule:` installs the cron entry; it does not run the command
  immediately. Add a second un-scheduled step with the same command if you
  want run-now-plus-scheduled behavior (see friday-drive-mounts: every
  doctor has a `-run` step and a `-schedule` step).
- Works on Linux and macOS (agent translates).
