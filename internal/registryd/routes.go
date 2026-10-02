// Package registryd serves the OCI Distribution V2 protocol routes
// (/v2/*) over the local storage.Storage.
//
// This is what makes cairn a registry, not just a registry UI: clients
// like `docker push` / `docker pull` / `skopeo copy` can talk directly
// to cairn without needing a separate Docker Registry deployment.
//
// Endpoints implemented (V2 spec minimum viable set):
//
//	GET    /v2/                                              # 200 OK
//	GET    /v2/_catalog                                      # list repos
//	GET    /v2/<repo>/tags/list                              # list tags
//	GET    /v2/<repo>/manifests/<ref>                        # fetch
//	HEAD   /v2/<repo>/manifests/<ref>                        # exists + digest
//	PUT    /v2/<repo>/manifests/<ref>                        # upload
//	DELETE /v2/<repo>/manifests/<digest>                     # delete
//	GET    /v2/<repo>/blobs/<digest>                         # fetch
//	HEAD   /v2/<repo>/blobs/<digest>                         # exists + size
//	POST   /v2/<repo>/blobs/uploads/                         # start upload
//	GET    /v2/<repo>/blobs/uploads/<uuid>                   # inspect
//	PATCH  /v2/<repo>/blobs/uploads/<uuid>                   # append chunk
//	PUT    /v2/<repo>/blobs/uploads/<uuid>?digest=<digest>   # commit
//
// What's NOT here (deferred):
//   - Cross-repo manifest references (manifests can list blobs from
//     other repos; we treat blob digest as global, which is enough for
//     common cases but not strictly spec-compliant)
//   - Tag delete (DELETE /v2/<repo>/tags/<tag> is non-standard; we use
//     the manifest DELETE path via tagDigest)
//   - Multi-chunk manifest uploads (Docker / skopeo always PUT the
//     whole manifest in one shot)
//   - Bearer-token auth (currently anonymous; lock at the reverse proxy)
//   - Garbage collection (orphaned blobs accumulate; sweep TODO)
package registryd

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Chenjinteng/cairn/internal/events"
	"github.com/Chenjinteng/cairn/internal/storage"
)

// basicAuthCreds returns (user, pass) the registry should accept. nil means
// auth is disabled (every /v2/* request passes through).
type basicAuthCreds func() (user, pass string)

// Handler bundles the storage and serves /v2/*. Build it via New and mount
// the returned http.Handler at /v2/* in your router.
type Handler struct {
	Store    storage.Storage
	getCreds basicAuthCreds // nil == auth disabled
	realm    string         // WWW-Authenticate realm; defaults to "cairn"

	// Events (v0.5.8) is the in-process heat-aggregator. Nil-safe:
	// every code path checks h.Events != nil before calling, so a
	// zero-value Handler is still usable for tests that don't care
	// about heat. Wire it in server.go from cfg → events.NewHandler
	// so /v2/* operations are observed exactly once and don't need
	// a separate webhook round-trip.
	Events *events.Handler

	// OnWrite (v0.6.32) is invoked after any successful state-changing
	// V2 operation (PUT manifest, DELETE manifest, upload commit,
	// upload cancel). Wired by NewWithOnWrite from server.go to drop
	// the /api/inventory cache so the next read sees the new state.
	// Nil = no-op, which is the default for the v0.6.x callers (tests)
	// that don't have an inventory cache to invalidate.
	OnWrite func()
}

// New returns a chi router pre-configured with the V2 protocol routes.
// Mount it at /v2/* — the routes are written relative to that prefix.
//
// getCreds is invoked on every request so v0.5.2+ settings-page edits
// (registry.username / registry.password via PATCH /api/config) take
// effect immediately without restarting the server. Pass nil to
// disable authentication entirely (the v0.4.0 default).
//
// eventsH (v0.5.8) is the heat-aggregator the built-in registry feeds
// on successful manifest HEAD (pull) and PUT (push). Pass nil to
// disable local heat counting (tests + the rare case where the
// aggregator should be wired later via direct field assignment).
//
// Wiring (server.go) is: events.NewHandler(...) → eventsH → New(...).
// External registries still POST to /api/events as before; the built-in
// registry now self-reports and skips the round-trip.
func New(store storage.Storage, getCreds basicAuthCreds, eventsH *events.Handler) http.Handler {
	return NewWithOnWrite(store, getCreds, eventsH, nil)
}

// NewWithOnWrite is the v0.6.32 variant of New that wires an OnWrite
// callback fired after every successful state-changing V2 operation.
// server.go passes a function that drops the /api/inventory cache;
// tests that don't have such a cache pass nil and get the same
// behaviour as the plain New constructor.
func NewWithOnWrite(store storage.Storage, getCreds basicAuthCreds, eventsH *events.Handler, onWrite func()) http.Handler {
	h := &Handler{Store: store, getCreds: getCreds, realm: "cairn", Events: eventsH, OnWrite: onWrite}
	r := chi.NewRouter()

	// Everything (including /v2/) goes through requireBasicAuth when
	// credentials are configured.
	//
	// v0.5.33: /v2/ 也放进 requireBasicAuth。之前以为 OCI spec 允许 /v2/ 在需要
	// auth 时也返 200(只挂 WWW-Authenticate header),实测 docker daemon 看到 200
	// 就**以为不需要 auth**,manifest/blobs 请求**不发 Authorization header** →
	// server 返 401 → docker daemon 报 "unauthorized"(根本没带 creds,没法 retry)。
	// 正确做法:需要 auth 时返 401 + WWW-Authenticate challenge,docker daemon 看到
	// 401 后会用 config.json 里的 credentials 重试 GET /v2/ → 200,后续 manifest
	// 请求**也**带 creds 才能过。
	r.Group(func(r chi.Router) {
		if h.getCreds != nil {
			r.Use(h.requireBasicAuth)
		}
		// v0.6.13 (REG-3): register HEAD on "/" too. docker daemon / skopeo
		// / monitoring probes that only send HEAD expect 200 (and the
		// version header below). Previously a HEAD /v2/ fell through to
		// chi's default 404, mis-reporting the service as down to those
		// callers. r.HandleFunc covers GET + HEAD on the same path with
		// one handler; the handler itself doesn't care which verb came in.
		r.HandleFunc("/", h.apiVersion)

		r.Get("/_catalog", h.catalog)

		// Repository-scoped routes all go through one wildcard dispatcher.
		//
		// chi's named params cannot span "/", so the obvious
		// r.Route("/{repo}", ...) only ever matched single-segment names
		// ("nginx") and 404'd on every real-world one ("library/nginx",
		// "team/app/api"). dispatchRepoRoute takes the whole remaining
		// path and splits it itself, then hands the pieces to the very
		// same handlers through chi's route context: no handler changes.
		r.HandleFunc("/*", h.dispatchRepoRoute)
	})
	return r
}

// --- /v2/ -------------------------------------------------------------------

func (h *Handler) apiVersion(w http.ResponseWriter, r *http.Request) {
	// v0.5.33 之前:这里在未带/错 credentials 时也返 200 + WWW-Authenticate header,
	// 导致 docker daemon 误判为「不需要 auth」后续请求不发 Authorization → server 401。
	// 现在让 requireBasicAuth middleware 处理:带对 creds 走到这里返 200,否则早就
	// 401 challenge 拦掉了。这里只关心 happy path。
	//
	// v0.6.13 (REG-3): 加上 Docker-Distribution-Api-Version: registry/2.0
	// 头。Distribution Spec §"Docker Distribution API Version Header" 要求
	// V2 registry 在 200 响应里带这个头;docker daemon 与各家 SDK / 监控探针
	// 普遍用它来确认「这是 V2 registry」,缺头会让探测方走猜测逻辑。
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	_, _ = w.Write([]byte(`{}`))
}

// --- basic auth middleware --------------------------------------------------

// requireBasicAuth returns 401 + WWW-Authenticate: Basic when either:
//
//   - the Authorization header is missing
//   - it doesn't parse as Basic <base64(user:pass)>
//   - the user/pass doesn't match the configured credentials
//
// On success it sets r.Header so handlers can audit who did what. Uses
// subtle.ConstantTimeCompare on the password to avoid leaking length via
// timing.
func (h *Handler) requireBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantUser, wantPass := h.getCreds()
		if wantUser == "" || wantPass == "" {
			// credentials got cleared at runtime; fall through (treat as
			// no auth). The settings page can re-enable by editing again.
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Basic "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			challenge(w, h.realm)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
		if err != nil {
			challenge(w, h.realm)
			return
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			challenge(w, h.realm)
			return
		}
		if subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(wantPass)) != 1 {
			challenge(w, h.realm)
			return
		}
		// Tag the request with the authenticated user for handler-side
		// auditing (handlers can read r.Header.Get("X-Auth-User")).
		r.Header.Set("X-Auth-User", user)
		next.ServeHTTP(w, r)
	})
}

func challenge(w http.ResponseWriter, realm string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
}

// --- /v2/_catalog -----------------------------------------------------------

// catalog serves GET /v2/_catalog.
//
// OCI Distribution Spec §_catalog supports two query parameters:
//   ?n=<max>     — max items per response
//   ?last=<repo> — exclusive cursor; return items lexicographically AFTER this
//
// Implementation notes:
//   - We always sort first. Some storage backends return repos in arbitrary
//     order; without a stable order, ?last= is meaningless across requests.
//   - ?last= uses sort.SearchStrings (O(log n)) to skip up to and including
//     the cursor, exactly per spec (exclusive).
//   - ?n= truncates the tail. Invalid / missing / n<=0 falls through (lenient;
//     spec says "SHOULD" error on n<=0 but old cairn silently ignored and
//     existing clients depend on it).
//   - Link header is emitted ONLY when we actually truncated, so clients
//     that omit `n` (the cairn internal scanner does) don't see pagination
//     noise.
//   - repos==nil must serialize as []string{}, not null (spec compliance
//     for the JSON shape — clients like Docker CLI choke on null repos).
func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	repos, err := h.Store.Repositories(r.Context())
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	sort.Strings(repos)

	q := r.URL.Query()
	if last := q.Get("last"); last != "" {
		// sort.SearchStrings returns the leftmost insertion point for `last`
		// in the sorted slice. Two cases:
		//   a) `last` exists at idx → skip idx+1..end
		//   b) `last` doesn't exist  → items at idx..end are all > `last`
		idx := sort.SearchStrings(repos, last)
		if idx < len(repos) && repos[idx] == last {
			repos = repos[idx+1:]
		} else {
			repos = repos[idx:]
		}
	}

	truncated := false
	n := 0
	if nStr := q.Get("n"); nStr != "" {
		if v, err := strconv.Atoi(nStr); err == nil && v > 0 {
			n = v
			if n < len(repos) {
				repos = repos[:n]
				truncated = true
			}
		}
	}

	if truncated {
		// Absolute URL is required here too — see absoluteLocation comment
		// for the v0.5.44 absolute-Location work that this extends. Clients
		// that follow Link headers generally won't accept relative paths.
		next := absoluteLocation(r, fmt.Sprintf("/v2/_catalog?n=%d&last=%s",
			n, url.QueryEscape(repos[len(repos)-1])))
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
	}

	if repos == nil {
		repos = []string{}
	}
	writeV2JSON(w, http.StatusOK, map[string]any{"repositories": repos})
}

// --- /v2/<repo>/tags/list ---------------------------------------------------

func (h *Handler) tagsList(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	if repo == "" {
		writeV2Error(w, http.StatusBadRequest, "NAME_INVALID", "repo required")
		return
	}
	tags, err := h.Store.Tags(r.Context(), repo)
	if err != nil {
		// v0.6.12 (REG-2): an unknown repo is now propagated as ErrNotFound
		// from the storage layer (see filesystem.go Tags); map it to the
		// spec-canonical NAME_UNKNOWN 404. A real but empty repo still gets
		// 200 + {"tags":[]} per Distribution.
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name is unknown")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	if tags == nil {
		tags = []string{} // spec: never null
	}
	writeV2JSON(w, http.StatusOK, map[string]any{"name": repo, "tags": tags})
}

// --- /v2/<repo>/manifests/<ref> --------------------------------------------

// localEvent builds an events.Event from an observed /v2/ operation.
// Returns nil if there's nothing meaningful to record (no manifest,
// non-manifest media type, etc.) — caller treats nil as "skip".
//
// tag may be empty when the caller resolved a digest (PUT-by-digest,
// re-tag). That matches Distribution semantics: tag is the human-facing
// name and a digest-only operation simply doesn't have one. ShouldCount
// handles "" tag gracefully (it groups the row under the repo only).
//
// method should be the HTTP verb that triggered this observation;
// action is "pull" or "push" (registry-manager naming).
func (h *Handler) localEvent(repo, tag, mediaType, action, method string, r *http.Request) *events.Event {
	if h.Events == nil {
		return nil
	}
	if mediaType == "" {
		return nil
	}
	ev := &events.Event{
		Action: action,
	}
	ev.Target.MediaType = mediaType
	ev.Target.Repository = repo
	ev.Target.Tag = tag
	ev.Request.Host = r.Host
	ev.Request.Method = method
	ev.Request.UserAgent = r.UserAgent()
	ev.Request.RemoteAddr = r.RemoteAddr
	ev.Actor.Name = r.Header.Get("X-Auth-User")
	return ev
}

func (h *Handler) manifestGet(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	m, err := h.Store.GetManifest(r.Context(), repo, ref)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(m.Body)
}

func (h *Handler) manifestHead(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	m, err := h.Store.GetManifest(r.Context(), repo, ref)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.Body)))
	if ev := h.localEvent(repo, ref, m.MediaType, "pull", http.MethodHead, r); ev != nil {
		h.Events.IngestLocal(*ev)
	}
	w.WriteHeader(http.StatusOK)
	// v0.5.32: 显式 flush,避免 HEAD 在 keep-alive 下僵持。OCI spec 要求 HEAD
	// manifest 响应里有 Content-Length(让 client 知道真实大小),Go net/http 看到
	// 有 Content-Length 就会等那么多字节的 body 才 finish response —— 我们
	// 没 body 要写,server 不主动 flush 就一直等,client 看到 Content-Length:2620
	// 也等 2620 bytes,双方僵持到 client 超时 → docker daemon 报 "unauthorized"
	// (它把所有类型的失败都简化成 unauthorized)。Flush 强制 server 标记响应结束。
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *Handler) manifestPut(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	if repo == "" || ref == "" {
		writeV2Error(w, http.StatusBadRequest, "NAME_INVALID", "repo and ref required")
		return
	}
	mediaType := r.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20)) // 32MB cap; manifest JSON is small
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	// v0.6.12 (REG-1): minimal manifest sanity check. Without this the
	// server happily accepted 47 bytes of garbage JSON, computed a digest
	// over it, and returned 201 Created — pushing the failure all the way
	// to docker's unpack step. The check below is intentionally permissive
	// (JSON + schemaVersion/mediaType) so we don't reject experimental
	// mediaTypes; the strict shape checks live downstream when a real
	// client tries to use the result.
	if err := validateManifestShape(body, mediaType); err != nil {
		writeV2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", err.Error())
		return
	}
	digest, err := h.Store.PutManifest(r.Context(), repo, ref, mediaType, body)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", err.Error())
		return
	}
	if ev := h.localEvent(repo, ref, mediaType, "push", http.MethodPut, r); ev != nil {
		h.Events.IngestLocal(*ev)
	}
	// v0.6.32: drop the /api/inventory cache so the next read sees the
	// new manifest. Nil-safe — OnWrite is unset in tests.
	if h.OnWrite != nil {
		h.OnWrite()
	}
	w.Header().Set("Docker-Content-Digest", digest)
	// v0.5.44: 绝对 URL —— 参见 absoluteLocation 注释。
	w.Header().Set("Location", absoluteLocation(r, fmt.Sprintf("/v2/%s/manifests/%s", repo, digest)))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) manifestDelete(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	if !strings.HasPrefix(ref, "sha256:") {
		writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "DELETE requires digest reference, not tag")
		return
	}
	if _, err := h.Store.DeleteManifest(r.Context(), repo, ref); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// v0.6.12 (REG-4): previously returned 404 with a 0-byte body.
			// Docker CLI and most SDKs parse `{"errors":[{"code":...}]}` and
			// surface "unexpected end of JSON input" on empty bodies, hiding
			// the real reason. Write the standard envelope so callers can
			// actually see MANIFEST_UNKNOWN.
			writeV2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	// v0.6.32: drop the /api/inventory cache so the next read sees the
	// deletion. Nil-safe — OnWrite is unset in tests.
	if h.OnWrite != nil {
		h.OnWrite()
	}
	w.WriteHeader(http.StatusAccepted)
}

// --- /v2/<repo>/blobs/<digest> ---------------------------------------------

func (h *Handler) blobGet(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	digest := chi.URLParam(r, "digest")
	body, size, err := h.Store.GetBlob(r.Context(), repo, digest)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
			return
		}
		if errors.Is(err, storage.ErrInvalidDigest) {
			writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	defer body.Close()
	// v0.6.13 (REG-5): 显式声明 Content-Type 与 Accept-Ranges。之前两
	// 个头都缺:Content-Type 靠 Go 的内容嗅探给出 "text/plain; charset=utf-8"
	// (取决于层文件头几个字节),Range 请求被完全忽略(永远 200 + 全量)。
	// docker 拉层按 digest 校验不依赖 Content-Type,但自研客户端/镜像校验
	// 工具会因嗅探值误判;Range 在 Distribution 里是 MAY,这里只声明
	// 不支持,把"沉默忽略"变成契约("Accept-Ranges: none" 即可)。
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *Handler) blobHead(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "digest")
	size, err := h.Store.StatBlob(r.Context(), digest)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// v0.6.13 (REG-5): HEAD 也补 Content-Type / Accept-Ranges,语义与 GET 一致。
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
	// v0.5.32: 跟 manifestHead 同样的 keep-alive 僵持问题,见 manifestHead 注释。
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// --- upload flow -----------------------------------------------------------

func (h *Handler) uploadStart(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	if repo == "" {
		writeV2Error(w, http.StatusBadRequest, "NAME_INVALID", "repo required")
		return
	}
	uuid, err := h.Store.StartUpload(r.Context(), repo)
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	// v0.5.44: 绝对 URL —— skopeo / docker daemon 拒绝相对路径的 Location。
	w.Header().Set("Location", absoluteLocation(r, fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid)))
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", ociRangeHeader(0))
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) uploadGet(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	u, err := h.Store.GetUpload(r.Context(), repo, uuid)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	w.Header().Set("Docker-Upload-UUID", u.UUID)
	w.Header().Set("Range", ociRangeHeader(u.Size))
	// v0.5.46: GET 上传进度(204)同样要求带 Location,与 PATCH 口径一致。
	w.Header().Set("Location", absoluteLocation(r, fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, u.UUID)))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) uploadPatch(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	offset := int64(-1)
	if cr := r.Header.Get("Content-Range"); cr != "" {
		// "bytes START-END"; we only care about START.
		parts := strings.SplitN(strings.TrimPrefix(cr, "bytes "), "-", 2)
		if len(parts) >= 1 {
			fmt.Sscanf(parts[0], "%d", &offset)
		}
	}
	size, err := h.Store.PatchUpload(r.Context(), repo, uuid, offset, r.Body)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", ociRangeHeader(size))
	// v0.5.46: OCI Distribution Spec 要求 PATCH 202 响应必须带 Location(下一步的
	// 上传 URL,服务端可以借机重定位会话)。skopeo/containers-image 传完数据后
	// 从这个头拿 PUT finalize 的地址,缺了直接报
	// "Error determining upload URL: http: no Location header in response"。
	// 我们不重定位会话,原样回传同一个上传 URL(绝对形式,同 v0.5.44 口径)。
	w.Header().Set("Location", absoluteLocation(r, fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid)))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusAccepted)
}

// uploadCancel handles DELETE /v2/<repo>/blobs/uploads/<uuid>
//
// v0.6.13 (REG-6): was previously not in the upload dispatcher's switch at
// all (only GET/PATCH/PUT were routed; DELETE fell through to writeV2MethodNotAllowed).
// Docker CLI sends DELETE to cancel an in-progress push when the user hits
// Ctrl-C; without this the server side kept a stale uploads/<repo>/<uuid>/
// directory until the next GC sweep (up to 24h, see Filesystem.GC). We now
// route DELETE to Store.CancelUpload which rm-rf's the directory and returns
// 204 No Content per spec:
//
//	On a successful response, the server MUST remove the upload session
//	directory and any in-progress blobs associated with it.
//	— OCI Distribution Spec §"Blob Upload"
//
// Idempotent: a second DELETE on an already-cancelled session also returns
// 204, because CancelUpload's underlying os.RemoveAll is silent when the
// directory doesn't exist. This matches typical REST DELETE semantics and
// keeps two racing DELETEs (e.g. duplicate browser tabs) safe. ErrNotFound
// from CancelUpload would surface as 404 BLOB_UPLOAD_UNKNOWN, but in
// practice only triggers when storage-layer access fails (permissions etc.).
func (h *Handler) uploadCancel(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	if err := h.Store.CancelUpload(r.Context(), repo, uuid); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) uploadPut(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	digest := r.URL.Query().Get("digest")
	if digest == "" {
		writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "missing digest query parameter")
		return
	}
	if err := h.Store.PutUpload(r.Context(), repo, uuid, digest); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		if errors.Is(err, storage.ErrInvalidDigest) {
			writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
			return
		}
		// Digest mismatch: surface as 400 with code BLOB_UPLOAD_INVALID.
		if strings.Contains(err.Error(), "digest mismatch") {
			writeV2Error(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", err.Error())
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	// v0.5.44: 绝对 URL。
	w.Header().Set("Location", absoluteLocation(r, fmt.Sprintf("/v2/%s/blobs/%s", repo, digest)))
	w.WriteHeader(http.StatusCreated)
	// v0.6.32: blob commit can introduce a new blob — drop the
	// /api/inventory cache so the next read rebuilds and counts it.
	// Nil-safe — OnWrite is unset in tests.
	if h.OnWrite != nil {
		h.OnWrite()
	}
}

// ociRangeHeader formats the `Range` value that goes back on POST/PATCH/GET
// upload responses.
//
// RFC 7233 byte ranges are inclusive on BOTH ends — `bytes 0-N` describes
// N+1 bytes total. So a file that actually contains `size` bytes has to be
// advertised as `bytes 0-(size-1)`. The empty case (size=0, the moment
// right after POST upload-start before any bytes have arrived) is
// `bytes 0--1`, the suffix range of zero length — this is the idiom the
// Distribution reference impl uses and what strict OCI clients expect.
//
// v0.5.50 fix: the previous code wrote `bytes 0-size`, advertising exactly
// ONE byte more than the file actually contained. regsync treats this
// header as authoritative for "bytes the server has received so far" and
// used it to compute the next PATCH's Content-Range start. The +1 then
// propagated through every subsequent chunk, producing the
// "expected 7266, received 7267" size mismatch against the upstream
// manifest that bitnami/redis-cluster exposed. skopeo/containers-image
// and docker daemon happen to compute their next start from the *body
// length they just wrote* (not from the Range header) so they were
// unaffected — that's why skopeo copied the same blob successfully while
// regsync fell over.
func ociRangeHeader(size int64) string {
	if size <= 0 {
		return "0--1"
	}
	return fmt.Sprintf("0-%d", size-1)
}

// --- helpers ---------------------------------------------------------------

// writeV2JSON writes a JSON-encoded V2 spec body. We don't wrap in
// {success, code, message, data} here because /v2/* is spec-locked —
// clients parse it directly.
func writeV2JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeV2Error writes a V2 spec error document: {"errors":[{"code":..., "message":...}]}.
func writeV2Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}

// digestMatches is a small helper used by future routes; kept here to avoid
// pulling in the validation helper from elsewhere.
func digestMatches(body []byte, declared string) bool {
	if !strings.HasPrefix(declared, "sha256:") {
		return false
	}
	sum := sha256.Sum256(body)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	return actual == declared
}

// validateManifestShape is the v0.6.12 (REG-1) minimal sanity check applied
// in manifestPut before handing the bytes to the storage layer.
//
// It accepts anything that:
//   1. Parses as a JSON object
//   2. Carries a numeric schemaVersion (1 or 2; both are still in the wild)
//   3. Carries a mediaType string (the request's Content-Type is allowed
//      to differ, but the body should at least carry the field; this guards
//      against the "47 bytes of garbage JSON" case the docs/issues report)
//
// We deliberately do NOT enforce the config/layers shape — that's a spec
// contract callers are expected to know, and validating here would risk
// rejecting experimental mediaTypes (OCI artifact, WASM modules, etc.)
// that real registries store as-is. The real "did the client actually push
// something usable" check happens at the consumer's unpack step, which is
// where it belongs.
func validateManifestShape(body []byte, mediaType string) error {
	if len(body) == 0 {
		return errors.New("manifest body is empty")
	}
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return fmt.Errorf("manifest is not JSON: %w", err)
	}
	probeMT, _ := probe["mediaType"].(string)
	if probeMT == "" && mediaType == "application/octet-stream" {
		return errors.New("manifest has no mediaType field and request had no Content-Type")
	}
	probeSV, ok := probe["schemaVersion"].(float64) // json.Unmarshal always uses float64 for numbers
	if !ok {
		return errors.New("manifest is missing schemaVersion (expected 1 or 2)")
	}
	if probeSV != 1 && probeSV != 2 {
		return fmt.Errorf("manifest schemaVersion=%v is not 1 or 2", probeSV)
	}
	return nil
}

// --- repository dispatch ----------------------------------------------------

// dispatchRepoRoute is the single entry point for every repository-scoped /v2
// route.
//
// It splits the remaining path the same greedy way CNCF Distribution does --
// most specific suffix first -- and injects "repo" plus the trailing
// identifier into chi's route context, so the handlers below keep working
// unchanged on chi.URLParam.
//
// The trailing identifier must be a single path segment: tag names, digests
// and upload UUIDs never contain "/", so anything else is a malformed request
// and gets a 404 rather than being silently folded into the repository name.
func (h *Handler) dispatchRepoRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(chi.URLParam(r, "*"), "/")
	if rest == "" {
		writeV2Error(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name required")
		return
	}

	if repo, ok := strings.CutSuffix(rest, "/tags/list"); ok && repo != "" {
		if r.Method != http.MethodGet {
			writeV2MethodNotAllowed(w, http.MethodGet)
			return
		}
		injectRouteParams(r, "repo", repo)
		h.tagsList(w, r)
		return
	}

	// Upload session start: POST /v2/<name>/blobs/uploads/
	// v0.5.44: 同时接受带/不带尾斜杠的写法 —— OCI spec 写的是
	// `/v2/<name>/blobs/uploads/`(带尾斜杠),但部分客户端省略尾斜杠,
	// 之前 CutSuffix("/blobs/uploads") 漏了带尾斜杠,请求 fall through 到 404。
	// 上一个相邻的 (location) "blobs/uploads/<uuid>" 用的是带斜杠的 marker,
	// 这里保持一致 + 同时容忍无斜杠。
	if repo, ok := strings.CutSuffix(rest, "/blobs/uploads/"); ok && repo != "" {
		if r.Method != http.MethodPost {
			writeV2MethodNotAllowed(w, http.MethodPost)
			return
		}
		injectRouteParams(r, "repo", repo)
		h.uploadStart(w, r)
		return
	}
	if repo, ok := strings.CutSuffix(rest, "/blobs/uploads"); ok && repo != "" {
		if r.Method != http.MethodPost {
			writeV2MethodNotAllowed(w, http.MethodPost)
			return
		}
		injectRouteParams(r, "repo", repo)
		h.uploadStart(w, r)
		return
	}

	if repo, ref, ok := splitRepoScoped(rest, "/manifests/"); ok {
		injectRouteParams(r, "repo", repo, "ref", ref)
		switch r.Method {
		case http.MethodGet:
			h.manifestGet(w, r)
		case http.MethodHead:
			h.manifestHead(w, r)
		case http.MethodPut:
			h.manifestPut(w, r)
		case http.MethodDelete:
			h.manifestDelete(w, r)
		default:
			writeV2MethodNotAllowed(w, http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete)
		}
		return
	}

	// Checked after /manifests/ and before /blobs/ so that
	// "<repo>/blobs/uploads/<uuid>" is never read as a blob called
	// "uploads/<uuid>" -- same ordering as Distribution's route parser.
	if repo, uuid, ok := splitRepoScoped(rest, "/blobs/uploads/"); ok {
		injectRouteParams(r, "repo", repo, "uuid", uuid)
		switch r.Method {
		case http.MethodGet:
			h.uploadGet(w, r)
		case http.MethodPatch:
			h.uploadPatch(w, r)
		case http.MethodPut:
			h.uploadPut(w, r)
		// v0.6.13 (REG-6): DELETE cancels an in-progress upload session
		// (see uploadCancel). Without it docker's Ctrl-C mid-push leaves
		// a stale uploads/<repo>/<uuid>/ directory until the next GC sweep.
		case http.MethodDelete:
			h.uploadCancel(w, r)
		default:
			writeV2MethodNotAllowed(w, http.MethodGet, http.MethodPatch, http.MethodPut, http.MethodDelete)
		}
		return
	}

	if repo, digest, ok := splitRepoScoped(rest, "/blobs/"); ok {
		injectRouteParams(r, "repo", repo, "digest", digest)
		switch r.Method {
		case http.MethodGet:
			h.blobGet(w, r)
		case http.MethodHead:
			h.blobHead(w, r)
		default:
			writeV2MethodNotAllowed(w, http.MethodGet, http.MethodHead)
		}
		return
	}

	writeV2Error(w, http.StatusNotFound, "NAME_UNKNOWN", "unsupported registry path")
}

// splitRepoScoped splits "<repo><marker><tail>" at the LAST occurrence of
// marker. Both halves must be non-empty and the tail must be a single segment,
// which is what makes "a/manifests/b/manifests/c" resolve to repo
// "a/manifests/b" with ref "c" instead of repo "a".
func splitRepoScoped(rest, marker string) (repo, tail string, ok bool) {
	i := strings.LastIndex(rest, marker)
	if i <= 0 {
		return "", "", false
	}
	repo, tail = rest[:i], rest[i+len(marker):]
	if repo == "" || tail == "" || strings.Contains(tail, "/") {
		return "", "", false
	}
	return repo, tail, true
}

// injectRouteParams pushes path params into chi's route context.
//
// chi.URLParam scans the params backwards and returns the last match, so what
// we add here wins over the mount point's own (blanked) "*" param.
func injectRouteParams(r *http.Request, kv ...string) {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return
	}
	for i := 0; i+1 < len(kv); i += 2 {
		rctx.URLParams.Add(kv[i], kv[i+1])
	}
}

// writeV2MethodNotAllowed answers with a V2 error document and the Allow
// header. The wildcard route matches every method, so chi's own 405 handler
// never fires for repository paths -- we do it here.
func writeV2MethodNotAllowed(w http.ResponseWriter, allow ...string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeV2Error(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
}

// v0.5.44: Location headers must be absolute URLs per OCI Distribution Spec.
// skopeo 在 v1.13+ 拒绝相对路径的 Location("http: no Location header in response"),
// docker daemon 老版本会自己拼但新版本也卡。r.Host 是 client 的 Host header(可能经
// 反代被改写),所以优先 X-Forwarded-Proto / X-Forwarded-Host,再回退 r.TLS / r.Host。
// 跟 internal/api/handlers.go 的 registryURL(r) 同样语义。
func absoluteLocation(r *http.Request, relPath string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.ToLower(firstCSVHeader(r, "X-Forwarded-Proto")); proto == "http" || proto == "https" {
		scheme = proto
	}
	host := r.Host
	if fh := firstCSVHeader(r, "X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host + relPath
}

func firstCSVHeader(r *http.Request, name string) string {
	v := r.Header.Get(name)
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
