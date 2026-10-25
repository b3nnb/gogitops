// Stdlib, GNOME desktop part — the gcr-ssh-agent standardization dance
// (enable the gcr socket, disable+mask the gpg-agent SSH hijacker, retire
// the empty ssh-agent.service) as one typed op. The choreography used to be
// three shell steps that swallowed their own errors with 2>/dev/null and
// reported success either way; the typed outputs say what actually happened.
//
//go:build !windows

package functionlib

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func init() {
	Register(Function{
		Name:        "system.gnome-keyring",
		Description: "Standardize the SSH agent on GNOME desktops: enable+start gcr-ssh-agent.socket, disable+mask gpg-agent-ssh.socket, disable ssh-agent.service, and upsert SSH_AUTH_SOCK to the GCR socket in rc files. Outputs: sock, enabled, masked, rc_lines.",
		Params: []Param{
			{Name: "rcfiles", Type: "string", Description: "Comma-separated rc files (~ expands) receiving the SSH_AUTH_SOCK upsert, e.g. ~/.bashrc"},
			{Name: "verify", Type: "bool", Description: "true = fail when the GCR socket is missing after enable (strict mode for abort recipes)"},
		},
		Run: gnomeKeyring,
	})
}

// gcrSocketPath is a package var so tests can pin the socket path (the
// real /run/user/<uid>/gcr/ssh may exist on a dev box).
var gcrSocketPath = func() string {
	return fmt.Sprintf("/run/user/%d/gcr/ssh", os.Getuid())
}

// gnomeKeyring performs the GCR standardization in-process: unit state via
// systemctl --user, socket path from the uid, rc upserts through
// system.rc-export (update-or-append, never a whole-file rewrite). Unit
// ops are best-effort — already-enabled/already-masked count as done, the
// way the originals' `|| echo ALREADY` did.
func gnomeKeyring(ctx Context, args map[string]any) (map[string]any, error) {
	rcraw, _ := args["rcfiles"].(string)
	rcfiles := []string{}
	for _, f := range strings.Split(rcraw, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			rcfiles = append(rcfiles, f)
		}
	}
	verify := parseBoolArg(args["verify"])

	sock := gcrSocketPath()
	enabled, masked := false, false

	// Enable the GCR socket (idempotent — enable --now on an enabled unit exits 0).
	if err := exec.Command("systemctl", "--user", "enable", "--now", "gcr-ssh-agent.socket").Run(); err == nil {
		enabled = true
	}
	// Disable + mask the hijacker.
	_ = exec.Command("systemctl", "--user", "disable", "--now", "gpg-agent-ssh.socket").Run()
	if err := exec.Command("systemctl", "--user", "mask", "gpg-agent-ssh.socket").Run(); err == nil {
		masked = true
	}
	// Disable the empty systemd ssh-agent.
	_ = exec.Command("systemctl", "--user", "disable", "--now", "ssh-agent.service").Run()

	rcDone := 0
	for _, f := range rcfiles {
		if _, err := rcExport(ctx, map[string]any{
			"file":    f,
			"var":     "SSH_AUTH_SOCK",
			"value":   sock,
			"comment": "GCR ssh-agent (modern GNOME)",
		}); err == nil {
			rcDone++
		}
	}

	// Optional strict verify: the socket must exist as a socket file.
	if verify {
		if _, serr := os.Stat(sock); serr != nil {
			return nil, fmt.Errorf("gcr socket %s not present after enable (reboot may be required)", sock)
		}
	}

	return map[string]any{
		"sock":     sock,
		"enabled":  enabled,
		"masked":   masked,
		"rc_lines": rcDone,
		"error":    "",
		"math":     fmt.Sprintf("sock=%s enabled=%v masked=%v rc_lines=%d/%d", sock, enabled, masked, rcDone, len(rcfiles)),
	}, nil
}
