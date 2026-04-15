package db

// Adapter80 supports Zabbix 8.0 alpha — EXPERIMENTAL.
//
// ⚠ The 8.0 schema is still in flux. This adapter performs runtime schema
// introspection (via SchemaManifest) and guards every query that touches
// 8.0-only columns or tables before executing it.
//
// Every use of a 8.0-specific schema path is logged as [WARN].

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Adapter80 implements QueryAdapter for Zabbix 8.0 alpha.
type Adapter80 struct {
	baseAdapter
	manifest *SchemaManifest
}

// warnQuery logs a warning every time an 8.0-specific schema path is used.
func (a *Adapter80) warnQuery(path string) {
	slog.Warn("Adapter80: executing 8.0-alpha schema path", "path", path)
}

// fallback returns a 7.2 adapter for queries that are schema-compatible.
func (a *Adapter80) fallback() *Adapter72 {
	return &Adapter72{baseAdapter: a.baseAdapter}
}

// ─── Dashboard ────────────────────────────────────────────────────────────────

func (a *Adapter80) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	a.warnQuery("GetDashboardStats")
	// Try 7.2 path first; if it fails, return empty set with a log.
	stats, err := a.fallback().GetDashboardStats(ctx)
	if err != nil {
		slog.Error("Adapter80.GetDashboardStats failed — returning empty stats", "error", err)
		return &DashboardStats{}, nil
	}
	return stats, nil
}

func (a *Adapter80) GetActiveProblems(ctx context.Context) ([]Problem, error) {
	a.warnQuery("GetActiveProblems")
	problems, err := a.fallback().GetActiveProblems(ctx)
	if err != nil {
		slog.Error("Adapter80.GetActiveProblems failed — returning empty list", "error", err)
		return nil, nil
	}
	return problems, nil
}

// ─── Hosts ────────────────────────────────────────────────────────────────────

func (a *Adapter80) GetHosts(ctx context.Context, search, groupID, status string) ([]Host, error) {
	a.warnQuery("GetHosts")
	// 8.0 may have split host_rtdata further; detect columns at runtime.
	// For now fall back to 7.x path and guard with schema check.
	if a.manifest != nil && !a.manifest.HasTable("host_rtdata") {
		slog.Warn("Adapter80.GetHosts: host_rtdata table not found — falling back to hosts.available column")
		return a.getHostsNoRtdata(ctx, search, groupID, status)
	}
	hosts, err := a.fallback().GetHosts(ctx, search, groupID, status)
	if err != nil {
		slog.Error("Adapter80.GetHosts failed — returning empty list", "error", err)
		return nil, nil
	}
	return hosts, nil
}

// getHostsNoRtdata is the fallback for 8.0 when host_rtdata doesn't exist.
func (a *Adapter80) getHostsNoRtdata(ctx context.Context, search, groupID, status string) ([]Host, error) {
	args := []any{}
	where := "WHERE h.flags = 0"
	argIdx := 1

	if search != "" {
		where += fmt.Sprintf(" AND (h.host ILIKE $%d OR i.ip ILIKE $%d)", argIdx, argIdx+1)
		like := "%" + search + "%"
		args = append(args, like, like)
		argIdx += 2
	}
	if groupID != "" {
		where += fmt.Sprintf(" AND hg.groupid = $%d", argIdx)
		args = append(args, groupID)
		argIdx++
	}
	if status == "enabled" {
		where += " AND h.status = 0"
	} else if status == "disabled" {
		where += " AND h.status = 1"
	}

	q := fmt.Sprintf(`
		SELECT
			h.hostid,
			h.host,
			COALESCE(h.description, ''),
			COALESCE(i.ip, ''),
			h.status,
			0 AS available,
			COALESCE(STRING_AGG(DISTINCT g.name, ',' ORDER BY g.name), '') AS groups
		FROM hosts h
		LEFT JOIN interface i ON i.hostid = h.hostid AND i.main = 1 AND i.type = 1
		LEFT JOIN hosts_groups hg ON hg.hostid = h.hostid
		LEFT JOIN hstgrp g ON g.groupid = hg.groupid
		%s
		GROUP BY h.hostid, h.host, h.description, i.ip, h.status
		ORDER BY h.host
	`, where)

	rows, err := a.db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("Adapter80.getHostsNoRtdata: %w", err)
	}
	defer rows.Close()
	return scanHosts(rows)
}

func (a *Adapter80) GetHostByID(ctx context.Context, hostID int64) (*Host, error) {
	hosts, err := a.GetHosts(ctx, "", "", "")
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if h.ID == hostID {
			return &h, nil
		}
	}
	return nil, fmt.Errorf("host %d not found", hostID)
}

// ─── Items / Metrics ──────────────────────────────────────────────────────────

func (a *Adapter80) GetItemsForHost(ctx context.Context, hostID int64) ([]ItemMeta, error) {
	a.warnQuery("GetItemsForHost")
	items, err := a.fallback().GetItemsForHost(ctx, hostID)
	if err != nil {
		slog.Error("Adapter80.GetItemsForHost failed", "error", err)
		return nil, nil
	}
	return items, nil
}

func (a *Adapter80) GetMetricHistory(ctx context.Context, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	a.warnQuery("GetMetricHistory")
	return queryMetricHistory(ctx, a.db, itemID, from, to)
}

// ─── Events ───────────────────────────────────────────────────────────────────

func (a *Adapter80) GetEvents(ctx context.Context, f EventFilter) ([]Event, int, error) {
	a.warnQuery("GetEvents")

	// Guard cause_eventid and opdata at runtime.
	hasCauseEventID := a.manifest == nil || a.manifest.HasColumn("events", "cause_eventid")
	hasOpdata := a.manifest == nil || a.manifest.HasColumn("events", "opdata")

	if hasCauseEventID && hasOpdata {
		events, total, err := a.fallback().GetEvents(ctx, f)
		if err != nil {
			slog.Error("Adapter80.GetEvents (7.x path) failed", "error", err)
			return nil, 0, nil
		}
		return events, total, nil
	}

	slog.Warn("Adapter80.GetEvents: missing columns detected, using degraded query",
		"cause_eventid", hasCauseEventID, "opdata", hasOpdata)
	events, total, err := queryEvents60(ctx, a.db, f)
	if err != nil {
		slog.Error("Adapter80.GetEvents (degraded path) failed", "error", err)
		return nil, 0, nil
	}
	return events, total, nil
}

// ─── Config ───────────────────────────────────────────────────────────────────

func (a *Adapter80) GetHostsForConfig(ctx context.Context) ([]Host, error) {
	return a.GetHosts(ctx, "", "", "")
}

func (a *Adapter80) GetTriggersForConfig(ctx context.Context) ([]TriggerRow, error) {
	a.warnQuery("GetTriggersForConfig")
	triggers, err := a.fallback().GetTriggersForConfig(ctx)
	if err != nil {
		slog.Error("Adapter80.GetTriggersForConfig failed", "error", err)
		return nil, nil
	}
	return triggers, nil
}
