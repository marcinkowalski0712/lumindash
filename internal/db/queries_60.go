package db

// Adapter60 supports Zabbix 6.0 LTS and 6.2.
//
// Key schema differences from later versions:
//   - events table has NO cause_eventid
//   - events table has NO opdata column
//   - hosts table uses proxy_hostid (no dedicated proxy table, no monitored_by)
//   - users table: login field is 'alias' (not 'username')
//   - trigger expressions still use old-style {host:key.func()} format

import (
	"context"
	"fmt"
	"time"
)

// Adapter60 implements QueryAdapter for Zabbix 6.0 / 6.2.
type Adapter60 struct {
	baseAdapter
}

// ─── Dashboard ────────────────────────────────────────────────────────────────

func (a *Adapter60) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{}

	// Total / enabled hosts (status: 0=enabled, 1=disabled; flags: 0=normal host)
	row := a.db.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE flags = 0),
			COUNT(*) FILTER (WHERE flags = 0 AND status = 0)
		FROM hosts
		WHERE flags = 0
	`)
	if err := row.Scan(&stats.TotalHosts, &stats.EnabledHosts); err != nil {
		return nil, fmt.Errorf("Adapter60.GetDashboardStats (host counts): %w", err)
	}

	// Active problems (value=1), grouped by severity
	// In 6.0 the problem table exists from 4.0 onwards.
	rows, err := a.db.pool.Query(ctx, `
		SELECT p.severity, COUNT(*) AS cnt
		FROM problem p
		WHERE p.source = 0       -- trigger-generated
		  AND p.object = 0       -- trigger
		  AND p.r_eventid IS NULL -- not yet resolved
		GROUP BY p.severity
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter60.GetDashboardStats (severity counts): %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sev, cnt int
		if err := rows.Scan(&sev, &cnt); err != nil {
			return nil, err
		}
		stats.ActiveTriggers += cnt
		switch sev {
		case 5:
			stats.Disaster = cnt
		case 4:
			stats.High = cnt
		case 3:
			stats.Average = cnt
		case 2:
			stats.Warning = cnt
		case 1:
			stats.Info = cnt
		}
	}

	// Hosts with at least one active problem
	row = a.db.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT h.hostid)
		FROM problem p
		JOIN triggers t ON t.triggerid = p.objectid
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		WHERE p.source = 0
		  AND p.r_eventid IS NULL
		  AND h.flags = 0
	`)
	if err := row.Scan(&stats.HostsWithProblems); err != nil {
		return nil, fmt.Errorf("Adapter60.GetDashboardStats (hosts with problems): %w", err)
	}

	return stats, nil
}

func (a *Adapter60) GetActiveProblems(ctx context.Context) ([]Problem, error) {
	// In 6.0/6.2: no opdata, no cause_eventid in events.
	rows, err := a.db.pool.Query(ctx, `
		SELECT
			p.eventid,
			h.hostid,
			h.host,
			i.ip,
			t.description,
			p.severity,
			p.clock,
			COALESCE(p.acknowledged, 0)
		FROM problem p
		JOIN triggers t ON t.triggerid = p.objectid
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		WHERE p.source = 0
		  AND p.r_eventid IS NULL
		  AND h.flags = 0
		  AND h.status = 0
		ORDER BY p.severity DESC, p.clock ASC
		LIMIT 500
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter60.GetActiveProblems: %w", err)
	}
	defer rows.Close()

	var out []Problem
	for rows.Next() {
		var prob Problem
		var clock int64
		var acked int
		if err := rows.Scan(
			&prob.EventID, &prob.HostID, &prob.HostName, &prob.HostIP,
			&prob.TriggerName, &prob.Severity, &clock, &acked,
		); err != nil {
			return nil, err
		}
		prob.Since = time.Unix(clock, 0)
		prob.Acknowledged = acked == 1
		out = append(out, prob)
	}
	return out, nil
}

// ─── Hosts ────────────────────────────────────────────────────────────────────

func (a *Adapter60) GetHosts(ctx context.Context, search, groupID, status string) ([]Host, error) {
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
			h.available,
			COALESCE(STRING_AGG(DISTINCT g.name, ',' ORDER BY g.name), '') AS groups
		FROM hosts h
		LEFT JOIN interface i ON i.hostid = h.hostid AND i.main = 1 AND i.type = 1
		LEFT JOIN hosts_groups hg ON hg.hostid = h.hostid
		LEFT JOIN hstgrp g ON g.groupid = hg.groupid
		%s
		GROUP BY h.hostid, h.host, h.description, i.ip, h.status, h.available
		ORDER BY h.host
	`, where)

	rows, err := a.db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("Adapter60.GetHosts: %w", err)
	}
	defer rows.Close()

	return scanHosts(rows)
}

func (a *Adapter60) GetHostByID(ctx context.Context, hostID int64) (*Host, error) {
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

func (a *Adapter60) GetItemsForHost(ctx context.Context, hostID int64) ([]ItemMeta, error) {
	rows, err := a.db.pool.Query(ctx, `
		SELECT itemid, name, units
		FROM items
		WHERE hostid = $1
		  AND status = 0
		  AND flags = 0
		  AND value_type IN (0, 3)  -- float, uint
		ORDER BY name
	`, hostID)
	if err != nil {
		return nil, fmt.Errorf("Adapter60.GetItemsForHost: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

func (a *Adapter60) GetMetricHistory(ctx context.Context, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	return queryMetricHistory(ctx, a.db, itemID, from, to)
}

// ─── Events ───────────────────────────────────────────────────────────────────

func (a *Adapter60) GetEvents(ctx context.Context, f EventFilter) ([]Event, int, error) {
	// 6.0: no opdata, no cause_eventid — skip those columns.
	return queryEvents60(ctx, a.db, f)
}

// ─── Config ───────────────────────────────────────────────────────────────────

func (a *Adapter60) GetHostsForConfig(ctx context.Context) ([]Host, error) {
	return a.GetHosts(ctx, "", "", "")
}

func (a *Adapter60) GetTriggersForConfig(ctx context.Context) ([]TriggerRow, error) {
	rows, err := a.db.pool.Query(ctx, `
		SELECT
			t.triggerid,
			h.hostid,
			h.host,
			t.description,
			t.expression,
			t.priority,
			t.status
		FROM triggers t
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		WHERE t.flags = 0
		  AND h.flags = 0
		GROUP BY t.triggerid, h.hostid, h.host, t.description, t.expression, t.priority, t.status
		ORDER BY h.host, t.description
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter60.GetTriggersForConfig: %w", err)
	}
	defer rows.Close()
	return scanTriggers(rows)
}
