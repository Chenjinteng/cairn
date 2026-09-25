// Package config owns the cairn runtime configuration.
//
// v0.5.9: business configuration is **single-sourced from the SQLite
// `settings` table** (which the UI edits via PATCH /api/config). The only
// env that remains are infrastructure / boot-only:
//
//   - PORT                        (HTTP listener port)
//   - REGISTRY_CREDENTIALS_DIR    (DATA_DIR for SQLite + credentials.json)
//   - REGISTRY_STORAGE_DIR        (blob / manifest / tag on-disk path)
//   - REGISTRY_CREDENTIAL_KEY     (AES-256-GCM key for the vault)
//   - GO_HUB_ENV                  (prod / dev log verbosity)
//
// Everything else (registry URL, proxy, auth, name, allow.delete,
// allow.pull, pull.platforms, notify token, allow.registry_events,
// stats.retention.days, stats.ignore_useragents, pull.history.retention.days)
// has exactly one source: the panel. boot hydrates from SQLite into
// cfg.Mutable, runtime read-paths go through the Config helper methods
// (c.RegistryURL / c.AllowDelete / ...) which resolve Mutable first and
// fall back to a hardcoded default. There is no env fallback for these.
//
// This file no longer reads the legacy REGISTRY_* business env. If a
// container still has them set in .env, they are ignored — the panel is
// authoritative. That makes deploys idempotent (changing .env doesn't
// change behaviour unless infrastructure env change) and removes a class
// of "which value wins?" confusion operators hit in v0.5.x.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	"registry.url",                // cairn's own exposed address; empty = pull tasks fall back to Docker Hub
	"registry.name",               // display name in UI; default "镜像仓库"
	"registry.username",           // Basic auth user for /v2/*; empty = anonymous
	"registry.password",           // Basic auth pass; stored in SQLite settings, UI never echoes
	"allow.delete",                // permit DELETE on /v2/* + GC; default true
	"allow.pull",                  // permit /api/pull/* writes; default true
	"allow.registry_events",       // heat ingestion on/off (kill switch); default true
	"stats.retention.days",        // days of activity_daily to retain; default 365
	"stats.ignore_useragents",     // CSV; events whose UA substring-matches are folded into ignored
	"pull.platforms",              // CSV of <os>/<arch>[/<variant>]; empty = pull every platform
	"pull.history.retention.days", // days of pull_jobs to retain; default 90
	"notify.token",                // HMAC shared secret for /api/events external webhook; empty = 401 fail-closed
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

// RegistryURL is accessor sugar for the most-read mutable: the pull
// source chain reads it through here so an in-flight pull picks up a
// panel change immediately. New code should call c.RegistryURL() (the
// Config helper) when it has a Config in hand; this is only for callers
// that already hold a *Mutable (orchestrator, executor).
func (m *Mutable) RegistryURL() string { return m.Get("registry.url") }

// Read-paths below resolve Mutable override first, then a hardcoded
// default. There is no env fallback — the panel is the single source of
// truth. Each helper is the canonical read site: callers should never
// reach for the legacy `Config.RegistryURL` / `Config.AllowDelete` fields
// directly because those hold *defaults only* (no env, no Mutable).
//
// `Mutable` carries the persisted overrides (loaded from SQLite at boot
// via server.go hydrate loop). When Mutable is nil — e.g. tests that build
// a Config without Load() — helpers gracefully fall through to defaults.

func (c *Config) RegistryURL() string {
	if c == nil || c.Mutable == nil {
		return ""
	}
	return strings.TrimRight(c.Mutable.Get("registry.url"), "/")
}

func (c *Config) RegistryName() string {
	if c != nil && c.Mutable != nil {
		if v := c.Mutable.Get("registry.name"); v != "" {
			return v
		}
	}
	return "镜像仓库"
}

func (c *Config) AllowDelete() bool {
	if c != nil && c.Mutable != nil {
		if v := c.Mutable.Get("allow.delete"); v != "" {
			return v == "true" || v == "1"
		}
	}
	return true
}

func (c *Config) AllowPull() bool {
	if c != nil && c.Mutable != nil {
		if v := c.Mutable.Get("allow.pull"); v != "" {
			return v == "true" || v == "1"
		}
	}
	return true
}

func (c *Config) AllowRegistryEvents() bool {
	if c != nil && c.Mutable != nil {
		if v := c.Mutable.Get("allow.registry_events"); v != "" {
			return v == "true" || v == "1"
		}
	}
	return true
}

func (c *Config) StatsRetentionDays() int {
	if c != nil && c.Mutable != nil {
		if v := c.Mutable.Get("stats.retention.days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return 365
}

func (c *Config) PullPlatforms() []string {
	var raw string
	if c != nil && c.Mutable != nil {
		raw = c.Mutable.Get("pull.platforms")
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

func (c *Config) RegistryUsername() string {
	if c == nil || c.Mutable == nil {
		return ""
	}
	return c.Mutable.Get("registry.username")
}

func (c *Config) RegistryPassword() string {
	if c == nil || c.Mutable == nil {
		return ""
	}
	return c.Mutable.Get("registry.password")
}

func (c *Config) NotifyToken() string {
	if c == nil || c.Mutable == nil {
		return ""
	}
	return c.Mutable.Get("notify.token")
}

func (c *Config) UsingAuth() bool {
	return c.RegistryUsername() != "" && c.RegistryPassword() != ""
}

func (c *Config) PullHistoryRetentionDays() int {
	if c != nil && c.Mutable != nil {
		if v := c.Mutable.Get("pull.history.retention.days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return 90
}

func (c *Config) StatsIgnoreUserAgents() []string {
	if c == nil || c.Mutable == nil {
		return nil
	}
	raw := c.Mutable.Get("stats.ignore_useragents")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Config is the resolved runtime configuration for cairn.
//
// Field semantics mirror registry-manager so we can swap .env.example wholesale.
// Fields are populated in Load(); no defaults are applied at struct-literal time.
type Config struct {
	// HTTP listener. Env only — boot must restart to change.
	Port int // PORT, default 8787

	// Infrastructure-only env: storage location + vault key. None of
	// these flow through the panel; boot fails fast if any required
	// field is missing. Mutable is the only edit channel for everything
	// else.
	//
	// StorageDir is where the embedded /v2 endpoint keeps blobs,
	// manifests and upload sessions. Mount a dedicated volume here in
	// containers: every pulled and pushed image lives on this path,
	// and recreating the container without it loses them.
	StorageDir string // REGISTRY_STORAGE_DIR, default "<REGISTRY_CREDENTIALS_DIR>/registry"

	// Credential vault
	CredentialKey  string // REGISTRY_CREDENTIAL_KEY, strongly recommended; absence disables vault (pulls still work anonymously)
	CredentialsDir string // REGISTRY_CREDENTIALS_DIR, default /app/data

	// Dev convenience
	Env string // "dev" / "prod", default "prod"

	// Mutable is the live override registry the UI writes to via PATCH
	// /api/config. server.go hydrates from SQLite at boot; read-paths
	// use the typed helpers above (c.RegistryURL / c.AllowDelete / ...)
	// which all resolve Mutable first and fall back to a hardcoded
	// default.
	Mutable *Mutable
}

// Load reads configuration from process env. Only infrastructure env
// (PORT, REGISTRY_CREDENTIALS_DIR, REGISTRY_STORAGE_DIR, REGISTRY_CREDENTIAL_KEY,
// GO_HUB_ENV) are honoured here; business fields come from Mutable, hydrated
// later by server.go from SQLite. There is no env fallback for business
// fields.
func Load() (*Config, error) {
	c := &Config{
		Port:           intEnv("PORT", 8787),
		CredentialKey:  os.Getenv("REGISTRY_CREDENTIAL_KEY"),
		CredentialsDir: strEnv("REGISTRY_CREDENTIALS_DIR", "/app/data"),
		Env:            strEnv("GO_HUB_ENV", "prod"),
	}
	// Keep the registry data next to the credential vault by default
	// (one volume covers both), while still allowing an explicit path /
	// its own volume.
	c.StorageDir = strEnv("REGISTRY_STORAGE_DIR", filepath.Join(c.CredentialsDir, "registry"))

	// v0.5.2: always seed Mutable so handlers can call Set/Get on it
	// without a nil-pointer guard. The server later hydrates from SQLite
	// at startup when settings rows exist.
	c.Mutable = &Mutable{values: map[string]string{}}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("PORT out of range: %d", c.Port)
	}
	if c.StorageDir == "" {
		return fmt.Errorf("REGISTRY_STORAGE_DIR resolved to an empty path")
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
