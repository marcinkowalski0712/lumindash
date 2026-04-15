package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types (returned by all adapters)
// ─────────────────────────────────────────────────────────────────────────────

// Problem is an active trigger firing right now.
type Problem struct {
	EventID     int64
	HostID      int64
	HostName    string
	HostIP      string
	TriggerName string
	Severity    int // 0=not classified … 5=disaster
	Since       time.Time
	Acknowledged bool
	OpData      string // populated in 6.4+
	CauseEventID *int64 // populated in 6.4+
}

// Host is a monitored host.
type Host struct {
	ID            int64
	Name          string
	Description   string
	IP            string
	Enabled       bool   // status==0
	Available     int    // 0=unknown, 1=available, 2=unavailable
	Groups        []string
	TemplateCount int
	ProblemCount  int
}

// Event is a historical alert record.
type Event struct {
	EventID      int64
	HostID       int64
	HostName     string
	TriggerName  string
	Severity     int
	Status       int    // 0=ok, 1=problem
	Clock        time.Time
	Acknowledged bool
	Duration     time.Duration
	OpData       string
}

// MetricPoint is a single time-series sample.
type MetricPoint struct {
	Clock time.Time
	Value float64
}

// ItemMeta is the metadata about a monitored item.
type ItemMeta struct {
	ID    int64
	Name  string
	Units string
}

// TriggerRow is a trigger as shown in the Config view.
type TriggerRow struct {
	ID         int64
	HostID     int64
	HostName   string
	Name       string
	Expression string
	Severity   int
	Enabled    bool
}

// DashboardStats are the summary numbers shown on the dashboard.
type DashboardStats struct {
	TotalHosts       int
	EnabledHosts     int
	HostsWithProblems int
	ActiveTriggers   int
	Disaster         int
	High             int
	Average          int
	Warning          int
	Info             int
}

// ─────────────────────────────────────────────────────────────────────────────
// QueryAdapter interface — every version-specific adapter implements this.
// ─────────────────────────────────────────────────────────────────────────────

// QueryAdapter abstracts all database reads so callers never hard-code schema.
// Write operations (ack, enable/disable) go through the Zabbix JSON-RPC API,
// so they do NOT appear here.
type QueryAdapter interface {
	// Dashboard
	GetDashboardStats(ctx context.Context) (*DashboardStats, error)
	GetActiveProblems(ctx context.Context) ([]Problem, error)

	// Hosts
	GetHosts(ctx context.Context, search, groupID, status string) ([]Host, error)
	GetHostByID(ctx context.Context, hostID int64) (*Host, error)

	// Items / Metrics
	GetItemsForHost(ctx context.Context, hostID int64) ([]ItemMeta, error)
	GetMetricHistory(ctx context.Context, itemID int64, from, to time.Time) ([]MetricPoint, error)

	// Events
	GetEvents(ctx context.Context, p EventFilter) ([]Event, int, error)

	// Config
	GetHostsForConfig(ctx context.Context) ([]Host, error)
	GetTriggersForConfig(ctx context.Context) ([]TriggerRow, error)
}

// EventFilter is the set of filters for the events page.
type EventFilter struct {
	HostID    int64
	Severity  int
	TimeRange string // "1h","6h","24h","7d","30d"
	Status    string // "problem","resolved",""
	Page      int
	PageSize  int
}

// ─────────────────────────────────────────────────────────────────────────────
// Adapter factory
// ─────────────────────────────────────────────────────────────────────────────

// NewAdapter returns the correct QueryAdapter for the given raw Zabbix version.
// It also receives the DB pool and a SchemaManifest (used only by Adapter80).
func NewAdapter(raw int, pool *DB, manifest *SchemaManifest) (QueryAdapter, error) {
	base := baseAdapter{db: pool}
	switch {
	case raw < 6_000_000:
		return nil, fmt.Errorf("unsupported Zabbix version (raw=%d): minimum supported is 6.0.0 (raw=6000000)", raw)
	case raw < 6_040_000:
		slog.Info("using Adapter60", "raw", raw)
		return &Adapter60{baseAdapter: base}, nil
	case raw < 7_000_000:
		slog.Info("using Adapter64", "raw", raw)
		return &Adapter64{baseAdapter: base}, nil
	case raw < 7_020_000:
		slog.Info("using Adapter70", "raw", raw)
		return &Adapter70{baseAdapter: base}, nil
	case raw < 8_000_000:
		slog.Info("using Adapter72", "raw", raw)
		return &Adapter72{baseAdapter: base}, nil
	default:
		slog.Warn("using Adapter80 (experimental — Zabbix 8.0 alpha)", "raw", raw)
		return &Adapter80{baseAdapter: base, manifest: manifest}, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// baseAdapter — shared helpers used by all concrete adapters
// ─────────────────────────────────────────────────────────────────────────────

type baseAdapter struct {
	db *DB
}

// severityWhere builds a WHERE fragment for severity filtering.
// It is version-agnostic because the severity column is present in all versions.
func severityWhere(severity int) (string, []any) {
	if severity > 0 {
		return "AND t.priority = $1", []any{severity}
	}
	return "", nil
}

// timeRangeDuration converts a time-range string to a Duration.
func timeRangeDuration(r string) time.Duration {
	switch r {
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}
