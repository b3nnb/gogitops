package config

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── Machine identity: machine-id primary, MACs corroboration ────────────────
//
// Identity hierarchy:
//
//  1. machine_id — per-OS-install stable ID (/etc/machine-id on Linux,
//     IOPlatformUUID on macOS). Cannot move between machines. THE key.
//  2. macs — physical interface MAC set. Corroborates when machine-id is
//     missing (legacy entries), and unions over time as adapters change.
//
// Match rules (check-in vs existing peers):
//   - machine-ids equal            → same machine: merge, union MACs.
//   - machine-ids differ, MAC overlaps → two machines sharing a MAC — a USB
//     adapter that moved, or a cheap adapter with a duplicate burned-in MAC.
//     NEVER merge; emit a conflict warning naming both sides.
//   - peer has no machine-id (legacy) + MAC overlap → merge and backfill the
//     machine-id (first upgrade wins).
//   - no signal matches            → new machine: register with both ids.
//
// A machine's own new adapter (dongle plugged in) unions into its own entry
// via the machine-id merge — harmless. If that adapter later moves to another
// machine, the differing machine-ids block the wrong merge.

var machineIDRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]+)"`)

// DetectMachineID returns a per-install machine identifier. Linux:
// /etc/machine-id (fallback /var/lib/dbus/machine-id). macOS: platform UUID
// via ioreg. Empty string when unavailable (identity falls back to MACs).
func DetectMachineID() string {
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	if data, err := os.ReadFile("/var/lib/dbus/machine-id"); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err == nil {
			if m := machineIDRe.FindStringSubmatch(string(out)); m != nil {
				return strings.ToLower(m[1])
			}
		}
	}
	return ""
}

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

// autoRegisterWith is the testable core of autoRegister: macs + machineID
// are injected so tests don't depend on the host machine's real state.
func autoRegisterWith(repoDir, hostname, nebulaIP, lanIP, osLabel string, macs []string, machineID string) (*NodeConfig, error) {
	if macs != nil || machineID != "" {
		match, conflicts := findPeerByIdentity(repoDir, macs, machineID)
		if match.hostname != "" {
			mergeIntoExistingPeer(repoDir, match.hostname, nebulaIP, lanIP, macs, machineID)
			if cfg, err := LoadNodeStrict(repoDir, match.hostname); err == nil {
				fmt.Fprintf(os.Stderr, "[gogitops] %s is a known machine (%s) — mesh entry refreshed, no duplicate created\n", hostname, match.hostname)
				return cfg, nil
			}
			fmt.Fprintf(os.Stderr, "[gogitops] %s is a known machine (%s) — mesh entry refreshed, no duplicate created\n", hostname, match.hostname)
			return &NodeConfig{
				Hostname:  match.hostname,
				NebulaIP:  nonEmpty(nebulaIP, match.facts.nebula),
				LanIP:     nonEmpty(lanIP, match.facts.lan),
				MachineID: machineID,
				Labels:    match.facts.labels,
			}, nil
		}
		// Conflicts (machine-ids differ but MACs overlap) must NOT merge —
		// surface them so a human can see the adapter moved.
		for _, c := range conflicts {
			fmt.Fprintf(os.Stderr, "[gogitops] WARNING: %s shares MAC %s with %s but machine-ids differ — USB adapter moved between machines or duplicate adapter MAC? NOT merging; registering separately.\n",
				hostname, c.mac, c.peerHostname)
		}
	}

	cfg := &NodeConfig{
		Hostname:  hostname,
		NebulaIP:  nebulaIP,
		LanIP:     lanIP,
		Labels:    []string{"compute", osLabel},
		Macs:      macs,
		MachineID: machineID,
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

	// 2. Add self to mesh.yaml (with MACs + machine-id)
	registerToMesh(repoDir, hostname, nebulaIP, lanIP, cfg.Labels, macs, machineID)

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

type identityMatch struct {
	hostname string
	facts    peerFacts
}

type identityConflict struct {
	mac, peerHostname string
}

// findPeerByIdentity resolves a check-in against existing mesh peers using
// the identity hierarchy: machine-id first, MACs as corroboration/fallback.
// Returns the matched peer (if any) plus any machine-id conflicts seen.
func findPeerByIdentity(repoDir string, macs []string, machineID string) (identityMatch, []identityConflict) {
	mesh, err := LoadMesh(repoDir)
	if err != nil {
		return identityMatch{}, nil
	}
	var conflicts []identityConflict

	// Pass 1: machine-id — definitive when both sides have one.
	if machineID != "" {
		for _, p := range mesh.Peers {
			if p.MachineID == machineID {
				return identityMatch{hostname: p.Hostname, facts: peerFacts{nebula: p.NebulaIP, lan: p.LanIP, labels: p.Labels}}, nil
			}
		}
	}

	// Pass 2: MACs — but a peer with a DIFFERENT machine-id is a different
	// machine no matter what MACs are shared (moved/duplicated adapter).
	for _, p := range mesh.Peers {
		if macOverlap(macs, p.Macs) {
			if machineID != "" && p.MachineID != "" && p.MachineID != machineID {
				for _, m := range p.Macs {
					if containsMAC(macs, m) {
						conflicts = append(conflicts, identityConflict{mac: m, peerHostname: p.Hostname})
					}
				}
				continue
			}
			// Peer without a machine-id (legacy entry): MAC match merges and
			// the merge backfills the machine-id.
			return identityMatch{hostname: p.Hostname, facts: peerFacts{nebula: p.NebulaIP, lan: p.LanIP, labels: p.Labels}}, conflicts
		}
	}
	return identityMatch{}, conflicts
}

func containsMAC(macs []string, m string) bool {
	for _, x := range macs {
		if x == m {
			return true
		}
	}
	return false
}

// mergeIntoExistingPeer refreshes a known machine's mesh entry: fresh IPs,
// MAC union, machine-id backfill. Hostname and labels are NOT touched (first
// registration wins the name; hand-curated labels are never clobbered).
func mergeIntoExistingPeer(repoDir, peerHostname, nebulaIP, lanIP string, macs []string, machineID string) {
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
			if machineID != "" && p.MachineID == "" {
				mesh.Peers[i].MachineID = machineID
			}
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