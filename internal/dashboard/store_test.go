package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestStoreRoundTrip exercises the SQLite store end-to-end with the pure-Go
// driver: open, record checks, read latest, 24h summary, register nodes,
// settings. Guards against driver-swap regressions (mattn → modernc).
func TestStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "status.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record a few checks
	if err := store.RecordCheck(ctx, "friday", "10.2.0.102", true, 11, 12, "v0.7.27", 120); err != nil {
		t.Fatalf("RecordCheck: %v", err)
	}
	if err := store.RecordCheck(ctx, "mini", "10.0.0.251", true, 3, 3, "v0.7.27", 98); err != nil {
		t.Fatalf("RecordCheck: %v", err)
	}
	if err := store.RecordCheck(ctx, "friday", "10.2.0.102", true, 12, 12, "v0.7.27", 99); err != nil {
		t.Fatalf("RecordCheck: %v", err)
	}

	// Latest checks: one per node, most recent
	checks, err := store.GetLatestChecks(ctx)
	if err != nil {
		t.Fatalf("GetLatestChecks: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(checks))
	}
	byName := map[string]NodeCheck{}
	for _, c := range checks {
		byName[c.NodeName] = c
	}
	if byName["friday"].ServicesUp != 12 {
		t.Errorf("friday latest services_up = %d, want 12", byName["friday"].ServicesUp)
	}
	if byName["friday"].CheckedAt.IsZero() {
		t.Errorf("friday checked_at failed to parse: %v", byName["friday"].CheckedAt)
	}
	if time.Since(byName["mini"].CheckedAt) > time.Minute {
		t.Errorf("mini checked_at looks stale: %v", byName["mini"].CheckedAt)
	}

	// 24h summary: all healthy → 100%
	pct, err := store.Get24hSummary(ctx, "friday")
	if err != nil {
		t.Fatalf("Get24hSummary: %v", err)
	}
	if pct != 100.0 {
		t.Errorf("24h summary = %v, want 100", pct)
	}

	// Register + upsert
	if err := store.RegisterNode(ctx, "wednesday", "10.0.0.213:7780", "10.0.0.213"); err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	if err := store.RegisterNode(ctx, "wednesday", "10.0.0.213:7780", "10.0.0.213"); err != nil {
		t.Fatalf("RegisterNode upsert: %v", err)
	}
	nodes, err := store.GetRegisteredNodes(ctx)
	if err != nil {
		t.Fatalf("GetRegisteredNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeName != "wednesday" {
		t.Fatalf("registered nodes = %+v", nodes)
	}
	if nodes[0].RegisteredAt.IsZero() || nodes[0].LastSeen.IsZero() {
		t.Errorf("registered timestamps failed to parse: %+v", nodes[0])
	}

	// Settings upsert
	if err := store.SetSetting(ctx, "dash_url", "http://10.2.0.102:7781"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	settings, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings["dash_url"] != "http://10.2.0.102:7781" {
		t.Errorf("settings = %v", settings)
	}
}
