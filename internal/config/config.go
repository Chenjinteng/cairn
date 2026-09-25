// Package config loads runtime configuration from environment variables.
//
// Mirrors registry-manager's env conventions so .env files can be reused:
//   - REGISTRY_URL         OPTIONAL, the default upstream registry pull jobs read from
//   - PORT                 default 8787, HTTP listen port
//   - REGISTRY_CREDENTIAL_KEY  AES-256-GCM key for the credential vault
//   - REGISTRY_NOTIFY_TOKEN    shared secret for distribution webhook events
//
// See ../../.env.example for the full list (filled in once modules land).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mutable holds runtime-editable settings. v0.5.1: the default upstream URL
// (REGISTRY_URL replacement) is persisted to SQLite so the settings page can
// edit it without restarting; env keeps the bootstrap default.
type Mutable struct {
	mu          sync.RWMutex
	registryURL string // "" == Docker Hub (pull.DefaultUpstream)
}

func (m *Mutable) RegistryURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registryURL
}

func (m *Mutable) SetRegistryURL(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registryURL = strings.TrimRight(strings.TrimSpace(v), "/")
}

// RegistryURLSource reports where the current value came from. "db" once
// the settings page has ever overridden; "env" while still the bootstrap
// default. The settings page surfaces this so operators can see whether
// they're looking at the env fallback or a persisted override.
func (m *Mutable) RegistryURLSource() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.registryURL != "" {
		return "db"
	}
	return "env"
}

// EffectiveRegistryURL returns Mutable.RegistryURL() if set, otherwise the
// env-loaded Config.RegistryURL fallback. Use this everywhere instead of
// reaching for Config.RegistryURL directly -- it keeps env vs db precedence
// in one place.
func (c *Config) EffectiveRegistryURL() string {
	if c == nil || c.Mutable == nil {
		return ""
	}
	if v := c.Mutable.RegistryURL(); v != "" {
		return v
	}
	return c.RegistryURL
}

// Config is the resolved runtime configuration for cairn.
//
// Field semantics mirror registry-manager so we can swap .env.example wholesale.
// Fields are populated in Load(); no defaults are applied at struct-literal time.
type Config struct {
	// HTTP
	Port int // PORT, default 8787

	// Embedded registry + optional upstream source.
	//
	// cairn IS the registry now: it serves /v2 itself out of StorageDir, so
	// REGISTRY_URL is OPTIONAL. When set it is only the DEFAULT upstream that a
	// pull job falls back to when the job itself names no source (the page form
	// and saved credentials still win). Leaving it empty is the normal
	// deployment — cairn then manages nothing but its own storage.
	RegistryURL      string        // REGISTRY_URL, optional default upstream
	RegistryProxy    string        // REGISTRY_PROXY, optional
	RegistryUsername string        // REGISTRY_USERNAME, optional
	RegistryPassword string        // REGISTRY_PASSWORD, optional
	RegistryName     string        // REGISTRY_NAME, default "镜像仓库"
	CacheTTL         time.Duration // REGISTRY_CACHE_TTL_SECONDS, default 60s
	// Mutable holds runtime-editable settings; populated in Load().
	// At read time, callers should prefer EffectiveRegistryURL() over the
	// bare env field so env vs db precedence is centralised.
	Mutable *Mutable

	// StorageDir is where the embedded /v2 endpoint keeps blobs, manifests and
	// upload sessions. REGISTRY_STORAGE_DIR, default "<REGISTRY_CREDENTIALS_DIR>/registry".
	// Mount a dedicated volume here in containers: every pulled and pushed image
	// lives on this path, and recreating the container without it loses them.
	StorageDir string

	// Capability gates (mirrors registry-manager)
	AllowDelete bool // REGISTRY_ALLOW_DELETE, default true
	AllowPull   bool // REGISTRY_ALLOW_PULL, default true

	// Pull queue
	PullQueueSize           int // REGISTRY_PULL_QUEUE_SIZE, default 50
	PullHistoryRetentionDay int // REGISTRY_PULL_HISTORY_RETENTION_DAYS, default 90

	// Heat / events
	NotifyToken              string        // REGISTRY_NOTIFY_TOKEN, optional but required for heat
	AllowRegistryEvents      bool          // REGISTRY_ALLOW_REGISTRY_EVENTS, default true
	StatsRetentionDay        int           // REGISTRY_STATS_RETENTION_DAYS, default 365
	StatsIgnoreUserAgents    []string      // REGISTRY_STATS_IGNORE_USERAGENTS, comma-separated, case-insensitive substring match
	StatsAggregationInterval time.Duration // derived from retention window; not env-driven

	// Credential vault
	CredentialKey  string // REGISTRY_CREDENTIAL_KEY, strongly recommended; absence disables vault (pulls still work anonymously)
	CredentialsDir string // REGISTRY_CREDENTIALS_DIR, default /app/data

	// Dev convenience
	Env string // "dev" / "prod", default "prod"
}

// Load reads configuration from process env. Required fields fail fast —
// we refuse to start half-configured, same as registry-manager's compose (${VAR:?}).
func Load() (*Config, error) {
	c := &Config{
		Port:                    intEnv("PORT", 8787),
		RegistryURL:             strings.TrimRight(os.Getenv("REGISTRY_URL"), "/"),
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
	// Keep the registry data next to the credential vault by default (one volume
	// covers both), while still allowing an explicit path / its own volume.
	c.StorageDir = strEnv("REGISTRY_STORAGE_DIR", filepath.Join(c.CredentialsDir, "registry"))

	// v0.5.1: always seed Mutable so handlers can call .SetRegistryURL() etc.
	// without a nil-pointer guard. The server later overwrites registryURL from
	// SQLite at startup when a settings row exists.
	c.Mutable = &Mutable{}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	// REGISTRY_URL is optional: cairn manages its own embedded registry. When
	// set it must still be a usable upstream base URL.
	if c.RegistryURL != "" && !strings.HasPrefix(c.RegistryURL, "http://") && !strings.HasPrefix(c.RegistryURL, "https://") {
		return fmt.Errorf("REGISTRY_URL must start with http:// or https://, got %q (leave it empty to manage the embedded registry only)", c.RegistryURL)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("PORT out of range: %d", c.Port)
	}
	if c.StorageDir == "" {
		return fmt.Errorf("REGISTRY_STORAGE_DIR resolved to an empty path")
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
