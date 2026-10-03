package docs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
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

// TestSpecIsValidYAMLShape sanity-checks the spec file: required top-level
// fields are present, and any single-line summary / description that
// embeds a colon (e.g. "Content-Type: application/x-tar") is wrapped in
// quotes — otherwise YAML misinterprets the colon as a mapping separator
// and swagger-ui fails to render with that exact "bad indentation of a
// mapping entry" message.
//
// Full YAML parsing is intentionally not done here — pulling gopkg.in/yaml
// just for a 50-endpoint spec is not worth a new runtime dep. The strict
// checks below catch the same class of mistakes that bit v0.7.19.
func TestSpecIsValidYAMLShape(t *testing.T) {
	body, err := specFS.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	text := string(body)

	for _, marker := range []string{
		"openapi: 3.0.3",
		"title: Cairn API",
		"/api/docs:",
		"/api/config:",
		"/api/pull/jobs:",
		"/api/stats/summary:",
		"/api/sync/:",
		"components:",
		"schemas:",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("spec missing required marker %q", marker)
		}
	}

	// Only flag single-line summary/description that has an embedded
	// colon *not* followed by a space-and-quote (which would make it a
	// valid quote-prefixed string with a colon inside). Block scalars
	// (| or >) and explicitly-quoted strings are fine.
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		var rest string
		switch {
		case strings.HasPrefix(trimmed, "summary:"):
			rest = strings.TrimSpace(strings.TrimPrefix(trimmed, "summary:"))
		case strings.HasPrefix(trimmed, "description:"):
			rest = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
		default:
			continue
		}
		if rest == "" || rest[0] == '|' || rest[0] == '>' || rest[0] == '#' {
			continue
		}
		// If quoted, OK regardless of contents.
		if rest[0] == '"' || rest[0] == '\'' {
			continue
		}
		// Bare: only safe if it has no colon at all, or every colon is
		// followed by whitespace (key for next mapping entry would need
		// a space then content). The classic bad pattern is bare text
		// containing "key: value" pairs in parentheses — "summary: foo
		// (X: Y)" parses as a mapping and crashes.
		if hasUnquotedMappingColon(rest) {
			t.Errorf("line %d: bare summary/description with embedded colon — wrap in quotes: %q",
				i+1, line)
		}
	}
}

// hasUnquotedMappingColon returns true if s contains a ":" followed by a
// non-whitespace, non-quote character — the YAML pattern that triggers
// "mapping values are not allowed here". Whitespace, then a quote, is OK
// because that's how a list-of-key-value pairs would parse.
func hasUnquotedMappingColon(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != ':' {
			continue
		}
		// Skip the colon at i; check the next non-whitespace char.
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j >= len(s) {
			continue // trailing colon, fine
		}
		// A colon followed by quote + value means it's a quoted string
		// with a colon, which is fine. Look at the next char.
		if s[j] == '"' || s[j] == '\'' {
			continue
		}
		return true
	}
	return false
}

// TestSpecConformsOpenAPI3 runs the full OpenAPI 3.0 semantic validation
// against the embedded spec using kin-openapi. This catches the class
// of bugs that pure YAML parsing misses (e.g. inline flow-style mappings
// silently truncated on commas, operations missing the required
// `responses` field, schema types in conflict). Earlier rounds of
// v0.7.19 shipped with three such issues — this test would have caught
// all three at unit-test time.
//
// The trade-off: pulls github.com/getkin/kin-openapi as a test-only
// dependency. The library is pure Go (no cgo), single-purpose, and we
// already vet dependencies per AGENTS.md, so the cost is just the
// module-graph bytes.
func TestSpecConformsOpenAPI3(t *testing.T) {
	body, err := specFS.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(body)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("OpenAPI 3.0 validation failed: %v", err)
	}
}
