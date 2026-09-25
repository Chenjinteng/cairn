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

// Mutable holds runtime-editable settings persisted to SQLite. v0.5.2
// generalises v0.5.1's single-field Mutable into a key->value map so the
// settings page can edit any field in MutableKeys without code changes.
// Env still seeds the bootstrap values; the settings page overrides
// anything in MutableKeys and that override survives restarts.
//
// Values are stored as strings (SQLite TEXT). Per-key helpers below know
// how to parse them back into the right Go type for Config.Effective*().
type Mutable struct {
	mu     sync.RWMutex
	values map[string]string
}

// MutableKeys is the whitelist of fields the settings page can edit. The
// map key matches the SQLite settings.key column. Anything not in this
// list is rejected by UpdateConfig so the UI can never write fields that
// would need a restart to take effect (e.g. PORT, REGISTRY_CREDENTIALS_DIR,
// REGISTRY_STORAGE_DIR, REGISTRY_CREDENTIAL_KEY).
//
// NOTE (v0.5.4): cache.ttl.seconds was REMOVED from this list. Since v0.5.0
// the inventory is read straight from local storage, so registry.CachedRegistry
// is no longer in any request path and the TTL had no consumer whatsoever --
// saving it changed nothing, which is worse than not offering it. The env var
// REGISTRY_CACHE_TTL_SECONDS is kept as a display-only value in
// GET /api/config so existing .env files keep loading.
//
// Adding a new editable field is three steps:
//  1. add the key here
//  2. add a Config.Effective<Field>() that falls back to the env value
//  3. surface it in handlers.UpdateConfig + types.ts MutableSettings
var MutableKeys = []string{
	"registry.url",          // Config.EffectiveRegistryURL
	"registry.proxy",        // Config.EffectiveRegistryProxy
	"registry.name",         // Config.EffectiveRegistryName
	"registry.username",     // Config.EffectiveRegistryUsername
	"registry.password",     // Config.EffectiveRegistryPassword (stored plaintext in SQLite; UI never echoes it back)
	"allow.delete",          // Config.EffectiveAllowDelete
	"allow.pull",            // Config.EffectiveAllowPull
	"allow.registry_events", // Config.EffectiveAllowRegistryEvents
	"stats.retention.days",  // Config.EffectiveStatsRetentionDays
	"pull.platforms",        // Config.EffectivePullPlatforms (CSV of <os>/<arch>[/<variant>]; empty = all)
}

// MutableKeysSet is the O(1) lookup version used by UpdateConfig.
var MutableKeysSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(MutableKeys))
	for _, k := range MutableKeys {
		m[k] = struct{}{}
	}
	return m
}()

// MutableFieldType tells UpdateConfig how to validate a key. Used to give
// nice 400s when the UI sends "true"/"false" for a numeric field etc.
var MutableFieldType = map[string]string{
	"registry.url":          "url",
	"registry.proxy":        "url",
	"registry.name":         "string",
	"registry.username":     "string",
	"registry.password":     "string",
	"allow.delete":          "bool",
	"allow.pull":            "bool",
	"allow.registry_events": "bool",
	"stats.retention.days":  "int",
	"pull.platforms":        "stringcsv",
}

// Get returns the persisted override for key, or "" if no override.
func (m *Mutable) Get(key string) string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.values[key]
}

// Set persists an override. Empty string clears the override (returns to
// env fallback on next Effective* call).
func (m *Mutable) Set(key, val string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]string{}
	}
	if val == "" {
		delete(m.values, key)
	} else {
		m.values[key] = val
	}
}

// Has reports whether key has an override in the map (i.e. source == "db").
func (m *Mutable) Has(key string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.values[key]
	return ok
}

// SetRegistryURL is preserved as a thin wrapper so server.go / handlers
// still compile after v0.5.1; new code should use Set("registry.url", v).
func (m *Mutable) SetRegistryURL(v string) { m.Set("registry.url", v) }

// RegistryURL / RegistryURLSource retained as accessor sugar.
func (m *Mutable) RegistryURL() string       { return m.Get("registry.url") }
func (m *Mutable) RegistryURLSource() string { return sourceLabel(m.Has("registry.url")) }

// sourceLabel returns "db" / "env" depending on whether key has an
// override. Centralises the labelling so the UI always reads the same.
func sourceLabel(overridden bool) string {
	if overridden {
		return "db"
	}
	return "env"
}

// Helpers below: each Effective*() resolves Mutable override > env value.
// This is the canonical read path; never reach for Config.RegistryURL
// directly elsewhere, otherwise the precedence chain breaks.

func (c *Config) EffectiveRegistryURL() string {
	if c == nil {
		return ""
	}
	if v := c.Mutable.Get("registry.url"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return c.RegistryURL
}

func (c *Config) EffectiveRegistryProxy() string {
	if c == nil {
		return ""
	}
	if v := c.Mutable.Get("registry.proxy"); v != "" {
		return strings.TrimSpace(v)
	}
	return c.RegistryProxy
}

func (c *Config) EffectiveRegistryName() string {
	if c == nil {
		return ""
	}
	if v := c.Mutable.Get("registry.name"); v != "" {
		return v
	}
	return c.RegistryName
}

func (c *Config) EffectiveAllowDelete() bool {
	if c == nil {
		return false
	}
	if v := c.Mutable.Get("allow.delete"); v != "" {
		return v == "true" || v == "1"
	}
	return c.AllowDelete
}

func (c *Config) EffectiveAllowPull() bool {
	if c == nil {
		return false
	}
	if v := c.Mutable.Get("allow.pull"); v != "" {
		return v == "true" || v == "1"
	}
	return c.AllowPull
}

func (c *Config) EffectiveAllowRegistryEvents() bool {
	if c == nil {
		return false
	}
	if v := c.Mutable.Get("allow.registry_events"); v != "" {
		return v == "true" || v == "1"
	}
	return c.AllowRegistryEvents
}

func (c *Config) EffectiveStatsRetentionDays() int {
	if c == nil {
		return 365
	}
	if v := c.Mutable.Get("stats.retention.days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return c.StatsRetentionDay
}

// EffectivePullPlatforms returns the allow-list of platforms the pull executor
// uses to filter multi-arch image indexes. Empty list = "pull everything";
// non-empty = "only fetch children whose OS/architecture[/variant] appears
// in this list". Read precedence is Mutable override > env bootstrap.
//
// A platform token is "os/arch" or "os/arch/variant" (e.g. "linux/amd64",
// "linux/arm/v7"). Validation is the caller's job — UpdateConfig already
// rejects garbage tokens, and the env bootstrap is operator-controlled.
// We trim and lowercase here so legacy env values written in a hurry don't
// fail to match upstream platform strings.
func (c *Config) EffectivePullPlatforms() []string {
	raw := ""
	if c != nil && c.Mutable != nil {
		raw = c.Mutable.Get("pull.platforms")
	}
	if raw == "" && c != nil {
		raw = c.PullPlatforms
	}
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.ToLower(strings.TrimSpace(p)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (c *Config) EffectiveRegistryUsername() string {
	if c == nil {
		return ""
	}
	if v := c.Mutable.Get("registry.username"); v != "" {
		return v
	}
	return c.RegistryUsername
}

func (c *Config) EffectiveRegistryPassword() string {
	if c == nil {
		return ""
	}
	if v := c.Mutable.Get("registry.password"); v != "" {
		return v
	}
	return c.RegistryPassword
}

// EffectiveUsingAuth is true iff both username and password are configured
// (either via env or via Mutable override). registryd uses this to decide
// whether to gate /v2/* behind Basic auth.
func (c *Config) EffectiveUsingAuth() bool {
	return c.EffectiveRegistryUsername() != "" && c.EffectiveRegistryPassword() != ""
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
	PullQueueSize           int    // REGISTRY_PULL_QUEUE_SIZE, default 50
	PullHistoryRetentionDay int    // REGISTRY_PULL_HISTORY_RETENTION_DAYS, default 90
	PullPlatforms           string // REGISTRY_PULL_PLATFORMS, optional CSV of <os>/<arch>[/<variant>]; empty = all platforms (current behaviour)

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
		PullPlatforms:           strings.TrimSpace(os.Getenv("REGISTRY_PULL_PLATFORMS")),
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

	// v0.5.2: always seed Mutable so handlers can call Set/Get on it
	// without a nil-pointer guard. The server later hydrates from SQLite at
	// startup when settings rows exist.
	c.Mutable = &Mutable{values: map[string]string{}}

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
