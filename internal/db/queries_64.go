package db

// Adapter64 supports Zabbix 6.4 LTS.
//
// New in 6.4 vs 6.0/6.2:
//   - events.cause_eventid added (root-cause correlation)
//   - events.opdata added (operational data from trigger expression)

import (
	"context"
	"fmt"
	"time"
)

// Adapter64 implements QueryAdapter for Zabbix 6.4.
type Adapter64 struct {
	baseAdapter
}

func (a *Adapter64) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	// Same schema as 6.0 for the problem/hosts tables.
	a60 := &Adapter60{baseAdapter: a.baseAdapter}
	return a60.GetDashboardStats(ctx)
}

func (a *Adapter64) GetActiveProblems(ctx context.Context) ([]Problem, error) {
	// Include opdata and cause_eventid (both added in 6.4).
	rows, err := a.db.pool.Query(ctx, `
		SELECT
			p.eventid,
			h.hostid,
			h.host,
			COALESCE(i.ip, ''),
			t.description,
			p.severity,
			p.clock,
			COALESCE(p.acknowledged, 0),
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
		  AND h.flags = 0
		  AND h.status = 0
		ORDER BY p.severity DESC, p.clock ASC
		LIMIT 500
	`)
	if err != nil {
		return nil, fmt.Errorf("Adapter64.GetActiveProblems: %w", err)
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

func (a *Adapter64) GetHosts(ctx context.Context, search, groupID, status string) ([]Host, error) {
	a60 := &Adapter60{baseAdapter: a.baseAdapter}
	return a60.GetHosts(ctx, search, groupID, status)
}

func (a *Adapter64) GetHostByID(ctx context.Context, hostID int64) (*Host, error) {
	a60 := &Adapter60{baseAdapter: a.baseAdapter}
	return a60.GetHostByID(ctx, hostID)
}

func (a *Adapter64) GetItemsForHost(ctx context.Context, hostID int64) ([]ItemMeta, error) {
	a60 := &Adapter60{baseAdapter: a.baseAdapter}
	return a60.GetItemsForHost(ctx, hostID)
}

func (a *Adapter64) GetMetricHistory(ctx context.Context, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	return queryMetricHistory(ctx, a.db, itemID, from, to)
}

func (a *Adapter64) GetEvents(ctx context.Context, f EventFilter) ([]Event, int, error) {
	// 6.4 adds opdata — use the 64-aware query.
	return queryEvents64(ctx, a.db, f)
}

func (a *Adapter64) GetHostsForConfig(ctx context.Context) ([]Host, error) {
	return a.GetHosts(ctx, "", "", "")
}

func (a *Adapter64) GetTriggersForConfig(ctx context.Context) ([]TriggerRow, error) {
	a60 := &Adapter60{baseAdapter: a.baseAdapter}
	return a60.GetTriggersForConfig(ctx)
}
