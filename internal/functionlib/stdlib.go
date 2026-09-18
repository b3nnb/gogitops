// Stdlib, portable part. Every stdlib set is declared here via
// reservedByStdlib + its functions registered in init — this file is the
// single place the two-tier boundary is enforced from.
package functionlib

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

func init() {
	reservedByStdlib("storage") // functions: stdlib_unix.go (disk-free needs statfs)
	reservedByStdlib("net")
	reservedByStdlib("system")

	// net.port-check — can we reach host:port right now?
	Register(Function{
		Name:        "net.port-check",
		Description: "TCP connect check to host:port. Outputs up (bool), latency_ms, and error text when down.",
		Params: []Param{
			{Name: "host", Type: "string", Description: "Hostname or IP (required)"},
			{Name: "port", Type: "int", Description: "TCP port (required)"},
			{Name: "timeout", Type: "duration", Description: "Dial timeout (default 3s)"},
		},
		Run: portCheck,
	})

	// system.info — the runner context, typed, for branching recipes.
	Register(Function{
		Name:        "system.info",
		Description: "Node identity + run context: hostname, os, arch. For when_attr branching without shell probes.",
		Params:      nil,
		Run: func(ctx Context, args map[string]any) (map[string]any, error) {
			return map[string]any{
				"hostname": ctx.Hostname,
				"os":       ctx.OS,
				"arch":     ctx.Arch,
			}, nil
		},
	})
}

func portCheck(ctx Context, args map[string]any) (map[string]any, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("host= required")
	}
	portStr, _ := args["port"].(string)
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("port= must be 1-65535, got %q", portStr)
	}
	timeout := 3 * time.Second
	if t, ok := args["timeout"].(string); ok && t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, portStr), timeout)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// A failed check is a RESULT, not an error — the recipe
		// decides via expect:/assert:/on_failure:.
		return map[string]any{
			"up":         false,
			"latency_ms": latency,
			"error":      err.Error(),
			"math":       fmt.Sprintf("tcp dial %s:%d, timeout %s", host, port, timeout),
		}, nil
	}
	conn.Close()
	return map[string]any{
		"up":         true,
		"latency_ms": latency,
		"math":       fmt.Sprintf("tcp dial %s:%d ok in %dms", host, port, latency),
	}, nil
}
