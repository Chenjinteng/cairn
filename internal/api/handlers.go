package api

import (
	"errors"
	"net/http"

	"cairn/internal/config"
	"cairn/internal/registry"
)

// Handlers bundles the dependencies every endpoint needs. It's passed by
// value so handlers can be registered on a chi router as plain functions.
type Handlers struct {
	Cfg      *config.Config
	Registry registry.Registry
	// Cached is set when Registry is a *CachedRegistry; nil is fine because
	// /api/refresh works regardless by delegating to Invalidate when available.
	Cached *registry.CachedRegistry
}

// PublicConfig is the JSON shape returned by GET /api/config.
//
// We intentionally exclude secrets (REGISTRY_PASSWORD, REGISTRY_CREDENTIAL_KEY,
// REGISTRY_NOTIFY_TOKEN). registry-manager's /api/config does the same.
type PublicConfig struct {
	RegistryName     string `json:"registryName"`
	RegistryURL      string `json:"registryUrl"`
	AllowDelete      bool   `json:"allowDelete"`
	AllowPull        bool   `json:"allowPull"`
	HasBasicAuth     bool   `json:"hasBasicAuth"`
	CacheTTLSeconds  int    `json:"cacheTtlSeconds"`
}

// GetConfig returns the safe-to-expose subset of Cfg.
func (h *Handlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, PublicConfig{
		RegistryName:    h.Cfg.RegistryName,
		RegistryURL:     h.Cfg.RegistryURL,
		AllowDelete:     h.Cfg.AllowDelete,
		AllowPull:       h.Cfg.AllowPull,
		HasBasicAuth:    h.Cfg.RegistryUsername != "",
		CacheTTLSeconds: int(h.Cfg.CacheTTL.Seconds()),
	})
}

// Probe tests registry reachability.
func (h *Handlers) Probe(w http.ResponseWriter, r *http.Request) {
	if err := h.Registry.Probe(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetInventory returns the full inventory (cached when fresh).
func (h *Handlers) GetInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := h.Registry.ScanInventory(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// RefreshInventory invalidates the cache and re-scans synchronously so the
// UI gets fresh data without a follow-up GET.
//
// registry-manager's /api/refresh kicks off an async scan; we do it sync
// because (a) scans are fast for normal registries and (b) the cache TTL is
// already short, so synchronous refresh is predictable.
func (h *Handlers) RefreshInventory(w http.ResponseWriter, r *http.Request) {
	if h.Cached != nil {
		h.Cached.Invalidate()
	}
	inv, err := h.Registry.ScanInventory(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// DeleteTag removes a manifest by digest from a repository.
//
// URL: DELETE /api/tags?repo=<name>&digest=<sha256:...>
//
// 403 if cfg.AllowDelete is false (read-only mode).
// 400 if repo or digest missing.
// 502 wrapping the registry's error otherwise (so the UI can show "registry
// said MANIFEST_UNKNOWN" verbatim).
func (h *Handlers) DeleteTag(w http.ResponseWriter, r *http.Request) {
	if !h.Cfg.AllowDelete {
		writeError(w, r, http.StatusForbidden, errDeleteDisabled)
		return
	}
	repo := r.URL.Query().Get("repo")
	digest := r.URL.Query().Get("digest")
	if repo == "" || digest == "" {
		writeError(w, r, http.StatusBadRequest, errMissingParam)
		return
	}
	affected, err := h.Registry.DeleteManifest(r.Context(), repo, digest)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":   true,
		"repo":      repo,
		"digest":    digest,
		"affected":  affected,
		"note":      "Manifest references removed; disk space is reclaimed only after registry garbage-collect.",
	})
}

// GetManifest returns the manifest for a single tag. Used by the per-tag
// detail view; the inventory list only carries lightweight TagInfo.
//
// URL: GET /api/repositories/{repo}/tags/{tag}/manifest
func (h *Handlers) GetManifest(w http.ResponseWriter, r *http.Request) {
	repo := chiURLParam(r, "repo")
	tag := chiURLParam(r, "tag")
	if repo == "" || tag == "" {
		writeError(w, r, http.StatusBadRequest, errMissingParam)
		return
	}
	m, err := h.Registry.GetManifest(r.Context(), repo, tag)
	if err != nil {
		var re *registry.Error
		if errors.As(err, &re) && re.IsNotFound() {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	m.Tag = tag
	writeJSON(w, http.StatusOK, m)
}