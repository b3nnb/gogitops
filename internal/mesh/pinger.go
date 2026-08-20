// Package mesh implements peer discovery and health pinging over Nebula.
package mesh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bennbanks/gogitops/internal/config"
)

// PeerHealth is the health payload each agent serves
type PeerHealth struct {
	Hostname       string            `json:"hostname"`
	AgentVersion   string            `json:"agent_version"`
	UptimeSeconds  int64             `json:"uptime_seconds"`
	NebulaRunning  bool              `json:"nebula_running"`
	NebulaIP       string            `json:"nebula_ip,omitempty"`
	Labels         []string          `json:"labels"`
	Services       map[string]string `json:"services"`
	DiskWarns      []string          `json:"disk_warns,omitempty"`
	PeersReachable []string          `json:"peers_reachable"`
	PeersUnreach   []string          `json:"peers_unreachable,omitempty"`
	LastGitPull    string            `json:"last_git_pull,omitempty"`
	ConfigHash     string            `json:"config_hash,omitempty"`
	System         SystemInfo        `json:"system"`
}

// SystemInfo is lightweight runtime system metadata
type SystemInfo struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	IP      string `json:"ip"`
	HostID  string `json:"host_id"`
}

// Pinger manages peer health checks
type Pinger struct {
	mesh   *config.MeshConfig
	client *http.Client
}

// NewPinger creates a peer pinger
func NewPinger(mesh *config.MeshConfig) *Pinger {
	return &Pinger{
		mesh: mesh,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// PingAll pings all peers (except self) and returns reachability map
func (p *Pinger) PingAll(selfHostname string) (reachable []string, unreachable []string) {
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, peer := range p.mesh.Peers {
		if peer.Hostname == selfHostname {
			continue
		}
		wg.Add(1)
		go func(peer config.Peer) {
			defer wg.Done()
			ok := p.PingPeer(peer)
			mu.Lock()
			if ok {
				reachable = append(reachable, peer.Hostname)
			} else {
				unreachable = append(unreachable, peer.Hostname)
			}
			mu.Unlock()
		}(peer)
	}
	wg.Wait()
	return reachable, unreachable
}

// PingPeer checks a single peer's health endpoint.
// Uses LAN IP preferentially, falls back to Nebula IP.
func (p *Pinger) PingPeer(peer config.Peer) bool {
	addr := peer.Address()
	if addr == "" {
		return false
	}
	url := fmt.Sprintf("http://%s/v1/health", addr)
	resp, err := p.client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// FetchPeerHealth gets the full health payload from a peer.
// Uses LAN IP preferentially, falls back to Nebula IP.
func (p *Pinger) FetchPeerHealth(peer config.Peer) (*PeerHealth, error) {
	addr := peer.Address()
	if addr == "" {
		return nil, fmt.Errorf("peer %s has no reachable address (neither lan_ip nor nebula_ip set)", peer.Hostname)
	}
	url := fmt.Sprintf("http://%s/v1/health", addr)
	resp, err := p.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("peer %s returned %d", peer.Hostname, resp.StatusCode)
	}
	var h PeerHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}
