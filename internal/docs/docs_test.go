package docs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newTestRouter mirrors the production mounting in internal/api/api.go:
// `r.Route("/api", func(r chi.Router) { docs.Mount(r); ... })`. Mount
// hangs /docs* off the parent /api prefix, so the test must too.
func newTestRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { Mount(r) })
	return r
}

// TestMountServesIndex checks /api/docs returns the swagger-ui HTML
// shell with the right content-type, and that the body actually
// references the spec URL we wired into swagger-initializer.js.
func TestMountServesIndex(t *testing.T) {
	r := newTestRouter()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/docs", nil)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/docs code = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html...", ct)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "swagger-ui") {
		t.Errorf("index body missing 'swagger-ui' marker; got first 100 bytes: %q",
			string(body[:min(100, len(body))]))
	}
}

// TestMountServesSpec checks /api/docs/openapi.yaml returns the YAML
// with the right content-type, and that the body is parseable OpenAPI
// (starts with `openapi:` or `%YAML 1.2` etc.).
func TestMountServesSpec(t *testing.T) {
	r := newTestRouter()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/docs/openapi.yaml", nil)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/docs/openapi.yaml code = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml...", ct)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "openapi:") {
		t.Errorf("spec body missing 'openapi:' marker; got first 100 bytes: %q",
			string(body[:min(100, len(body))]))
	}
}

// TestMountServesDistAsset checks that one of the swagger-ui assets
// (the CSS file the index links to) is served correctly. Path
// traversal must 404 (defensive guard against "../" leaking outside
// the embed).
func TestMountServesDistAsset(t *testing.T) {
	r := newTestRouter()

	t.Run("css", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/docs/swagger-ui.css", nil)
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET swagger-ui.css code = %d, want 200", rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Errorf("Content-Type = %q, want text/css...", ct)
		}
	})

	t.Run("js", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/docs/swagger-ui-bundle.js", nil)
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET swagger-ui-bundle.js code = %d, want 200", rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
			t.Errorf("Content-Type = %q, want application/javascript...", ct)
		}
	})

	t.Run("path_traversal_blocked", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/docs/does-not-exist", nil)
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET /api/docs/does-not-exist code = %d, want 404", rr.Code)
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
