package handlers

import (
	"encoding/json"
	"net/http"

	"lumindash/internal/config"
	"lumindash/internal/db"
)

// HealthState carries the runtime detection results populated at startup.
// It is populated once and shared read-only across requests.
type HealthState struct {
	ZabbixVersion       config.ZabbixVersion
	TimescaleDB         bool
	PartitionedHistory  bool
	SchemaManifestCached bool // true if manifest was built (Adapter80 only)
	Adapter             string
	LumindashVersion    string
}

// HealthHandler returns a handler for GET /healthz.
func HealthHandler(database *db.DB, state *HealthState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbStatus := "ok"
		if err := database.Ping(r.Context()); err != nil {
			dbStatus = "error: " + err.Error()
		}

		resp := map[string]any{
			"status":                  "ok",
			"db":                      dbStatus,
			"zabbix_version":          state.ZabbixVersion.String(),
			"zabbix_version_raw":      state.ZabbixVersion.Raw,
			"zabbix_stability":        state.ZabbixVersion.Stability(),
			"adapter":                 state.Adapter,
			"timescaledb":             state.TimescaleDB,
			"partitioned_history":     state.PartitionedHistory,
			"schema_manifest_cached":  state.SchemaManifestCached,
			"lumindash_version":       state.LumindashVersion,
			"supported":               state.ZabbixVersion.IsSupported(),
		}

		w.Header().Set("Content-Type", "application/json")
		if dbStatus != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
			resp["status"] = "degraded"
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}
