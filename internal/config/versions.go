package config

import (
	"fmt"
	"time"
)

// ZabbixVersion holds the parsed Zabbix version from the config table.
type ZabbixVersion struct {
	Major   int
	Minor   int
	Patch   int
	Raw     int
	IsAlpha bool // true for 8.0 alpha
}

// String returns a human-readable version string, e.g. "7.2.1".
func (v ZabbixVersion) String() string {
	if v.Raw == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// AdapterName returns the name of the adapter that should be used.
func (v ZabbixVersion) AdapterName() string {
	switch {
	case v.Raw < 6_040_000:
		return "Adapter60"
	case v.Raw < 7_000_000:
		return "Adapter64"
	case v.Raw < 7_020_000:
		return "Adapter70"
	case v.Raw < 8_000_000:
		return "Adapter72"
	default:
		return "Adapter80"
	}
}

// Stability returns "stable", "alpha", etc.
func (v ZabbixVersion) Stability() string {
	if s, ok := ZabbixStability[v.Major]; ok {
		return s
	}
	return "unknown"
}

// IsSupported returns true if the version meets the minimum requirement.
func (v ZabbixVersion) IsSupported() bool {
	return v.Raw >= MinSupportedRaw
}

// EOLDate returns the EOL time for this major version (zero = no EOL defined).
func (v ZabbixVersion) EOLDate() time.Time {
	return ZabbixEOL[v.Major]
}

// IsEOL returns true if the EOL date is known and has passed.
func (v ZabbixVersion) IsEOL() bool {
	eol := v.EOLDate()
	return !eol.IsZero() && time.Now().After(eol)
}

// Banner returns a UI banner level ("", "yellow", "orange") and message.
// Empty level means no banner.
func (v ZabbixVersion) Banner() (level, message string) {
	if v.IsAlpha {
		return "orange", fmt.Sprintf(
			"Zabbix %s alpha detected — lumindash support is experimental. Some views may show incomplete data.",
			v.String(),
		)
	}
	if v.IsEOL() {
		return "yellow", fmt.Sprintf(
			"Zabbix %s has reached end-of-life (%s). Consider upgrading.",
			v.String(), v.EOLDate().Format("January 2006"),
		)
	}
	return "", ""
}

const MinSupportedRaw = 6_000_000 // 6.0.0

// ZabbixEOL maps major version → EOL date. Zero value = no banner shown.
var ZabbixEOL = map[int]time.Time{
	6: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), // 6.0 LTS
	// 6.4 LTS EOL TBD
	// 7.0 LTS EOL TBD
}

// ZabbixStability maps major version → stability label.
var ZabbixStability = map[int]string{
	6: "stable",
	7: "stable",
	8: "alpha",
}

// ParseRawVersion converts the integer stored in config.value into ZabbixVersion.
// The raw format is AABBCCC  e.g. 7002001 = 7.2.1, 6000000 = 6.0.0
func ParseRawVersion(raw int) ZabbixVersion {
	major := raw / 1_000_000
	minor := (raw % 1_000_000) / 1_000
	patch := raw % 1_000
	return ZabbixVersion{
		Major:   major,
		Minor:   minor,
		Patch:   patch,
		Raw:     raw,
		IsAlpha: major >= 8,
	}
}
