package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Chenjinteng/cairn/internal/config"
	"github.com/Chenjinteng/cairn/internal/credentials"
	"github.com/Chenjinteng/cairn/internal/db"
	"github.com/Chenjinteng/cairn/internal/events"
	"github.com/Chenjinteng/cairn/internal/proxies"
	"github.com/Chenjinteng/cairn/internal/storage"
	"github.com/Chenjinteng/cairn/internal/version"
)

// Handlers bundles the dependencies every endpoint needs.
//
// As of v0.5, cairn IS the registry: browse/delete/inventory operate on the
// local storage, and pull jobs name their own upstream per job. There is no
// managed external registry handle to carry here any more.
//
// v0.4.0 adds the optional service handles (Vault/Proxies/DB) plus their
// startup error strings so GET /api/config can tell the UI *why* a
// feature is unavailable instead of just flipping a boolean.
//
// v0.6.32: Inventory is the shared memoization layer for /api/inventory.
// Wired in server.go so both Handlers.GetInventory and the V2 / extra
// write handlers share the same instance. Nil-safe — Handlers built
// without it (tests, partial init) just skip caching.
type Handlers struct {
	Cfg   *config.Config
	Store storage.Storage

	// Optional services; nil means unavailable at startup.
	Vault   *credentials.Vault
	Proxies *proxies.Store
	DB      *db.Db

	// Inventory (v0.6.32): short-window cache for GET /api/inventory.
	// See inventory_cache.go. Nil = no caching (always rebuild), which
	// is the safe default for tests that don't wire it.
	Inventory *InventoryCache

	// Startup failure reasons for the optional services ("" when available).
	VaultErr   string
	ProxiesErr string
	DBErr      string
}

// --- /api/config ------------------------------------------------------------

// apiErr is the {code,message} object the UI renders inline for
// credentialError / statsError.
type apiErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AppConfig is the JSON shape returned by GET /api/config. Field-for-field
// the UI's AppConfig (web/src/types.ts) — keep the two in lockstep.
type AppConfig struct {
	Name                  string   `json:"name"`
	Version               string   `json:"version"`
	// v0.5.34: 暴露 cfg.Port 让 settings page 能只读显示当前监听端口。
	// 见 settings page 「监听端口」—— 端口本身不可热改。
	Port                  int      `json:"port"`
	// v0.5.40: 宿主机侧对外端口,由 docker-compose 把 HOST_PORT env 传进来。
	// 0 = 与 Port 同值(无端口映射,直接容器访问)。UI 「监听端口」字段同时显示
	// 「容器内 Port」与「宿主机 HostPort」,让运维一眼分清两件事。
	HostPort              int      `json:"hostPort"`
	URL                   string   `json:"url"`
	Host                  string   `json:"host"`
	UsingAuth             bool     `json:"usingAuth"`
	CacheTTLSeconds       int      `json:"cacheTtlSeconds"`
	AllowDelete           bool     `json:"allowDelete"`
	AllowPull             bool     `json:"allowPull"`
	PullQueueSize         int      `json:"pullQueueSize"`
	AllowCredentials      bool     `json:"allowCredentials"`
	AllowProxies          bool     `json:"allowProxies"`
	CredentialsDir        string   `json:"credentialsDir"`
	CredentialError       *apiErr  `json:"credentialError"`
	StatsEnabled          bool     `json:"statsEnabled"`
	AllowRegistryEvents   bool     `json:"allowRegistryEvents"`
	StatsError            *apiErr  `json:"statsError"`
	NotifyTokenConfigured bool     `json:"notifyTokenConfigured"`
	StatsSince            *string  `json:"statsSince"`
	StatsRetentionDays    int      `json:"statsRetentionDays"`
	StatsIgnoreUseragents []string `json:"statsIgnoreUseragents"`

	// v0.5.1: runtime-editable settings the operator can change on the
	// settings page; backed by SQLite, override env at read time. Each
	// `*Source` field tells the UI which value is in effect right now so
	// operators can see "db" overrides vs the "env" bootstrap default.
	MutableSettings MutableSettings `json:"mutable"`
}

// MutableSettings is the v0.5.2 editable subset of AppConfig. Anything
// here can be changed from the settings page without restarting the
// service; env remains the bootstrap default on first boot. Each field
// is paired with a "source" label ("env"/"db") so the UI can show
// whether the displayed value is a live override or the bootstrap.
type MutableSettings struct {
	RegistryURL  string `json:"registryUrl"`
	RegistryName string `json:"registryName"`
	// RegistryUsername mirrors the Basic-auth user; the UI uses it to
	// show whether auth is configured. The password itself is never
	// echoed back to the client -- the UI only sends it on save.
	RegistryUsername string `json:"registryUsername"`
	// UsingAuth is true when both username + password are set (env or
	// Mutable). Drives the basic-auth toggle indicator on the UI.
	UsingAuth           bool `json:"usingAuth"`
	AllowDelete         bool `json:"allowDelete"`
	AllowPull           bool `json:"allowPull"`
	AllowRegistryEvents bool `json:"allowRegistryEvents"`
	StatsRetentionDays  int  `json:"statsRetentionDays"`
	// PullPlatforms is the platform allow-list applied to
	// multi-arch image indexes on pull. Empty string = "pull every
	// platform" (current behaviour); CSV of "<os>/<arch>[/<variant>]"
	// tokens otherwise (e.g. "linux/amd64,linux/arm64"). UI renders
	// this as a chip multi-select so operators can flip their deployment
	// from "all architectures" to "just x86+arm64" without restarting.
	PullPlatforms string `json:"pullPlatforms"`
	// PullKnownHosts (v0.5.48) is the operator-maintained CSV of
	// third-party registry base URLs ("<scheme>://host[:port]", one per
	// entry). The pull page merges it with the built-in well-known hosts
	// for image-reference parsing and the source autocomplete; entries
	// with an explicit http:// scheme also let dot-less intranet hosts
	// ("harbor.local") round-trip through the smart parser.
	PullKnownHosts string `json:"pullKnownHosts"`
}

// GetConfig returns the safe-to-expose runtime configuration.
func (h *Handlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	var statsSince *string
	if h.DB != nil {
		if day, err := h.DB.GetFirstDay(r.Context()); err == nil && day != "" {
			statsSince = &day
		}
	}

	var credErr *apiErr
	if h.Vault == nil {
		msg := h.VaultErr
		if msg == "" {
			msg = "credential vault unavailable"
		}
		credErr = &apiErr{Code: "VAULT_UNAVAILABLE", Message: msg}
	}
	var statsErr *apiErr
	if h.DB == nil {
		msg := h.DBErr
		if msg == "" {
			msg = "stats database unavailable"
		}
		statsErr = &apiErr{Code: "DB_UNAVAILABLE", Message: msg}
	}

	// Effective ignore list = env rules ∪ panel rules (panel lives in SQLite).
	ignore := events.SetBaseIgnoreUAs(h.Cfg.StatsIgnoreUserAgents())
	if h.DB != nil {
		if panel, err := h.DB.ListIgnore(r.Context()); err == nil {
			ignore = events.MergeIgnore(ignore, panel)
		}
	}

	// v0.5.2: URL/Host reflect whichever source wins right now (Mutable
	// override > env). displayURL is the v0.5 "manage itself" fallback
	// when no upstream is configured anywhere.
	//
	// v0.5.28: RegistryURL 现在存的是「裸 host:port」(协议留给 v0.6.0 的 http/https 切换),
	// 渲染 URL 时按当前协议补上 http://。v0.6.0 起这里改成读 https toggle 状态。
	//
	// v0.5.39: 回退 0.5.38 的「不再 fallback」—— 0.5.38 把 displayURL 改成空字符串,
	// 让 badge 与表单同时「未配置」,但实际部署里大多数用户希望「地址自动识别」,
	// 看到什么就改什么。恢复 r.Host 兜底,前端设置页在编辑态会用这个值预填表单
	// (见 web/src/pages/settings-page.tsx 的 draftState 初始化),做到 badge ↔
	// 表单视觉一致。
	//
	// v0.6.7 hotfix: 当 saved URL 没有端口时,append HostPort —— UpdateConfig 的
	// HOST_PORT_PATTERN 只接受裸 host(端口由 docker-compose 的 HOST_PORT env 决定),
	// 导致 displayURL = "http://<host>" 不带端口。但 docker daemon 默认走 80/443,
	// 容器内 8787 映射到宿主机非标准端口(如 10001)时,「复制 docker pull 命令」
	// 直接拷贝 host 而不带端口 → 镜像拉不到。补救:如果 saved URL 没端口 + HostPort
	// 与 Port 不同(即存在端口映射),在前面构造 URL 时拼上 HostPort。
	displayURL := h.Cfg.RegistryURL()
	if displayURL != "" {
		if !hasExplicitPort(displayURL) && h.Cfg.HostPort != 0 && h.Cfg.HostPort != h.Cfg.Port {
			displayURL = fmt.Sprintf("%s:%d", displayURL, h.Cfg.HostPort)
		}
		displayURL = "http://" + displayURL
	} else {
		displayURL = "http://" + r.Host // cairn manages itself; no upstream set; r.Host 自带端口
	}
	writeJSON(w, http.StatusOK, AppConfig{
		// v0.5.4: every field the settings page can edit is read through
		// Effective*() here, so the top-level view and the `mutable` block
		// can no longer disagree and the UI reflects a saved override
		// immediately instead of the env bootstrap value.
		Name:                  h.Cfg.RegistryName(),
		Version:               version.Version,
		// v0.5.34: 暴露 cfg.Port 让 settings page 能只读显示当前监听端口。
		// 端口本身不可热改(改需要 graceful restart + 改 docker-compose 的
		// HOST_PORT 端口映射),UI 只显示不改 —— 见 settings page 「监听端口」字段。
		Port:                  h.Cfg.Port,
		HostPort:              h.Cfg.HostPort,
		URL:                   displayURL,
		Host:                  hostOf(displayURL),
		UsingAuth:             h.Cfg.UsingAuth(),
		AllowDelete:           h.Cfg.AllowDelete(),
		AllowPull:             h.Cfg.AllowPull(),
		AllowCredentials:      h.Vault != nil,
		AllowProxies:          h.Proxies != nil,
		CredentialsDir:        h.Cfg.CredentialsDir,
		CredentialError:       credErr,
		StatsEnabled:          h.Cfg.AllowRegistryEvents() && h.DB != nil,
		AllowRegistryEvents:   h.Cfg.AllowRegistryEvents(),
		StatsError:            statsErr,
		NotifyTokenConfigured: h.Cfg.NotifyToken() != "",
		StatsSince:            statsSince,
		StatsRetentionDays:    h.Cfg.StatsRetentionDays(),
		StatsIgnoreUseragents: ignore,
		MutableSettings: MutableSettings{
			RegistryURL:         h.Cfg.RegistryURL(),
			RegistryName:        h.Cfg.RegistryName(),
			RegistryUsername:    h.Cfg.RegistryUsername(),
			UsingAuth:           h.Cfg.UsingAuth(),
			AllowDelete:         h.Cfg.AllowDelete(),
			AllowPull:           h.Cfg.AllowPull(),
			AllowRegistryEvents: h.Cfg.AllowRegistryEvents(),
			StatsRetentionDays:  h.Cfg.StatsRetentionDays(),
			PullPlatforms:       strings.Join(h.Cfg.PullPlatforms(), ","),
			PullKnownHosts:      h.Cfg.PullKnownHosts(),
		},
	})
}

// UpdateConfig persists editable settings (v0.5.2: anything in
// config.MutableKeys). URL: PATCH /api/config
// body: {"mutable": {"<key>": "<value>", ...}}
//
// 503 if the SQLite backing store is unavailable -- without it, the new
// value would not survive a restart. 400 on any key not in MutableKeys
// or failing type validation; accepted types are bool / int / string /
// url (the latter must start with http:// or https://).
func (h *Handlers) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		reason := h.DBErr
		if reason == "" {
			reason = "settings database unavailable"
		}
		writeError(w, r, http.StatusServiceUnavailable, errors.New(reason))
		return
	}
	var body struct {
		Mutable map[string]string `json:"mutable"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if len(body.Mutable) == 0 {
		writeError(w, r, http.StatusBadRequest, errors.New("mutable payload is empty"))
		return
	}
	for key, rawVal := range body.Mutable {
		if _, ok := config.MutableKeysSet[key]; !ok {
			writeError(w, r, http.StatusBadRequest,
				errors.New("unknown setting: "+key+" (allowed: "+strings.Join(config.MutableKeys, ", ")+")"))
			return
		}
		val := strings.TrimSpace(rawVal)
		switch config.MutableFieldType[key] {
		case "url":
			// v0.5.28: 字段语义改成「裸 host:port」。协议留给 v0.6.0 的 http/https 切换,
			// 现在写死 http 所以这里只接受 host[:port],不允许任何协议前缀。
			if val != "" {
				if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") {
					writeError(w, r, http.StatusBadRequest,
						errors.New(key+" 不要带协议前缀,只填 host:port(协议留给 v0.6.0 的 http/https 切换)"))
					return
				}
				if !isValidHostPort(val) {
					writeError(w, r, http.StatusBadRequest,
						errors.New(key+" 必须是合法 host(字母数字 . _ -,只接受裸 host 不要带端口或协议;端口由 HOST_PORT env 决定)"))
					return
				}
			}
		case "int":
			if n, err := strconv.Atoi(val); err != nil || n <= 0 {
				writeError(w, r, http.StatusBadRequest,
					errors.New(key+" must be a positive integer"))
				return
			}
		case "bool":
			if val != "true" && val != "false" && val != "1" && val != "0" {
				writeError(w, r, http.StatusBadRequest,
					errors.New(key+" must be true/false"))
				return
			}
		case "stringcsv":
			// Each token is <os>/<arch>[/<variant>] with empty
			// meaning "all platforms". Reject garbage here so a typo
			// ("amd64" with no "linux/" prefix) doesn't silently turn
			// into a pull-no-op at runtime.
			for _, tok := range strings.Split(val, ",") {
				tok = strings.TrimSpace(tok)
				if tok == "" {
					continue
				}
				parts := strings.Split(tok, "/")
				if len(parts) < 2 || len(parts) > 3 {
					writeError(w, r, http.StatusBadRequest,
						errors.New(key+" token "+tok+" must be <os>/<arch>[/<variant>] (e.g. linux/amd64 or linux/arm/v7)"))
					return
				}
				for _, seg := range parts {
					if seg == "" || strings.ContainsAny(seg, " \t\n") {
						writeError(w, r, http.StatusBadRequest,
							errors.New(key+" token "+tok+" has an empty segment"))
						return
					}
				}
			}
		case "hostcsv":
			// v0.5.48: CSV of third-party registry hosts. Each entry is
			// normalised to "<scheme>://host[:port]" (bare entries get the
			// same protocol guess as the frontend parser: no port / 443 /
			// 8443 / 5000 → https, anything else → http). Garbage (paths,
			// credentials, non-http schemes, empty hosts) is rejected here
			// so the pull page never sees half-parsed entries.
			normalized, err := normalizeHostCSV(val)
			if err != nil {
				writeError(w, r, http.StatusBadRequest,
					errors.New(key+": "+err.Error()))
				return
			}
			val = normalized
		}
		// Persist + apply. Empty string means "clear the override".
		if val == "" {
			if err := h.DB.DeleteSetting(r.Context(), key); err != nil {
				writeError(w, r, http.StatusInternalServerError, err)
				return
			}
		} else {
			if err := h.DB.SetSetting(r.Context(), key, val); err != nil {
				writeError(w, r, http.StatusInternalServerError, err)
				return
			}
		}
		if h.Cfg.Mutable != nil {
			h.Cfg.Mutable.Set(key, val)
		}
	}
	// Return the freshly-applied view so the UI updates without a refetch.
	h.GetConfig(w, r)
}

// Probe checks the local registry is readable and reports its identity.
// Response shape: the UI's { apiVersion, host }.
func (h *Handlers) Probe(w http.ResponseWriter, r *http.Request) {
	if _, err := h.Store.Repositories(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"apiVersion": "2",
		"host":       hostOf(h.registryURL(r)),
	})
}

// registryURL is the externally visible registry address.
//
// v0.5 flips the precedence: cairn manages *itself*, so the address the
// client actually used to reach us is the truth. The Host header wins
// (honouring X-Forwarded-Proto / X-Forwarded-Host behind a reverse proxy);
// REGISTRY_URL is now only a fallback for setups where the panel is reached
// on an address that must not be advertised to `docker` (e.g. a compose
// service name or a loopback bind).
func (h *Handlers) registryURL(r *http.Request) string {
	if r != nil && r.Host != "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if proto := forwardedProto(r); proto != "" {
			scheme = proto
		}
		host := r.Host
		if fh := firstCSV(r.Header.Get("X-Forwarded-Host")); fh != "" {
			host = fh
		}
		return scheme + "://" + host
	}
	return strings.TrimSuffix(h.Cfg.RegistryURL(), "/")
}

// forwardedProto returns "http" / "https" when a reverse proxy supplied
// X-Forwarded-Proto. The header may be a comma-separated hop list; the
// client-facing (first) entry wins.
func forwardedProto(r *http.Request) string {
	if r == nil {
		return ""
	}
	switch strings.ToLower(firstCSV(r.Header.Get("X-Forwarded-Proto"))) {
	case "http", "https":
		return strings.ToLower(firstCSV(r.Header.Get("X-Forwarded-Proto")))
	}
	return ""
}

// firstCSV returns the first element of a comma-separated header value.
func firstCSV(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// hostOf extracts the host[:port] part of a registry URL (best-effort).
func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// hasExplicitPort reports whether value carries a `:port` suffix. The
// current UpdateConfig validation (HOST_PORT_PATTERN) only accepts bare
// host, so freshly-saved values never have a port — but legacy values
// from before v0.5.42 might still carry one or even a scheme prefix
// (e.g., "http://registry.example.com:8787"); handle both so the v0.6.7
// port-append fix doesn't double-up "http://host:hostPort:hostPort".
//
// Distinguishes scheme `:` (e.g. "http://") from port `:` (followed by
// digits) by stripping the scheme first, then checking the last `:`.
func hasExplicitPort(value string) bool {
	v := value
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.LastIndex(v, ":"); i >= 0 {
		rest := v[i+1:]
		if rest != "" {
			if _, err := strconv.Atoi(rest); err == nil {
				return true
			}
		}
	}
	return false
}

// v0.5.28 + v0.5.42: 仓库地址 = 裸 host(只 IP 或域名),不带 http(s):// 也不带端口。
// 协议留给后续 v0.6.0 的 http/https 切换;现在写死 http。
// 端口不再接受 —— 端口由 docker-compose 的 HOST_PORT 决定,UI 在「监听端口」字段单独显示。
// host 段允许字母数字 . _ -,首位与末位必须字母数字(避免 `-.foo` / `foo.-`)。
var hostPortRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?$`)

func isValidHostPort(val string) bool {
	return hostPortRe.MatchString(val)
}

// --- inventory ---------------------------------------------------------------

// Inventory is the JSON shape returned by GET /api/inventory. Field-for-field
// the UI's Inventory type. (v0.6.10: prior POST /api/refresh alias was removed —
// it was identical to this endpoint and conflated "refresh view" with
// "rescan registry" in the UI.)
type Inventory struct {
	RefreshedAt  time.Time        `json:"refreshedAt"`
	APIVersion   string           `json:"apiVersion"`
	Host         string           `json:"host"`
	DurationMs   int64            `json:"durationMs"`
	Truncated    bool             `json:"truncated"`
	Repositories []Repository     `json:"repositories"`
	Errors       []InventoryError `json:"errors"`
	ErrorCount   int              `json:"errorCount"`
}

// Repository is one row of the images list.
type Repository struct {
	Name      string    `json:"name"`
	Tags      []TagInfo `json:"tags"`
	TagCount  int       `json:"tagCount"`
	TotalSize int64     `json:"totalSize"`
}

// TagInfo is one tag of a repository. Size is the sum of the config blob
// and all layers (index manifests sum their platforms, best-effort).
type TagInfo struct {
	Tag           string     `json:"tag"`
	Digest        string     `json:"digest"`
	Size          int64      `json:"size"`
	LayerCount    int        `json:"layerCount"`
	Architecture  string     `json:"architecture"`
	OS            string     `json:"os"`
	PlatformCount int        `json:"platformCount"`
	CreatedAt     *time.Time `json:"createdAt"`
}

// InventoryError records one tag (or whole-repo listing) that failed to
// read; the rest of the inventory still renders.
type InventoryError struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

// GetInventory returns the full inventory (built from local storage).
//
// v0.6.32: results are memoized in h.Inventory (InventoryCache) for
// 30s, with every write path (DeleteRepository / DeleteManifestByDigest /
// DeleteTag / RunGC / the V2 protocol's PUT / DELETE / upload-complete)
// calling Invalidate on success. A miss falls through to buildInventory
// and the fresh snapshot is stored for the next reader.
func (h *Handlers) GetInventory(w http.ResponseWriter, r *http.Request) {
	host := hostOf(h.registryURL(r))
	inv, err := h.Inventory.GetOrBuild(host, func() (*Inventory, error) {
		return buildInventory(r.Context(), h.Store, host)
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}
// buildInventory walks local storage to assemble the inventory.
func buildInventory(ctx context.Context, store storage.Storage, host string) (*Inventory, error) {
	start := time.Now()
	inv := &Inventory{
		RefreshedAt:  start.UTC(),
		APIVersion:   "2",
		Host:         host,
		Repositories: []Repository{},
		Errors:       []InventoryError{},
	}
	repos, err := store.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(repos)
	for _, name := range repos {
		repoView, errs := buildRepoView(ctx, store, name)
		inv.Repositories = append(inv.Repositories, repoView)
		inv.Errors = append(inv.Errors, errs...)
	}
	inv.DurationMs = time.Since(start).Milliseconds()
	inv.ErrorCount = len(inv.Errors)
	return inv, nil
}

// buildRepoView assembles one repository's tag list. Per-tag failures are
// returned as InventoryError entries instead of failing the whole repo.
func buildRepoView(ctx context.Context, store storage.Storage, name string) (Repository, []InventoryError) {
	repo := Repository{Name: name, Tags: []TagInfo{}}
	var errs []InventoryError
	tags, err := store.Tags(ctx, name)
	if err != nil {
		errs = append(errs, InventoryError{
			Repository: name, Tag: "(list)", Code: "TAGS_LIST", Message: err.Error(),
		})
		return repo, errs
	}
	sort.Strings(tags)
	for _, t := range tags {
		info, err := buildTagInfo(ctx, store, name, t)
		if err != nil {
			errs = append(errs, InventoryError{
				Repository: name, Tag: t, Code: "MANIFEST_READ", Message: err.Error(),
			})
			continue
		}
		repo.Tags = append(repo.Tags, info)
		repo.TotalSize += info.Size
	}
	repo.TagCount = len(repo.Tags)
	return repo, nil2Empty(errs)
}

func nil2Empty(errs []InventoryError) []InventoryError {
	if errs == nil {
		return []InventoryError{}
	}
	return errs
}

// manifestDoc is the union of the manifest JSON shapes we understand:
// schema2 image manifest (config + layers) and image index (manifests).
type manifestDoc struct {
	SchemaVersion int `json:"schemaVersion"`
	Config        *struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

// imageConfigDoc is the (small) subset of the OCI/Docker image config blob
// we surface: platform identity + build time.
type imageConfigDoc struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Created      string `json:"created"`
}

// buildTagInfo reads one tag's manifest and extracts size / platform /
// created metadata. Everything beyond the digest is best-effort: an
// unparseable manifest still yields a usable row.
func buildTagInfo(ctx context.Context, store storage.Storage, repo, tag string) (TagInfo, error) {
	digest, err := store.TagDigest(ctx, repo, tag)
	if err != nil {
		return TagInfo{}, err
	}
	m, err := store.GetManifest(ctx, repo, tag)
	if err != nil {
		return TagInfo{}, err
	}
	info := TagInfo{Tag: tag, Digest: digest, PlatformCount: 1}
	if !m.CreatedAt.IsZero() {
		ct := m.CreatedAt.UTC()
		info.CreatedAt = &ct
	}

	var doc manifestDoc
	if err := json.Unmarshal(m.Body, &doc); err != nil {
		return info, nil // opaque manifest: digest-only row
	}

	if len(doc.Manifests) > 0 {
		// Image index: aggregate platform identities and sum sub-manifest sizes.
		info.PlatformCount = len(doc.Manifests)
		archSeen := map[string]struct{}{}
		osSeen := map[string]struct{}{}
		archs := []string{}
		oss := []string{}
		for _, m := range doc.Manifests {
			if a := m.Platform.Architecture; a != "" {
				if _, ok := archSeen[a]; !ok {
					archSeen[a] = struct{}{}
					archs = append(archs, a)
				}
			}
			if o := m.Platform.OS; o != "" {
				if _, ok := osSeen[o]; !ok {
					osSeen[o] = struct{}{}
					oss = append(oss, o)
				}
			}
		}
		info.Architecture = strings.Join(archs, ",")
		info.OS = strings.Join(oss, ",")
		size, layers := sumIndexMembers(ctx, store, repo, doc)
		info.Size = size
		info.LayerCount = layers
		return info, nil
	}

	if doc.Config != nil {
		info.Size += doc.Config.Size
		// Best-effort: read the config blob for arch/os/created.
		if cfgDoc, ok := readImageConfig(ctx, store, repo, doc.Config.Digest); ok {
			if cfgDoc.Architecture != "" {
				info.Architecture = cfgDoc.Architecture
			}
			if cfgDoc.OS != "" {
				info.OS = cfgDoc.OS
			}
			if ts, err := time.Parse(time.RFC3339, cfgDoc.Created); err == nil {
				t2 := ts.UTC()
				info.CreatedAt = &t2
			}
		}
	}
	for _, l := range doc.Layers {
		info.Size += l.Size
	}
	info.LayerCount = len(doc.Layers)
	return info, nil
}

// readImageConfig fetches and parses a config blob (capped at 4 MiB).
func readImageConfig(ctx context.Context, store storage.Storage, repo, digest string) (imageConfigDoc, bool) {
	var out imageConfigDoc
	if digest == "" {
		return out, false
	}
	rc, _, err := store.GetBlob(ctx, repo, digest)
	if err != nil {
		return out, false
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 4<<20))
	if err != nil {
		return out, false
	}
	if json.Unmarshal(body, &out) != nil {
		return out, false
	}
	return out, true
}

// sumIndexMembers walks an index's sub-manifests (one level; nested
// indexes contribute 0) to total the image size, and reports the layer
// count of the first readable platform manifest. Capped at 16 members so
// a pathological index can't stall the scan.
func sumIndexMembers(ctx context.Context, store storage.Storage, repo string, doc manifestDoc) (size int64, layerCount int) {
	limit := len(doc.Manifests)
	if limit > 16 {
		limit = 16
	}
	first := true
	for i := 0; i < limit; i++ {
		sub, err := store.GetManifest(ctx, repo, doc.Manifests[i].Digest)
		if err != nil {
			continue
		}
		var sd manifestDoc
		if json.Unmarshal(sub.Body, &sd) != nil {
			continue
		}
		if sd.Config != nil {
			size += sd.Config.Size
		}
		for _, l := range sd.Layers {
			size += l.Size
		}
		if first {
			layerCount = len(sd.Layers)
			first = false
		}
	}
	return size, layerCount
}

// --- delete ------------------------------------------------------------------

// DeleteTag removes one tag (and every sibling tag pointing at the same
// digest) from local storage.
//
// URL: DELETE /api/tags?repository=<name>&tag=<tag>
//
// 403 if allow.delete is false -- either the env bootstrap
// (REGISTRY_ALLOW_DELETE) or a runtime override saved from the settings
// page. 400 if a parameter is missing.
// 200 returns the UI's DeleteTagPayload: which tag was asked for, the
// digest it resolved to, ALL tags that shared the digest (the blast
// radius, computed before deletion), and the rebuilt repository view.
//
// Note: deleting a manifest only removes references — registryd's storage
// keeps the blobs until a GC pass runs (out of scope for v0.4.0).
func (h *Handlers) DeleteTag(w http.ResponseWriter, r *http.Request) {
	if !h.Cfg.AllowDelete() {
		writeError(w, r, http.StatusForbidden, errDeleteDisabled)
		return
	}
	repo := r.URL.Query().Get("repository")
	tag := r.URL.Query().Get("tag")
	if repo == "" || tag == "" {
		writeError(w, r, http.StatusBadRequest, errMissingParam)
		return
	}
	digest, err := h.Store.TagDigest(r.Context(), repo, tag)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// Blast radius BEFORE deleting: every tag currently on this digest.
	affected := tagsForDigest(r.Context(), h.Store, repo, digest)
	if _, err := h.Store.DeleteManifest(r.Context(), repo, digest); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// v0.6.32: drop the /api/inventory cache so the next read sees the
	// deletion. Nil-safe — Inventory is unset in tests.
	if h.Inventory != nil {
		h.Inventory.Invalidate()
	}
	repoView, _ := buildRepoView(r.Context(), h.Store, repo)
	writeJSON(w, http.StatusOK, map[string]any{
		"deletedTag":   tag,
		"digest":       digest,
		"affectedTags": affected,
		"repository":   repoView,
	})
}

// GetManifest returns the manifest for a single tag.
func (h *Handlers) GetManifest(w http.ResponseWriter, r *http.Request) {
	repo := chiURLParam(r, "repo")
	tag := chiURLParam(r, "tag")
	if repo == "" || tag == "" {
		writeError(w, r, http.StatusBadRequest, errMissingParam)
		return
	}
	m, err := h.Store.GetManifest(r.Context(), repo, tag)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	m.Repo = repo
	writeJSON(w, http.StatusOK, m)
}

// tagsForDigest lists all tags in repo that currently point at digest.
// Best-effort: missing-tag fetches are skipped silently.
func tagsForDigest(ctx context.Context, store storage.Storage, repo, digest string) []string {
	tags, err := store.Tags(ctx, repo)
	if err != nil {
		return []string{}
	}
	hits := []string{}
	for _, t := range tags {
		d, err := store.TagDigest(ctx, repo, t)
		if err != nil {
			continue
		}
		if d == digest {
			hits = append(hits, t)
		}
	}
	sort.Strings(hits)
	return hits
}
