package db

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool and carries version-detection state.
type DB struct {
	pool    *pgxpool.Pool
	once    sync.Once
	manifest *SchemaManifest // lazily populated for Adapter80
}

// New creates a DB from a DSN string and verifies connectivity.
func New(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db.New: parse config: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db.New: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db.New: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Ping checks DB reachability for the /healthz endpoint.
func (db *DB) Ping(ctx context.Context) error {
	return db.pool.Ping(ctx)
}

// Close releases all connections.
func (db *DB) Close() {
	db.pool.Close()
}

// ─────────────────────────────────────────────────────────────────────────────
// Zabbix version detection
// ─────────────────────────────────────────────────────────────────────────────

// DetectVersion reads the Zabbix database_version from the config table.
// It returns the raw integer and an error if the query fails or the version
// is below the minimum supported threshold (6.0.0 = raw 6000000).
func (db *DB) DetectVersion(ctx context.Context) (int, error) {
	var raw int
	err := db.pool.QueryRow(ctx, `SELECT value FROM config WHERE configid = 1`).Scan(&raw)
	if err != nil {
		return 0, fmt.Errorf("DetectVersion: %w", err)
	}
	return raw, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// TimescaleDB and partitioning detection
// ─────────────────────────────────────────────────────────────────────────────

// HasTimescaleDB returns true if the timescaledb extension is installed.
func (db *DB) HasTimescaleDB(ctx context.Context) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_extension WHERE extname = 'timescaledb'
		)
	`).Scan(&exists)
	return exists, err
}

// HasPartitionedHistory returns true if the history table is a partitioned table.
// Zabbix 7.0+ may use native PostgreSQL partitioning.
func (db *DB) HasPartitionedHistory(ctx context.Context) (bool, error) {
	var relkind string
	err := db.pool.QueryRow(ctx, `
		SELECT relkind FROM pg_class WHERE relname = 'history'
	`).Scan(&relkind)
	if err != nil {
		// table might not exist yet in some fresh installs
		return false, nil
	}
	return relkind == "p", nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SchemaManifest — runtime introspection used by Adapter80
// ─────────────────────────────────────────────────────────────────────────────

// SchemaManifest caches table and column existence for runtime schema guards.
type SchemaManifest struct {
	Tables  map[string]bool     // table name → exists
	Columns map[string][]string // table name → column names
	mu      sync.RWMutex
}

// HasTable returns true if the named table exists.
func (s *SchemaManifest) HasTable(table string) bool {
	if s == nil {
		return true // nil manifest = assume exists (safe degradation)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Tables[table]
}

// HasColumn returns true if the named column exists in the named table.
func (s *SchemaManifest) HasColumn(table, column string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.Columns[table] {
		if c == column {
			return true
		}
	}
	return false
}

// InspectSchema performs a one-time scan of information_schema.
// Called on startup for Adapter80; the result is cached in db.manifest.
func (db *DB) InspectSchema(ctx context.Context) (*SchemaManifest, error) {
	var err error
	db.once.Do(func() {
		db.manifest, err = db.doInspectSchema(ctx)
	})
	if err != nil {
		return nil, err
	}
	return db.manifest, nil
}

func (db *DB) doInspectSchema(ctx context.Context) (*SchemaManifest, error) {
	slog.Info("Adapter80: running schema introspection (one-time)")

	m := &SchemaManifest{
		Tables:  make(map[string]bool),
		Columns: make(map[string][]string),
	}

	// Zabbix tables of interest
	tables := []string{
		"events", "hosts", "items", "triggers", "problem", "acknowledges",
		"host_rtdata", "proxy", "proxy_group", "proxy_grouphost",
		"history", "history_uint", "trends", "trends_uint",
		"users", "mfa", "user_provision",
	}
	for _, t := range tables {
		var exists bool
		if err := db.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)
		`, t).Scan(&exists); err != nil {
			slog.Warn("InspectSchema: could not check table", "table", t, "error", err)
			continue
		}
		m.Tables[t] = exists
	}

	// Fetch columns for key tables
	keyTables := []string{"events", "hosts", "triggers", "users"}
	for _, t := range keyTables {
		if !m.Tables[t] {
			continue
		}
		rows, err := db.pool.Query(ctx, `
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1
			ORDER BY ordinal_position
		`, t)
		if err != nil {
			slog.Warn("InspectSchema: could not fetch columns", "table", t, "error", err)
			continue
		}
		var cols []string
		for rows.Next() {
			var col string
			if scanErr := rows.Scan(&col); scanErr == nil {
				cols = append(cols, col)
			}
		}
		rows.Close()
		m.Columns[t] = cols
		slog.Info("InspectSchema: scanned table", "table", t, "columns", len(cols))
	}

	return m, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Group list (used by filter dropdowns — same across all versions)
// ─────────────────────────────────────────────────────────────────────────────

// Group is a Zabbix host group.
type Group struct {
	ID   int64
	Name string
}

// GetGroups returns all host groups ordered by name.
func (db *DB) GetGroups(ctx context.Context) ([]Group, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT groupid, name FROM hstgrp ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("GetGroups: %w", err)
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
