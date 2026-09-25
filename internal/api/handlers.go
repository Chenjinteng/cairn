package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"cairn/internal/config"
	"cairn/internal/registry"
	"cairn/internal/storage"
	"cairn/internal/version"
)

// Handlers bundles the dependencies every endpoint needs. It's passed by
// value so handlers can be registered on a chi router as plain functions.
//
// As of v0.3, the browse/delete endpoints operate on the local storage
// (cairn IS the registry); the registry.Registry field is kept only for
// future use by pull jobs that fetch from external sources.
type Handlers struct {
	Cfg     *config.Config
	Store   storage.Storage
	// Registry is the (optional) external registry client used by pull jobs
	// to fetch from upstream sources. Admin browse/delete no longer use it.
	Registry registry.Registry
}

// PublicConfig is the JSON shape returned by GET /api/config.
type PublicConfig struct {
	Version         string `json:"version"`
	RegistryName    string `json:"registryName"`
	RegistryURL     string `json:"registryUrl"`
	DataDir         string `json:"dataDir"`
	AllowDelete     bool   `json:"allowDelete"`
	AllowPull       bool   `json:"allowPull"`
	HasBasicAuth    bool   `json:"hasBasicAuth"`
	CacheTTLSeconds int    `json:"cacheTtlSeconds"`
}

// GetConfig returns the safe-to-expose subset of Cfg.
func (h *Handlers) GetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, PublicConfig{
		Version:         version.Version,
		RegistryName:    h.Cfg.RegistryName,
		RegistryURL:     h.Cfg.RegistryURL,
		DataDir:         h.Cfg.CredentialsDir,
		AllowDelete:     h.Cfg.AllowDelete,
		AllowPull:       h.Cfg.AllowPull,
		HasBasicAuth:    h.Cfg.RegistryUsername != "",
		CacheTTLSeconds: int(h.Cfg.CacheTTL.Seconds()),
	})
}

// Probe tests that we can read the local registry root.
func (h *Handlers) Probe(w http.ResponseWriter, r *http.Request) {
	if _, err := h.Store.Repositories(r.Context()); err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetInventory returns the full inventory (built from local storage).
func (h *Handlers) GetInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := buildInventory(r.Context(), h.Store)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// RefreshInventory rebuilds the inventory synchronously.
func (h *Handlers) RefreshInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := buildInventory(r.Context(), h.Store)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// DeleteTag removes a manifest by digest from a repository.
//
// URL: DELETE /api/tags?repo=<name>&digest=<sha256:...>
//
// 403 if cfg.AllowDelete is false.
// 400 if repo or digest missing.
// 200 returns the affected tags (best-effort: tags whose manifests have
// already been removed in the meantime are silently skipped).
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
	if err := h.Store.DeleteManifest(r.Context(), repo, digest); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// Recompute affected tags for the response.
	affected := tagsForDigest(r.Context(), h.Store, repo, digest)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":  true,
		"repo":     repo,
		"digest":   digest,
		"affected": affected,
		"note":     "Manifest references removed; disk space is reclaimed only after registry garbage-collect.",
	})
}

// GetManifest returns the manifest for a single tag.
func (h *Handlers) GetManifest(w http.ResponseWriter, r *http.Request) {
	repo := chiURLParam(r, "repo")
	tag := chiURLParam(r, "tag")
	if repo == "" || tag == "" {
		writeError(w, r, http.StatusBadRequest, errMissingParam)
		return
	}
	m, err := h.Store.GetManifest(r.Context(), repo, tag)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	m.Repo = repo
	// Best-effort: extract created/architecture/size from body.
	extractManifestMetadata(m)
	writeJSON(w, http.StatusOK, m)
}

// --- inventory builder -----------------------------------------------------

// Inventory is the JSON shape returned by GET /api/inventory.
//
// Mirrors the shape registry-manager's inventory.mjs produced so the React
// frontend works without changes.
type Inventory struct {
	Repositories []Repository   `json:"repositories"`
	Totals       InventoryTotal `json:"totals"`
	RefreshAt    time.Time      `json:"refreshAt"`
	FailedTags   []FailedTag    `json:"failedTags,omitempty"`
}

type Repository struct {
	Name      string    `json:"name"`
	TagCount  int       `json:"tagCount"`
	Tags      []TagInfo `json:"tags"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TagInfo struct {
	Name         string     `json:"name"`
	Digest       string     `json:"digest,omitempty"`
	Size         int64      `json:"size,omitempty"`
	Created      *time.Time `json:"created,omitempty"`
	Architecture string     `json:"architecture,omitempty"`
}

type InventoryTotal struct {
	RepoCount  int   `json:"repoCount"`
	TagCount   int   `json:"tagCount"`
	LayerCount int   `json:"layerCount"`
	TotalSize  int64 `json:"totalSize"`
}

type FailedTag struct {
	Repo  string `json:"repo"`
	Tag   string `json:"tag"`
	Error string `json:"error"`
}

// buildInventory walks local storage to assemble the inventory.
//
// Per-repo errors are collected into FailedTags; the rest of the inventory
// still renders so the UI can show "12 of 77 read failed".
func buildInventory(ctx context.Context, store storage.Storage) (*Inventory, error) {
	inv := &Inventory{RefreshAt: time.Now().UTC()}
	repos, err := store.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range repos {
		repo := Repository{Name: name, UpdatedAt: time.Now().UTC()}
		tags, err := store.Tags(ctx, name)
		if err != nil {
			inv.FailedTags = append(inv.FailedTags, FailedTag{Repo: name, Tag: "(list)", Error: err.Error()})
		}
		for _, t := range tags {
			digest, err := store.TagDigest(ctx, name, t)
			if err != nil {
				inv.FailedTags = append(inv.FailedTags, FailedTag{Repo: name, Tag: t, Error: err.Error()})
				continue
			}
			m, err := store.GetManifest(ctx, name, t)
			if err != nil {
				inv.FailedTags = append(inv.FailedTags, FailedTag{Repo: name, Tag: t, Error: err.Error()})
				continue
			}
			tag := TagInfo{Name: t, Digest: digest, Size: int64(len(m.Body))}
			if !m.CreatedAt.IsZero() {
				ct := m.CreatedAt
				tag.Created = &ct
			}
			repo.Tags = append(repo.Tags, tag)
			inv.Totals.TotalSize += tag.Size
		}
		repo.TagCount = len(repo.Tags)
		inv.Repositories = append(inv.Repositories, repo)
		inv.Totals.RepoCount++
		inv.Totals.TagCount += repo.TagCount
	}
	return inv, nil
}

// extractManifestMetadata best-effort parses m.Body for created/architecture.
// We don't decode the full OCI image config (which lives in a separate
// blob) at this level; the per-tag detail view does that on demand.
func extractManifestMetadata(m *storage.Manifest) {
	// Minimal JSON peek for size; richer parsing would need the image
	// config blob. For v0.3 we surface CreatedAt (manifest mtime) and
	// leave Created / Architecture unset.
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
}

// tagsForDigest lists all tags in repo that currently point at digest.
// Best-effort: missing-tag fetches are skipped silently.
func tagsForDigest(ctx context.Context, store storage.Storage, repo, digest string) []string {
	tags, err := store.Tags(ctx, repo)
	if err != nil {
		return nil
	}
	var hits []string
	for _, t := range tags {
		d, err := store.TagDigest(ctx, repo, t)
		if err != nil {
			continue
		}
		if d == digest {
			hits = append(hits, t)
		}
	}
	return hits
}