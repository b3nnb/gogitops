// Package config loads node, mesh, group, and stack configuration from the git repo.
package config

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// NodeConfig is the per-device configuration (nodes/<hostname>.yaml)
type NodeConfig struct {
	Hostname string      `yaml:"hostname"`
	NebulaIP string      `yaml:"nebula_ip"`
	LanIP    string      `yaml:"lan_ip"`
	Labels   []string    `yaml:"labels"`
	Services []Service   `yaml:"services"`
	Disk     []DiskCheck `yaml:"disk"`
	Agent    AgentConfig `yaml:"agent"`
}

func (n NodeConfig) Address() string {
	host := ""
	if n.LanIP != "" && n.LanIP != "null" {
		host = n.LanIP
	} else if n.NebulaIP != "" && n.NebulaIP != "null" {
		host = n.NebulaIP
	}
	return host
}

type Service struct {
	Name       string   `yaml:"name"`
	Type       string   `yaml:"type"`
	Unit       string   `yaml:"unit,omitempty"`
	User       bool     `yaml:"user,omitempty"`
	Port       int      `yaml:"port,omitempty"`
	Container  string   `yaml:"container,omitempty"`
	Containers []string `yaml:"containers,omitempty"`
	Path       string   `yaml:"path,omitempty"`
	Pattern    string   `yaml:"pattern,omitempty"`
	Check      string   `yaml:"check,omitempty"`
	DockerBin  string   `yaml:"docker_binary,omitempty"`
}

type DiskCheck struct {
	Mount   string `yaml:"mount"`
	WarnPct int    `yaml:"warn_pct"`
	CritPct int    `yaml:"crit_pct"`
}

type AgentConfig struct {
	UpdateURL       string `yaml:"update_url"`
	CheckInterval   string `yaml:"check_interval"`
	GitRepo         string `yaml:"git_repo"`
	GitBranch       string `yaml:"git_branch"`
	GitPullInterval string `yaml:"git_pull_interval"`
}

type MeshConfig struct {
	Peers []Peer `yaml:"peers"`
}

type Peer struct {
	Hostname string   `yaml:"hostname"`
	NebulaIP string   `yaml:"nebula_ip"`
	LanIP    string   `yaml:"lan_ip"`
	Port     int      `yaml:"port"`
	Labels   []string `yaml:"labels"`
}

func (p Peer) Address() string {
	host := ""
	if p.LanIP != "" && p.LanIP != "null" {
		host = p.LanIP
	} else if p.NebulaIP != "" && p.NebulaIP != "null" {
		host = p.NebulaIP
	}
	if host == "" {
		return ""
	}
	port := p.Port
	if port == 0 {
		port = 7780
	}
	return fmt.Sprintf("%s:%d", host, port)
}

type GroupConfig struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Type        string       `yaml:"type"`
	Members     []Member     `yaml:"members"`
	DependsOn   []Dependency `yaml:"depends_on,omitempty"`
	Config      GroupCfg     `yaml:"config"`
}

type Member struct {
	Node  string `yaml:"node,omitempty"`
	Label string `yaml:"label,omitempty"`
	Stack string `yaml:"stack,omitempty"`
}

type Dependency struct {
	Group string `yaml:"group"`
}

type GroupCfg struct {
	MinMembers      int    `yaml:"min_members"`
	CheckPort       int    `yaml:"check_port"`
	Coordinator     string `yaml:"coordinator"`
	MarketHoursOnly bool   `yaml:"market_hours_only"`
	AlertOnDown     string `yaml:"alert_on_down"`
}

// ── Loaders ────────────────────────────────────────────────────────────────

// LoadNode loads a node config by hostname. If the file doesn't exist,
// the node auto-detects its network info and self-registers.
func LoadNode(repoDir, hostname string) (*NodeConfig, error) {
	path := filepath.Join(repoDir, "nodes", hostname+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return autoRegister(repoDir, hostname)
		}
		return nil, fmt.Errorf("read node config: %w", err)
	}
	var cfg NodeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse node config: %w", err)
	}
	return &cfg, nil
}

// LoadMesh loads the mesh peer list
func LoadMesh(repoDir string) (*MeshConfig, error) {
	path := filepath.Join(repoDir, "mesh.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mesh config: %w", err)
	}
	var cfg MeshConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse mesh config: %w", err)
	}
	return &cfg, nil
}

// LoadGroups loads all group configs
func LoadGroups(repoDir string) ([]GroupConfig, error) {
	dir := filepath.Join(repoDir, "groups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var groups []GroupConfig
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".yaml" && filepath.Ext(e.Name()) != ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var g GroupConfig
		if err := yaml.Unmarshal(data, &g); err != nil {
			continue
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// ── Auto-detection & self-registration ─────────────────────────────────────

// DetectHostname returns the system hostname (stripped of domain)
func DetectHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	if idx := indexOf(h, '.'); idx > 0 {
		h = h[:idx]
	}
	return h
}

// DetectNebulaIP returns the Nebula tunnel IP (10.200.0.x) by inspecting
// network interfaces.
func DetectNebulaIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				// Try parsing CIDR string
				s := addr.String()
				if strings.HasPrefix(s, "10.200.0.") {
					return strings.Split(s, "/")[0]
				}
				continue
			}
			if ip.To4() != nil && strings.HasPrefix(ip.String(), "10.200.0.") {
				return ip.String()
			}
		}
	}

	// Fallback: try `nebula` CLI
	out, err := exec.Command("nebula", "list").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "10.200.0.") {
				for _, f := range strings.Fields(line) {
					if strings.HasPrefix(f, "10.200.0.") {
						return strings.Split(f, "/")[0]
					}
				}
			}
		}
	}

	return ""
}

// DetectLanIP returns the LAN IP by finding a non-loopback IPv4 address
// that isn't in the Nebula or Docker ranges.
func DetectLanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil || ip.IsLoopback() {
				continue
			}
			s := ip.String()
			if strings.HasPrefix(s, "10.200.") || strings.HasPrefix(s, "172.1") {
				continue
			}
			return s
		}
	}
	return ""
}

// detectOSLabel returns a label for the current OS
func detectOSLabel() string {
	out, _ := exec.Command("uname", "-s").Output()
	os := strings.TrimSpace(strings.ToLower(string(out)))
	switch os {
	case "linux":
		return "linux"
	case "darwin":
		return "macos"
	default:
		return os
	}
}

// autoRegister creates a node config file and adds the node to mesh.yaml.
// This is the decentralized self-registration: a new node discovers its
// own network identity and writes it into the mesh.
func autoRegister(repoDir, hostname string) (*NodeConfig, error) {
	nebulaIP := DetectNebulaIP()
	lanIP := DetectLanIP()
	osLabel := detectOSLabel()

	cfg := &NodeConfig{
		Hostname: hostname,
		NebulaIP: nebulaIP,
		LanIP:    lanIP,
		Labels:   []string{"compute", osLabel},
	}

	// 1. Write nodes/<hostname>.yaml
	nodesDir := filepath.Join(repoDir, "nodes")
	os.MkdirAll(nodesDir, 0755)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal node config: %w", err)
	}
	nodePath := filepath.Join(nodesDir, hostname+".yaml")
	if err := os.WriteFile(nodePath, data, 0644); err != nil {
		return nil, fmt.Errorf("write node config: %w", err)
	}

	// 2. Add self to mesh.yaml
	registerToMesh(repoDir, hostname, nebulaIP, lanIP, cfg.Labels)

	logPrefix := "auto-registered"
	if nebulaIP != "" {
		logPrefix = fmt.Sprintf("auto-registered (nebula=%s", nebulaIP)
		if lanIP != "" {
			logPrefix += fmt.Sprintf(", lan=%s", lanIP)
		}
		logPrefix += ")"
	}
	fmt.Fprintf(os.Stderr, "[gogitops] %s as %s\n", logPrefix, hostname)

	return cfg, nil
}

// registerToMesh adds or updates a peer entry in mesh.yaml
func registerToMesh(repoDir, hostname, nebulaIP, lanIP string, labels []string) {
	meshPath := filepath.Join(repoDir, "mesh.yaml")
	data, err := os.ReadFile(meshPath)
	if err != nil {
		return
	}

	var mesh MeshConfig
	if err := yaml.Unmarshal(data, &mesh); err != nil {
		return
	}

	// Check if already registered
	for i, p := range mesh.Peers {
		if p.Hostname == hostname {
			// Update existing entry with detected IPs
			if nebulaIP != "" {
				mesh.Peers[i].NebulaIP = nebulaIP
			}
			if lanIP != "" {
				mesh.Peers[i].LanIP = lanIP
			}
			if len(labels) > 0 {
				mesh.Peers[i].Labels = labels
			}
			if out, err := yaml.Marshal(&mesh); err == nil {
				os.WriteFile(meshPath, out, 0644)
			}
			return
		}
	}

	// Not found — add new peer
	peer := Peer{
		Hostname: hostname,
		NebulaIP: nebulaIP,
		LanIP:    lanIP,
		Port:     7780,
		Labels:   labels,
	}
	mesh.Peers = append(mesh.Peers, peer)

	if out, err := yaml.Marshal(&mesh); err == nil {
		os.WriteFile(meshPath, out, 0644)
	}
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
