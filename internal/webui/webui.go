// Package webui serves the embedded React SPA (web/ built by vite).
//
// The assets are compiled in ONLY with the `webui` build tag:
//
//	CGO_ENABLED=0 go build -tags webui ./cmd/server
//
// Without the tag a stub is linked in and every request gets a 404 with a
// hint — this keeps `go build ./...` working on a fresh checkout where
// web/dist has not been produced yet (and keeps CI fast).
package webui

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// Handler returns an http.Handler that serves the SPA with client-side
// routing fallback: unknown paths (that are not real files) get index.html
// so react-router can render its 404/redirect logic.
func Handler() http.Handler {
	assets, ok := Assets()
	if !ok {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web UI is not embedded in this binary (rebuild with `-tags webui` and a built web/dist)", http.StatusNotFound)
		})
	}

	fileServer := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean the path and strip the leading slash for fs lookups.
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := assets.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve index.html for any non-file route.
		index, err := assets.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer index.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, index)
	})
}

// Enabled reports whether SPA assets are embedded in this binary.
func Enabled() bool {
	_, ok := Assets()
	return ok
}

// Assets returns the embedded dist tree (root = index.html) and whether it
// is present. Implemented by assets_built.go (webui tag) / assets_stub.go.
func Assets() (fs.FS, bool) { return assetsFS() }
