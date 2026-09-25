package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cairn/internal/api"
	"cairn/internal/config"
	"cairn/internal/registry"
)

// fakeBackend is a registry.Registry implementation backed by a fake HTTP
// registry. It lets us exercise the HTTP handlers end-to-end without spinning
// up a real Distribution.
type fakeBackend struct {
	catalog   []string
	tags      map[string][]string
	manifests map[string]string
}

func (f *fakeBackend) Probe(ctx context.Context) error { return nil }

func (f *fakeBackend) ListRepositories(ctx context.Context) ([]string, error) {
	return f.catalog, nil
}

func (f *fakeBackend) ListTags(ctx context.Context, repo string) ([]string, error) {
	return f.tags[repo], nil
}

func (f *fakeBackend) GetManifest(ctx context.Context, repo, ref string) (*registry.Manifest, error) {
	key := repo + "/" + ref
	body, ok := f.manifests[key]
	if !ok {
		return nil, fmt.Errorf("manifest not found: %s", key)
	}
	return &registry.Manifest{
		Digest:    "sha256:" + repo + ref,
		MediaType: "application/vnd.docker.distribution.manifest.v2+json",
		Raw:       json.RawMessage(body),
	}, nil
}

func (f *fakeBackend) DeleteManifest(ctx context.Context, repo, digest string) ([]string, error) {
	return f.tags[repo], nil
}

func (f *fakeBackend) ScanInventory(ctx context.Context) (*registry.Inventory, error) {
	inv := &registry.Inventory{
		Repositories: []registry.Repository{},
		RefreshAt:    time.Now().UTC(),
	}
	for _, name := range f.catalog {
		r := registry.Repository{Name: name}
		for _, t := range f.tags[name] {
			r.Tags = append(r.Tags, registry.TagInfo{Name: t, Digest: "sha256:" + t})
		}
		r.TagCount = len(r.Tags)
		inv.Repositories = append(inv.Repositories, r)
		inv.Totals.RepoCount++
		inv.Totals.TagCount += r.TagCount
	}
	return inv, nil
}

func newTestRouter(t *testing.T) (http.Handler, *fakeBackend) {
	t.Helper()
	cfg := &config.Config{
		RegistryURL:  "http://fake",
		RegistryName: "Test",
		AllowDelete:  true,
		AllowPull:    true,
		CacheTTL:     0, // disable caching for tests
		Env:          "test",
	}
	be := &fakeBackend{
		catalog: []string{"alpine", "redis"},
		tags: map[string][]string{
			"alpine": {"3.19", "3.20"},
			"redis":  {"7.2"},
		},
		manifests: map[string]string{
			"alpine/3.19": `{"layers":[]}`,
			"alpine/3.20": `{"layers":[]}`,
			"redis/7.2":   `{"layers":[]}`,
		},
	}
	h := &api.Handlers{Cfg: cfg, Registry: be}
	return api.NewRouter(h, cfg), be
}

func TestGetConfig(t *testing.T) {
	r, _ := newTestRouter(t)
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
	r, _ := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/inventory", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"alpine"`) || !strings.Contains(body, `"name":"redis"`) {
		t.Errorf("body missing repos: %s", body)
	}
	if !strings.Contains(body, `"tagCount":2`) || !strings.Contains(body, `"tagCount":1`) {
		t.Errorf("body missing tag counts: %s", body)
	}
}

func TestDeleteTagForbidden(t *testing.T) {
	r, _ := newTestRouter(t)
	// We can't easily flip AllowDelete on the existing router (it's frozen),
	// so just check the success path; the 403 branch is covered by the
	// dedicated TestDeleteDisabledConfig test below.
	t.Run("success path", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/tags?repo=alpine&digest=sha256:abc", nil)
		r.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"deleted":true`) {
			t.Errorf("body missing deleted:true: %s", rr.Body.String())
		}
	})
}

func TestDeleteDisabledConfig(t *testing.T) {
	cfg := &config.Config{
		RegistryURL: "http://fake",
		AllowDelete: false,
		Env:         "test",
	}
	h := &api.Handlers{Cfg: cfg, Registry: &fakeBackend{}}
	r := api.NewRouter(h, cfg)
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
	r, _ := newTestRouter(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/tags?repo=alpine", nil) // no digest
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHealthz(t *testing.T) {
	r, _ := newTestRouter(t)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}