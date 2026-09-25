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

	"cairn/internal/api"
	"cairn/internal/config"
	"cairn/internal/registryd"
	"cairn/internal/storage"
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
	cfg := &config.Config{
		RegistryURL:  "http://fake",
		RegistryName: "Test",
		AllowDelete:  true,
		AllowPull:    true,
		CacheTTL:     0,
		Env:          "test",
	}
	store := newFakeStore(t)
	h := &api.Handlers{Cfg: cfg, Store: store}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)
	mux.Mount("/v2", registryd.New(store))
	return mux
}

func newTestRouterNoDelete(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{
		RegistryURL: "http://fake",
		AllowDelete: false,
		Env:         "test",
	}
	store := newFakeStore(t)
	h := &api.Handlers{Cfg: cfg, Store: store}
	mux := api.NewRouterWithExtras(h, nil, cfg).(chi.Router)
	mux.Mount("/v2", registryd.New(store))
	return mux
}

func TestGetConfig(t *testing.T) {
	r := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"registryName":"Test"`) {
		t.Errorf("body missing registryName: %s", rr.Body.String())
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
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repo=alpine&digest="+digest, nil)
	r.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Errorf("body missing deleted:true: %s", rr.Body.String())
	}
}

func TestDeleteDisabledConfig(t *testing.T) {
	r := newTestRouterNoDelete(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repo=alpine&digest=sha256:abc", nil)
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
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repo=alpine", nil) // no digest
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
	uuid := strings.TrimPrefix(loc, "/v2/alpine/blobs/uploads/")
	if uuid == "" {
		t.Fatalf("no Location header: %v", rr.Header())
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
	uuid := strings.TrimPrefix(loc, "/v2/alpine/blobs/uploads/")

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