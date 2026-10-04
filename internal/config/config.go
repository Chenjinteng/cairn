// Package config owns the cairn runtime configuration.
//
// v0.5.9: business configuration is **single-sourced from the SQLite
// `settings` table** (which the UI edits via PATCH /api/config). The only
// env that remains are infrastructure / boot-only:
//
//   - PORT                        (HTTP listener port; container-internal, default 8787)
//   - HOST_PORT                   (宿主机对外端口;docker-compose 的 ports 左侧;v0.5.40 新增,
//                                  0 或未设 = 与 PORT 视为同一端口)
//   - REGISTRY_CREDENTIAL_KEY     (AES-256-GCM key for the vault)
//   - CAIRN_ENV                   (prod / dev log verbosity; v0.5.23 由 GO_HUB_ENV 改名)
//
// Container-internal paths are compile-time constants (DataDirPath,
// StorageDirPath) rather than knobs: where a file sits inside the
// container is an implementation detail. Relocate the host side with a
// bind mount on /app/data instead of with env.
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
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mutable holds runtime-editable settings persisted to SQLite. v0.5.2
// generalises v0.5.1's single-field Mutable into a key->value map so the
// settings page can edit any field in MutableKeys without code changes.
// Values come from the SQLite settings table (hydrated at boot); the
// settings page writes into MutableKeys and those writes survive restarts.
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
// would need a restart to take effect (e.g. PORT, the container-internal
// paths, REGISTRY_CREDENTIAL_KEY).
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
	"pull.known_hosts",            // CSV of third-party registry hosts the UI should recognise; empty = built-in list only
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
	"pull.known_hosts":      "hostcsv",
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

// PullKnownHosts returns the raw CSV of operator-added third-party registry
// hosts (v0.5.48). Values are normalised on write (see api.normalizeHostCSV):
// each entry is "<scheme>://host[:port]", deduped by host. Empty string means
// "only the built-in known hosts".
func (c *Config) PullKnownHosts() string {
	if c == nil || c.Mutable == nil {
		return ""
	}
	return c.Mutable.Get("pull.known_hosts")
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

// Container-internal paths are compile-time constants, not knobs. A path
// inside the container is an implementation detail; the host side is moved
// with a bind mount (see docker-compose.yml), never with env.
const (
	// DataDirPath holds the credential vault, the SQLite databases and the
	// embedded registry storage.
	DataDirPath = "/app/data"
	// StorageDirPath is where the embedded /v2 endpoint keeps blobs,
	// manifests and upload sessions. It nests inside DataDirPath so a single
	// bind mount carries both the metadata and every pulled/pushed image.
	StorageDirPath = DataDirPath + "/registry"
)

// Day-scaled durations.
//
// v0.7.37 (review §1.2): `24 * time.Hour` was hardcoded in two places
// (server.go's retentionLoop and storage/filesystem.go's upload-session
// sweep) with no shared source. They happen to match today; sourcing both
// from DayInterval keeps a future change to be one-line.
//
// These are not in Mutable — adjusting them is operator-grade (changes
// resource reclamation cadence) and there is no observed demand for a UI
// knob. If that changes, see docs/review/v0.7.36-cross-module.md §1.2
// for the full UI-configurable Timeouts design.
const (
	// DayInterval is the canonical "1 day" duration. Used as the base
	// for both the retention sweep interval and the upload-session TTL.
	DayInterval = 24 * time.Hour

	// RetentionSweepInterval is how often retentionLoop wakes up to
	// prune stats / events / pull history past the configured retention
	// window. (server.go: retentionLoop)
	RetentionSweepInterval = DayInterval

	// UploadSessionTTL bounds how long an abandoned cross-mount upload
	// session can live before the storage sweep reclaims it. (storage/
	// filesystem.go: sweepUploads)
	UploadSessionTTL = DayInterval
)

// Config is the resolved runtime configuration for cairn.
//
// Fields are populated in Load(); no defaults are applied at struct-literal time.
type Config struct {
	// HTTP listener. Env only — boot must restart to change.
	Port int // PORT, default 8787

	// v0.5.40: HOST_PORT — 宿主机侧对外端口。cairn 进程跑在容器内,本来
	// 看不到 docker-compose 的 ports 映射,UI 显示的「监听端口」永远只能
	// 是容器内 8787。现在通过 docker-compose 的 environment: 块把
	// HOST_PORT 传进来,UI 可以同时展示「容器内 / 宿主机」两个端口。
	// 0 或未设 = 与 Port 同值(直接容器访问,无端口映射)。
	HostPort int // HOST_PORT, default 0 → use Port

	// Infrastructure-only: storage location + vault key. Neither flows
	// through the panel; Mutable is the only edit channel for everything
	// else.
	//
	// StorageDir and CredentialsDir are always the StorageDirPath /
	// DataDirPath constants for Load()-produced configs. They stay
	// settable so tests can build a Config by hand, but are never read
	// from env.
	StorageDir string // always StorageDirPath

	// Credential vault
	CredentialKey  string // REGISTRY_CREDENTIAL_KEY, strongly recommended; absence disables vault (pulls still work anonymously)
	CredentialsDir string // always DataDirPath

	// Dev convenience
	Env string // "dev" / "prod", default "prod"

	// Mutable is the live override registry the UI writes to via PATCH
	// /api/config. server.go hydrates from SQLite at boot; read-paths
	// use the typed helpers above (c.RegistryURL / c.AllowDelete / ...)
	// which all resolve Mutable first and fall back to a hardcoded
	// default.
	Mutable *Mutable
}

// Load reads configuration from process env. Only four infrastructure env
// are honoured: PORT, HOST_PORT, REGISTRY_CREDENTIAL_KEY and CAIRN_ENV.
// Container paths are compile-time constants (DataDirPath / StorageDirPath) —
// the host side is relocated with a bind mount, not with env. Business fields
// come from Mutable, hydrated later by server.go from SQLite. There is no env
// fallback for business fields.
//
// v0.5.23: GO_HUB_ENV → CAIRN_ENV。改名不引入兼容期 —— 见 CHANGELOG
// 该条目「未触动项」段的说明。
// v0.5.40: 新增 HOST_PORT —— docker-compose 把 ${HOST_PORT:-8787} 传进来,
// UI 才能同时显示容器内监听端口和宿主机对外端口。
func Load() (*Config, error) {
	c := &Config{
		Port:           intEnv("PORT", 8787),
		HostPort:       intEnv("HOST_PORT", 0),
		CredentialKey:  os.Getenv("REGISTRY_CREDENTIAL_KEY"),
		CredentialsDir: DataDirPath,
		StorageDir:     StorageDirPath,
		Env:            strEnv("CAIRN_ENV", "prod"),
	}

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
	// v0.5.40: HOST_PORT 允许 0(「与容器同端口」快捷写法),其余走端口范围检查。
	if c.HostPort < 0 || c.HostPort > 65535 {
		return fmt.Errorf("HOST_PORT out of range: %d", c.HostPort)
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
