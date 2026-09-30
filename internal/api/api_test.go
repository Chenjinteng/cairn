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

// v0.5.54 hotfix (与 0.6.7 同步): docker-compose 把容器内 8787 映射到宿主机
// 10001 时,`registry.url = "10.11.27.58"` 走保存后,GET /api/config 必须
// 返 host:port 都齐的 URL —— 否则右上角 badge 跟「复制 docker pull」都漏掉
// 端口,镜像拉不到。回归测试三条路径:
//
//   - HostPort != Port + saved URL 无端口 → 应追加 HostPort
//   - HostPort == Port(无映射) + saved URL 无端口 → 不应追加
//   - saved URL 已显式带端口 → 不应被 HostPort 覆盖
func TestGetConfigAppendsHostPortWhenMapped(t *testing.T) {
	cfg := &config.Config{Env: "test", Port: 8787, HostPort: 10001}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "10.11.27.58")
	cfg.Mutable.Set("registry.name", "ops")
	h := &api.Handlers{Cfg: cfg, Store: newFakeStore(t)}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"url":"http://10.11.27.58:10001"`) {
		t.Errorf("URL missing host:port (want http://10.11.27.58:10001): %s", body)
	}
	if !strings.Contains(body, `"host":"10.11.27.58:10001"`) {
		t.Errorf("host missing port (want 10.11.27.58:10001): %s", body)
	}
}

func TestGetConfigNoPortWhenNoMapping(t *testing.T) {
	cfg := &config.Config{Env: "test", Port: 8787, HostPort: 8787}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "10.11.27.58")
	h := &api.Handlers{Cfg: cfg, Store: newFakeStore(t)}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `"url":"http://10.11.27.58"`) {
		t.Errorf("URL should not have port when HostPort==Port: %s", body)
	}
	if strings.Contains(body, `"host":"10.11.27.58:`) {
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
