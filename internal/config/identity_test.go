package config

import (
	"os"
	"path/filepath"
	"testing"
)

// identity_test covers the machine identity hierarchy: machine-id primary,
// MACs corroboration. MACs + machine-id are injected so tests don't depend
// on the host's real state (CI-safe).

const testMesh = `peers:
    - hostname: friday
      nebula_ip: 10.200.0.4
      lan_ip: 10.2.0.102
      port: 7780
      machine_id: mid-friday-0001
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
      machine_id: mid-mini-0002
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

// A machine checking in under its SYSTEM hostname (e.g. i-wanna-be-a-mac)
// with the same machine-id as a known peer must: return the existing node's
// config, create NO junk node yaml, add NO mesh peer, refresh the peer's
// IPs, and union the MACs.
func TestMergeKnownMachine(t *testing.T) {
	repo := setupRepo(t)

	freshMACs := []string{"aa:bb:cc:dd:ee:02", "ff:00:11:22:33:44"} // eth matches friday, new wifi
	cfg, err := autoRegisterWith(repo, "i-wanna-be-a-mac", "10.200.0.4", "10.2.0.102", "linux", freshMACs, "mid-friday-0001")
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
	if len(mesh.Peers) != 3 {
		t.Errorf("known machine must not add a mesh peer — want 3 peers, got %d", len(mesh.Peers))
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

// THE USB ADAPTER CASE: a machine whose machine-id matches NO peer shares a
// MAC with a peer that HAS a machine-id (adapter moved, or duplicate
// adapter MAC). Must NOT merge — registers separately so two real machines
// never fuse.
func TestUSBAdapterMovedBetweenMachines(t *testing.T) {
	repo := setupRepo(t)

	// framework plugs in friday's old USB dongle
	dongleMAC := []string{"aa:bb:cc:dd:ee:01", "11:22:33:44:55:01"}
	cfg, err := autoRegisterWith(repo, "framework", "", "10.0.0.229", "linux", dongleMAC, "mid-framework-0003")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "framework" {
		t.Errorf("adapter-sharing machine must register as ITSELF, got %q", cfg.Hostname)
	}

	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 4 {
		t.Errorf("adapter-sharing machine registers separately — want 4 peers, got %d", len(mesh.Peers))
	}
	for _, p := range mesh.Peers {
		if p.Hostname == "friday" && p.MachineID != "mid-friday-0001" {
			t.Errorf("friday's identity must be untouched, got machine_id %q", p.MachineID)
		}
		if p.Hostname == "framework" && p.MachineID != "mid-framework-0003" {
			t.Errorf("framework must carry its own machine-id, got %q", p.MachineID)
		}
	}
}

// A machine ADDING its own adapter (same machine-id): unions into its own
// entry — the dongle MAC becomes part of its set, harmlessly.
func TestAdapterAddedToOwnMachine(t *testing.T) {
	repo := setupRepo(t)

	cfg, err := autoRegisterWith(repo, "mini", "", "10.0.0.251", "darwin", []string{"aa:bb:cc:dd:ee:03", "aa:bb:cc:dd:ee:01"}, "mid-mini-0002")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "mini" {
		t.Errorf("machine adding an adapter stays itself, got %q", cfg.Hostname)
	}
	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 3 {
		t.Errorf("no new peer expected — want 3, got %d", len(mesh.Peers))
	}
	for _, p := range mesh.Peers {
		if p.Hostname == "mini" {
			if len(p.Macs) != 2 {
				t.Errorf("mini should hold its own MAC + the dongle MAC, got %v", p.Macs)
			}
		}
		if p.Hostname == "friday" {
			if len(p.Macs) != 2 {
				t.Errorf("friday must be untouched by mini's adapter, got %v", p.Macs)
			}
		}
	}
}

// Legacy peer entry (no machine_id) + MAC overlap → merge, and the merge
// backfills the machine-id (first upgrade wins).
func TestLegacyPeerBackfillsMachineID(t *testing.T) {
	repo := setupRepo(t)

	cfg, err := autoRegisterWith(repo, "legacybox-renamed", "", "10.0.0.77", "linux", []string{"aa:bb:cc:dd:ee:09"}, "mid-legacy-0009")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "legacybox" {
		t.Errorf("legacy entry match by MAC should merge into legacybox, got %q", cfg.Hostname)
	}
	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 3 {
		t.Errorf("legacy merge must not add a peer — want 3, got %d", len(mesh.Peers))
	}
	for _, p := range mesh.Peers {
		if p.Hostname == "legacybox" && p.MachineID != "mid-legacy-0009" {
			t.Errorf("machine-id must be backfilled into legacy entry, got %q", p.MachineID)
		}
	}
}

// A genuinely new machine (no MAC overlap, no machine-id match) registers
// normally: node yaml + new mesh peer carrying both ids.
func TestNewMachineRegisters(t *testing.T) {
	repo := setupRepo(t)

	newMACs := []string{"99:88:77:66:55:01", "99:88:77:66:55:02"}
	cfg, err := autoRegisterWith(repo, "newbox", "10.200.0.9", "10.2.0.109", "linux", newMACs, "mid-newbox-0004")
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
	if len(mesh.Peers) != 4 {
		t.Errorf("new machine adds a peer — want 4, got %d", len(mesh.Peers))
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
	if newPeer.MachineID != "mid-newbox-0004" {
		t.Errorf("new peer must record its machine-id, got %q", newPeer.MachineID)
	}
}

// Duplicate adapter MACs: two brand-new machines share a cheap dongle's MAC
// (both check in fresh). Second one must not fuse into the first.
func TestDuplicateAdapterMACs(t *testing.T) {
	repo := setupRepo(t)

	_, err := autoRegisterWith(repo, "boxA", "", "10.0.0.61", "linux", []string{"aa:bb:cc:dd:ee:01", "de:ad:be:ef:00:01"}, "mid-boxa-0011")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := autoRegisterWith(repo, "boxB", "", "10.0.0.62", "linux", []string{"aa:bb:cc:dd:ee:01", "de:ad:be:ef:00:02"}, "mid-boxb-0012")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "boxB" {
		t.Errorf("boxB must stay itself (machine-ids differ), got %q", cfg.Hostname)
	}
	mesh, err := LoadMesh(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Peers) != 5 { // 3 fixture + boxA + boxB
		t.Errorf("both boxes must exist separately — want 5 peers, got %d", len(mesh.Peers))
	}
}