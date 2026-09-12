// Report mount state for a mountpoint: device, options, fstab persistence.
// Args: <mountpoint> (default: /). Prints attr-friendly lines. Exit 0 when
// mounted, 1 when not — usable with expect_exit and when: conditions.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	at := "/"
	if len(os.Args) > 1 && os.Args[1] != "" {
		at = os.Args[1]
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		fmt.Println("mount-info: cannot read /proc/mounts (linux only)")
		os.Exit(1)
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		// /proc/mounts escapes spaces as \040
		if strings.ReplaceAll(f[1], "\\040", " ") == at {
			device := strings.ReplaceAll(f[0], "\\040", " ")
			opts := f[3]
			inFstab := "no"
			if fstab, err := os.ReadFile("/etc/fstab"); err == nil &&
				strings.Contains(string(fstab), " "+at+" ") {
				inFstab = "yes"
			}
			fmt.Printf("state=mounted device=%s point=%s fstab=%s opts=%s\n", device, at, inFstab, opts)
			return
		}
	}
	fmt.Printf("state=not-mounted point=%s\n", at)
	os.Exit(1)
}
