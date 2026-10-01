package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Chenjinteng/cairn/internal/api"
	"github.com/Chenjinteng/cairn/internal/config"
	"github.com/Chenjinteng/cairn/internal/registryd"
	"github.com/Chenjinteng/cairn/internal/storage"
)

// fakeBackend is a filesystem-backed storage.Storage in a t.TempDir().
// It exercises the same code paths as the production /api/inventory +
// /api/tags handlers, only against a real (temp) directory layout.
func newFakeStore(t *testing.T) storage.Storage {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewFilesystem(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	// Seed with one repo + one tag + one manifest so list/delete have something.
	ctx := context.Background()
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:c","size":10},"layers":[{"digest":"sha256:l","size":100,"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip"}]}`)
	if _, err := s.PutManifest(ctx, "alpine", "3.19", "application/vnd.docker.distribution.manifest.v2+json", body); err != nil {
		t.Fatalf("seed PutManifest: %v", err)
	}
	if _, err := s.PutManifest(ctx, "redis", "7.2", "application/vnd.docker.distribution.manifest.v2+json", body); err != nil {
		t.Fatalf("seed PutManifest: %v", err)
	}
	// And a blob so GetBlob works in tests.
	if _, err := s.StartUpload(ctx, "alpine"); err != nil {
		t.Fatalf("seed StartUpload: %v", err)
	}
	return s
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{Env: "test"}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "") // initialise the map
	cfg.Mutable.Set("registry.url", "fake")
	cfg.Mutable.Set("registry.name", "Test")
	cfg.Mutable.Set("allow.delete", "true")
	cfg.Mutable.Set("allow.pull", "true")
	store := newFakeStore(t)
	h := &api.Handlers{Cfg: cfg, Store: store}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)
	mux.Mount("/v2", registryd.New(store, nil, nil))
	return mux
}

func newTestRouterNoDelete(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{Env: "test"}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "") // initialise the map
	cfg.Mutable.Set("registry.url", "fake")
	cfg.Mutable.Set("allow.delete", "false")
	store := newFakeStore(t)
	h := &api.Handlers{Cfg: cfg, Store: store}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)
	mux.Mount("/v2", registryd.New(store, nil, nil))
	return mux
}

func TestGetConfig(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"name":"Test"`) {
		t.Errorf("body missing name: %s", rr.Body.String())
	}
}

// v0.6.7 hotfix: docker-compose 把容器内 8787 映射到宿主机 10001 时,
// `registry.url = "client.local"` 走保存后,GET /api/config 必须返
// host:port 都齐的 URL —— 否则右上角 badge 跟「复制 docker pull」
// 都漏掉端口,镜像拉不到。回归测试三条路径:
//
//   - HostPort != Port + saved URL 无端口 → 应追加 HostPort
//   - HostPort == Port(无映射) + saved URL 无端口 → 不应追加
//   - saved URL 已显式带端口 → 不应被 HostPort 覆盖
func TestGetConfigAppendsHostPortWhenMapped(t *testing.T) {
	cfg := &config.Config{Env: "test", Port: 8787, HostPort: 10001}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "client.local")
	cfg.Mutable.Set("registry.name", "ops")
	h := &api.Handlers{Cfg: cfg, Store: newFakeStore(t)}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"url":"http://client.local:10001"`) {
		t.Errorf("URL missing host:port (want http://client.local:10001): %s", body)
	}
	if !strings.Contains(body, `"host":"client.local:10001"`) {
		t.Errorf("host missing port (want client.local:10001): %s", body)
	}
}

func TestGetConfigNoPortWhenNoMapping(t *testing.T) {
	cfg := &config.Config{Env: "test", Port: 8787, HostPort: 8787}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "client.local")
	h := &api.Handlers{Cfg: cfg, Store: newFakeStore(t)}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `"url":"http://client.local"`) {
		t.Errorf("URL should not have port when HostPort==Port: %s", body)
	}
	if strings.Contains(body, `"host":"client.local:`) {
		t.Errorf("host should not have port when HostPort==Port: %s", body)
	}
}

func TestGetConfigRespectsExplicitPortInSavedURL(t *testing.T) {
	cfg := &config.Config{Env: "test", Port: 8787, HostPort: 10001}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "legacy.example.com:8080") // 迁移期旧值
	h := &api.Handlers{Cfg: cfg, Store: newFakeStore(t)}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `"url":"http://legacy.example.com:8080"`) {
		t.Errorf("explicit port must win over HostPort (want 8080 not 10001): %s", body)
	}
}

func TestGetInventory(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/inventory", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"alpine"`) || !strings.Contains(body, `"name":"redis"`) {
		t.Errorf("body missing repos: %s", body)
	}
	if !strings.Contains(body, `"tagCount":1`) {
		t.Errorf("body missing tag counts: %s", body)
	}
}

func TestDeleteTagSuccess(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	// First look up the digest of alpine:3.19 via /api/inventory.
	inv := httptest.NewRecorder()
	r.ServeHTTP(inv, httptest.NewRequest(http.MethodGet, "/api/inventory", nil))
	digest := extractDigest(t, inv.Body.String(), "alpine")
	if digest == "" {
		t.Fatalf("could not extract digest from inventory body: %s", inv.Body.String())
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repository=alpine&tag=3.19", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"deletedTag":"3.19"`) {
		t.Errorf("body missing deletedTag: %s", body)
	}
	if !strings.Contains(body, `"digest":"`+digest+`"`) {
		t.Errorf("body missing resolved digest %s: %s", digest, body)
	}
	if !strings.Contains(body, `"affectedTags":["3.19"]`) {
		t.Errorf("body missing affectedTags: %s", body)
	}
}

func TestDeleteDisabledConfig(t *testing.T) {
	r := newTestRouterNoDelete(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repository=alpine&tag=3.19", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("body missing FORBIDDEN: %s", rr.Body.String())
	}
}

func TestDeleteMissingParams(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repository=alpine", nil) // no tag
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHealthz(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestRegistryRoot(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (v2 root)", rr.Code)
	}
}

func TestRegistryCatalog(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/_catalog", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"repositories":["alpine","redis"]`) {
		t.Errorf("body missing repos: %s", rr.Body.String())
	}
}

func TestRegistryPushPullRoundTrip(t *testing.T) {
	r := newTestRouter(t)
	// 1. start upload
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v2/alpine/blobs/uploads/", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("start upload status=%d body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatalf("no Location header: %v", rr.Header())
	}
	// v0.5.44 起 Location 是绝对 URL(httptest.NewRequest 默认 Host=example.com),
	// 取路径最后一段作为 uuid —— 对相对/绝对两种形式都成立。
	uuid := loc[strings.LastIndex(loc, "/")+1:]
	if uuid == "" {
		t.Fatalf("cannot parse uuid from Location %q", loc)
	}

	// 2. PATCH chunk (sha256 of "hello world" = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824)
	chunk := []byte("hello world")
	patchURL := "/v2/alpine/blobs/uploads/" + uuid
	pReq := httptest.NewRequest(http.MethodPatch, patchURL, bytesReader(chunk))
	pReq.Header.Set("Content-Range", "bytes 0-10")
	pReq.Header.Set("Content-Type", "application/octet-stream")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, pReq)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("patch status=%d body=%s", rr.Code, rr.Body.String())
	}

	// 3. PUT commit (sha256 of "hello world")
	digest := "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	putURL := patchURL + "?digest=" + digest
	puReq := httptest.NewRequest(http.MethodPut, putURL, nil)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, puReq)
	if rr.Code != http.StatusCreated {
		t.Fatalf("put status=%d body=%s", rr.Code, rr.Body.String())
	}

	// 4. GET blob back
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/alpine/blobs/"+digest, nil))
	if rr.Code != 200 {
		t.Fatalf("get blob status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, _ := io.ReadAll(rr.Body)
	if string(got) != "hello world" {
		t.Errorf("blob body = %q, want %q", string(got), "hello world")
	}
}

func TestRegistryPushInvalidDigest(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v2/alpine/blobs/uploads/", nil))
	loc := rr.Header().Get("Location")
	uuid := loc[strings.LastIndex(loc, "/")+1:] // v0.5.44+: 绝对 URL,取最后一段

	// PATCH something
	pReq := httptest.NewRequest(http.MethodPatch, "/v2/alpine/blobs/uploads/"+uuid, bytesReader([]byte("abc")))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, pReq)

	// PUT commit with WRONG digest (declares sha256 of "hello" instead of "abc")
	wrongDigest := "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	puReq := httptest.NewRequest(http.MethodPut, "/v2/alpine/blobs/uploads/"+uuid+"?digest="+wrongDigest, nil)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, puReq)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"BLOB_UPLOAD_INVALID"`) {
		t.Errorf("body missing BLOB_UPLOAD_INVALID: %s", rr.Body.String())
	}
}

// extractDigest returns the manifest digest for the first tag of the
// given repo, parsed from an /api/inventory response body.
func extractDigest(t *testing.T, body, repo string) string {
	t.Helper()
	needle := `"name":"` + repo + `"`
	i := strings.Index(body, needle)
	if i < 0 {
		return ""
	}
	rest := body[i:]
	j := strings.Index(rest, `"digest":"sha256:`)
	if j < 0 {
		return ""
	}
	start := j + len(`"digest":"`)
	end := strings.Index(rest[start:], `"`)
	if end < 0 {
		return ""
	}
	return rest[start : start+end]
}

func bytesReader(b []byte) io.Reader { return &sliceR{b: b} }

// extractGenericDigest pulls the first "digest":"sha256:..." value out of
// any JSON body. Used by MA-1 tests where the response shape doesn't carry
// the repo name explicitly (storage.Manifest.Repo is json:"-" so it never
// reaches the wire).
func extractGenericDigest(t *testing.T, body string) string {
	t.Helper()
	const key = `"digest":"sha256:`
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	start := i + len(`"digest":"`)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

type sliceR struct {
	b []byte
	i int
}

func (r *sliceR) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// silence unused imports
var _ = errors.New
var _ = time.Now
var _ = os.Getenv

// --- v0.6.12 (MA-1): /api/repositories/{repo} 含 "/" 仓库名链路回归 -----
//
// 158 实测 84% 的真实仓库名都含 "/"（如 "library/alpine"、"3proxy/3proxy"）。
// 0.6.11 及之前使用 chi "{repo}" 命名参数,跨不过 "/",导致 /api/repositories
// 路由全部失效。这三段测试覆盖 wildcard dispatcher 后正确恢复的三条路径:
//   1. GET    /repositories/{repo}/tags/{tag}/manifest   → Handlers.GetManifest
//   2. DELETE /repositories/{repo}/manifests/{digest}   → ExtraHandlers.DeleteManifestByDigest
//   3. DELETE /repositories/{repo}                      → ExtraHandlers.DeleteRepository
//
// 仓库名 seed 时也用含 "/" 的（"team/foo/alpine"）以保证 dispatcher 真的在
// 处理多段 repo,而不是悄悄走单段兜底。

// multiSegmentRepoName 含 "/", 用于 MA-1 测试的所有 fixture。
const ma1MultiSegmentRepo = "team/foo/alpine"
const ma1MultiSegmentTag = "3.19"

// newRouterWithExtras builds a router including a non-nil ExtraHandlers
// (with only Store + Full wired — Vault / Proxies / DB / Events / Sync all
// nil, which the 503 guards handle as "unavailable" for those endpoints).
// Returns both the handler and the store so tests can re-seed the filesystem
// directly. This is needed for MA-1 / MA-4 / MA-3 tests because the
// dispatcher only routes DELETE /gc when extras != nil (matching how
// production wires it).
func newRouterWithExtras(t *testing.T, allowDelete string) (http.Handler, storage.Storage) {
	t.Helper()
	cfg := &config.Config{Env: "test"}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "")
	cfg.Mutable.Set("registry.url", "fake")
	cfg.Mutable.Set("registry.name", "Test")
	cfg.Mutable.Set("allow.delete", allowDelete)
	cfg.Mutable.Set("allow.pull", "true")
	store := newFakeStore(t)
	h := &api.Handlers{Cfg: cfg, Store: store}
	e := &api.ExtraHandlers{
		Full:  cfg,
		Store: store,
	}
	return api.NewRouterWithExtras(h, e, cfg).(chi.Router), store
}

func newMA1TestRouter(t *testing.T) http.Handler {
	t.Helper()
	r, store := newRouterWithExtras(t, "true")
	// Seed the multi-segment repo used by the MA-1 dispatcher tests.
	ctx := context.Background()
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:c","size":10},"layers":[{"digest":"sha256:l","size":100,"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip"}]}`)
	if _, err := store.PutManifest(ctx, ma1MultiSegmentRepo, ma1MultiSegmentTag, "application/vnd.docker.distribution.manifest.v2+json", body); err != nil {
		t.Fatalf("seed multi-segment PutManifest: %v", err)
	}
	return r
}

func TestMA1_GetManifestWithMultiSegmentRepo(t *testing.T) {
	r := newMA1TestRouter(t)
	rr := httptest.NewRecorder()
	// 字面量路径:repo 名里 / 不需要 URL 编码。
	req := httptest.NewRequest(http.MethodGet,
		"/api/repositories/"+ma1MultiSegmentRepo+"/tags/"+ma1MultiSegmentTag+"/manifest", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// GetManifest 故意不在响应里返 repo/tag（顶层 envelope 的 data 字段是
	// storage.Manifest,Repo 字段 json:"-"` —— 见 storage.go:40）。
	// 我们改验:抓出的 digest 必须 == 我们 seed 的 manifest 的 digest,
	// 也就是 dispatcher 真的把 "team/foo/alpine" + "3.19" 这两个参数解出来了。
	digest := extractGenericDigest(t, rr.Body.String())
	if digest == "" {
		t.Fatalf("body missing digest field: %s", rr.Body.String())
	}
	// sha256 of the seed body — known constant for our test fixture.
	const want = "sha256:4eeede658ee68427089bb8846998d44a9f349becca5dfb4b6a96924c61125d7f"
	if digest != want {
		t.Fatalf("dispatcher resolved wrong manifest (repo=%q tag=%q): got %s want %s",
			ma1MultiSegmentRepo, ma1MultiSegmentTag, digest, want)
	}
}

func TestMA1_DeleteManifestByDigestWithMultiSegmentRepo(t *testing.T) {
	r := newMA1TestRouter(t)
	// 拿 manifest 的 digest。
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/repositories/"+ma1MultiSegmentRepo+"/tags/"+ma1MultiSegmentTag+"/manifest", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("seed get manifest status=%d body=%s", rr.Code, rr.Body.String())
	}
	digest := extractGenericDigest(t, rr.Body.String())
	if digest == "" {
		t.Fatalf("body missing digest: %s", rr.Body.String())
	}
	// DELETE: repo 字面量 + manifest 路径
	rr = httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete,
		"/api/repositories/"+ma1MultiSegmentRepo+"/manifests/"+digest, nil)
	r.ServeHTTP(rr, delReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Errorf("body missing deleted:true: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"digest":"`+digest+`"`) {
		t.Errorf("body missing full digest: %s", rr.Body.String())
	}
}

func TestMA1_DeleteRepositoryWithMultiSegmentRepo(t *testing.T) {
	r := newMA1TestRouter(t)
	rr := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete,
		"/api/repositories/"+ma1MultiSegmentRepo, nil)
	r.ServeHTTP(rr, delReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Errorf("body missing deleted:true: %s", rr.Body.String())
	}
	// 验证仓库真的被删了 —— /api/inventory 应该不再列它
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/inventory", nil))
	if strings.Contains(rr.Body.String(), `"name":"`+ma1MultiSegmentRepo+`"`) {
		t.Errorf("multi-segment repo should be deleted but still in inventory: %s", rr.Body.String())
	}
}

// v0.6.12 (MA-4): 删一个不存在的仓库必须是 404,不是 500。
func TestMA4_DeleteNonexistentRepoReturns404(t *testing.T) {
	r, _ := newRouterWithExtras(t, "true")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/repositories/qa-probe-no-such-repo", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"code":"NOT_FOUND"`) {
		t.Errorf("body missing NOT_FOUND code: %s", rr.Body.String())
	}
}

// v0.6.12 (MA-3): /api/gc 收到截断的非法 JSON 必须 400,不能静默走完。
func TestMA3_RunGCRejectsInvalidJSON(t *testing.T) {
	r, _ := newRouterWithExtras(t, "true")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gc", strings.NewReader(`{"cleanEmptyRepos": tru`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"code":"BAD_REQUEST"`) {
		t.Errorf("body missing BAD_REQUEST code: %s", rr.Body.String())
	}
	// 同时确认空 body 仍然走默认 false(不破坏 v0.5.18 老调用方)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/gc", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty body should still be 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
}
