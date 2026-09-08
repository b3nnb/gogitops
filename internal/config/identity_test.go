package config

import (
	"os"
	"path/filepath"
	"testing"
)

// identity_test covers the machine identity hierarchy: machine-id primary,
// built-in MACs corroboration, portable MACs (USB dongles/docks) invisible
// to matching. All inputs are injected so tests don't depend on the host's
// real state (CI-safe).

const testMesh = `peers:
    - hostname: friday
      machine_id: mid-friday-0001
      nebula_ip: 10.200.0.4
      lan_ip: 10.2.0.102
      port: 7780
      macs:
        - aa:bb:cc:dd:ee:01
        - aa:bb:cc:dd:ee:02
      portable_macs:
        - de:ad:be:ef:00:99
      labels:
        - primary-compute
        - gpu
    - hostname: mini
      machine_id: mid-mini-0002
      nebula_ip: ""
      lan_ip: 10.0.0.251
      port: 7780
      macs:
        - aa:bb:cc:dd:ee:03
      labels:
        - compute
        - llm-inference
    - hostname: legacybox
      nebula_ip: ""
      lan_ip: 10.0.0.77
      port: 7780
      macs:
        - aa:bb:cc:dd:ee:09
      labels:
        - compute
`

const fridayNode = `hostname: friday
nebula_ip: 10.200.0.4
lan_ip: 10.2.0.102
machine_id: mid-friday-0001
macs:
    - aa:bb:cc:dd:ee:01
    - aa:bb:cc:dd:ee:02
labels:
    - primary-compute
    - docker-host
    - gpu
`

func setupRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "nodes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mesh.yaml"), []byte(testMesh), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nodes", "friday.yaml"), []byte(fridayNode), 0644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func mustPeers(t *testing.T, repo string) []Peer {
	t.Helper()
	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	return mesh.Peers
}

func findPeer(peers []Peer, name string) *Peer {
	for i := range peers {
		if peers[i].Hostname == name {
			return &peers[i]
		}
	}
	return nil
}

// A machine checking in under its SYSTEM hostname with the same machine-id
// must: return the existing node's config, create NO junk node yaml, add NO
// mesh peer, refresh IPs, union identity MACs.
func TestMergeKnownMachine(t *testing.T) {
	repo := setupRepo(t)

	nic := NICIdentity{Macs: []string{"aa:bb:cc:dd:ee:02", "ff:00:11:22:33:44"}, Portable: []string{"de:ad:be:ef:00:99"}}
	cfg, err := autoRegisterWith(repo, "i-wanna-be-a-mac", "10.200.0.4", "10.2.0.102", "linux", nic, "mid-friday-0001")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Hostname != "friday" {
		t.Errorf("merged check-in should return the machine's real identity (friday), got %q", cfg.Hostname)
	}
	if _, err := os.Stat(filepath.Join(repo, "nodes", "i-wanna-be-a-mac.yaml")); !os.IsNotExist(err) {
		t.Error("no junk node yaml must be created for a known machine")
	}
	peers := mustPeers(t, repo)
	if len(peers) != 3 {
		t.Fatalf("known machine must not add a mesh peer — want 3, got %d", len(peers))
	}
	friday := findPeer(peers, "friday")
	if len(friday.Macs) != 3 {
		t.Errorf("identity MAC union failed: want 3, got %v", friday.Macs)
	}
	if len(friday.PortableMacs) != 1 {
		t.Errorf("portable MAC union failed: want 1, got %v", friday.PortableMacs)
	}
	if friday.Labels[0] == "compute" {
		t.Errorf("hand-curated labels must not be clobbered: %v", friday.Labels)
	}
}

// THE PORTABLE CASE: a machine whose ONLY shared MAC with another peer is a
// portable (USB dongle). Must match nothing — register as new, NO warning
// (portable MACs are invisible, not suspicious).
func TestPortableMACNeverMatches(t *testing.T) {
	repo := setupRepo(t)

	// newbox plugs in friday's USB dongle (de:ad:be:ef:00:99)
	nic := NICIdentity{Macs: []string{"99:88:77:66:55:01"}, Portable: []string{"de:ad:be:ef:00:99"}}
	cfg, err := autoRegisterWith(repo, "newbox", "", "10.0.0.61", "linux", nic, "mid-newbox-0061")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "newbox" {
		t.Errorf("portable MAC overlap must not merge — got %q", cfg.Hostname)
	}
	peers := mustPeers(t, repo)
	if len(peers) != 4 {
		t.Errorf("newbox registers itself — want 4 peers, got %d", len(peers))
	}
	nb := findPeer(peers, "newbox")
	if nb == nil || len(nb.PortableMacs) != 1 {
		t.Errorf("newbox must record the dongle as portable inventory: %+v", nb)
	}
	if nb != nil && len(nb.Macs) != 1 {
		t.Errorf("dongle must stay out of newbox's identity set: %v", nb.Macs)
	}
	friday := findPeer(peers, "friday")
	if friday.MachineID != "mid-friday-0001" || len(friday.Macs) != 2 {
		t.Errorf("friday untouched: %+v", friday)
	}
}

// A machine ADDING its own adapter: unions into its own entry via machine-id.
// The dongle lands in portable inventory, not the identity set.
func TestAdapterAddedToOwnMachine(t *testing.T) {
	repo := setupRepo(t)

	nic := NICIdentity{Macs: []string{"aa:bb:cc:dd:ee:03"}, Portable: []string{"aa:bb:cc:dd:ee:01"}}
	cfg, err := autoRegisterWith(repo, "mini", "", "10.0.0.251", "darwin", nic, "mid-mini-0002")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "mini" {
		t.Errorf("machine adding an adapter stays itself, got %q", cfg.Hostname)
	}
	peers := mustPeers(t, repo)
	if len(peers) != 3 {
		t.Fatalf("no new peer expected — want 3, got %d", len(peers))
	}
	mini := findPeer(peers, "mini")
	if len(mini.Macs) != 1 || len(mini.PortableMacs) != 1 {
		t.Errorf("mini: identity={%v} portable={%v} — dongle must be portable inventory", mini.Macs, mini.PortableMacs)
	}
}

// Built-in MAC shared but machine-ids differ → hardware reuse/suspect →
// NEVER merge, warn, register separately.
func TestBuiltInMACConflictNoMerge(t *testing.T) {
	repo := setupRepo(t)

	nic := NICIdentity{Macs: []string{"aa:bb:cc:dd:ee:01", "11:22:33:44:55:01"}}
	cfg, err := autoRegisterWith(repo, "framework", "", "10.0.0.229", "linux", nic, "mid-framework-0003")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "framework" {
		t.Errorf("identity-MAC sharing machine must register as ITSELF, got %q", cfg.Hostname)
	}
	peers := mustPeers(t, repo)
	if len(peers) != 4 {
		t.Errorf("registers separately — want 4 peers, got %d", len(peers))
	}
	if p := findPeer(peers, "friday"); p.MachineID != "mid-friday-0001" {
		t.Errorf("friday's identity must be untouched, got %q", p.MachineID)
	}
}

// Legacy peer entry (no machine_id) + identity-MAC overlap → merge, backfill
// the machine-id.
func TestLegacyPeerBackfillsMachineID(t *testing.T) {
	repo := setupRepo(t)

	nic := NICIdentity{Macs: []string{"aa:bb:cc:dd:ee:09"}}
	cfg, err := autoRegisterWith(repo, "legacybox-renamed", "", "10.0.0.77", "linux", nic, "mid-legacy-0009")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "legacybox" {
		t.Errorf("legacy MAC match should merge into legacybox, got %q", cfg.Hostname)
	}
	peers := mustPeers(t, repo)
	if len(peers) != 3 {
		t.Fatalf("legacy merge must not add a peer — want 3, got %d", len(peers))
	}
	if p := findPeer(peers, "legacybox"); p.MachineID != "mid-legacy-0009" {
		t.Errorf("machine-id must backfill into legacy entry, got %q", p.MachineID)
	}
}

// A genuinely new machine registers normally with both id types recorded.
func TestNewMachineRegisters(t *testing.T) {
	repo := setupRepo(t)

	nic := NICIdentity{Macs: []string{"99:88:77:66:55:01", "99:88:77:66:55:02"}, Portable: []string{"de:ad:be:ef:00:77"}}
	cfg, err := autoRegisterWith(repo, "newbox", "10.200.0.9", "10.2.0.109", "linux", nic, "mid-newbox-0004")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "newbox" {
		t.Errorf("new machine keeps its name, got %q", cfg.Hostname)
	}
	if _, err := os.Stat(filepath.Join(repo, "nodes", "newbox.yaml")); err != nil {
		t.Error("new machine must get its node yaml")
	}
	peers := mustPeers(t, repo)
	if len(peers) != 4 {
		t.Fatalf("new machine adds a peer — want 4, got %d", len(peers))
	}
	nb := findPeer(peers, "newbox")
	if len(nb.Macs) != 2 || len(nb.PortableMacs) != 1 || nb.MachineID != "mid-newbox-0004" {
		t.Errorf("new peer must record all ids: %+v", nb)
	}
}

// MANUAL DECLARATION: SyncSelfToMesh moves a node-yaml-declared portable MAC
// out of the peer's identity set into portable inventory — and stays
// byte-identical (no rewrite) when nothing changes.
func TestSyncSelfToMeshManualPortableAndNoChurn(t *testing.T) {
	repo := setupRepo(t)

	// friday declares its second NIC (actually a thunderbolt dock — sysfs
	// says PCI) as portable in the node yaml:
	nic := NICIdentity{Macs: []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "ff:00:11:22:33:44"}}
	nic.Portable = append(nic.Portable, "ff:00:11:22:33:44") // declared portable
	SyncSelfToMesh(repo, "friday", "10.200.0.4", "10.2.0.102", nic, "mid-friday-0001")

	peers := mustPeers(t, repo)
	friday := findPeer(peers, "friday")
	if len(friday.Macs) != 2 || containsMAC(friday.Macs, "ff:00:11:22:33:44") {
		t.Errorf("declared-portable MAC must leave the identity set: %v", friday.Macs)
	}
	if !containsMAC(friday.PortableMacs, "ff:00:11:22:33:44") {
		t.Errorf("declared-portable MAC must appear in portable inventory: %v", friday.PortableMacs)
	}

	// Second sync with same inputs = byte-identical → NO rewrite.
	before, _ := os.ReadFile(filepath.Join(repo, "mesh.yaml"))
	SyncSelfToMesh(repo, "friday", "10.200.0.4", "10.2.0.102", nic, "mid-friday-0001")
	after, _ := os.ReadFile(filepath.Join(repo, "mesh.yaml"))
	if string(before) != string(after) {
		t.Error("steady-state sync must be a no-op (byte-identical) — churn would break the agent's own git pull")
	}
}