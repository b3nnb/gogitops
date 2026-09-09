// Example: Go script that collects attributes and returns them as JSON.
// Go scripts are run via `go run` — no pre-compilation needed.
// This demonstrates using Go for more complex attribute collection logic
// that would be awkward in shell.
//
// Output: JSON object on stdout with key-value pairs.
// The recipe can use set_attr to capture stdout, or parse: regex to extract fields.
// Args: none

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type SystemAttrs struct {
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Hostname     string `json:"hostname"`
	LanIP        string `json:"lan_ip"`
	Kernel       string `json:"kernel"`
	DockerRunning bool  `json:"docker_running"`
	DockerVersion string `json:"docker_version,omitempty"`
}

func main() {
	attrs := SystemAttrs{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Hostname: hostname(),
		LanIP:    lanIP(),
		Kernel:   kernel(),
	}

	// Check Docker
	if out, err := exec.Command("docker", "--version").Output(); err == nil {
		attrs.DockerRunning = true
		attrs.DockerVersion = strings.TrimSpace(strings.TrimPrefix(string(out), "Docker version "))
	}

	out, _ := json.MarshalIndent(attrs, "", "  ")
	fmt.Println(string(out))
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func lanIP() string {
	// UDP dial trick — no traffic sent, reveals primary outbound IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "unknown"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func kernel() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
