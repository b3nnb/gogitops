package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// First write must have self-migrated into the node's OWN file, and
	// the legacy mesh.yaml must be untouched.
	selfFile := filepath.Join(repo, "mesh.d", "friday.yaml")
	if _, err := os.Stat(selfFile); err != nil {
		t.Fatal("sync must write the node's own mesh.d/friday.yaml")
	}
	legacy, _ := os.ReadFile(filepath.Join(repo, "mesh.yaml"))

	// Second sync with same inputs = byte-identical → NO rewrite.
	before, _ := os.ReadFile(selfFile)
	SyncSelfToMesh(repo, "friday", "10.200.0.4", "10.2.0.102", nic, "mid-friday-0001")
	after, _ := os.ReadFile(selfFile)
	if string(before) != string(after) {
		t.Error("steady-state sync must be a no-op (byte-identical) — churn would break the agent's own git pull")
	}
	legacyAfter, _ := os.ReadFile(filepath.Join(repo, "mesh.yaml"))
	if string(legacy) != string(legacyAfter) {
		t.Error("legacy mesh.yaml must never be touched once mesh.d/ writes begin")
	}
}

// mesh.d/ union read: per-node files concatenate in sorted order; a legacy
// mesh.yaml still contributes hostnames that have no mesh.d/ file; on a
// hostname collision the mesh.d/ file wins.
func TestMeshDUnionAndPrecedence(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "mesh.d"), 0755)
	os.WriteFile(filepath.Join(repo, "mesh.d", "mini.yaml"),
		[]byte("peers:\n    - hostname: mini\n      machine_id: mid-mini-0002\n      nebula_ip: \"\"\n      lan_ip: 10.9.9.9\n      port: 7780\n      macs:\n        - aa:bb:cc:dd:ee:03\n"), 0644)
	os.WriteFile(filepath.Join(repo, "mesh.yaml"),
		[]byte("peers:\n    - hostname: mini\n      lan_ip: 10.0.0.251\n      port: 7780\n    - hostname: friday\n      lan_ip: 10.2.0.102\n      port: 7780\n"), 0644)

	peers := mustPeers(t, repo)
	if len(peers) != 2 {
		t.Fatalf("union read: mesh.d/ mini + legacy friday — want 2, got %d", len(peers))
	}
	if mini := findPeer(peers, "mini"); mini.LanIP != "10.9.9.9" {
		t.Errorf("mesh.d/ file must win on collision, got lan_ip %q", mini.LanIP)
	}
	if findPeer(peers, "friday") == nil {
		t.Error("legacy-only entries must still be visible")
	}
}

// A node with no entry anywhere (fresh standalone) stays untouched by
// SyncSelfToMesh — it registers via the auto-register path instead.
func TestSyncSelfNoEntryNoFile(t *testing.T) {
	repo := setupRepo(t)
	SyncSelfToMesh(repo, "stranger", "10.200.0.99", "10.0.0.99", NICIdentity{}, "mid-stranger-9999")
	if _, err := os.Stat(filepath.Join(repo, "mesh.d", "stranger.yaml")); !os.IsNotExist(err) {
		t.Error("sync must not invent an entry for an unknown node")
	}
	peers := mustPeers(t, repo)
	if len(peers) != 3 {
		t.Errorf("unknown node sync must not change the mesh — want 3, got %d", len(peers))
	}
}

// The submit half of the per-node mesh model: saveOwnMeshEntry must commit
// the node's OWN mesh.d/ file and push it to origin. Local bare remote —
// proves the full add/commit/push mechanics without touching the real repo.
func TestSubmitMeshFilePushes(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	remote := filepath.Join(dir, "remote.git")
	run := func(args ...string) (string, bool) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err == nil
	}
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"init", "-q", "--bare", remote},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if _, ok := run("remote", "add", "origin", remote); !ok {
		t.Fatal("remote add failed")
	}
	run("config", "user.email", "agent@test")
	run("config", "user.name", "agent-test")

	p := Peer{Hostname: "friday", NebulaIP: "10.200.0.4", LanIP: "10.2.0.102", Port: 7780,
		Macs: []string{"aa:bb:cc:dd:ee:01"}, Labels: []string{"primary-compute"}}
	saveOwnMeshEntry(repo, "friday", p)

	if _, err := os.Stat(filepath.Join(repo, "mesh.d", "friday.yaml")); err != nil {
		t.Fatal("own mesh.d file must exist")
	}
	if _, ok := run("log", "-1", "--oneline"); !ok {
		t.Fatal("submit must leave a commit on the branch")
	}
	// the push must have landed on the bare remote
	cmd := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "refs/heads/main")
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "refs/heads/master")
		if out2, err2 := cmd.CombinedOutput(); err2 != nil {
			t.Fatalf("push did not land on remote main/master: %v / %v (%s / %s)", err, err2, out, out2)
		}
	}
	nameOnly, _ := exec.Command("git", "--git-dir", remote, "ls-tree", "-r", "--name-only", "HEAD").CombinedOutput()
	if !strings.Contains(string(nameOnly), "mesh.d/friday.yaml") {
		t.Errorf("remote HEAD must contain the submitted file, got: %s", nameOnly)
	}

	// Steady state: identical content → no churn, no second commit.
	before, _ := run("rev-list", "--count", "HEAD")
	saveOwnMeshEntry(repo, "friday", p)
	after, _ := run("rev-list", "--count", "HEAD")
	if before != after {
		t.Errorf("steady-state write must not commit again (%s → %s)", before, after)
	}
}
