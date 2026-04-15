package handlers

// CtxKey is a typed key for values stored in request contexts.
type CtxKey string

const (
	// CtxKeyZabbixVersion is the context key for the detected ZabbixVersion.
	CtxKeyZabbixVersion CtxKey = "zabbix_version"
)
