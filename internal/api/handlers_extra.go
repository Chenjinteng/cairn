package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"cairn/internal/config"
	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/events"
	"cairn/internal/proxies"
	"cairn/internal/pull"
	"cairn/internal/registry"
	"cairn/internal/storage"
	"cairn/internal/version"
)

// ExtraHandlers bundles deps that aren't in Handlers yet (so the v0.1
// handlers.go stays small and reviewable).
type ExtraHandlers struct {
	Cfg *ConfigExtras
	// Full (v0.5.1) is the live Config pointer; use it for any field that
	// has a runtime source (MutableRegistryURL etc). Cfg above is the
	// immutable subset for the API surface.
	Full     *config.Config
	Executor *pull.Executor
	Vault    *credentials.Vault
	Proxies  *proxies.Store
	DB       *db.Db
	Events   *events.Handler
	Store    storage.Storage

	// v0.4.0: startup failure reasons for the optional stores ("" when
	// available). Surfaced by the 503 guards so the UI can explain *why*
	// a panel is disabled.
	VaultErr   string
	ProxiesErr string
	DBErr      string
}

// ConfigExtras holds the ENV-ONLY config the extra handlers need.
//
// v0.5.4 dropped AllowPull / AllowDelete / RegistryURL from here. They were
// startup snapshots copied out of *config.Config, so an override saved from
// the settings page never reached the request path: the gates kept reading
// the env bootstrap value for the whole process lifetime. Everything
// runtime-editable is now read through Full.Effective*() -- see
// allowPull()/allowDelete() below.
//
// IgnoreUserAgents stays: the env half of the ignore list really is
// immutable, and the panel half lives in SQLite and is merged per request
// (envIgnore() + writeIgnoreRules()).
type ConfigExtras struct {
	IgnoreUserAgents []string
}

// allowPull / allowDelete are the runtime-effective switches (v0.5.4).
// Full is the live *config.Config; Effective* is nil-safe and fails closed,
// so a missing Full means "disabled" rather than "wide open".
func (e *ExtraHandlers) allowPull() bool   { return e.Full.AllowPull() }
func (e *ExtraHandlers) allowDelete() bool { return e.Full.AllowDelete() }

// RegisterRoutes mounts the v0.2 + v0.3 + v0.4 endpoints on the chi router.
//
// IMPORTANT: paths here are RELATIVE to the /api group (api.go calls this
// inside r.Route("/api", ...)). The v0.2 implementation mounted "/api/..."
// paths here, producing the double-prefix bug (/api/api/credentials).
func (e *ExtraHandlers) RegisterRoutes(r chi.Router) {
	r.Route("/pull", func(r chi.Router) {
		r.Get("/jobs", e.ListPullJobs)
		r.Post("/jobs", e.CreatePullJob)
		r.Get("/jobs/{id}", e.GetPullJob)
		r.Post("/jobs/{id}/cancel", e.CancelPullJob)
		r.Delete("/jobs/{id}", e.DeletePullJob)
		r.Post("/probe", e.ProbePullSource)
	})

	r.Route("/credentials", func(r chi.Router) {
		r.Get("/", e.ListCredentials)
		r.Post("/", e.CreateCredential)
		r.Get("/{id}", e.GetCredential)
		r.Patch("/{id}", e.UpdateCredential)
		r.Delete("/{id}", e.DeleteCredential)
		r.Post("/{id}/test", e.TestCredential)
	})

	r.Route("/proxies", func(r chi.Router) {
		r.Get("/", e.ListProxies)
		r.Post("/", e.CreateProxy)
		r.Get("/{id}", e.GetProxy)
		r.Patch("/{id}", e.UpdateProxy)
		r.Delete("/{id}", e.DeleteProxy)
		r.Post("/{id}/test", e.TestProxy)
		// v0.5.9: single-entry on-demand probe (the per-period background
		// probe already updates LastProbeStatus on every tick; this is the
		// manual button in the management page).
		r.Post("/{id}/probe", e.ProbeProxy)
		// v0.5.12: batch probe behind the "探测全部" button. A static
		// segment, so it differs from "/{id}/probe" in segment count and
		// chi has no ambiguity; real ids are "YYYYMMDD-HHMMSS-mmm-<hex>"
		// and can never collide with the literal "probe" either.
		r.Post("/probe", e.ProbeAllProxies)
		// v0.5.13: "test before save" — dials through a proxy config that is
		// not in the store yet (the 新增代理 dialog). Static segment again,
		// distinct from "/{id}/test" by segment count.
		r.Post("/test", e.TestProxyDraft)
	})

	if e.Events != nil {
		// Webhook ingest (registry-side notification endpoint).
		r.Post("/events", e.Events.ServeHTTP)
	}

	// v0.5.0: deletion is operational work, so it lives on the API surface
	// (UI buttons) and is gated by AllowDelete.
	r.Route("/repositories", func(r chi.Router) {
		r.Delete("/{repo}", e.DeleteRepository)
		r.Delete("/{repo}/manifests/{digest}", e.DeleteManifestByDigest)
	})
	r.Post("/gc", e.RunGC)

	r.Route("/stats", func(r chi.Router) {
		r.Get("/summary", e.StatsSummary)
		r.Get("/top", e.StatsTop)
		r.Get("/series", e.StatsSeries)
		r.Get("/repositories", e.StatsRepositories)
		r.Get("/events", e.StatsEvents)
		r.Get("/clients", e.StatsClients)
		r.Get("/ignore", e.StatsIgnoreGet)
		r.Post("/ignore", e.StatsIgnoreAdd)
		r.Delete("/ignore", e.StatsIgnoreRemove)
		r.Delete("/heat", e.StatsHeatDelete)
	})
}

// --- guards -----------------------------------------------------------------

func (e *ExtraHandlers) vaultUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if e.Vault == nil {
		reason := e.VaultErr
		if reason == "" {
			reason = "credential store unavailable"
		}
		writeError(w, r, http.StatusServiceUnavailable, errors.New(reason))
		return true
	}
	return false
}

func (e *ExtraHandlers) proxiesUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if e.Proxies == nil {
		reason := e.ProxiesErr
		if reason == "" {
			reason = "proxy store unavailable"
		}
		writeError(w, r, http.StatusServiceUnavailable, errors.New(reason))
		return true
	}
	return false
}

// --- Pull -------------------------------------------------------------------

// CreatePullJobReq mirrors the UI PullJobInput shape (web/src/types.ts):
// source url, inline auth and proxy all travel with the job, because v0.5.0
// has no configured external registry -- every pull states its own source
// explicitly. Unset optional fields fall back to credential or default.
type CreatePullJobReq struct {
	SourceUrl          string `json:"sourceUrl,omitempty"`
	SourceRef          string `json:"sourceRef"`
	SourceProxy        string `json:"sourceProxy,omitempty"`
	SourceProxyID      string `json:"sourceProxyId,omitempty"`
	DestRepo           string `json:"destRepo"`
	DestTag            string `json:"destTag"`
	SourceCredentialID string `json:"sourceCredentialId,omitempty"`
	// One-shot credential for this job only; never persisted.
	SourceAuthInline *struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"sourceAuthInline,omitempty"`
}

// uiJobView maps pull.JobView onto the UI's PullJob shape (web/src/types.ts).
// v0.5.0 keys match the UI exactly: destRepo/destTag (was targetRepo/targetTag),
// errorMessage (was error), finalDigest. v0.5.7: phases[] carries the live
// per-step detail (manifest / config / blob:N / child-manifests) that the
// expanded task row renders.
func uiJobView(j pull.JobView) map[string]any {
	sourceRepo, sourceTag, _ := splitSourceRef(j.SourceRef)
	var startedAt, finishedAt any
	if !j.StartedAt.IsZero() {
		startedAt = j.StartedAt
	}
	if !j.EndedAt.IsZero() {
		finishedAt = j.EndedAt
	}
	return map[string]any{
		"id":           j.ID,
		"status":       string(j.State),
		"sourceRepo":   sourceRepo,
		"sourceTag":    sourceTag,
		"sourceUrl":    j.SourceURL,
		"destRepo":     j.DestRepo,
		"destTag":      j.DestTag,
		"credential":   j.Credential,
		"proxy":        j.Proxy,
		"bytes":        j.BytesDone,
		"totalBytes":   j.BytesTotal,
		"blobsDone":    j.BlobsDone,
		"blobsTotal":   j.BlobsTotal,
		"finalDigest":  j.FinalDigest,
		"errorMessage": j.Error,
		"createdAt":    j.CreatedAt,
		"startedAt":    startedAt,
		"finishedAt":   finishedAt,
		"phases":       uiPhases(j.Phases),
	}
}

// uiPhases guarantees a JSON array (never null) for the UI's PullPhase[]:
// pull-page.tsx reads job.phases.length without a guard.
func uiPhases(phases []pull.Phase) []any {
	out := make([]any, 0, len(phases))
	for _, p := range phases {
		out = append(out, p)
	}
	return out
}

func (e *ExtraHandlers) CreatePullJob(w http.ResponseWriter, r *http.Request) {
	if !e.allowPull() {
		writeError(w, r, http.StatusForbidden,
			errors.New("pull is disabled (allow.pull=false)"))
		return
	}
	var req CreatePullJobReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if _, _, ok := splitSourceRef(req.SourceRef); !ok {
		writeError(w, r, http.StatusBadRequest,
			errors.New("sourceRef must be <repo>:<tag>"))
		return
	}
	if strings.TrimSpace(req.DestRepo) == "" || strings.TrimSpace(req.DestTag) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("destRepo and destTag are required"))
		return
	}
	nj := pull.NewJob{
		SourceRef:    req.SourceRef,
		DestRepo:     strings.TrimSpace(req.DestRepo),
		DestTag:      strings.TrimSpace(req.DestTag),
		CredentialID: req.SourceCredentialID,
		ProxyID:      req.SourceProxyID,
		SourceURL:    strings.TrimSpace(req.SourceUrl),
	}
	if req.SourceAuthInline != nil && req.SourceAuthInline.Username != "" {
		nj.SourceUser = req.SourceAuthInline.Username
		nj.SourcePass = req.SourceAuthInline.Password
	}
	if p := strings.TrimSpace(req.SourceProxy); p != "" {
		nj.ProxyURL = p
	}
	if nj.SourceURL == "" && e.Cfg != nil {
		if v := e.Full.RegistryURL(); v != "" {
			nj.SourceURL = strings.TrimRight(v, "/")
		}
	}
	job := e.Executor.Submit(nj)
	writeJSON(w, http.StatusCreated, uiJobView(job))
}

func (e *ExtraHandlers) ListPullJobs(w http.ResponseWriter, r *http.Request) {
	if e.Executor == nil {
		// Read-only list degrades to an empty array so the UI doesn't explode
		// when the pull feature is disabled.
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	jobs := e.Executor.List()
	out := make([]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, uiJobView(j))
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) GetPullJob(w http.ResponseWriter, r *http.Request) {
	if e.Executor == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("pull queue is not enabled"))
		return
	}
	id := chiURLParam(r, "id")
	if j := e.Executor.Get(id); j.ID != "" {
		writeJSON(w, http.StatusOK, uiJobView(j))
		return
	}
	writeError(w, r, http.StatusNotFound, errors.New("job not found"))
}

func (e *ExtraHandlers) CancelPullJob(w http.ResponseWriter, r *http.Request) {
	if e.Executor == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("pull queue is not enabled"))
		return
	}
	id := chiURLParam(r, "id")
	if err := e.Executor.Cancel(id); err != nil {
		if errors.Is(err, pull.ErrJobNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	// Return the cancelled job view so the UI can update in place.
	writeJSON(w, http.StatusOK, uiJobView(e.Executor.Get(id)))
}

func (e *ExtraHandlers) DeletePullJob(w http.ResponseWriter, r *http.Request) {
	if e.Executor == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("pull queue is not enabled"))
		return
	}
	id := chiURLParam(r, "id")
	if err := e.Executor.Delete(id); err != nil {
		if errors.Is(err, pull.ErrJobNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, pull.ErrJobNotTerminal) {
			writeError(w, r, http.StatusConflict, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	// 200 + body (not 204): the UI's api() helper rejects empty bodies.
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (e *ExtraHandlers) ProbePullSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceURL    string `json:"sourceUrl,omitempty"`
		SourceProxy  string `json:"sourceProxy,omitempty"`
		ProxyID      string `json:"proxyId,omitempty"`
		CredentialID string `json:"credentialId,omitempty"`
		SourceRef    string `json:"sourceRef,omitempty"`
		DestRepo     string `json:"destRepo,omitempty"`
		DestTag      string `json:"destTag,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	target := strings.TrimSpace(req.SourceURL)
	if target == "" && e.Cfg != nil {
		target = strings.TrimRight(e.Full.RegistryURL(), "/")
	}
	if target == "" {
		writeError(w, r, http.StatusBadRequest,
			errors.New("sourceUrl required and REGISTRY_URL not set"))
		return
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		writeError(w, r, http.StatusBadRequest, errors.New("sourceUrl must be http(s)://..."))
		return
	}
	cfg := registry.Config{BaseURL: target, Timeout: 5 * time.Second}
	if req.CredentialID != "" {
		if e.Vault == nil {
			writeError(w, r, http.StatusServiceUnavailable, errors.New("credential store unavailable"))
			return
		}
		c, err := e.Vault.Get(req.CredentialID)
		if err != nil {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		cfg.Username = c.Username
		cfg.Password = c.Password
	}
	// Inline proxy URL wins; stored proxy id is the fallback. Auth (if any)
	// is baked into the URL by the caller so we don't see userinfo here.
	if p := strings.TrimSpace(req.SourceProxy); p != "" {
		cfg.Proxy = p
	} else if req.ProxyID != "" && e.Proxies != nil {
		if p, err := e.Proxies.Get(req.ProxyID); err == nil {
			cfg.Proxy = strings.TrimSpace(p.URL)
		}
	}
	client, err := registry.NewClient(cfg)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	start := time.Now()
	probeErr := client.Probe(r.Context())
	out := map[string]any{
		"ok":         probeErr == nil,
		"apiVersion": "2",
		"host":       target,
		"sourceUrl":  target,
		"usingProxy": cfg.Proxy != "",
		"elapsedMs":  time.Since(start).Milliseconds(),
	}
	if probeErr != nil {
		out["error"] = probeErr.Error()
	}
	// v0.5.17: "/v2/ 可达"并不等于"这个源镜像能拉"。此前只探 /v2/，于是出网不通
	// （或源上根本没有这个 tag）时界面照样打绿勾，任务入队后才卡住——用户看到的
	// 就是"检测通过，却一直卡在拉取中"。这里按拉取任务同一套规则（同限定、同凭据、
	// 同代理）解析出真实的 repo:tag 并取一次 manifest，让预检结论与真实拉取一致。
	var (
		sourceRepo   string
		sourceTag    string
		sourceDigest string
		sourceExists bool
		hasSource    bool
	)
	if probeErr == nil {
		if repo, tag, ok := splitSourceRef(req.SourceRef); ok {
			sourceRepo, sourceTag, hasSource = pull.QualifySourceRepo(target, repo), tag, true
			out["sourceRepo"] = sourceRepo
			out["sourceTag"] = sourceTag
			manifest, merr := client.GetManifest(r.Context(), sourceRepo, sourceTag)
			var re *registry.Error
			switch {
			case merr == nil:
				sourceExists = true
				sourceDigest = manifest.Digest
			case errors.As(merr, &re) && re.IsNotFound():
				out["ok"] = false
				out["error"] = fmt.Sprintf(
					"源 registry 可达，但源镜像 %s:%s 不存在——请检查镜像名与 tag 拼写",
					sourceRepo, sourceTag)
			default:
				out["ok"] = false
				out["error"] = fmt.Sprintf("源镜像 %s:%s 探测失败：%v",
					sourceRepo, sourceTag, merr)
			}
			out["sourceExists"] = sourceExists
			if sourceDigest != "" {
				out["sourceDigest"] = sourceDigest
			}
		}
	}
	// Probe the destination tag too so the UI can warn about overwrites.
	if req.DestRepo != "" && req.DestTag != "" {
		dest, derr := e.probeDest(r.Context(), req.DestRepo, req.DestTag)
		if derr != "" {
			// probeDest 失败时也得给出 dest 对象：前端"未能确认目标 tag 的现状"
			// 那条提示读的是 dest.probeError（types.ts 里就是这么声明的）。
			// 不设 exists，避免被误判成"将新建"。
			out["destError"] = derr
			fallback := map[string]any{
				"destRepo":   req.DestRepo,
				"destTag":    req.DestTag,
				"probeError": derr,
			}
			if hasSource {
				fallback["sourceRepo"] = sourceRepo
				fallback["sourceTag"] = sourceTag
				fallback["sourceExists"] = sourceExists
			}
			out["dest"] = fallback
		} else {
			// v0.5.17: dest 一并带上源侧事实。types.ts 早就声明了
			// sourceRepo/sourceTag/sourceExists/sourceDigest/identical，
			// 但 Go 侧从未填充，前端那几条提示一直是死代码。
			if hasSource {
				dest["sourceRepo"] = sourceRepo
				dest["sourceTag"] = sourceTag
				dest["sourceExists"] = sourceExists
				if sourceDigest != "" {
					dest["sourceDigest"] = sourceDigest
				}
			}
			// 目标 tag 已存在时用 digest 比对区分"替换"与"内容完全一致"，
			// 而不是无条件 willReplace。
			if existing, _ := dest["existingDigest"].(string); existing != "" && sourceDigest != "" {
				same := existing == sourceDigest
				dest["identical"] = same
				dest["willReplace"] = !same
			}
			out["dest"] = dest
		}
	}
	// v0.5.17: 源侧现在还要取一次 manifest，耗时不再只等于 /v2/ 探测；重算一次，
	// 免得运维拿这个数字判断"探测很快所以网络没问题"。
	out["elapsedMs"] = time.Since(start).Milliseconds()
	writeJSON(w, http.StatusOK, out)
}

func splitSourceRef(ref string) (repo, tag string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", false
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return "", "", false
	}
	repo = strings.TrimLeft(ref[:i], "/")
	tag = ref[i+1:]
	if repo == "" || tag == "" {
		return "", "", false
	}
	return repo, tag, true
}

// firstNonEmptyStr returns the first non-empty string (whitespace-trimmed).
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// defaultUpstream returns the configured REGISTRY_URL when set, otherwise
// pull.DefaultUpstream (Docker Hub). Safe to call when e is nil.
func (e *ExtraHandlers) defaultUpstream() string {
	if e == nil || e.Cfg == nil {
		return pull.DefaultUpstream
	}
	if v := strings.TrimRight(e.Full.RegistryURL(), "/"); v != "" {
		return v
	}
	return pull.DefaultUpstream
}

// proxyLabel returns the proxy's name for a given id, or the id as-is. The
// job view uses it so the UI never has to leak a stored proxy URL.
func (e *ExtraHandlers) proxyLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || e.Proxies == nil {
		return id
	}
	if p, err := e.Proxies.Get(id); err == nil && p.Name != "" {
		return p.Name
	}
	return id
}

// probeDest reports whether destRepo:destTag already exists locally. Used
// by ProbePullSource so the UI can flag a would-overwrite before submit.
func (e *ExtraHandlers) probeDest(ctx context.Context, repo, tag string) (map[string]any, string) {
	if e.Store == nil {
		return nil, "storage backend unavailable"
	}
	digest, err := e.Store.TagDigest(ctx, repo, tag)
	if errors.Is(err, storage.ErrNotFound) {
		return map[string]any{
			"destRepo":    repo,
			"destTag":     tag,
			"exists":      false,
			"willReplace": false,
		}, ""
	}
	if err != nil {
		return nil, err.Error()
	}
	return map[string]any{
		"destRepo":       repo,
		"destTag":        tag,
		"exists":         true,
		"existingDigest": digest,
		"willReplace":    true,
	}, ""
}

// DeleteRepository drops an entire repository (all tags + manifests). Gated
// by AllowDelete so a single env flag can lock down destructive ops.
func (e *ExtraHandlers) DeleteRepository(w http.ResponseWriter, r *http.Request) {
	if !e.allowDelete() {
		writeError(w, r, http.StatusForbidden,
			errors.New("delete is disabled (allow.delete=false)"))
		return
	}
	repo := chiURLParam(r, "repo")
	if repo == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("repo required"))
		return
	}
	if e.Store == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("storage backend unavailable"))
		return
	}
	if err := e.Store.DeleteRepository(r.Context(), repo); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": repo, "deleted": true})
}

// DeleteManifestByDigest removes a manifest by digest and reports the tags
// that previously pointed at it. Same AllowDelete gate as above.
func (e *ExtraHandlers) DeleteManifestByDigest(w http.ResponseWriter, r *http.Request) {
	if !e.allowDelete() {
		writeError(w, r, http.StatusForbidden,
			errors.New("delete is disabled (allow.delete=false)"))
		return
	}
	repo := chiURLParam(r, "repo")
	digest := chiURLParam(r, "digest")
	if repo == "" || digest == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("repo and digest required"))
		return
	}
	if !strings.HasPrefix(digest, "sha256:") {
		writeError(w, r, http.StatusBadRequest, errors.New("digest must be sha256:..."))
		return
	}
	if e.Store == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("storage backend unavailable"))
		return
	}
	affected, err := e.Store.TagsForDigest(r.Context(), repo, digest)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if _, err := e.Store.DeleteManifest(r.Context(), repo, digest); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repo":         repo,
		"digest":       digest,
		"affectedTags": affected,
		"deleted":      true,
	})
}

// RunGC triggers an inline storage GC sweep and returns the freed-blob stats.
func (e *ExtraHandlers) RunGC(w http.ResponseWriter, r *http.Request) {
	if e.Store == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("storage backend unavailable"))
		return
	}
	res, err := e.Store.GC(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"removedBlobs": res.RemovedBlobs,
		"freedBytes":   res.FreedBytes,
	})
}

// --- Credentials ------------------------------------------------------------
//
// JSON views never leak the password back to the client (hasPassword flag
// instead). The UI's Credential shape: {id,name,registryUrl,username,
// hasPassword,note?,createdAt,updatedAt}.

type credentialView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RegistryURL string `json:"registryUrl"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
	Note        string `json:"note,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func toCredentialView(c credentials.Credential) credentialView {
	return credentialView{
		ID:          c.ID,
		Name:        c.Name,
		RegistryURL: c.URL,
		Username:    c.Username,
		HasPassword: c.Password != "",
		Note:        c.Note,
		CreatedAt:   c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.Format(time.RFC3339),
	}
}

// credentialInput is the create/update request body. On update an empty
// password means "keep the stored one" (the UI never round-trips secrets).
type credentialInput struct {
	Name        string `json:"name"`
	RegistryURL string `json:"registryUrl"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Note        string `json:"note"`
}

func (e *ExtraHandlers) ListCredentials(w http.ResponseWriter, r *http.Request) {
	if e.vaultUnavailable(w, r) {
		return
	}
	all := e.Vault.List()
	out := make([]credentialView, 0, len(all))
	for _, c := range all {
		out = append(out, toCredentialView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) CreateCredential(w http.ResponseWriter, r *http.Request) {
	if e.vaultUnavailable(w, r) {
		return
	}
	var in credentialInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.RegistryURL) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("name and registryUrl are required"))
		return
	}
	c := credentials.Credential{
		ID:       newID(),
		Name:     strings.TrimSpace(in.Name),
		URL:      strings.TrimSpace(in.RegistryURL),
		Username: in.Username,
		Password: in.Password,
		Note:     in.Note,
	}
	if err := e.Vault.Put(c); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	stored, _ := e.Vault.Get(c.ID)
	writeJSON(w, http.StatusCreated, toCredentialView(stored))
}

func (e *ExtraHandlers) GetCredential(w http.ResponseWriter, r *http.Request) {
	if e.vaultUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	c, err := e.Vault.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, toCredentialView(c))
}

func (e *ExtraHandlers) UpdateCredential(w http.ResponseWriter, r *http.Request) {
	if e.vaultUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	existing, err := e.Vault.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	var in credentialInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Name) != "" {
		existing.Name = strings.TrimSpace(in.Name)
	}
	if strings.TrimSpace(in.RegistryURL) != "" {
		existing.URL = strings.TrimSpace(in.RegistryURL)
	}
	existing.Username = in.Username
	if in.Password != "" { // empty = keep stored password
		existing.Password = in.Password
	}
	existing.Note = in.Note
	if err := e.Vault.Put(existing); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	stored, _ := e.Vault.Get(id)
	writeJSON(w, http.StatusOK, toCredentialView(stored))
}

func (e *ExtraHandlers) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	if e.vaultUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	if err := e.Vault.Delete(id); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// TestCredential probes /v2/ on the credential's registry with basic auth.
// Transport/domain failures are returned as HTTP 200 {ok:false} — the UI
// renders them inline next to the form.
func (e *ExtraHandlers) TestCredential(w http.ResponseWriter, r *http.Request) {
	if e.vaultUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	c, err := e.Vault.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	base := strings.TrimSuffix(c.URL, "/")
	endpoint := base + "/v2/"

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "registryUrl": base,
		})
		return
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	req.Header.Set("User-Agent", "cairn/"+version.Version)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "elapsedMs": elapsed, "registryUrl": base,
		})
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          resp.StatusCode < 400,
		"status":      resp.StatusCode,
		"statusText":  resp.Status,
		"elapsedMs":   elapsed,
		"registryUrl": base,
		"apiVersion":  "2",
		"purpose":     "source",
	})
}

// --- Proxies ----------------------------------------------------------------

type proxyView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	HasAuth   bool   `json:"hasAuth"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	// v0.5.9: reachability status from the background probe loop.
	LastProbeStatus string `json:"lastProbeStatus,omitempty"`
	LastProbeAt     string `json:"lastProbeAt,omitempty"`
	LastProbeError  string `json:"lastProbeError,omitempty"`
	// v0.5.15: TCP connect round-trip in ms; omitted when the probe could
	// not connect (and on entries last probed by an older build).
	LastProbeLatencyMs float64 `json:"lastProbeLatencyMs,omitempty"`
}

func toProxyView(p proxies.Proxy) proxyView {
	return proxyView{
		ID:                 p.ID,
		Name:               p.Name,
		URL:                p.URL,
		Username:           p.Username,
		HasAuth:            p.Password != "",
		Note:               p.Note,
		CreatedAt:          p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          p.UpdatedAt.Format(time.RFC3339),
		LastProbeStatus:    p.LastProbeStatus,
		LastProbeAt:        lastProbeAtString(p.LastProbeAt),
		LastProbeError:     p.LastProbeError,
		LastProbeLatencyMs: p.LastProbeLatencyMs,
	}
}

// lastProbeAtString formats a possibly-zero time as RFC3339 or empty
// string. Used so the UI distinguishes "never probed" from "probed at
// this very moment" without a separate boolean.
func lastProbeAtString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// proxyInput: on update an empty password CLEARS the stored one (proxies are
// often anonymous; the UI sends "" when the auth toggle is off).
type proxyInput struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note"`
}

func (e *ExtraHandlers) ListProxies(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	all := e.Proxies.List()
	out := make([]proxyView, 0, len(all))
	for _, p := range all {
		out = append(out, toProxyView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) CreateProxy(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	var in proxyInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.URL) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("name and url are required"))
		return
	}
	p := proxies.Proxy{
		ID:       newID(),
		Name:     strings.TrimSpace(in.Name),
		URL:      strings.TrimSpace(in.URL),
		Username: in.Username,
		Password: in.Password,
		Note:     in.Note,
	}
	if err := e.Proxies.Put(p); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	stored, _ := e.Proxies.Get(p.ID)
	writeJSON(w, http.StatusCreated, toProxyView(stored))
}

func (e *ExtraHandlers) GetProxy(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	p, err := e.Proxies.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, toProxyView(p))
}

func (e *ExtraHandlers) UpdateProxy(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	existing, err := e.Proxies.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	var in proxyInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Name) != "" {
		existing.Name = strings.TrimSpace(in.Name)
	}
	if strings.TrimSpace(in.URL) != "" {
		existing.URL = strings.TrimSpace(in.URL)
	}
	existing.Username = in.Username
	existing.Password = in.Password // empty = clear (anonymous proxy)
	existing.Note = in.Note
	if err := e.Proxies.Put(existing); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	stored, _ := e.Proxies.Get(id)
	writeJSON(w, http.StatusOK, toProxyView(stored))
}

func (e *ExtraHandlers) DeleteProxy(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	if err := e.Proxies.Delete(id); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// TestProxy dials targetUrl THROUGH the proxy and reports timing + status.
// A transport failure is returned as HTTP 200 {ok:false} — it's a domain
// result, not a server error.
func (e *ExtraHandlers) TestProxy(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	p, err := e.Proxies.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}

	var body struct {
		TargetURL string `json:"targetUrl"`
	}
	_ = decodeJSON(r, &body)
	target := e.proxyTestTarget(r, body.TargetURL)

	proxyURL, err := proxyURLFrom(p.URL, p.Username, p.Password)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "targetUrl": target,
		})
		return
	}
	e.proxyTestThrough(w, r, proxyURL, target)
}

// v0.5.13: TestProxyDraft is "test before save" — it dials targetUrl through a
// proxy config taken straight from the request body, so the create form can
// find out whether what the user just typed actually works *before* it is
// committed to proxies.json.
//
// Why this needs its own endpoint: both TestProxy and ProbeProxy require the
// entry to exist in the store, which is exactly what the 新增代理 dialog is
// trying to establish. Nothing here is persisted and no probe status is
// recorded, so a draft that fails to dial never shows up in the list and can
// never overwrite the status of a saved entry.
//
// v0.5.15: the same endpoint now also serves the *edit* dialog, which is why
// the body accepts an optional id. The edit form never echoes the stored
// password, so a blank password is ambiguous: "keep the stored credential" or
// "this proxy is anonymous now"? The stored password is reused only when the
// id resolves AND the submitted username still equals the stored one, which
// keeps the test in step with what the user is looking at (a cleared or
// changed username falls through to an anonymous / new-credential dial).
func (e *ExtraHandlers) TestProxyDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		TargetURL string `json:"targetUrl"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	raw := strings.TrimSpace(body.URL)
	if raw == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("url is required"))
		return
	}
	target := e.proxyTestTarget(r, body.TargetURL)

	// Editing a saved entry: the form cannot echo the stored password, so an
	// empty field means "reuse it" while the username is unchanged. Guarded
	// on e.Proxies because this handler has no proxiesUnavailable() check.
	username := strings.TrimSpace(body.Username)
	password := body.Password
	if password == "" && body.ID != "" && e.Proxies != nil {
		if stored, gerr := e.Proxies.Get(body.ID); gerr == nil && strings.TrimSpace(stored.Username) == username {
			password = stored.Password
		}
	}

	proxyURL, err := proxyURLFrom(raw, username, password)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "targetUrl": target,
		})
		return
	}
	e.proxyTestThrough(w, r, proxyURL, target)
}

// proxyURLFrom parses a proxy address (typed in the form, or read back from the
// store) and attaches credentials when a username is set.
//
// The scheme check is deliberate: the most common typo in the create form is a
// bare "proxy.corp:8080", which url.Parse happily reads as scheme "proxy" and
// which would otherwise only surface later as an opaque transport error.
func proxyURLFrom(raw, username, password string) (*url.URL, error) {
	// Look for a scheme *before* url.Parse: a bare "proxy.corp:8080" is
	// rejected by url.Parse itself ("first path segment in URL cannot contain
	// colon"), and surfacing that raw Go error to the form helps nobody. The
	// message below is the same one the form's own validator shows for this
	// exact typo.
	if !strings.Contains(raw, "://") {
		return nil, errors.New("代理地址无效：需要以 http:// 或 https:// 开头，例如 http://proxy.example.com:8080")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("代理地址无效：" + err.Error())
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, errors.New("代理地址无效：需要以 http:// 或 https:// 开头，例如 http://proxy.example.com:8080")
	}
	if u.Host == "" {
		return nil, errors.New("代理地址无效：缺少主机名或端口")
	}
	if username != "" {
		u.User = url.UserPassword(username, password)
	}
	return u, nil
}

// proxyTestTarget resolves which URL a connectivity test fetches: the caller's
// explicit target, else this registry's own /v2/ (which proves both "the proxy
// is reachable" and "it can reach this registry").
func (e *ExtraHandlers) proxyTestTarget(r *http.Request, want string) string {
	if t := strings.TrimSpace(want); t != "" {
		return t
	}
	base := strings.TrimSuffix(e.Full.RegistryURL(), "/")
	if base == "" {
		base = "http://" + r.Host
	}
	return base + "/v2/"
}

// proxyTestThrough performs the actual dial-through-proxy and writes the
// result.
//
// Always answers 200: "the proxy refused / could not be reached" is precisely
// the answer this endpoint exists to produce, not a server error. Shared by
// TestProxy (saved entry) and TestProxyDraft (unsaved form values) so both
// report the identical shape.
func (e *ExtraHandlers) proxyTestThrough(w http.ResponseWriter, r *http.Request, proxyURL *url.URL, target string) {
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "targetUrl": target,
		})
		return
	}
	req.Header.Set("User-Agent", "cairn/"+version.Version)

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "elapsedMs": elapsed, "targetUrl": target,
		})
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	out := map[string]any{
		"ok":         resp.StatusCode < 400,
		"status":     resp.StatusCode,
		"statusText": resp.Status,
		"elapsedMs":  elapsed,
		"targetUrl":  target,
	}
	if resp.StatusCode < 400 {
		out["registryApiVersion"] = "2"
	}
	writeJSON(w, http.StatusOK, out)
}

// v0.5.9: ProbeProxy triggers an immediate reachability check for one
// proxy entry.
//
// v0.5.15: the check is a plain TCP connect to the proxy's ip:port. Nothing
// is sent and no proxy protocol is negotiated, so the result answers exactly
// one question: "can this host reach that socket, and how slow is it". The
// protocol-level question (does the proxy actually forward traffic) stays
// with TestProxy, which dials a target *through* the entry.
func (e *ExtraHandlers) ProbeProxy(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	if err := e.Proxies.Probe(id); err != nil {
		// Still return 200 with ok:false so the UI can show the error
		// inline; a 5xx here would force the page into a retry loop.
		p, _ := e.Proxies.Get(id)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":        id,
			"ok":        false,
			"error":     err.Error(),
			"status":    p.LastProbeStatus,
			"probedAt":  p.LastProbeAt,
			"latencyMs": p.LastProbeLatencyMs,
		})
		return
	}
	p, _ := e.Proxies.Get(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"ok":        true,
		"status":    p.LastProbeStatus,
		"probedAt":  p.LastProbeAt,
		"latencyMs": p.LastProbeLatencyMs,
	})
}

// proxyProbeView is one row of the batch-probe summary. Deliberately leaner
// than proxyView: the page only needs to know which entry ended up unusable.
type proxyProbeView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Status   string `json:"status,omitempty"`
	ProbedAt string `json:"probedAt,omitempty"`
	Error    string `json:"error,omitempty"`
	// v0.5.15: TCP connect round-trip; omitted when the probe could not
	// connect (and always omitted on successful-by-luck 0ms entries).
	LatencyMs float64 `json:"latencyMs,omitempty"`
}

// v0.5.12: ProbeAllProxies probes every proxy entry at once — the "探测全部"
// button on the management page.
//
// Always answers 200 with a summary: "some proxies are unreachable" is the
// very thing this endpoint exists to report, not a server error. ProbeAll
// only surfaces the *first* failure it saw, which carries no more information
// than the per-entry statuses re-read below, so its return value is ignored.
func (e *ExtraHandlers) ProbeAllProxies(w http.ResponseWriter, r *http.Request) {
	if e.proxiesUnavailable(w, r) {
		return
	}
	// ProbeAll probes in parallel and waits for all of them, so the batch
	// costs about as much as a single entry (5s worst case) rather than N×5s.
	_ = e.Proxies.ProbeAll(r.Context())

	items := e.Proxies.List()
	results := make([]proxyProbeView, 0, len(items))
	okN, failedN, unknownN := 0, 0, 0
	for _, p := range items {
		switch p.LastProbeStatus {
		case "ok":
			okN++
		case "failed":
			failedN++
		default:
			// Never probed, or the entry disappeared while its probe was in
			// flight. Reporting that as "failed" would be a lie, so it gets
			// its own bucket instead.
			unknownN++
		}
		results = append(results, proxyProbeView{
			ID:        p.ID,
			Name:      p.Name,
			OK:        p.LastProbeStatus == "ok",
			Status:    p.LastProbeStatus,
			ProbedAt:  lastProbeAtString(p.LastProbeAt),
			Error:     p.LastProbeError,
			LatencyMs: p.LastProbeLatencyMs,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":   len(items),
		"ok":      okN,
		"failed":  failedN,
		"unknown": unknownN,
		"results": results,
	})
}

// --- Stats ------------------------------------------------------------------

func (e *ExtraHandlers) StatsSummary(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 7)
	out := map[string]any{
		"days":         days,
		"total":        int64(0),
		"repositories": 0,
		"tags":         0,
		"lastAt":       nil,
		"push":         int64(0),
		"pull":         int64(0),
	}
	if e.DB != nil {
		if s, err := e.DB.GetSummary(r.Context(), daysSince(days)); err == nil {
			out["total"] = s.TotalPulls + s.TotalPushes
			out["repositories"] = s.UniqueRepos
			out["tags"] = s.UniqueTags
			out["push"] = s.TotalPushes
			out["pull"] = s.TotalPulls
			if !s.LastSeen.IsZero() {
				out["lastAt"] = s.LastSeen.Format(time.RFC3339)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) StatsTop(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 7)
	limit := parseLimit(r, 10)
	by := r.URL.Query().Get("by")
	byTag := by == "tag"
	if by != "repository" && by != "tag" {
		by = "repository"
		byTag = false
	}
	out := map[string]any{"days": days, "by": by, "items": []any{}}
	if e.DB != nil {
		if items, err := e.DB.GetTop(r.Context(), daysSince(days), limit, byTag); err == nil {
			out["items"] = items // TopItem json tags already match the UI
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) StatsSeries(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 7)
	repo := r.URL.Query().Get("repository")
	out := map[string]any{"days": days, "repository": repo, "points": []any{}}
	if e.DB != nil {
		if pts, err := e.DB.GetSeriesByDay(r.Context(), daysSince(days), repo); err == nil {
			out["points"] = pts
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) StatsRepositories(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 7)
	out := map[string]any{"days": days, "items": map[string]any{}}
	if e.DB != nil {
		if stats, err := e.DB.GetRepoStats(r.Context(), daysSince(days)); err == nil {
			items := make(map[string]any, len(stats))
			for _, s := range stats {
				items[s.Repository] = map[string]any{
					"events": s.Events,
					"pull":   s.Pull,
					"push":   s.Push,
					"lastAt": s.LastAt,
				}
			}
			out["items"] = items
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *ExtraHandlers) StatsEvents(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 50)
	items := []any{}
	totals := map[string]any{
		"accepted": int64(0), "rejected": int64(0), "buffered": 0,
		"bufferSize": 0, "self": int64(0), "ignored": int64(0),
	}
	if e.Events != nil {
		evs := e.Events.RecentEvents()
		if limit > 0 && limit < len(evs) {
			evs = evs[:limit]
		}
		for _, ev := range evs {
			items = append(items, ev)
		}
		t := e.Events.SnapshotTotals()
		totals["accepted"] = t.Accepted
		totals["rejected"] = t.Rejected
		totals["buffered"] = t.Buffered
		totals["bufferSize"] = t.BufferSize
		totals["self"] = t.Self
		totals["ignored"] = t.Ignored
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"totals": totals,
	})
}

func (e *ExtraHandlers) StatsClients(w http.ResponseWriter, r *http.Request) {
	days := parseDays(r, 7)
	since := daysSince(days)
	items := []any{}
	if e.Events != nil {
		for _, c := range e.Events.SnapshotClients() {
			if c.LastSeenAt.Before(since) {
				continue
			}
			items = append(items, c) // ClientStat json tags already match the UI
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "items": items})
}

// StatsHeatDelete purges ALL derived heat data (activity_daily rows).
// UI shape: {activity, seen}. There is no event_seen table in v0.4.0, so
// seen is always 0.
func (e *ExtraHandlers) StatsHeatDelete(w http.ResponseWriter, r *http.Request) {
	var activity int64
	if e.DB != nil {
		n, err := e.DB.PurgeAll(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		activity = n
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": activity, "seen": 0})
}

// --- Ignore rules -----------------------------------------------------------
//
// effective = env ∪ panel. Env rules come from STATS_IGNORE_USER_AGENTS and
// are read-only; panel rules live in SQLite and are mutable at runtime.
// Every mutation recomputes the effective set and pushes it into the live
// events handler so changes apply immediately.

func (e *ExtraHandlers) envIgnore() []string {
	out := make([]string, 0, len(e.Cfg.IgnoreUserAgents))
	out = append(out, e.Cfg.IgnoreUserAgents...)
	sort.Strings(out)
	return out
}

func (e *ExtraHandlers) panelIgnore(ctx context.Context) []string {
	if e.DB == nil {
		return []string{}
	}
	list, err := e.DB.ListIgnore(ctx)
	if err != nil {
		return []string{}
	}
	sort.Strings(list)
	return list
}

func (e *ExtraHandlers) writeIgnoreRules(w http.ResponseWriter, r *http.Request, status int) {
	env := e.envIgnore()
	panel := e.panelIgnore(r.Context())
	effective := events.MergeIgnore(env, panel)
	if e.Events != nil {
		e.Events.SetIgnoreUAs(effective)
	}
	writeJSON(w, status, map[string]any{
		"env":       env,
		"panel":     panel,
		"effective": effective,
	})
}

func (e *ExtraHandlers) StatsIgnoreGet(w http.ResponseWriter, r *http.Request) {
	e.writeIgnoreRules(w, r, http.StatusOK)
}

func (e *ExtraHandlers) StatsIgnoreAdd(w http.ResponseWriter, r *http.Request) {
	if e.DB == nil {
		reason := e.DBErr
		if reason == "" {
			reason = "stats database unavailable — cannot persist ignore rules"
		}
		writeError(w, r, http.StatusServiceUnavailable, errors.New(reason))
		return
	}
	var body struct {
		UserAgent string `json:"useragent"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	ua := strings.TrimSpace(body.UserAgent)
	if ua == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("useragent is required"))
		return
	}
	if err := e.DB.AddIgnore(r.Context(), ua); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	e.writeIgnoreRules(w, r, http.StatusCreated)
}

func (e *ExtraHandlers) StatsIgnoreRemove(w http.ResponseWriter, r *http.Request) {
	if e.DB == nil {
		reason := e.DBErr
		if reason == "" {
			reason = "stats database unavailable — cannot persist ignore rules"
		}
		writeError(w, r, http.StatusServiceUnavailable, errors.New(reason))
		return
	}
	var body struct {
		UserAgent string `json:"useragent"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	ua := strings.TrimSpace(body.UserAgent)
	if ua == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("useragent is required"))
		return
	}
	if err := e.DB.RemoveIgnore(r.Context(), ua); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	e.writeIgnoreRules(w, r, http.StatusOK)
}

// --- helpers ----------------------------------------------------------------

// parseDays reads ?days=N (plain integer). The v0.2 implementation used
// time.ParseDuration(q+"h"), which silently treated "7" as 7 HOURS — the UI
// always sends plain day counts.
func parseDays(r *http.Request, defDays int) int {
	if q := r.URL.Query().Get("days"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			return n
		}
	}
	return defDays
}

func daysSince(days int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -days)
}

func parseLimit(r *http.Request, def int) int {
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// newID returns a short, time-ordered id of the form 20060102-150405.000-xxxxxxxx.
// It is the primary key of both the credential and the proxy library.
//
// v0.5.10: the random suffix used to be derived from time.Now().UnixNano()%16
// with a time.Sleep(time.Microsecond) per character. The low bits of the clock
// repeat under load, so two concurrent creates could receive the SAME id, and
// because Put overwrites by id the second one silently replaced the first
// (measured: 20 concurrent creates lost 3 entries). The suffix is now drawn
// from crypto/rand.
//
// v0.5.11: that suffix was only 16 bits, so the millisecond timestamp was still
// the real discriminator — 400 ids minted in one millisecond collide ~1.2 times
// by birthday, and the v0.5.10 regression test failed 13 runs out of 20.
// Widened to 32 bits, which drops the expected collisions for the same 400 ids
// to ~2e-5.
func newID() string {
	return strings.ReplaceAll(time.Now().UTC().Format("20060102-150405.000"), ".", "-") + "-" + randHex(8)
}

// randHex returns n lowercase hex characters from crypto/rand, so concurrent
// callers cannot collide the way the old nanosecond-clock derivation did.
func randHex(n int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, n)
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand only fails when the OS entropy source is broken and
		// there is no clean recovery inside a request handler: degrade to the
		// clock (which still advances per call) rather than panicking.
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano())
		}
	}
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			out[i] = hex[buf[i/2]>>4]
		} else {
			out[i] = hex[buf[i/2]&0x0f]
		}
	}
	return string(out)
}

func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
