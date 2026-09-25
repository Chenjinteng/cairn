// Package registryd serves the OCI Distribution V2 protocol routes
// (/v2/*) over the local storage.Storage.
//
// This is what makes cairn a registry, not just a registry UI: clients
// like `docker push` / `docker pull` / `skopeo copy` can talk directly
// to cairn without needing a separate Docker Registry deployment.
//
// Endpoints implemented (V2 spec minimum viable set):
//
//	GET    /v2/                                              # 200 OK
//	GET    /v2/_catalog                                      # list repos
//	GET    /v2/<repo>/tags/list                              # list tags
//	GET    /v2/<repo>/manifests/<ref>                        # fetch
//	HEAD   /v2/<repo>/manifests/<ref>                        # exists + digest
//	PUT    /v2/<repo>/manifests/<ref>                        # upload
//	DELETE /v2/<repo>/manifests/<digest>                     # delete
//	GET    /v2/<repo>/blobs/<digest>                         # fetch
//	HEAD   /v2/<repo>/blobs/<digest>                         # exists + size
//	POST   /v2/<repo>/blobs/uploads/                         # start upload
//	GET    /v2/<repo>/blobs/uploads/<uuid>                   # inspect
//	PATCH  /v2/<repo>/blobs/uploads/<uuid>                   # append chunk
//	PUT    /v2/<repo>/blobs/uploads/<uuid>?digest=<digest>   # commit
//
// What's NOT here (deferred):
//   - Cross-repo manifest references (manifests can list blobs from
//     other repos; we treat blob digest as global, which is enough for
//     common cases but not strictly spec-compliant)
//   - Tag delete (DELETE /v2/<repo>/tags/<tag> is non-standard; we use
//     the manifest DELETE path via tagDigest)
//   - Multi-chunk manifest uploads (Docker / skopeo always PUT the
//     whole manifest in one shot)
//   - Bearer-token auth (currently anonymous; lock at the reverse proxy)
//   - Garbage collection (orphaned blobs accumulate; sweep TODO)
package registryd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"cairn/internal/storage"
)

// Handler bundles the storage and serves /v2/*. Build it via New and mount
// the returned http.Handler at /v2/* in your router.
type Handler struct {
	Store storage.Storage
}

// New returns a chi router pre-configured with the V2 protocol routes.
// Mount it at /v2/* — the routes are written relative to that prefix.
func New(store storage.Storage) http.Handler {
	h := &Handler{Store: store}
	r := chi.NewRouter()

	r.Get("/", h.apiVersion)
	r.Get("/_catalog", h.catalog)

	// Tags
	r.Route("/{repo}", func(r chi.Router) {
		r.Get("/tags/list", h.tagsList)
		r.Get("/manifests/{ref}", h.manifestGet)
		r.Head("/manifests/{ref}", h.manifestHead)
		r.Put("/manifests/{ref}", h.manifestPut)
		r.Delete("/manifests/{ref}", h.manifestDelete)

		r.Get("/blobs/{digest}", h.blobGet)
		r.Head("/blobs/{digest}", h.blobHead)

		r.Route("/blobs/uploads", func(r chi.Router) {
			r.Post("/", h.uploadStart)
			r.Get("/{uuid}", h.uploadGet)
			r.Patch("/{uuid}", h.uploadPatch)
			r.Put("/{uuid}", h.uploadPut)
		})
	})
	return r
}

// --- /v2/ -------------------------------------------------------------------

func (h *Handler) apiVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

// --- /v2/_catalog -----------------------------------------------------------

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	repos, err := h.Store.Repositories(r.Context())
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	writeV2JSON(w, http.StatusOK, map[string]any{"repositories": repos})
}

// --- /v2/<repo>/tags/list ---------------------------------------------------

func (h *Handler) tagsList(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	if repo == "" {
		writeV2Error(w, http.StatusBadRequest, "NAME_INVALID", "repo required")
		return
	}
	tags, err := h.Store.Tags(r.Context(), repo)
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	if tags == nil {
		tags = []string{} // spec: never null
	}
	writeV2JSON(w, http.StatusOK, map[string]any{"name": repo, "tags": tags})
}

// --- /v2/<repo>/manifests/<ref> --------------------------------------------

func (h *Handler) manifestGet(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	m, err := h.Store.GetManifest(r.Context(), repo, ref)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(m.Body)
}

func (h *Handler) manifestHead(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	m, err := h.Store.GetManifest(r.Context(), repo, ref)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.Body)))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) manifestPut(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	if repo == "" || ref == "" {
		writeV2Error(w, http.StatusBadRequest, "NAME_INVALID", "repo and ref required")
		return
	}
	mediaType := r.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20)) // 32MB cap; manifest JSON is small
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	digest, err := h.Store.PutManifest(r.Context(), repo, ref, mediaType, body)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", err.Error())
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, digest))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) manifestDelete(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	if !strings.HasPrefix(ref, "sha256:") {
		writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "DELETE requires digest reference, not tag")
		return
	}
	if err := h.Store.DeleteManifest(r.Context(), repo, ref); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// --- /v2/<repo>/blobs/<digest> ---------------------------------------------

func (h *Handler) blobGet(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	digest := chi.URLParam(r, "digest")
	body, size, err := h.Store.GetBlob(r.Context(), repo, digest)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
			return
		}
		if errors.Is(err, storage.ErrInvalidDigest) {
			writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	defer body.Close()
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *Handler) blobHead(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "digest")
	size, err := h.Store.StatBlob(r.Context(), digest)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

// --- upload flow -----------------------------------------------------------

func (h *Handler) uploadStart(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	if repo == "" {
		writeV2Error(w, http.StatusBadRequest, "NAME_INVALID", "repo required")
		return
	}
	uuid, err := h.Store.StartUpload(r.Context(), repo)
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid))
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) uploadGet(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	u, err := h.Store.GetUpload(r.Context(), repo, uuid)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	w.Header().Set("Docker-Upload-UUID", u.UUID)
	w.Header().Set("Range", fmt.Sprintf("0-%d", u.Size))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) uploadPatch(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	offset := int64(-1)
	if cr := r.Header.Get("Content-Range"); cr != "" {
		// "bytes START-END"; we only care about START.
		parts := strings.SplitN(strings.TrimPrefix(cr, "bytes "), "-", 2)
		if len(parts) >= 1 {
			fmt.Sscanf(parts[0], "%d", &offset)
		}
	}
	size, err := h.Store.PatchUpload(r.Context(), repo, uuid, offset, r.Body)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", fmt.Sprintf("0-%d", size))
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) uploadPut(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	uuid := chi.URLParam(r, "uuid")
	digest := r.URL.Query().Get("digest")
	if digest == "" {
		writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "missing digest query parameter")
		return
	}
	if err := h.Store.PutUpload(r.Context(), repo, uuid, digest); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
			return
		}
		if errors.Is(err, storage.ErrInvalidDigest) {
			writeV2Error(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
			return
		}
		// Digest mismatch: surface as 400 with code BLOB_UPLOAD_INVALID.
		if strings.Contains(err.Error(), "digest mismatch") {
			writeV2Error(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", err.Error())
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "UNSUPPORTED", err.Error())
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, digest))
	w.WriteHeader(http.StatusCreated)
}

// --- helpers ---------------------------------------------------------------

// writeV2JSON writes a JSON-encoded V2 spec body. We don't wrap in
// {success, code, message, data} here because /v2/* is spec-locked —
// clients parse it directly.
func writeV2JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeV2Error writes a V2 spec error document: {"errors":[{"code":..., "message":...}]}.
func writeV2Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}

// digestMatches is a small helper used by future routes; kept here to avoid
// pulling in the validation helper from elsewhere.
func digestMatches(body []byte, declared string) bool {
	if !strings.HasPrefix(declared, "sha256:") {
		return false
	}
	sum := sha256.Sum256(body)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	return actual == declared
}