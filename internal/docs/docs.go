// Package docs serves the embedded Swagger UI + OpenAPI spec under /api/docs.
//
// v0.7.19: hand-rolled OpenAPI 3.0 spec (internal/docs/openapi.yaml) +
// vendored swagger-ui 5.33.1 static bundle (internal/docs/dist/). No
// build-time npm/swag CLI needed; the only "external" content is the
// vendored dist directory which is checked into git.
//
// Two routes:
//
//	GET /api/docs              — swagger-ui HTML shell
//	GET /api/docs/openapi.yaml — the spec itself
//
// Both are served with no auth (same as /healthz); the /api/docs page
// is for human operators reading the API, not for clients to consume.
package docs

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
)

//go:embed all:dist
var distFS embed.FS

//go:embed openapi.yaml
var specFS embed.FS

// Mount registers /api/docs routes on r. The swagger-ui HTML expects
// the spec at "/api/docs/openapi.yaml" (relative URL set in the
// vendored swagger-initializer.js), so the spec is served there even
// though it lives at /api/docs/openapi.yaml in the API surface.
func Mount(r chi.Router) {
	r.Get("/api/docs", serveIndex)
	r.Get("/api/docs/openapi.yaml", serveSpec)
	r.Get("/api/docs/*", serveDist)
}

func serveIndex(w http.ResponseWriter, _ *http.Request) {
	// swagger-ui's index.html asks the browser to load
	// ./swagger-ui.css, ./swagger-ui-bundle.js, etc. — all relative.
	// Serve the index from the embedded dist/ tree.
	body, err := distFS.ReadFile("dist/index.html")
	if err != nil {
		http.Error(w, "docs: index.html not embedded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

func serveSpec(w http.ResponseWriter, _ *http.Request) {
	body, err := specFS.ReadFile("openapi.yaml")
	if err != nil {
		http.Error(w, "docs: openapi.yaml not embedded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// serveDist serves every other file in dist/ (css / js / favicon).
// Chi's wildcard "/api/docs/*" captures everything after the prefix;
// we trim it back to a dist-relative path and read from the embed.
func serveDist(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	// Defensive: stop path traversal. dist/ is flat (no subdirs), but
	// a stray "../something" should 404 not silently read outside.
	clean := path.Clean(rest)
	if clean == "." || strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
		http.NotFound(w, r)
		return
	}
	f, err := distFS.Open("dist/" + clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	ctype := mimeByExt(clean)
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	// Static assets — long cache is fine, the spec is the only thing
	// that changes and it lives on its own route with no-cache.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, clean, stat.ModTime(), rs)
		return
	}
	// Fallback if embed ever returns a non-seekable handle (modernc
	// / io/fs doesn't, but the contract is technically satisfied
	// without it).
	buf := make([]byte, stat.Size())
	if _, err := f.Read(buf); err != nil {
		http.Error(w, "docs: read failed", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(buf)
}

// mimeByExt returns a Content-Type for the swagger-ui assets we ship.
// Kept narrow — anything we don't recognise gets no header, which
// triggers http.ServeContent's sniffer.
func mimeByExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	}
	return ""
}

// Unused — keep imports tidy if someone deletes the Stat fallback.
var (
	_ fs.File
)
