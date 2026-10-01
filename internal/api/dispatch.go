package api

// This file is the /api/repositories/* wildcard dispatcher.
//
// Why: chi's named params ("{repo}", "{tag}", "{digest}") can't contain "/",
// so a route like /api/repositories/{repo}/manifests/{digest} only ever matches
// single-segment repo names. Real-world registry names almost always contain
// at least one "/" ("library/alpine", "3proxy/3proxy"), and 84% of the
// repositories on the UAT reference registry fall there too (see
// docs/issues/management-api.md MA-1). The /v2/* side solved this the same
// way (internal/registryd/routes.go:115-123): mount a single wildcard route,
// split the path by hand, then push "repo" + tail params into chi's route
// context so the existing handlers keep working unchanged.
//
// Supported actions after the dispatcher splits the path:
//
//	GET    /repositories/{repo}/tags/{tag}/manifest  → Handlers.GetManifest
//	DELETE /repositories/{repo}                      → ExtraHandlers.DeleteRepository
//	DELETE /repositories/{repo}/manifests/{digest}    → ExtraHandlers.DeleteManifestByDigest
//
// Everything else → 404 with the same JSON envelope the rest of /api/* uses.

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// dispatchRepositoriesRoute is the single entry point for every /api/repositories/*
// route. rest is the path tail after "/api/repositories/" with surrounding
// slashes already trimmed.
//
// It mirrors the structure of internal/registryd.dispatchRepoRoute but only
// handles the three actions listed above; the registry protocol has more
// actions and stricter semantics that don't belong in the management API.
//
// e may be nil (the v0.1 router passes nil extras to keep the surface tight);
// in that case the DELETE actions return 404 since the rest of the DELETE
// machinery (allow-delete gate, storage handle) lives on ExtraHandlers.
func dispatchRepositoriesRoute(w http.ResponseWriter, r *http.Request, h *Handlers, e *ExtraHandlers) {
	rest := strings.Trim(chi.URLParam(r, "*"), "/")
	if rest == "" {
		writeError(w, r, http.StatusNotFound, errRepoRequired)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// GET /repositories/{repo}/tags/{tag}/manifest
		// rest = "<repo>/tags/<tag>/manifest"
		if h != nil {
			if repo, tag, ok := splitGetManifest(rest); ok {
				injectRouteParams(r, "repo", repo, "tag", tag)
				h.GetManifest(w, r)
				return
			}
		}
	case http.MethodDelete:
		if e == nil {
			writeError(w, r, http.StatusNotFound, errRepoRequired)
			return
		}
		// DELETE /repositories/{repo}/manifests/{digest}
		// rest = "<repo>/manifests/<digest>"
		if repo, digest, ok := splitLastSegment(rest, "/manifests/"); ok {
			injectRouteParams(r, "repo", repo, "digest", digest)
			e.DeleteManifestByDigest(w, r)
			return
		}
		// DELETE /repositories/{repo}
		// rest = "<repo>" (with no further slashes after trim)
		if !strings.Contains(rest, "/manifests/") {
			injectRouteParams(r, "repo", rest)
			e.DeleteRepository(w, r)
			return
		}
	}

	writeError(w, r, http.StatusNotFound, errUnsupportedRepoPath)
}

// splitGetManifest parses "<repo>/tags/<tag>/manifest" into repo + tag.
// It splits at the LAST "/tags/" so a repo name like "team/tags/fun/manifest"
// would match with repo="team/tags/fun" and tag="manifest", which is wrong
// — but tag names are single-segment per the rest of the codebase, and a
// repo containing "/tags/" is so unusual we treat it as 404 (the handler
// would error anyway on a non-existent tag).
func splitGetManifest(rest string) (repo, tag string, ok bool) {
	const tagsMarker = "/tags/"
	const tail = "/manifest"
	i := strings.LastIndex(rest, tagsMarker)
	if i <= 0 {
		return "", "", false
	}
	after := rest[i+len(tagsMarker):]
	if !strings.HasSuffix(after, tail) {
		return "", "", false
	}
	tag = strings.TrimSuffix(after, tail)
	if tag == "" || strings.Contains(tag, "/") {
		return "", "", false
	}
	return rest[:i], tag, true
}

// splitLastSegment splits "<repo><marker><tail>" at the LAST occurrence of
// marker. Both halves must be non-empty and the tail must be a single segment
// (no further "/"). Same shape as registryd.splitRepoScoped; we keep a local
// copy so the two packages stay independent.
func splitLastSegment(rest, marker string) (repo, tail string, ok bool) {
	i := strings.LastIndex(rest, marker)
	if i <= 0 {
		return "", "", false
	}
	repo, tail = rest[:i], rest[i+len(marker):]
	if repo == "" || tail == "" || strings.Contains(tail, "/") {
		return "", "", false
	}
	return repo, tail, true
}

// injectRouteParams pushes path params into chi's route context so handlers
// using chi.URLParam(r, "repo") / "digest" / "tag" keep working unchanged.
//
// chi.URLParam scans the params backwards and returns the last match, so
// what we add here wins over the mount point's own (blanked) "*" param.
//
// Copy of registryd.injectRouteParams kept local to avoid widening that
// package's exported surface for one consumer.
func injectRouteParams(r *http.Request, kv ...string) {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return
	}
	for i := 0; i+1 < len(kv); i += 2 {
		rctx.URLParams.Add(kv[i], kv[i+1])
	}
}