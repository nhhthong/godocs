// Package config loads application configuration from environment variables,
// enforcing fail-fast validation for critical parameters.
//
// Adhering to the 12-Factor App methodology, configuration state resides strictly in the environment
// rather than version-controlled repository files. Sane production defaults are provided throughout,
// permitting zero-configuration local execution via `go run ./cmd/api`.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config represents the aggregated configuration parameters governing the service.
type Config struct {
	Addr        string        // TCP network address to bind (e.g. ":8080")
	DSN         string        // SQLite data source connection string
	StorageRoot string        // Root filesystem directory for uploaded blobs
	MaxUpload   int64         // Maximum permitted upload size per file (in bytes)
	CacheTTL    time.Duration // Time-to-live duration for in-memory document caching
	Workers     int           // Concurrency level for background indexing goroutines
	QueueSize   int           // Capacity of the background indexing job buffer
	LogDir      string        // Target directory for log files (empty string or "stdout" outputs solely to stdout)

	// --- Authentication Parameters ---
	SessionTTL   time.Duration // Lifespan of an authenticated user session
	CookieSecure bool          // Enforces Secure flag on session cookies (set to false for plaintext HTTP dev)

	// --- Cross-Origin Resource Sharing (CORS) ---
	CORSOrigins []string // Permitted cross-origin origins; empty slice denotes same-origin only

	// --- Global Token-Bucket Rate Limiting ---
	RateRPS   float64 // Token replenishment rate per second per IP
	RateBurst int     // Maximum burst token capacity per IP
}

// Load populates and validates a Config struct from ambient environment variables.
func Load() (Config, error) {
	c := Config{
		Addr:        env("APP_ADDR", ":8080"),
		DSN:         env("APP_DSN", "file:godocs.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"),
		StorageRoot: env("APP_STORAGE", "./data/uploads"),
		MaxUpload:   int64(envInt("APP_MAX_UPLOAD_MB", 25)) << 20,
		CacheTTL:    time.Duration(envInt("APP_CACHE_TTL_SEC", 60)) * time.Second,
		Workers:     envInt("APP_WORKERS", 4),
		QueueSize:   envInt("APP_QUEUE_SIZE", 128),
		LogDir:      env("APP_LOG_DIR", "log"), // defaults to "log" folder; set to empty or "stdout" for terminal only

		SessionTTL:   time.Duration(envInt("APP_SESSION_TTL_HOURS", 168)) * time.Hour, // 168 hours = 7 days
		CookieSecure: envBool("APP_COOKIE_SECURE", true),

		CORSOrigins: envList("APP_CORS_ORIGINS"), // e.g. "https://app.example.com,https://admin.example.com"

		RateRPS:   envFloat("APP_RATE_RPS", 10),
		RateBurst: envInt("APP_RATE_BURST", 20),
	}

	if c.Workers < 1 {
		return c, fmt.Errorf("config: APP_WORKERS must be >= 1")
	}
	if c.QueueSize < 1 {
		return c, fmt.Errorf("config: APP_QUEUE_SIZE must be >= 1")
	}
	if c.MaxUpload <= 0 {
		return c, fmt.Errorf("config: APP_MAX_UPLOAD_MB must be > 0")
	}
	if c.RateRPS <= 0 || c.RateBurst < 1 {
		return c, fmt.Errorf("config: APP_RATE_RPS must be > 0 and APP_RATE_BURST must be >= 1")
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

// envInt retrieves an integer environment variable. If set but unparseable,
// it logs an explicit warning rather than silently reverting to default.
func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("config: invalid integer format, reverting to default", "key", k, "value", v, "default", def)
		return def
	}
	return n
}

func envFloat(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Warn("config: invalid float format, reverting to default", "key", k, "value", v, "default", def)
		return def
	}
	return f
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v) // accepts "1", "t", "true", "0", "f", "false"
	if err != nil {
		slog.Warn("config: invalid boolean format, reverting to default", "key", k, "value", v, "default", def)
		return def
	}
	return b
}

// envList splits a comma-delimited environment variable into a slice of trimmed, non-empty strings.
func envList(k string) []string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := parts[:0] // reuses backing memory allocation
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
