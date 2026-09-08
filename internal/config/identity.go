package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── MAC-based machine identity ──────────────────────────────────────────────
//
// A machine's identity is the SET of its physical interface MACs (wifi +
// ethernet + any other real NICs) — stable across IP changes, DHCP churn and
// hostname changes. LAN IPs are NOT identity: they move with the network.
//
// autoRegister probes the local MACs and checks them against every mesh peer.
// Any overlap = known machine (possibly checking in under a different name,
// e.g. the system hostname instead of the fleet name) → the existing entry is
// refreshed (IPs + MAC union) and NO duplicate is created. First registration
// wins the hostname, so accidental identities can't rename a fleet node.
// No overlap = genuinely new machine → normal registration, now recording
// its MACs so future check-ins match it.

// macSkipPrefixes are virtual interface name prefixes whose MACs are
// random-per-boot or meaningless for identity (containers, bridges, VPNs).
var macSkipPrefixes = []string{
	"lo", "docker", "veth", "br-", "virbr", "bridge", "vEthernet",
	"tap", "tun", "utun", "wg", "awdl", "llw", "nebula", "tailscale",
	"ZeroTier", "zt", "ham", "anpi", "bridge", "gpd-", "rmnet",
}

// DetectMACs returns the sorted hardware addresses of all physical network
// interfaces (loopback + virtual filtered, empty/zero MACs skipped).
func DetectMACs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var macs []string
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		mac := i.HardwareAddr.String()
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		skip := false
		for _, p := range macSkipPrefixes {
			if strings.HasPrefix(i.Name, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if !seen[mac] {
			seen[mac] = true
			macs = append(macs, mac)
		}
	}
	sort.Strings(macs)
	return macs
}

// macOverlap reports whether any MAC appears in both sets.
func macOverlap(a, b []string) bool {
	set := map[string]bool{}
	for _, m := range a {
		set[m] = true
	}
	for _, m := range b {
		if set[m] {
			return true
		}
	}
	return false
}

// unionMACs merges two MAC sets (deduped, sorted).
func unionMACs(a, b []string) []string {
	seen := map[string]bool{}
	out := append([]string{}, a...)
	for _, m := range a {
		seen[m] = true
	}
	for _, m := range b {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// autoRegisterWith is the testable core of autoRegister: macs are injected so
// tests don't depend on the host machine's real interfaces.
func autoRegisterWith(repoDir, hostname, nebulaIP, lanIP, osLabel string, macs []string) (*NodeConfig, error) {
	// MAC identity guard: known machine → merge into its existing entry.
	if macs != nil {
		if peerHostname, peerLabels, ok := findPeerByMAC(repoDir, macs); ok {
			mergeIntoExistingPeer(repoDir, peerHostname, nebulaIP, lanIP, macs)
			// Return the machine's REAL config: its existing node yaml when
			// present, else a config synthesized from the peer entry.
			if cfg, err := LoadNodeStrict(repoDir, peerHostname); err == nil {
				fmt.Fprintf(os.Stderr, "[gogitops] %s is a known machine (%s) — mesh entry refreshed, no duplicate created\n", hostname, peerHostname)
				return cfg, nil
			}
			fmt.Fprintf(os.Stderr, "[gogitops] %s is a known machine (%s) — mesh entry refreshed, no duplicate created\n", hostname, peerHostname)
			return &NodeConfig{
				Hostname: peerHostname,
				NebulaIP: nonEmpty(nebulaIP, peerLabels.nebula),
				LanIP:    nonEmpty(lanIP, peerLabels.lan),
				Labels:   peerLabels.labels,
			}, nil
		}
	}

	cfg := &NodeConfig{
		Hostname: hostname,
		NebulaIP: nebulaIP,
		LanIP:    lanIP,
		Labels:   []string{"compute", osLabel},
		Macs:     macs,
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

	// 2. Add self to mesh.yaml (with MACs)
	registerToMesh(repoDir, hostname, nebulaIP, lanIP, cfg.Labels, macs)

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

type peerFacts struct {
	nebula, lan string
	labels      []string
}

// findPeerByMAC scans mesh.yaml for a peer sharing any MAC with macs.
// Returns the matched peer's hostname + its recorded facts.
func findPeerByMAC(repoDir string, macs []string) (string, peerFacts, bool) {
	mesh, err := LoadMesh(repoDir)
	if err != nil {
		return "", peerFacts{}, false
	}
	for _, p := range mesh.Peers {
		if macOverlap(macs, p.Macs) {
			return p.Hostname, peerFacts{nebula: p.NebulaIP, lan: p.LanIP, labels: p.Labels}, true
		}
	}
	return "", peerFacts{}, false
}

// mergeIntoExistingPeer refreshes a known machine's mesh entry: fresh IPs,
// MAC union. Hostname and labels are NOT touched (first registration wins
// the name; hand-curated labels are never clobbered by generic ones).
func mergeIntoExistingPeer(repoDir, peerHostname, nebulaIP, lanIP string, macs []string) {
	meshPath := filepath.Join(repoDir, "mesh.yaml")
	data, err := os.ReadFile(meshPath)
	if err != nil {
		return
	}
	var mesh MeshConfig
	if err := yaml.Unmarshal(data, &mesh); err != nil {
		return
	}
	for i, p := range mesh.Peers {
		if p.Hostname == peerHostname {
			if nebulaIP != "" {
				mesh.Peers[i].NebulaIP = nebulaIP
			}
			if lanIP != "" {
				mesh.Peers[i].LanIP = lanIP
			}
			mesh.Peers[i].Macs = unionMACs(p.Macs, macs)
			break
		}
	}
	if out, err := yaml.Marshal(&mesh); err == nil {
		os.WriteFile(meshPath, out, 0644)
	}
}

func nonEmpty(val, fallback string) string {
	if val != "" {
		return val
	}
	return fallback
}