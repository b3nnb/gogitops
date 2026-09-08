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
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── Machine identity: machine-id primary, MACs corroboration ────────────────
//
// Identity hierarchy:
//
//  1. machine_id — per-OS-install stable ID (/etc/machine-id on Linux,
//     IOPlatformUUID on macOS). Cannot move between machines. THE key.
//  2. macs — BUILT-IN physical interface MACs. Corroborates when machine-id
//     is missing (legacy entries), unions over time as adapters change.
//  3. portable_macs — USB adapters / docks / known-shared dongles. Recorded
//     for inventory, EXCLUDED from identity matching: a portable MAC can be
//     plugged into a different machine without fusing identities. USB NICs
//     are auto-detected via sysfs (Linux); thunderbolt docks present as PCI
//     and need a manual `portable_macs:` entry in the node yaml.
//
// Match rules (check-in vs existing peers):
//   - machine-ids equal            → same machine: merge, union MACs.
//   - machine-ids differ, identity-MAC overlaps → two machines sharing a
//     built-in MAC (hardware reuse / spoofing) — NEVER merge; warn loudly.
//   - peer has no machine-id (legacy) + identity-MAC overlap → merge and
//     backfill the machine-id (first upgrade wins).
//   - portable-MAC overlap only     → invisible to matching by design.
//   - no signal matches            → new machine: register with all ids.
//
// Mesh writes are byte-stable: unmarshal → marshal → compare → write only on
// real change. The daemon git-pulls this same repo, so a formatting-churn
// write would dirty the tree and break its own pull.

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
	"ZeroTier", "zt", "ham", "anpi", "bridge", "gpd-", "rmnet", "ap",
}

// NICIdentity is a machine's split interface inventory.
type NICIdentity struct {
	Macs      []string // built-in / identity-grade MACs
	Portable  []string // USB adapters + docks + declared-shared: recorded, never matched
}

// DetectNICs enumerates physical interfaces and splits them into identity
// vs portable MACs. Linux: portable = USB-attached (sysfs device path
// contains "usb"). Darwin: built-ins take the lowest enN indices — en0/en1
// are identity, higher enN are adapters or system-virtual shadows (ANPI
// holders, bridge members) and are skipped; macOS adapters rely on the
// machine-id match instead (macOS always has an IOPlatformUUID).
func DetectNICs() NICIdentity {
	ifaces, err := net.Interfaces()
	if err != nil {
		return NICIdentity{}
	}
	var id NICIdentity
	seenI, seenP := map[string]bool{}, map[string]bool{}
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
		if runtime.GOOS == "darwin" && strings.HasPrefix(i.Name, "en") {
			if n, err := strconv.Atoi(i.Name[2:]); err == nil && n > 1 {
				// higher enN: adapter or system-virtual shadow — skip
				continue
			}
		}
		if isUSBInterface(i.Name) {
			if !seenP[mac] {
				seenP[mac] = true
				id.Portable = append(id.Portable, mac)
			}
		} else if !seenI[mac] {
			seenI[mac] = true
			id.Macs = append(id.Macs, mac)
		}
	}
	sort.Strings(id.Macs)
	sort.Strings(id.Portable)
	return id
}

// isUSBInterface reports whether the interface is USB-attached (portable by
// definition — the adapter moves with the dongle). Best-effort sysfs check,
// Linux only; false on other platforms.
func isUSBInterface(name string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	link, err := os.Readlink(filepath.Join("/sys/class/net", name, "device"))
	if err != nil {
		return false
	}
	return strings.Contains(link, "usb")
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

// subtractMACs returns the MACs of a that are not in b.
func subtractMACs(a, b []string) []string {
	remove := map[string]bool{}
	for _, m := range b {
		remove[m] = true
	}
	out := append([]string{}, a...)
	n := 0
	for _, m := range out {
		if !remove[m] {
			out[n] = m
			n++
		}
	}
	out = out[:n]
	sort.Strings(out)
	return out
}

// autoRegisterWith is the testable core of autoRegister. nic + machineID are
// injected so tests don't depend on the host machine's real state.
func autoRegisterWith(repoDir, hostname, nebulaIP, lanIP, osLabel string, nic NICIdentity, machineID string) (*NodeConfig, error) {
	if nic.Macs != nil || nic.Portable != nil || machineID != "" {
		match, conflicts := findPeerByIdentity(repoDir, nic, machineID)
		if match.hostname != "" {
			mergeIntoExistingPeer(repoDir, match.hostname, nebulaIP, lanIP, nic, machineID)
			if cfg, err := LoadNodeStrict(repoDir, match.hostname); err == nil {
				fmt.Fprintf(os.Stderr, "[gogitops] %s is a known machine (%s) — mesh entry refreshed, no duplicate created\n", hostname, match.hostname)
				return cfg, nil
			}
			fmt.Fprintf(os.Stderr, "[gogitops] %s is a known machine (%s) — mesh entry refreshed, no duplicate created\n", hostname, match.hostname)
			return &NodeConfig{
				Hostname:    match.hostname,
				NebulaIP:    nonEmpty(nebulaIP, match.facts.nebula),
				LanIP:       nonEmpty(lanIP, match.facts.lan),
				MachineID:   machineID,
				PortableMacs: nic.Portable,
				Labels:      match.facts.labels,
			}, nil
		}
		// Conflicts (machine-ids differ but BUILT-IN MACs overlap) must NOT
		// merge — surface them so a human can investigate.
		for _, c := range conflicts {
			fmt.Fprintf(os.Stderr, "[gogitops] WARNING: %s shares built-in MAC %s with %s but machine-ids differ — hardware reuse or spoofing? NOT merging; registering separately.\n",
				hostname, c.mac, c.peerHostname)
		}
	}

	cfg := &NodeConfig{
		Hostname:     hostname,
		NebulaIP:    nebulaIP,
		LanIP:       lanIP,
		Labels:      []string{"compute", osLabel},
		MachineID:   machineID,
		Macs:        nic.Macs,
		PortableMacs: nic.Portable,
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

	// 2. Add self to mesh.yaml (with identity + portable MACs + machine-id)
	registerToMesh(repoDir, hostname, nebulaIP, lanIP, cfg.Labels, nic, machineID)

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

// SyncSelfToMesh refreshes a KNOWN machine's own mesh entry at daemon
// startup: detected IPs, identity MAC union, portable MAC union (including
// the node yaml's declared portable_macs — the manual override for
// thunderbolt docks etc.), machine-id backfill. Byte-stable: writes only
// when content actually changes (the daemon git-pulls this repo — a churn
// write would dirty the tree and break its own pull).
func SyncSelfToMesh(repoDir, hostname, nebulaIP, lanIP string, nic NICIdentity, machineID string) {
	meshPath := filepath.Join(repoDir, "mesh.yaml")
	data, err := os.ReadFile(meshPath)
	if err != nil {
		return
	}
	var mesh MeshConfig
	if err := yaml.Unmarshal(data, &mesh); err != nil {
		return
	}
	changed := false
	for i, p := range mesh.Peers {
		if p.Hostname != hostname {
			continue
		}
		// manual portable declarations win over sysfs: a MAC declared
		// portable in the node yaml leaves the identity set
		portable := unionMACs(p.PortableMacs, nic.Portable)
		identity := subtractMACs(unionMACs(p.Macs, nic.Macs), portable)
		if len(identity) != len(p.Macs) || !macEqual(identity, p.Macs) {
			mesh.Peers[i].Macs = identity
			changed = true
		}
		if !macEqual(portable, p.PortableMacs) {
			mesh.Peers[i].PortableMacs = portable
			changed = true
		}
		if machineID != "" && p.MachineID == "" {
			mesh.Peers[i].MachineID = machineID
			changed = true
		}
		if nebulaIP != "" && p.NebulaIP != nebulaIP {
			mesh.Peers[i].NebulaIP = nebulaIP
			changed = true
		}
		if lanIP != "" && p.LanIP != lanIP {
			mesh.Peers[i].LanIP = lanIP
			changed = true
		}
		break
	}
	if !changed {
		return
	}
	if out, err := yaml.Marshal(&mesh); err == nil && string(out) != string(data) {
		if werr := os.WriteFile(meshPath, out, 0644); werr == nil {
			for _, p := range mesh.Peers {
				if p.Hostname == hostname {
					fmt.Fprintf(os.Stderr, "[gogitops] mesh entry for %s refreshed (identity MACs=%d portable MACs=%d)\n", hostname, len(p.Macs), len(p.PortableMacs))
					break
				}
			}
		}
	}
}

func macEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// findPeerByIdentity resolves a check-in against existing mesh peers.
// Portable MACs are invisible on BOTH sides — a shared dongle can never
// fuse identities. Returns the matched peer (if any) + machine-id conflicts.
func findPeerByIdentity(repoDir string, nic NICIdentity, machineID string) (identityMatch, []identityConflict) {
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

	// Pass 2: built-in MACs only. A peer with a DIFFERENT machine-id is a
	// different machine no matter what MACs are shared.
	for _, p := range mesh.Peers {
		if macOverlap(nic.Macs, p.Macs) {
			if machineID != "" && p.MachineID != "" && p.MachineID != machineID {
				for _, m := range p.Macs {
					if containsMAC(nic.Macs, m) {
						conflicts = append(conflicts, identityConflict{mac: m, peerHostname: p.Hostname})
					}
				}
				continue
			}
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
// identity + portable MAC unions, machine-id backfill. Hostname and labels
// are NOT touched. Byte-stable write.
func mergeIntoExistingPeer(repoDir, peerHostname, nebulaIP, lanIP string, nic NICIdentity, machineID string) {
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
			portable := unionMACs(p.PortableMacs, nic.Portable)
			mesh.Peers[i].PortableMacs = portable
			mesh.Peers[i].Macs = subtractMACs(unionMACs(p.Macs, nic.Macs), portable)
			if machineID != "" && p.MachineID == "" {
				mesh.Peers[i].MachineID = machineID
			}
			break
		}
	}
	if out, err := yaml.Marshal(&mesh); err == nil && string(out) != string(data) {
		os.WriteFile(meshPath, out, 0644)
	}
}

func nonEmpty(val, fallback string) string {
	if val != "" {
		return val
	}
	return fallback
}