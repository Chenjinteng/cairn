// Package config loads runtime configuration from environment variables.
//
// Mirrors registry-manager's env conventions so .env files can be reused:
//   - REGISTRY_URL         required, the OCI Distribution endpoint we manage
//   - PORT                 default 8787, HTTP listen port
//   - REGISTRY_CREDENTIAL_KEY  AES-256-GCM key for the credential vault
//   - REGISTRY_NOTIFY_TOKEN    shared secret for distribution webhook events
//
// See ../../.env.example for the full list (filled in once modules land).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved runtime configuration for cairn.
//
// Field semantics mirror registry-manager so we can swap .env.example wholesale.
// Fields are populated in Load(); no defaults are applied at struct-literal time.
type Config struct {
	// HTTP
	Port int // PORT, default 8787

	// Managed registry (the OCI Distribution we read/delete/pull into)
	RegistryURL      string        // REGISTRY_URL, required
	RegistryProxy    string        // REGISTRY_PROXY, optional
	RegistryUsername string        // REGISTRY_USERNAME, optional
	RegistryPassword string        // REGISTRY_PASSWORD, optional
	RegistryName     string        // REGISTRY_NAME, default "镜像仓库"
	CacheTTL         time.Duration // REGISTRY_CACHE_TTL_SECONDS, default 60s

	// Capability gates (mirrors registry-manager)
	AllowDelete bool // REGISTRY_ALLOW_DELETE, default true
	AllowPull   bool // REGISTRY_ALLOW_PULL, default true

	// Pull queue
	PullQueueSize           int // REGISTRY_PULL_QUEUE_SIZE, default 50
	PullHistoryRetentionDay int // REGISTRY_PULL_HISTORY_RETENTION_DAYS, default 90

	// Heat / events
	NotifyToken             string        // REGISTRY_NOTIFY_TOKEN, optional but required for heat
	AllowRegistryEvents     bool          // REGISTRY_ALLOW_REGISTRY_EVENTS, default true
	StatsRetentionDay       int           // REGISTRY_STATS_RETENTION_DAYS, default 365
	StatsIgnoreUserAgents   []string      // REGISTRY_STATS_IGNORE_USERAGENTS, comma-separated, case-insensitive substring match
	StatsAggregationInterval time.Duration // derived from retention window; not env-driven

	// Credential vault
	CredentialKey string // REGISTRY_CREDENTIAL_KEY, strongly recommended; absence disables vault (pulls still work anonymously)
	CredentialsDir string // REGISTRY_CREDENTIALS_DIR, default /app/data

	// Dev convenience
	Env string // "dev" / "prod", default "prod"
}

// Load reads configuration from process env. Required fields fail fast —
// we refuse to start half-configured, same as registry-manager's compose (${VAR:?}).
func Load() (*Config, error) {
	c := &Config{
		Port:                    intEnv("PORT", 8787),
		RegistryURL:             os.Getenv("REGISTRY_URL"),
		RegistryProxy:           os.Getenv("REGISTRY_PROXY"),
		RegistryUsername:        os.Getenv("REGISTRY_USERNAME"),
		RegistryPassword:        os.Getenv("REGISTRY_PASSWORD"),
		RegistryName:            strEnv("REGISTRY_NAME", "镜像仓库"),
		CacheTTL:                time.Duration(intEnv("REGISTRY_CACHE_TTL_SECONDS", 60)) * time.Second,
		AllowDelete:             boolEnv("REGISTRY_ALLOW_DELETE", true),
		AllowPull:               boolEnv("REGISTRY_ALLOW_PULL", true),
		PullQueueSize:           intEnv("REGISTRY_PULL_QUEUE_SIZE", 50),
		PullHistoryRetentionDay: intEnv("REGISTRY_PULL_HISTORY_RETENTION_DAYS", 90),
		NotifyToken:             os.Getenv("REGISTRY_NOTIFY_TOKEN"),
		AllowRegistryEvents:     boolEnv("REGISTRY_ALLOW_REGISTRY_EVENTS", true),
		StatsRetentionDay:       intEnv("REGISTRY_STATS_RETENTION_DAYS", 365),
		CredentialKey:           os.Getenv("REGISTRY_CREDENTIAL_KEY"),
		CredentialsDir:          strEnv("REGISTRY_CREDENTIALS_DIR", "/app/data"),
		Env:                     strEnv("GO_HUB_ENV", "prod"),
	}
	if g := os.Getenv("REGISTRY_STATS_IGNORE_USERAGENTS"); g != "" {
		for _, part := range strings.Split(g, ",") {
			if t := strings.TrimSpace(part); t != "" {
				c.StatsIgnoreUserAgents = append(c.StatsIgnoreUserAgents, t)
			}
		}
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.RegistryURL == "" {
		return errors.New("REGISTRY_URL is required (must be http(s)://... endpoint of the OCI Distribution we manage)")
	}
	if !strings.HasPrefix(c.RegistryURL, "http://") && !strings.HasPrefix(c.RegistryURL, "https://") {
		return fmt.Errorf("REGISTRY_URL must start with http:// or https://, got %q", c.RegistryURL)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("PORT out of range: %d", c.Port)
	}
	if c.AllowDelete == false && c.AllowPull == false {
		// not an error, just intentional read-only + no-pull mode
	}
	if c.CredentialKey != "" && len(c.CredentialKey) < 32 {
		return fmt.Errorf("REGISTRY_CREDENTIAL_KEY should be at least 32 chars when set (got %d); use `openssl rand -hex 32`", len(c.CredentialKey))
	}
	return nil
}

func intEnv(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func boolEnv(name string, def bool) bool {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func strEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}