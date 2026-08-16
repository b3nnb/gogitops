// Package config loads node, mesh, group, and stack configuration from the git repo.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// NodeConfig is the per-device configuration (nodes/<hostname>.yaml)
type NodeConfig struct {
	Hostname string   `yaml:"hostname"`
	NebulaIP string   `yaml:"nebula_ip"`
	LanIP    string   `yaml:"lan_ip"`
	Labels   []string `yaml:"labels"`
	Services []Service `yaml:"services"`
	Disk     []DiskCheck `yaml:"disk"`
	Agent    AgentConfig `yaml:"agent"`
}

// Address returns the best host:port to reach this node.
// Prefers LAN IP (local subnet), falls back to Nebula IP.
func (n NodeConfig) Address() string {
	host := ""
	if n.LanIP != "" && n.LanIP != "null" {
		host = n.LanIP
	} else if n.NebulaIP != "" && n.NebulaIP != "null" {
		host = n.NebulaIP
	}
	return host
}

// Service is a single service check definition
type Service struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"` // systemd | tcp | docker | docker-compose | process
	Unit        string   `yaml:"unit,omitempty"`        // for systemd
	User        bool     `yaml:"user,omitempty"`        // for systemd: user-level unit
	Port        int      `yaml:"port,omitempty"`        // for tcp
	Container   string   `yaml:"container,omitempty"`   // for docker
	Containers  []string `yaml:"containers,omitempty"`  // for docker-compose
	Path        string   `yaml:"path,omitempty"`        // for docker-compose
	Pattern     string   `yaml:"pattern,omitempty"`     // for process
	Check       string   `yaml:"check,omitempty"`       // for docker: daemon-alive | container-alive
	DockerBin   string   `yaml:"docker_binary,omitempty"` // custom docker path (NAS)
}

// DiskCheck defines a mount to monitor
type DiskCheck struct {
	Mount   string `yaml:"mount"`
	WarnPct int    `yaml:"warn_pct"`
	CritPct int    `yaml:"crit_pct"`
}

// AgentConfig is the agent's own behavior config
type AgentConfig struct {
	UpdateURL        string `yaml:"update_url"`
	CheckInterval    string `yaml:"check_interval"`
	GitRepo          string `yaml:"git_repo"`
	GitBranch        string `yaml:"git_branch"`
	GitPullInterval  string `yaml:"git_pull_interval"`
}

// MeshConfig is the peer list (mesh.yaml)
type MeshConfig struct {
	Peers []Peer `yaml:"peers"`
}

// Peer is a single mesh peer
type Peer struct {
	Hostname string   `yaml:"hostname"`
	NebulaIP string   `yaml:"nebula_ip"`
	LanIP    string   `yaml:"lan_ip"`
	Port     int      `yaml:"port"`
	Labels   []string `yaml:"labels"`
}

// Address returns the best host:port to reach this peer.
// Prefers LAN IP when set (local subnet), falls back to Nebula IP.
// Returns empty string if no reachable address exists.
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

// GroupConfig is a named collection (groups/*.yaml)
type GroupConfig struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Type        string       `yaml:"type"` // node-group | stack-group
	Members     []Member     `yaml:"members"`
	DependsOn   []Dependency `yaml:"depends_on,omitempty"`
	Config      GroupCfg     `yaml:"config"`
}

// Member is a group membership reference
type Member struct {
	Node  string `yaml:"node,omitempty"`
	Label string `yaml:"label,omitempty"`
	Stack string `yaml:"stack,omitempty"`
}

// Dependency is a group-to-group dependency
type Dependency struct {
	Group string `yaml:"group"`
}

// GroupCfg is group-level behavior
type GroupCfg struct {
	MinMembers      int    `yaml:"min_members"`
	CheckPort       int    `yaml:"check_port"`
	Coordinator     string `yaml:"coordinator"`
	MarketHoursOnly bool   `yaml:"market_hours_only"`
	AlertOnDown     string `yaml:"alert_on_down"`
}

// LoadNode loads a node config by hostname
func LoadNode(repoDir, hostname string) (*NodeConfig, error) {
	path := filepath.Join(repoDir, "nodes", hostname+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
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
			continue // skip malformed
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// DetectHostname returns the system hostname
func DetectHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	// strip domain suffix
	if idx := indexOf(h, '.'); idx > 0 {
		h = h[:idx]
	}
	return h
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
