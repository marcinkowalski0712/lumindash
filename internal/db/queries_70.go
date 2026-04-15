package db

// Adapter70 supports Zabbix 7.0 LTS.
//
// New in 7.0 vs 6.4:
//   - hosts.monitored_by added (0=server, 1=proxy, 2=proxy_group)
//   - hosts.proxyid replaces proxy_hostid in some contexts
//   - New dedicated proxy table; proxy_group table added
//   - Trigger expression format fully migrated to new syntax
//   - Trigger adds: event_name, uuid columns
//   - users.alias renamed to users.username + MFA columns added
//   - event_tag table now heavily used for tag-based correlation
//   - events.acknowledged replaced by partial ack model (see acknowledges table)
//   - events.suppressed column added
//   - host_rtdata table introduced (runtime data split from hosts)
//   - New tables: proxy, proxy_group, proxy_grouphost, host_rtdata

import (
	"context"
	"fmt"
	"time"
)

// Adapter70 implements QueryAdapter for Zabbix 7.0.
type Adapter70 struct {
	baseAdapter
}

func (a *Adapter70) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{}

	row := a.db.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE flags = 0),
			COUNT(*) FILTER (WHERE flags = 0 AND status = 0)
		FROM hosts
		WHERE flags = 0
	`)
	if err := row.Scan(&stats.TotalHosts, &stats.EnabledHosts); err != nil {
		return nil, fmt.Errorf("Adapter70.GetDashboardStats (host counts): %w", err)
	}

	rows, err := a.db.pool.Query(ctx, `
		SELECT p.severity, COUNT(*) AS cnt
		FROM problem p
		WHERE p.source = 0
		  AND p.r_eventid IS NULL
		  AND COALESCE(p.suppressed, 0) = 0
		GROUP BY p.severity
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter70.GetDashboardStats (severity counts): %w", err)
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

	row = a.db.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT h.hostid)
		FROM problem p
		JOIN triggers t ON t.triggerid = p.objectid
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		WHERE p.source = 0
		  AND p.r_eventid IS NULL
		  AND COALESCE(p.suppressed, 0) = 0
		  AND h.flags = 0
	`)
	if err := row.Scan(&stats.HostsWithProblems); err != nil {
		return nil, fmt.Errorf("Adapter70.GetDashboardStats (hosts with problems): %w", err)
	}

	return stats, nil
}

func (a *Adapter70) GetActiveProblems(ctx context.Context) ([]Problem, error) {
	// 7.0: partial ack model — p.acknowledged may be 0/1/2;
	//      we join acknowledges table to get the truest "fully acknowledged" status.
	//      Also: suppressed column now present.
	rows, err := a.db.pool.Query(ctx, `
		SELECT
			p.eventid,
			h.hostid,
			h.host,
			COALESCE(i.ip, ''),
			t.description,
			p.severity,
			p.clock,
			-- acknowledged: 1 if at least one ack action exists
			CASE WHEN EXISTS (
				SELECT 1 FROM acknowledges a WHERE a.eventid = p.eventid
			) THEN 1 ELSE 0 END,
			COALESCE(e.opdata, ''),
			e.cause_eventid
		FROM problem p
		JOIN triggers t ON t.triggerid = p.objectid
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		JOIN events e ON e.eventid = p.eventid
		WHERE p.source = 0
		  AND p.r_eventid IS NULL
		  AND COALESCE(p.suppressed, 0) = 0
		  AND h.flags = 0
		  AND h.status = 0
		ORDER BY p.severity DESC, p.clock ASC
		LIMIT 500
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter70.GetActiveProblems: %w", err)
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
			&prob.OpData, &prob.CauseEventID,
		); err != nil {
			return nil, err
		}
		prob.Since = time.Unix(clock, 0)
		prob.Acknowledged = acked == 1
		out = append(out, prob)
	}
	return out, nil
}

// GetHosts in 7.0 uses monitored_by instead of relying solely on proxy_hostid.
func (a *Adapter70) GetHosts(ctx context.Context, search, groupID, status string) ([]Host, error) {
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

	// 7.0: host_rtdata holds available/errors_from; fall back gracefully.
	q := fmt.Sprintf(`
		SELECT
			h.hostid,
			h.host,
			COALESCE(h.description, ''),
			COALESCE(i.ip, ''),
			h.status,
			COALESCE(hr.available, 0),
			COALESCE(STRING_AGG(DISTINCT g.name, ',' ORDER BY g.name), '') AS groups
		FROM hosts h
		LEFT JOIN interface i ON i.hostid = h.hostid AND i.main = 1 AND i.type = 1
		LEFT JOIN hosts_groups hg ON hg.hostid = h.hostid
		LEFT JOIN hstgrp g ON g.groupid = hg.groupid
		LEFT JOIN host_rtdata hr ON hr.hostid = h.hostid
		%s
		GROUP BY h.hostid, h.host, h.description, i.ip, h.status, hr.available
		ORDER BY h.host
	`, where)

	rows, err := a.db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("Adapter70.GetHosts: %w", err)
	}
	defer rows.Close()
	return scanHosts(rows)
}

func (a *Adapter70) GetHostByID(ctx context.Context, hostID int64) (*Host, error) {
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

func (a *Adapter70) GetItemsForHost(ctx context.Context, hostID int64) ([]ItemMeta, error) {
	a60 := &Adapter60{baseAdapter: a.baseAdapter}
	return a60.GetItemsForHost(ctx, hostID)
}

func (a *Adapter70) GetMetricHistory(ctx context.Context, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	return queryMetricHistory(ctx, a.db, itemID, from, to)
}

func (a *Adapter70) GetEvents(ctx context.Context, f EventFilter) ([]Event, int, error) {
	return queryEvents70(ctx, a.db, f)
}

func (a *Adapter70) GetHostsForConfig(ctx context.Context) ([]Host, error) {
	return a.GetHosts(ctx, "", "", "")
}

func (a *Adapter70) GetTriggersForConfig(ctx context.Context) ([]TriggerRow, error) {
	// 7.0: triggers.event_name and triggers.uuid added; still same join pattern.
	rows, err := a.db.pool.Query(ctx, `
		SELECT
			t.triggerid,
			h.hostid,
			h.host,
			COALESCE(NULLIF(t.event_name, ''), t.description) AS display_name,
			t.expression,
			t.priority,
			t.status
		FROM triggers t
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		WHERE t.flags = 0
		  AND h.flags = 0
		GROUP BY t.triggerid, h.hostid, h.host, t.description, t.event_name, t.expression, t.priority, t.status
		ORDER BY h.host, t.description
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter70.GetTriggersForConfig: %w", err)
	}
	defer rows.Close()
	return scanTriggers(rows)
}
