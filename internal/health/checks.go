// Package health implements service checkers: systemd, tcp, docker, process.
package health

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/bennbanks/gogitops/internal/config"
)

// Status is the result of a service check
type Status struct {
	Name   string `json:"name"`
	Status string `json:"status"` // running | down | error
	Detail string `json:"detail,omitempty"`
}

// CheckResult is the aggregate health snapshot
type CheckResult struct {
	Services   []Status         `json:"services"`
	DiskWarns  []string         `json:"disk_warns"`
	DiskCrits  []string         `json:"disk_crits"`
	Healthy    bool             `json:"healthy"`
}

// CheckService runs a single service check
func CheckService(svc config.Service) Status {
	switch svc.Type {
	case "systemd":
		return checkSystemd(svc)
	case "tcp":
		return checkTCP(svc)
	case "docker":
		return checkDocker(svc)
	case "docker-compose":
		return checkCompose(svc)
	case "process":
		return checkProcess(svc)
	default:
		return Status{Name: svc.Name, Status: "error", Detail: fmt.Sprintf("unknown check type: %s", svc.Type)}
	}
}

// checkSystemd checks if a systemd unit is active
func checkSystemd(svc config.Service) Status {
	out, err := exec.Command("systemctl", "is-active", svc.Unit).Output()
	if err != nil {
		return Status{Name: svc.Name, Status: "down", Detail: err.Error()}
	}
	state := strings.TrimSpace(string(out))
	if state == "active" {
		return Status{Name: svc.Name, Status: "running"}
	}
	return Status{Name: svc.Name, Status: "down", Detail: state}
}

// checkTCP checks if a TCP port is accepting connections
func checkTCP(svc config.Service) Status {
	addr := fmt.Sprintf("127.0.0.1:%d", svc.Port)
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return Status{Name: svc.Name, Status: "down", Detail: fmt.Sprintf("tcp %s: %v", addr, err)}
	}
	conn.Close()
	return Status{Name: svc.Name, Status: "running"}
}

// checkDocker checks Docker daemon or a specific container
func checkDocker(svc config.Service) Status {
	dockerBin := svc.DockerBin
	if dockerBin == "" {
		dockerBin = "docker"
	}

	if svc.Check == "daemon-alive" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, dockerBin, "info", "--format", "{{.ServerVersion}}").Output()
		if err != nil {
			return Status{Name: svc.Name, Status: "down", Detail: "docker daemon unreachable"}
		}
		return Status{Name: svc.Name, Status: "running", Detail: strings.TrimSpace(string(out))}
	}

	if svc.Check == "container-alive" && svc.Container != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, dockerBin, "inspect", "-f", "{{.State.Status}}", svc.Container).Output()
		if err != nil {
			return Status{Name: svc.Name, Status: "down", Detail: fmt.Sprintf("inspect failed: %v", err)}
		}
		state := strings.TrimSpace(string(out))
		if state == "running" {
			return Status{Name: svc.Name, Status: "running"}
		}
		return Status{Name: svc.Name, Status: "down", Detail: state}
	}

	return Status{Name: svc.Name, Status: "error", Detail: "docker check requires 'check' field (daemon-alive or container-alive)"}
}

// checkCompose checks all containers in a compose stack
func checkCompose(svc config.Service) Status {
	dockerBin := svc.DockerBin
	if dockerBin == "" {
		dockerBin = "docker"
	}
	down := []string{}
	for _, c := range svc.Containers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, dockerBin, "inspect", "-f", "{{.State.Status}}", c).Output()
		cancel()
		if err != nil {
			down = append(down, c)
			continue
		}
		if strings.TrimSpace(string(out)) != "running" {
			down = append(down, c)
		}
	}
	if len(down) > 0 {
		return Status{Name: svc.Name, Status: "down", Detail: fmt.Sprintf("down containers: %s", strings.Join(down, ","))}
	}
	return Status{Name: svc.Name, Status: "running"}
}

// checkProcess checks if a process matching a pattern is running
func checkProcess(svc config.Service) Status {
	out, err := exec.Command("pgrep", "-f", svc.Pattern).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return Status{Name: svc.Name, Status: "down", Detail: fmt.Sprintf("no process matching %q", svc.Pattern)}
	}
	return Status{Name: svc.Name, Status: "running"}
}

// RunAllChecks executes all service + disk checks for a node
func RunAllChecks(node *config.NodeConfig) CheckResult {
	result := CheckResult{Healthy: true}

	for _, svc := range node.Services {
		status := CheckService(svc)
		result.Services = append(result.Services, status)
		if status.Status != "running" {
			result.Healthy = false
		}
	}

	// Disk checks
	for _, dc := range node.Disk {
		pct, err := diskUsage(dc.Mount)
		if err != nil {
			continue
		}
		mountWarn := fmt.Sprintf("%s %d%%", dc.Mount, pct)
		if pct >= dc.CritPct {
			result.DiskCrits = append(result.DiskCrits, mountWarn)
			result.Healthy = false
		} else if pct >= dc.WarnPct {
			result.DiskWarns = append(result.DiskWarns, mountWarn)
		}
	}

	return result
}

// diskUsage returns the percentage used for a mount point (Linux statfs)
func diskUsage(mount string) (int, error) {
	out, err := exec.Command("df", "-P", mount).Output()
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, fmt.Errorf("unexpected df fields")
	}
	// fields: Filesystem 1024-blocks Used Available Capacity Mounted
	var pct int
	fmt.Sscanf(strings.TrimSuffix(fields[4], "%"), "%d", &pct)
	return pct, nil
}
