package main

// Network-volume mounting for the mount: universal step type — SMB (//host/share)
// and NFS (host:/path) shares, on top of the existing local-device flow.
//
// Linux: systemd .mount + .automount units under /etc/systemd/system — the
// same boot-safe, lazy-mount pattern used for Bifrost/MiddleEarth. First
// access mounts; a down server never blocks boot. Unit files are written
// idempotently (content-compared), never clobbering units we don't own
// (first line must say gogitops or we refuse).
//
// macOS: mount_smbfs / mount_nfs at /Volumes/<name> — the recipe's at: path
// is IGNORED on darwin (volumes mount where Finder expects them). Auth comes
// from the user's Keychain (mount_smbfs reads it); no tty prompt is possible
// under the agent, so a missing Keychain entry fails with a clear hint.
//
// Root escalation (Linux, non-root agents): sudo -n (NOPASSWD or cached
// ticket) → pkexec (polkit GUI dialog, best-effort, 120s cap) → clean fail
// with an actionable hint. The step NEVER hangs waiting on a password.

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// isNetworkDevice classifies a mount device: "//host/share" → SMB,
// "host:/path" → NFS. uuid=/label=//dev/... stay local-device.
func isNetworkDevice(dev string) (smb, nfs bool) {
	if dev == "" {
		return false, false
	}
	if strings.HasPrefix(dev, "//") {
		return true, false
	}
	// NFS: "server:/export" — a colon before any slash, not a uuid=/label= spec
	if strings.HasPrefix(dev, "uuid=") || strings.HasPrefix(dev, "label=") || strings.HasPrefix(dev, "/") {
		return false, false
	}
	ci := strings.Index(dev, ":")
	si := strings.Index(dev, "/")
	return false, ci > 0 && (si < 0 || ci < si)
}

// systemdEscapePath converts an absolute path to the systemd unit-name
// fragment (systemd-escape rules): "/" → "-", characters outside
// [a-zA-Z0-9:_.-] → \xHH. "/media/benn/Bifrost" → "media-benn-Bifrost".
func systemdEscapePath(p string) string {
	p = strings.TrimPrefix(p, "/") // leading "/" separates — unit names start at the first component
	var sb strings.Builder
	for _, r := range p {
		switch {
		case r == '/':
			sb.WriteByte('-')
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == ':' || r == '.' || r == '_' || r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteString(fmt.Sprintf(`\x%02x`, r))
		}
	}
	return sb.String()
}

// expandHomePath replaces a leading "~" with the given home dir.
func expandHomePath(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// translateNetworkMount builds the bash for network-share mount steps
// (SMB //server/share, NFS server:/path). See the file header for the
// per-OS strategy. States emitted (attr-friendly key=value):
//
//	state=already-mounted | mounted | units-armed | units-active |
//	mounted-fallback | fail reason=…
func translateNetworkMount(step recipeStep) string {
	name := step.Mount
	dev := step.MountDevice
	_, nfs := isNetworkDevice(dev)

	u := currentUserInfo()
	userName, uid, gid, home := "user", "1000", "1000", ""
	if u != nil {
		userName, uid, gid = u.Username, u.Uid, u.Gid
		home = u.HomeDir
	}
	if home == "" {
		home = os.Getenv("HOME")
	}

	// Resolve credentials (cifs only). Three sources, in order:
	//   credentials: nenv:<ns>/<user>,nenv:<ns>/<pass>  → agent ensures the
	//     credentials file at RUNTIME (never embeds secret values)
	//   credentials: <path>                             → use that file
	//   (empty)                                         → ~/.smbcredentials
	//     when present, else guest with a warning
	creds := ""
	credsWarn := ""
	credsBootstrap := ""
	if !nfs && step.MountCreds != "none" && step.MountCreds != "guest" {
		if file, bootstrap, ok := credsFromNenv(step.MountCreds, home); ok {
			creds, credsBootstrap = file, bootstrap
		} else {
			c := step.MountCreds
			if c == "" {
				c = "~/.smbcredentials"
			}
			c = expandHomePath(c, home)
			if _, err := os.Stat(c); err == nil {
				creds = c
			} else {
				credsWarn = fmt.Sprintf(`echo "warn=no-credentials-file path=%s hint='create a credentials file (username=/password= lines, chmod 600), set credentials: nenv:ns/KEY,ns/KEY, or store creds in nenv'"`, shellQuote(c))
			}
		}
	}

	if runtime.GOOS == "darwin" {
		return translateNetworkMountDarwin(step, name, dev, nfs, credsWarn, credsBootstrap)
	}
	return translateNetworkMountLinux(step, name, dev, nfs, creds, credsWarn, credsBootstrap, userName, uid, gid, home)
}

// parseNenvRef splits "nenv:<ns>/<key>" (or "nenv:<key>" → global ns).
func parseNenvRef(ref string) (ns, key string) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "nenv:")
	ns, key = "global", ref
	if i := strings.Index(ref, "/"); i >= 0 {
		ns, key = ref[:i], ref[i+1:]
	}
	return ns, key
}

// credsFromNenv handles credentials: values that reference NetEnv —
// "nenv:<ns>/<userkey>,nenv:<ns>/<passkey>". The agent resolves the refs at
// RUNTIME inside the generated bash (values never appear in the translated
// script, dry-run output, or logs) and ensures the credentials file exists
// before the mount proceeds. Returns (file path, bootstrap bash, true).
func credsFromNenv(spec, home string) (string, string, bool) {
	if !strings.Contains(spec, "nenv:") {
		return "", "", false
	}
	refs := strings.Split(spec, ",")
	if len(refs) < 2 {
		return "", "", false
	}
	uns, ukey := parseNenvRef(refs[0])
	pns, pkey := parseNenvRef(refs[1])
	file := filepath.Join(home, ".smbcredentials")

	// Built with plain shell indirection ($NU/$NP) — no secrets in the text.
	// nenv resolves with a ~/.local/bin fallback: daemons run with the systemd
	// default PATH where ~/.local/bin is absent (framework, other user-mode
	// installs), while server hosts keep nenv in /usr/local/bin.
	bootstrap := fmt.Sprintf(`NENV_BIN=$(command -v nenv 2>/dev/null || true)
if [ -z "$NENV_BIN" ] && [ -x "$HOME/.local/bin/nenv" ]; then NENV_BIN="$HOME/.local/bin/nenv"; fi
if [ -z "$NENV_BIN" ]; then
  echo "warn=nenv-cli-not-installed"
fi
NU=$(${NENV_BIN:-nenv} get %s %s 2>/dev/null)
NP=$(${NENV_BIN:-nenv} get %s %s 2>/dev/null)
if [ -n "$NU" ] && [ -n "$NP" ]; then
  { echo "username=$NU"; echo "password=$NP"; } > %s
  chmod 600 %s
  echo "note=creds-refreshed-from-nenv file=%s"
elif [ -f %s ]; then
  echo "warn=nenv-unresolved-using-existing-creds file=%s"
else
  echo "state=fail reason=no-credentials hint='nenv keys unset/unreachable and no credentials file — configure nenv (setup-nenv recipe) or create the file'"
  exit 1
fi
`,
		shellQuote(uns), shellQuote(ukey),
		shellQuote(pns), shellQuote(pkey),
		shellQuote(file), shellQuote(file), shellQuote(file),
		shellQuote(file), shellQuote(file))
	return file, bootstrap, true
}

// currentUserInfo is os/user.Current with a nil-safe fallback.
func currentUserInfo() *user.User {
	u, err := user.Current()
	if err != nil {
		return nil
	}
	return u
}

// ── Linux: systemd .mount + .automount units ───────────────────────────────

func translateNetworkMountLinux(step recipeStep, name, dev string, nfs bool, creds, credsWarn, credsBootstrap, userName, uid, gid, home string) string {
	at := step.MountAt
	if at == "" {
		at = "/media/" + userName + "/" + name
	}
	at = expandHomePath(at, home)

	fstype := "cifs"
	if nfs {
		fstype = "nfs"
	}

	opts := "_netdev"
	if !nfs {
		opts += ",iocharset=utf8"
		if creds != "" {
			opts += ",credentials=" + creds
		}
		opts += ",uid=" + uid + ",gid=" + gid
	}
	if step.MountOptions != "" {
		opts += "," + step.MountOptions
	}

	unitN := systemdEscapePath(at) // e.g. media-benn-Bifrost

	mountUnit := fmt.Sprintf(`# gogitops-managed: %s — edits may be overwritten by the mount: recipe step
[Unit]
Description=Mount %s (gogitops)
StartLimitIntervalSec=0
StartLimitBurst=0

[Mount]
What=%s
Where=%s
Type=%s
Options=%s

[Install]
WantedBy=multi-user.target
`, name, name, dev, at, fstype, opts)

	autoUnit := fmt.Sprintf(`# gogitops-managed: %s — edits may be overwritten by the mount: recipe step
[Unit]
Description=Automount %s (gogitops)
ConditionPathIsDirectory=%s

[Automount]
Where=%s

[Install]
WantedBy=multi-user.target
`, name, name, at, at)

	// Root script — fully literal (Go interpolates every value), delivered via
	// a quoted heredoc to a temp file, then run as root through whichever
	// escalation path is available. Never prompts on a tty.
	rootScript := fmt.Sprintf(`set -u
UM=%s
UA=%s
for F in "$UM" "$UA"; do
  if [ -f "$F" ] && ! head -n 1 "$F" | grep -q gogitops 2>/dev/null; then
    echo "state=fail reason=foreign-unit file=$F"
    exit 1
  fi
done
if [ "%s" = "cifs" ] && ! command -v mount.cifs >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then apt-get install -y cifs-utils >/dev/null 2>&1 && echo "note=cifs-utils-installed" || echo "warn=cifs-utils-install-failed";
  elif command -v dnf >/dev/null 2>&1; then dnf install -y cifs-utils >/dev/null 2>&1 && echo "note=cifs-utils-installed" || echo "warn=cifs-utils-install-failed";
  elif command -v zypper >/dev/null 2>&1; then zypper install -y cifs-utils >/dev/null 2>&1 && echo "note=cifs-utils-installed" || echo "warn=cifs-utils-install-failed";
  elif command -v pacman >/dev/null 2>&1; then pacman -S --noconfirm cifs-utils >/dev/null 2>&1 && echo "note=cifs-utils-installed" || echo "warn=cifs-utils-install-failed";
  elif command -v apk >/dev/null 2>&1; then apk add cifs-utils >/dev/null 2>&1 && echo "note=cifs-utils-installed" || echo "warn=cifs-utils-install-failed";
  fi
fi
mkdir -p %s || { echo "state=fail reason=cannot-create-mountpoint point=%s"; exit 1; }
CHANGED=0
T=$(mktemp)
printf '%%s\n' %s > "$T"
if ! cmp -s "$T" "$UM"; then cp "$T" "$UM"; CHANGED=1; fi
printf '%%s\n' %s > "$T"
if ! cmp -s "$T" "$UA"; then cp "$T" "$UA"; CHANGED=1; fi
rm -f "$T"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload
  systemctl enable %s.automount >/dev/null 2>&1
  systemctl start %s.automount 2>/dev/null
  systemctl start %s.mount >/dev/null 2>&1
  echo "state=units-armed unit=%s.automount changed=$CHANGED"
  exit 0
fi
# No systemd (container) — try a direct mount so the step still works.
if mount -t %s -o %s %s %s >/dev/null 2>&1; then
  echo "state=mounted-fallback changed=$CHANGED"
  exit 0
fi
echo "state=fail reason=no-systemd-mount-failed point=%s"
exit 1
`,
		shellQuote("/etc/systemd/system/"+unitN+".mount"),
		shellQuote("/etc/systemd/system/"+unitN+".automount"),
		fstype,
		shellQuote(at), shellQuote(at),
		shellQuote(mountUnit),
		shellQuote(autoUnit),
		unitN, unitN, unitN, unitN,
		fstype, shellQuote(opts), shellQuote(dev), shellQuote(at),
		shellQuote(at),
	)

	// Outer script — everything before the root part runs as the agent user
	// (no privilege needed), then verify + trigger the lazy mount.
	script := fmt.Sprintf(`M_DEV=%s
M_AT=%s
M_UNIT=%s
%s
if findmnt -rn "$M_AT" >/dev/null 2>&1; then
  echo "state=already-mounted device=$(findmnt -rn -o SOURCE "$M_AT") point=$M_AT"
  exit 0
fi
# Ensure credentials (nenv-backed) before the privileged part.
%s
RF=$(mktemp /tmp/gogitops-mount.XXXXXX)
chmod 600 "$RF"
cat > "$RF" <<'GOGITOPS_MOUNT_ROOT'
%s
GOGITOPS_MOUNT_ROOT
if [ "$(id -u)" = "0" ]; then
  bash "$RF"
elif sudo -n true 2>/dev/null; then
  sudo -n bash "$RF"
elif command -v pkexec >/dev/null 2>&1; then
  echo "note=pkexec: approving password dialog on the desktop (120s)…" >&2
  timeout 120 pkexec bash "$RF" 2>/dev/null
else
  rm -f "$RF"
  echo "state=fail reason=needs-sudo hint='run from your terminal with sudo -v first, or add a scoped NOPASSWD sudoers entry for this user'"
  exit 1
fi
RC=$?
rm -f "$RF"
[ $RC -ne 0 ] && exit $RC
# Verify + trigger the automount (listing contents forces the lazy mount).
if timeout 20 ls "$M_AT" >/dev/null 2>&1; then
  echo "state=mounted device=$(findmnt -rn -o SOURCE "$M_AT" 2>/dev/null) point=$M_AT unit=$M_UNIT.automount"
  exit 0
fi
if systemctl is-active --quiet "$M_UNIT.automount" 2>/dev/null; then
  echo "state=units-active point=$M_AT note='automount armed — first access mounts (is the share reachable?)'"
  exit 0
fi
echo "state=fail reason=mount-verify-failed point=$M_AT"
exit 1
`,
		shellQuote(dev),
		shellQuote(at),
		shellQuote(unitN),
		credsWarn,
		credsBootstrap,
		rootScript,
	)
	return script
}

// ── macOS: /Volumes/<name>, recipe path ignored ────────────────────────────

func translateNetworkMountDarwin(step recipeStep, name, dev string, nfs bool, credsWarn, credsBootstrap string) string {
	at := "/Volumes/" + name
	note := ""
	if step.MountAt != "" {
		note = fmt.Sprintf(`echo "note=macOS ignores at=%s — network volumes mount under /Volumes"`, shellQuote(step.MountAt))
	}
	// SMB URL already in mount_smbfs form (//user@host/share); NFS passes through.
	mounter := fmt.Sprintf("mkdir -p %s 2>/dev/null; mount_smbfs %s %s", shellQuote(at), shellQuote(dev), shellQuote(at))
	if nfs {
		mounter = fmt.Sprintf("mkdir -p %s 2>/dev/null; mount_nfs %s %s", shellQuote(at), shellQuote(dev), shellQuote(at))
	}
	return fmt.Sprintf(`M_DEV=%s
M_AT=%s
%s
%s
if mount | grep -q " on $M_AT ("; then
  echo "state=already-mounted device=$M_DEV point=$M_AT"
  exit 0
fi
if %s >/dev/null 2>&1 && mount | grep -q " on $M_AT ("; then
  echo "state=mounted device=$M_DEV point=$M_AT"
  exit 0
fi
echo "state=fail reason=mount-failed point=$M_AT hint='auth: store the server password in Keychain (mount_smbfs/mount_nfs read it); NFS may need root (sudo mount_nfs)'"
exit 1
`,
		shellQuote(dev), shellQuote(at), note, credsWarn, mounter,
	)
}
