// Package dashboard provides a SQLite-backed rolling status store
// for the GoGitOps status dashboard.
package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// NodeCheck represents a single health check record for a node.
type NodeCheck struct {
	NodeName     string
	DisplayIP    string
	Healthy      bool
	ServicesUp   int
	ServicesTotal int
	Version      string
	CheckedAt    time.Time
	ResponseMs   int
}

// Store wraps a SQLite database for persisting node health checks.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite database at dbPath and ensures
// the schema is ready. It also runs a cleanup of rows older than 24h.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode and reasonable pragmas
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Create schema
	schema := `
CREATE TABLE IF NOT EXISTS node_status (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_name TEXT NOT NULL,
	display_ip TEXT NOT NULL,
	healthy BOOLEAN,
	services_up INTEGER,
	services_total INTEGER,
	version TEXT,
	checked_at TIMESTAMPTZ NOT NULL,
	response_time_ms INTEGER
);
CREATE INDEX IF NOT EXISTS idx_node_checked
	ON node_status (node_name, checked_at DESC);

CREATE TABLE IF NOT EXISTS registered_nodes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_name TEXT NOT NULL UNIQUE,
	address TEXT NOT NULL,
	display_ip TEXT NOT NULL,
	registered_at TIMESTAMPTZ NOT NULL,
	last_seen TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// Migrate old nebula_ip column → display_ip if needed
	if _, err := db.Exec(`ALTER TABLE node_status RENAME COLUMN nebula_ip TO display_ip`); err != nil {
		// Already migrated or column doesn't exist — safe to ignore
	}

	s := &Store{db: db}
	if err := s.cleanup(context.Background()); err != nil {
		// Non-fatal: log but don't fail startup
		fmt.Printf("warning: initial cleanup failed: %v\n", err)
	}

	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// cleanup deletes rows older than 24 hours.
func (s *Store) cleanup(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM node_status WHERE checked_at < ?",
		time.Now().Add(-24*time.Hour),
	)
	return err
}

// RecordCheck inserts a new health check result and prunes old data.
func (s *Store) RecordCheck(ctx context.Context, nodeName, displayIP string, healthy bool, servicesUp, servicesTotal int, version string, responseMs int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO node_status (node_name, display_ip, healthy, services_up, services_total, version, checked_at, response_time_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nodeName, displayIP, healthy, servicesUp, servicesTotal, version, time.Now().UTC(), responseMs,
	)
	if err != nil {
		return fmt.Errorf("insert check: %w", err)
	}

	// Best-effort cleanup on each write
	_ = s.cleanup(ctx)
	return nil
}

// GetLatestChecks returns the most recent check for each node.
func (s *Store) GetLatestChecks(ctx context.Context) ([]NodeCheck, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_name, display_ip, healthy, services_up, services_total, version, checked_at, response_time_ms
		 FROM node_status
		 WHERE id IN (
		   SELECT MAX(id) FROM node_status GROUP BY node_name
		 )
		 ORDER BY node_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("query latest checks: %w", err)
	}
	defer rows.Close()

	var checks []NodeCheck
	for rows.Next() {
		var c NodeCheck
		var checkedAt string
		if err := rows.Scan(&c.NodeName, &c.DisplayIP, &c.Healthy, &c.ServicesUp, &c.ServicesTotal, &c.Version, &checkedAt, &c.ResponseMs); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		c.CheckedAt, _ = time.Parse(time.RFC3339, checkedAt)
		if c.CheckedAt.IsZero() {
			c.CheckedAt, _ = time.Parse("2006-01-02 15:04:05Z07:00", checkedAt)
		}
		checks = append(checks, c)
	}
	return checks, rows.Err()
}

// Get24hSummary returns the uptime percentage for a given node over the last 24 hours.
func (s *Store) Get24hSummary(ctx context.Context, nodeName string) (float64, error) {
	var total, healthy sql.NullInt64

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(CASE WHEN healthy = 1 THEN 1 ELSE 0 END)
		 FROM node_status
		 WHERE node_name = ? AND checked_at >= ?`,
		nodeName, time.Now().Add(-24*time.Hour).UTC(),
	).Scan(&total, &healthy)

	if err != nil {
		return 0, fmt.Errorf("query 24h summary: %w", err)
	}
	if !total.Valid || total.Int64 == 0 {
		return 0, nil // no data → 0%
	}
	h := healthy.Int64
	if !healthy.Valid {
		h = 0
	}
	return (float64(h) / float64(total.Int64)) * 100.0, nil
}

// RegisterNode upserts a node into the registered_nodes table.
// If the node already exists, it updates last_seen and address.
func (s *Store) RegisterNode(ctx context.Context, nodeName, address, displayIP string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registered_nodes (node_name, address, display_ip, registered_at, last_seen)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(node_name) DO UPDATE SET
		   address = excluded.address,
		   display_ip = excluded.display_ip,
		   last_seen = excluded.last_seen`,
		nodeName, address, displayIP, now, now,
	)
	if err != nil {
		return fmt.Errorf("register node: %w", err)
	}
	return nil
}

// RegisteredNode is a self-registered node entry.
type RegisteredNode struct {
	NodeName     string
	Address      string
	DisplayIP    string
	RegisteredAt time.Time
	LastSeen     time.Time
}

// GetRegisteredNodes returns all registered nodes.
func (s *Store) GetRegisteredNodes(ctx context.Context) ([]RegisteredNode, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_name, address, display_ip, registered_at, last_seen
		 FROM registered_nodes
		 ORDER BY node_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("query registered nodes: %w", err)
	}
	defer rows.Close()

	var nodes []RegisteredNode
	for rows.Next() {
		var n RegisteredNode
		var regAt, seenAt string
		if err := rows.Scan(&n.NodeName, &n.Address, &n.DisplayIP, &regAt, &seenAt); err != nil {
			return nil, fmt.Errorf("scan registered node: %w", err)
		}
		n.RegisteredAt, _ = time.Parse(time.RFC3339, regAt)
		n.LastSeen, _ = time.Parse(time.RFC3339, seenAt)
		if n.RegisteredAt.IsZero() {
			n.RegisteredAt, _ = time.Parse("2006-01-02 15:04:05Z07:00", regAt)
		}
		if n.LastSeen.IsZero() {
			n.LastSeen, _ = time.Parse("2006-01-02 15:04:05Z07:00", seenAt)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// GetSettings returns all key/value settings.
func (s *Store) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, fmt.Errorf("query settings: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting upserts a single setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, value)
	if err != nil {
		return fmt.Errorf("upsert setting %s: %w", key, err)
	}
	return nil
}
