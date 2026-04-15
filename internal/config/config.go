package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	DBHost       string
	DBPort       string
	DBUser       string
	DBPass       string
	DBName       string
	APIURL       string
	APIUser      string
	APIPass      string
	ListenAddr   string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		DBHost:       getenv("ZBX_DB_HOST", "localhost"),
		DBPort:       getenv("ZBX_DB_PORT", "5432"),
		DBUser:       getenv("ZBX_DB_USER", "zabbix"),
		DBPass:       getenv("ZBX_DB_PASS", "zabbix"),
		DBName:       getenv("ZBX_DB_NAME", "zabbix"),
		APIURL:       getenv("ZBX_API_URL", "http://zabbix-server/api_jsonrpc.php"),
		APIUser:      getenv("ZBX_API_USER", "Admin"),
		APIPass:      getenv("ZBX_API_PASS", "zabbix"),
		ListenAddr:   getenv("LISTEN_ADDR", ":8090"),
		ReadTimeout:  parseDuration(getenv("READ_TIMEOUT", "30s")),
		WriteTimeout: parseDuration(getenv("WRITE_TIMEOUT", "30s")),
	}
}

// DSN builds a pgx connection string.
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName,
	)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 30 * time.Second
	}
	return d
}
