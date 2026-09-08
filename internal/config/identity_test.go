package config

import (
	"os"
	"path/filepath"
	"testing"
)

// identity_test covers the MAC-based machine identity guard: a machine
// checking in under a new name must merge into its known mesh entry, not
// create a duplicate. MACs are injected so tests don't depend on the host's
// real interfaces (CI-safe).

const testMesh = `peers:
    - hostname: friday
      nebula_ip: 10.200.0.4
      lan_ip: 10.2.0.102
      port: 7780
      macs:
        - aa:bb:cc:dd:ee:01
        - aa:bb:cc:dd:ee:02
      labels:
        - primary-compute
        - gpu
    - hostname: mini
      nebula_ip: ""
      lan_ip: 10.0.0.251
      port: 7780
      macs:
        - aa:bb:cc:dd:ee:03
      labels:
        - compute
        - llm-inference
`

const fridayNode = `hostname: friday
nebula_ip: 10.200.0.4
lan_ip: 10.2.0.102
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

// A machine checking in under its SYSTEM hostname (e.g. i-wanna-be-a-mac)
// with MACs matching a known peer must: return the existing node's config,
// create NO junk node yaml, add NO mesh peer, refresh the peer's IPs, and
// union the MACs.
func TestMACMergeKnownMachine(t *testing.T) {
	repo := setupRepo(t)

	freshMACs := []string{"aa:bb:cc:dd:ee:02", "ff:00:11:22:33:44"} // eth matches friday, new wifi
	cfg, err := autoRegisterWith(repo, "i-wanna-be-a-mac", "10.200.0.4", "10.2.0.102", "linux", freshMACs)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Hostname != "friday" {
		t.Errorf("merged check-in should return the machine's real identity (friday), got %q", cfg.Hostname)
	}
	if _, err := os.Stat(filepath.Join(repo, "nodes", "i-wanna-be-a-mac.yaml")); !os.IsNotExist(err) {
		t.Error("no junk node yaml must be created for a known machine")
	}

	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 2 {
		t.Errorf("known machine must not add a mesh peer — want 2 peers, got %d", len(mesh.Peers))
	}
	for _, p := range mesh.Peers {
		if p.Hostname == "friday" {
			wantMACs := []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "ff:00:11:22:33:44"}
			if len(p.Macs) != len(wantMACs) {
				t.Errorf("MAC union failed: want %v, got %v", wantMACs, p.Macs)
			}
			if len(p.Labels) == 2 && p.Labels[0] == "compute" {
				t.Errorf("hand-curated labels must not be clobbered by generic ones: %v", p.Labels)
			}
		}
	}
}

// A genuinely new machine (no MAC overlap) registers normally: node yaml +
// new mesh peer carrying its MACs.
func TestMACNewMachineRegisters(t *testing.T) {
	repo := setupRepo(t)

	newMACs := []string{"99:88:77:66:55:01", "99:88:77:66:55:02"}
	cfg, err := autoRegisterWith(repo, "newbox", "10.200.0.9", "10.2.0.109", "linux", newMACs)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "newbox" {
		t.Errorf("new machine keeps its name, got %q", cfg.Hostname)
	}
	if _, err := os.Stat(filepath.Join(repo, "nodes", "newbox.yaml")); err != nil {
		t.Error("new machine must get its node yaml")
	}

	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 3 {
		t.Errorf("new machine adds a peer — want 3, got %d", len(mesh.Peers))
	}
	var newPeer *Peer
	for i := range mesh.Peers {
		if mesh.Peers[i].Hostname == "newbox" {
			newPeer = &mesh.Peers[i]
		}
	}
	if newPeer == nil {
		t.Fatal("newbox peer missing from mesh")
	}
	if len(newPeer.Macs) != 2 {
		t.Errorf("new peer must record its MACs, got %v", newPeer.Macs)
	}
}

// wifi-only overlap still identifies the machine (fuzzy set match).
func TestMACWifiOnlyOverlap(t *testing.T) {
	repo := setupRepo(t)
	_, err := autoRegisterWith(repo, "macbook-air", "", "10.0.0.99", "darwin", []string{"aa:bb:cc:dd:ee:03"})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 2 {
		t.Errorf("wifi MAC alone must match the known machine — want 2 peers, got %d", len(mesh.Peers))
	}
}
