package db

// Adapter72 supports Zabbix 7.2.
//
// Changes vs 7.0:
//   - proxy_group table extended (proxy HA enhancements)
//   - host templates linkage table changed
//   - trigger tags (trigger_tag table) more deeply integrated
//   - Tags-based correlation becomes primary mechanism
//   - manual_close column semantics on triggers changed

import (
	"context"
	"time"
)

// Adapter72 implements QueryAdapter for Zabbix 7.2.
// The core schema is identical to 7.0 for all queries lumindash uses;
// the main changes (proxy_group HA, template linkage) don't affect
// the read-only dashboard views.
type Adapter72 struct {
	baseAdapter
}

// Delegate everything to Adapter70 — schema is compatible for all views.
func (a *Adapter72) delegate() *Adapter70 {
	return &Adapter70{baseAdapter: a.baseAdapter}
}

func (a *Adapter72) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	return a.delegate().GetDashboardStats(ctx)
}

func (a *Adapter72) GetActiveProblems(ctx context.Context) ([]Problem, error) {
	return a.delegate().GetActiveProblems(ctx)
}

func (a *Adapter72) GetHosts(ctx context.Context, search, groupID, status string) ([]Host, error) {
	return a.delegate().GetHosts(ctx, search, groupID, status)
}

func (a *Adapter72) GetHostByID(ctx context.Context, hostID int64) (*Host, error) {
	return a.delegate().GetHostByID(ctx, hostID)
}

func (a *Adapter72) GetItemsForHost(ctx context.Context, hostID int64) ([]ItemMeta, error) {
	return a.delegate().GetItemsForHost(ctx, hostID)
}

func (a *Adapter72) GetMetricHistory(ctx context.Context, itemID int64, from, to time.Time) ([]MetricPoint, error) {
	return queryMetricHistory(ctx, a.db, itemID, from, to)
}

func (a *Adapter72) GetEvents(ctx context.Context, f EventFilter) ([]Event, int, error) {
	return a.delegate().GetEvents(ctx, f)
}

func (a *Adapter72) GetHostsForConfig(ctx context.Context) ([]Host, error) {
	return a.delegate().GetHostsForConfig(ctx)
}

func (a *Adapter72) GetTriggersForConfig(ctx context.Context) ([]TriggerRow, error) {
	return a.delegate().GetTriggersForConfig(ctx)
}
