package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Chenjinteng/cairn/internal/api"
	"github.com/Chenjinteng/cairn/internal/config"
	"github.com/Chenjinteng/cairn/internal/db"
	"github.com/Chenjinteng/cairn/internal/registry"
	"github.com/Chenjinteng/cairn/internal/registryd"
	"github.com/Chenjinteng/cairn/internal/storage"
	syncpkg "github.com/Chenjinteng/cairn/internal/sync"
)

// syncTestHarness spins up a fake source registry, a tmp storage, a
// tmp SQLite, and wires an in-process cairn API. Returns the api
// handler mux + the source URL so individual tests can drive the
// sync endpoints end-to-end.
//
// The harness is intentionally minimal — it covers the surface that
// smoke-tests for /api/sync/* need. Full lifecycle (engine.Run under
// load, cron, retention) is exercised by internal/sync/*_test.go.
type syncTestHarness struct {
	mux      http.Handler
	src      *fakeSyncSource
	cleanup  func()
}

// fakeSyncSource is a minimal V2 server used by the API tests. Defined
// in this file because it's only needed at the API surface; the sync
// package tests use their own copy.
type fakeSyncSource struct {
	server  *httptest.Server
	repos   map[string][]string // repo → tags
	blobs   map[string][]byte
	manifests map[string][]byte // tag → raw manifest bytes
}

func newFakeSyncSource() *fakeSyncSource {
	fs := &fakeSyncSource{
		repos:     map[string][]string{},
		blobs:     map[string][]byte{},
		manifests: map[string][]byte{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", fs.handle)
	fs.server = httptest.NewServer(mux)
	return fs
}

func (f *fakeSyncSource) Close() { f.server.Close() }
func (f *fakeSyncSource) URL() string { return f.server.URL }

func (f *fakeSyncSource) handle(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	parts := strings.Split(p, "/")
	if len(parts) == 1 && parts[0] == "_catalog" {
		names := make([]string, 0, len(f.repos))
		for n := range f.repos {
			names = append(names, n)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": names})
		return
	}
	if len(parts) < 3 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	kind := parts[len(parts)-2]
	ref := parts[len(parts)-1]
	repo := strings.Join(parts[:len(parts)-2], "/")

	switch kind {
	case "tags":
		tags := f.repos[repo]
		_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
	case "manifests":
		body, ok := f.manifests[repo+":"+ref]
		if !ok {
			http.Error(w, "no such tag", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		_, _ = w.Write(body)
	case "blobs":
		d := strings.TrimPrefix(ref, "uploads/")
		if u, err := url.Parse(ref); err == nil {
			if qd := u.Query().Get("digest"); qd != "" {
				d = qd
			}
		}
		body, ok := f.blobs[d]
		if !ok {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	default:
		http.Error(w, "unknown kind", http.StatusNotFound)
	}
}

func newSyncTestHarness(t *testing.T) *syncTestHarness {
	t.Helper()
	src := newFakeSyncSource()

	dir := t.TempDir()
	storeDB, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	localStore, err := storage.NewFilesystem(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatalf("storage.NewFilesystem: %v", err)
	}

	syncStore := syncpkg.NewStore(storeDB)
	syncEngine := syncpkg.NewEngine(
		localStore,
		func(_ context.Context, baseURL, user, pass string) (*registry.Client, error) {
			return registry.NewClient(registry.Config{BaseURL: baseURL, Username: user, Password: pass})
		},
		nil, // no vault in this harness
		nil,
	)
	apiSync := api.NewSyncAPI(syncEngine, syncStore)

	// Real *config.Config so the API constructor is happy. Env=test
	// and minimal settings.
	cfg := &config.Config{Env: "test"}
	cfg.Mutable = &config.Mutable{}
	cfg.Mutable.Set("registry.url", "http://fake")
	cfg.Mutable.Set("allow.delete", "true")
	cfg.Mutable.Set("allow.pull", "true")

	h := &api.Handlers{Cfg: cfg, Store: localStore}
	extras := &api.ExtraHandlers{
		Full: cfg,
		Cfg:  &api.ConfigExtras{},
		DB:   storeDB,
		Sync: apiSync,
	}
	mux := api.NewRouterWithExtras(h, extras, cfg).(chi.Router)
	mux.Mount("/v2", registryd.New(localStore, nil, nil))

	return &syncTestHarness{
		mux: mux,
		src: src,
		cleanup: func() {
			src.Close()
			_ = storeDB.Close()
		},
	}
}

// -- the actual test cases -----------------------------------------------

func TestSyncAPI_CreateAndList(t *testing.T) {
	h := newSyncTestHarness(t)
	defer h.cleanup()

	// Create
	body := map[string]any{
		"name":      "nightly",
		"direction": "pull",
		"sourceUrl": h.src.URL(),
		"denyList":  []string{"library/busybox"},
	}
	bb, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rr.Code, rr.Body.String())
	}
	created := decodeDataTask(t, rr.Body.Bytes())
	if created.ID == "" || created.Name != "nightly" {
		t.Fatalf("created task shape: %+v", created)
	}
	if len(created.DenyList) != 1 {
		t.Fatalf("deny list round-trip: %v", created.DenyList)
	}

	// List
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/", nil)
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status %d", rr.Code)
	}
	list := decodeDataTasks(t, rr.Body.Bytes())
	if len(list) != 1 {
		t.Fatalf("expected 1 task, got %d", len(list))
	}
}

func TestSyncAPI_DirectionPushRejected(t *testing.T) {
	h := newSyncTestHarness(t)
	defer h.cleanup()

	body := map[string]any{
		"name":      "x",
		"direction": "push",
		"sourceUrl": h.src.URL(),
	}
	bb, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("push direction should be rejected with 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSyncAPI_Get404(t *testing.T) {
	h := newSyncTestHarness(t)
	defer h.cleanup()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/st-doesnotexist", nil)
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing task should be 404, got %d", rr.Code)
	}
}

func TestSyncAPI_RunTaskEndToEnd(t *testing.T) {
	h := newSyncTestHarness(t)
	defer h.cleanup()

	// Seed a fake source: 1 repo, 1 tag, 1 config blob, 1 layer blob.
	cfgContent := []byte("fake-config-json")
	layerContent := []byte("fake-layer-tar-gz")
	cfgDigest := digestString(cfgContent)
	layerDigest := digestString(layerContent)
	manifest := []byte(fmt.Sprintf(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {"mediaType":"application/vnd.docker.container.image.v1+json","digest":"%s","size":%d},
		"layers": [{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"%s","size":%d}]
	}`, cfgDigest, len(cfgContent), layerDigest, len(layerContent)))
	repo := "library/alpine"
	h.src.repos[repo] = []string{"3.19"}
	h.src.manifests[repo+":3.19"] = manifest
	h.src.blobs[cfgDigest] = cfgContent
	h.src.blobs[layerDigest] = layerContent

	// Create a task pointing at the source.
	body := map[string]any{
		"name":      "alpine-nightly",
		"direction": "pull",
		"sourceUrl": h.src.URL(),
	}
	bb, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	created := decodeDataTask(t, rr.Body.Bytes())

	// Trigger a run.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/"+created.ID+"/run", nil)
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("run: %d body=%s", rr.Code, rr.Body.String())
	}
	run := decodeDataRun(t, rr.Body.Bytes())
	if run.State != syncpkg.RunStateSuccess {
		t.Fatalf("run state = %q, want success; err=%q", run.State, run.Error)
	}
	if run.BlobsCopied != 2 {
		t.Fatalf("expected 2 blobs copied, got %d", run.BlobsCopied)
	}
	if run.ManifestsCopied != 1 {
		t.Fatalf("expected 1 manifest copied, got %d", run.ManifestsCopied)
	}

	// Listing runs should return this one.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/"+created.ID+"/runs", nil)
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list runs: %d", rr.Code)
	}
	runs := decodeDataRuns(t, rr.Body.Bytes())
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	// Re-run should be a no-op for blobs (idempotency).
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/"+created.ID+"/run", nil)
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second run: %d body=%s", rr.Code, rr.Body.String())
	}
	run2 := decodeDataRun(t, rr.Body.Bytes())
	if run2.BlobsCopied != 0 {
		t.Fatalf("second run should not copy any blobs, got %d", run2.BlobsCopied)
	}
	if run2.BlobsSkipped != 2 {
		t.Fatalf("second run should skip 2 blobs, got %d", run2.BlobsSkipped)
	}
}

func TestSyncAPI_CredentialWithoutVault(t *testing.T) {
	h := newSyncTestHarness(t)
	defer h.cleanup()

	// credentialId without a vault must be rejected at create time
	// — saving a task with a dangling credential id leads to silent
	// failure at /run, which is worse UX than the 400 here.
	body := map[string]any{
		"name":         "x",
		"direction":    "pull",
		"sourceUrl":    h.src.URL(),
		"credentialId": "some-id",
	}
	bb, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("credentialId without vault should be 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "vault") {
		t.Fatalf("error body should mention vault, got %s", rr.Body.String())
	}
}

// digestString is a minimal sha256 helper to avoid importing the full
// crypto packages at the top — these tests only need a stable digest.
func digestString(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// decodeDataTask extracts a Task from the API's standard envelope
// ({success,code,message,data}). The api.writeJSON helper wraps every
// successful body in this envelope; the test harness needs to unpack
// it to assert on the task fields.
func decodeDataTask(t *testing.T, body []byte) syncpkg.Task {
	t.Helper()
	var env struct {
		Data syncpkg.Task `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, string(body))
	}
	return env.Data
}

func decodeDataTasks(t *testing.T, body []byte) []syncpkg.Task {
	t.Helper()
	var env struct {
		Data []syncpkg.Task `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, string(body))
	}
	return env.Data
}

func decodeDataRun(t *testing.T, body []byte) syncpkg.Run {
	t.Helper()
	var env struct {
		Data syncpkg.Run `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, string(body))
	}
	return env.Data
}

func decodeDataRuns(t *testing.T, body []byte) []syncpkg.Run {
	t.Helper()
	var env struct {
		Data []syncpkg.Run `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, string(body))
	}
	return env.Data
}