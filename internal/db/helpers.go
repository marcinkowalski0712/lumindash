package db

// helpers.go — shared query helpers used by multiple adapters.
// Keeps the adapter files focused on version-specific differences.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Row scanners (shared across adapters)
// ─────────────────────────────────────────────────────────────────────────────

// scanHosts reads rows of: hostid, host, description, ip, status, available, groups(csv)
func scanHosts(rows pgx.Rows) ([]Host, error) {
	var out []Host
	for rows.Next() {
		var h Host
		var groupCSV string
		var status, available int
		if err := rows.Scan(&h.ID, &h.Name, &h.Description, &h.IP, &status, &available, &groupCSV); err != nil {
			return nil, err
		}
		h.Enabled = status == 0
		h.Available = available
		if groupCSV != "" {
			h.Groups = strings.Split(groupCSV, ",")
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// scanItems reads rows of: itemid, name, units
func scanItems(rows pgx.Rows) ([]ItemMeta, error) {
	var out []ItemMeta
	for rows.Next() {
		var m ItemMeta
		if err := rows.Scan(&m.ID, &m.Name, &m.Units); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// scanTriggers reads rows of: triggerid, hostid, host, name, expression, priority, status
func scanTriggers(rows pgx.Rows) ([]TriggerRow, error) {
	var out []TriggerRow
	for rows.Next() {
		var t TriggerRow
		var status int
		if err := rows.Scan(&t.ID, &t.HostID, &t.HostName, &t.Name, &t.Expression, &t.Severity, &status); err != nil {
			return nil, err
		}
		t.Enabled = status == 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Metric history — shared, version-agnostic
// Uses trends for ranges >7d (TimescaleDB / native partitioning safe).
// ─────────────────────────────────────────────────────────────────────────────

func queryMetricHistory(ctx context.Context, db *DB, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	dur := to.Sub(from)
	useTrends := dur > 7*24*time.Hour

	if useTrends {
		// Prefer trends_uint first; fall back to trends.
		// Both tables have: itemid, clock, num, value_min, value_avg, value_max
		for _, table := range []string{"trends_uint", "trends"} {
			pts, err := queryTrends(ctx, db, table, itemID, from, to)
			if err == nil && len(pts) > 0 {
				return pts, nil
			}
		}
		return nil, nil
	}

	// For short ranges, try history_uint then history (float).
	for _, table := range []string{"history_uint", "history"} {
		pts, err := queryRawHistory(ctx, db, table, itemID, from, to)
		if err == nil {
			return pts, nil
		}
	}
	return nil, fmt.Errorf("queryMetricHistory: no history found for item %d", itemID)
}

func queryRawHistory(ctx context.Context, db *DB, table string, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	rows, err := db.pool.Query(ctx,
		fmt.Sprintf(`
			SELECT clock, value
			FROM %s
			WHERE itemid = $1
			  AND clock >= $2
			  AND clock <= $3
			ORDER BY clock
		`, table),
		itemID, from.Unix(), to.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricPoint
	for rows.Next() {
		var pt MetricPoint
		var clock int64
		var val float64
		if err := rows.Scan(&clock, &val); err != nil {
			return nil, err
		}
		pt.Clock = time.Unix(clock, 0)
		pt.Value = val
		out = append(out, pt)
	}
	return out, rows.Err()
}

func queryTrends(ctx context.Context, db *DB, table string, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	rows, err := db.pool.Query(ctx,
		fmt.Sprintf(`
			SELECT clock, value_avg
			FROM %s
			WHERE itemid = $1
			  AND clock >= $2
			  AND clock <= $3
			ORDER BY clock
		`, table),
		itemID, from.Unix(), to.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricPoint
	for rows.Next() {
		var pt MetricPoint
		var clock int64
		var val float64
		if err := rows.Scan(&clock, &val); err != nil {
			return nil, err
		}
		pt.Clock = time.Unix(clock, 0)
		pt.Value = val
		out = append(out, pt)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Version-specific event queries
// ─────────────────────────────────────────────────────────────────────────────

// queryEvents60 — Zabbix 6.0/6.2: no opdata, no cause_eventid.
func queryEvents60(ctx context.Context, db *DB, f EventFilter) ([]Event, int, error) {
	return queryEventsGeneric(ctx, db, f, false, false)
}

// queryEvents64 — Zabbix 6.4: opdata present, no acknowledged partial-ack complexity.
func queryEvents64(ctx context.Context, db *DB, f EventFilter) ([]Event, int, error) {
	return queryEventsGeneric(ctx, db, f, true, false)
}

// queryEvents70 — Zabbix 7.0+: opdata + partial ack via acknowledges table.
func queryEvents70(ctx context.Context, db *DB, f EventFilter) ([]Event, int, error) {
	return queryEventsGeneric(ctx, db, f, true, true)
}

// queryEventsGeneric builds the events query, guarding optional columns.
// hasOpdata: include e.opdata in SELECT
// useAckTable: derive acknowledged from acknowledges table rather than e.acknowledged
func queryEventsGeneric(ctx context.Context, db *DB, f EventFilter, hasOpdata, useAckTable bool) ([]Event, int, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize < 1 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	dur := timeRangeDuration(f.TimeRange)
	fromUnix := time.Now().Add(-dur).Unix()

	args := []any{fromUnix}
	argIdx := 2
	where := "WHERE e.source = 0 AND e.object = 0 AND e.clock >= $1"

	if f.HostID > 0 {
		where += fmt.Sprintf(" AND h.hostid = $%d", argIdx)
		args = append(args, f.HostID)
		argIdx++
	}
	if f.Severity > 0 {
		where += fmt.Sprintf(" AND t.priority = $%d", argIdx)
		args = append(args, f.Severity)
		argIdx++
	}
	if f.Status == "problem" {
		where += " AND e.value = 1"
	} else if f.Status == "resolved" {
		where += " AND e.value = 0"
	}

	var ackExpr string
	if useAckTable {
		ackExpr = "CASE WHEN EXISTS (SELECT 1 FROM acknowledges a WHERE a.eventid = e.eventid) THEN 1 ELSE 0 END"
	} else {
		ackExpr = "COALESCE(e.acknowledged, 0)"
	}

	opdataExpr := "''"
	if hasOpdata {
		opdataExpr = "COALESCE(e.opdata, '')"
	}

	baseQ := fmt.Sprintf(`
		FROM events e
		JOIN triggers t ON t.triggerid = e.objectid
		JOIN functions f ON f.triggerid = t.triggerid
		JOIN items i ON i.itemid = f.itemid
		JOIN hosts h ON h.hostid = i.hostid
		%s
	`, where)

	// Total count
	var total int
	countQ := "SELECT COUNT(*) " + baseQ
	if err := db.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("queryEventsGeneric (count): %w", err)
	}

	// Data query — add LIMIT/OFFSET at the end
	dataArgs := append(args, pageSize, offset)
	dataQ := fmt.Sprintf(`
		SELECT
			e.eventid,
			h.hostid,
			h.host,
			t.description,
			t.priority,
			e.value,
			e.clock,
			%s,
			%s
		%s
		GROUP BY e.eventid, h.hostid, h.host, t.description, t.priority, e.value, e.clock, e.opdata, e.acknowledged
		ORDER BY e.clock DESC
		LIMIT $%d OFFSET $%d
	`, ackExpr, opdataExpr, baseQ, argIdx, argIdx+1)

	rows, err := db.pool.Query(ctx, dataQ, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("queryEventsGeneric (data): %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var ev Event
		var clock int64
		var acked int
		if err := rows.Scan(
			&ev.EventID, &ev.HostID, &ev.HostName, &ev.TriggerName,
			&ev.Severity, &ev.Status, &clock, &acked, &ev.OpData,
		); err != nil {
			return nil, 0, err
		}
		ev.Clock = time.Unix(clock, 0)
		ev.Acknowledged = acked == 1
		events = append(events, ev)
	}
	return events, total, rows.Err()
}
