package api

// v0.7.1: regression tests for the %2F repo encoding bug.
//
// Bug: /api/repositories/mcr.microsoft.com%2Fplaywright/tags/v1.61.0-jammy/export
// returns a 0-byte tar because chi preserves %2F in r.URL.RawPath and the
// dispatcher injectRouteParams pushed the literal "%2F" string straight
// into the route context — so handlers built paths like
//
//	/app/data/registry/repos/mcr.microsoft.com%2Fplaywright/...
//
// which does not exist (the real dir is mcr.microsoft.com/playwright/...).
//
// The fix is a one-liner in injectRouteParams (url.PathUnescape on each
// value before adding it to chi's RouteContext), but we lock the contract
// down with these tests so future refactors can't quietly re-introduce it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// splitGetExport is a pure string splitter; the contract is that it returns
// the repo substring verbatim, including any %XX sequences the URL carried.
// The dispatcher contract for URL decoding lives in injectRouteParams.
func TestSplitGetExport(t *testing.T) {
	cases := []struct {
		name    string
		rest    string
		wantR   string
		wantT   string
		wantOk  bool
	}{
		{
			name:   "single segment repo",
			rest:   "alpine/tags/3.19/export",
			wantR:  "alpine",
			wantT:  "3.19",
			wantOk: true,
		},
		{
			name:   "two-segment repo, no encoding",
			rest:   "library/alpine/tags/3.19/export",
			wantR:  "library/alpine",
			wantT:  "3.19",
			wantOk: true,
		},
		{
			name:   "two-segment repo, %2F-encoded (the bug case)",
			rest:   "mcr.microsoft.com%2Fplaywright/tags/v1.61.0-jammy/export",
			wantR:  "mcr.microsoft.com%2Fplaywright",
			wantT:  "v1.61.0-jammy",
			wantOk: true,
		},
		{
			name:   "three-segment repo, encoded",
			rest:   "registry%2Fns%2Fapp/tags/v1/export",
			wantR:  "registry%2Fns%2Fapp",
			wantT:  "v1",
			wantOk: true,
		},
		{
			name:   "missing /export tail → no match",
			rest:   "alpine/tags/3.19",
			wantOk: false,
		},
		{
			name:   "multi-segment tag → no match",
			rest:   "alpine/tags/3.19/extra/export",
			wantOk: false,
		},
		{
			name:   "empty tag → no match",
			rest:   "alpine/tags//export",
			wantOk: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotR, gotT, gotOk := splitGetExport(c.rest)
			if gotR != c.wantR || gotT != c.wantT || gotOk != c.wantOk {
				t.Fatalf("splitGetExport(%q) = (%q, %q, %v), want (%q, %q, %v)",
					c.rest, gotR, gotT, gotOk, c.wantR, c.wantT, c.wantOk)
			}
		})
	}
}

// injectRouteParams must URL-decode each value before adding it to chi's
// route context. The contract is verified by reading back via chi.URLParam
// from a request that already has a wildcard RouteContext set up (this is
// how the production router mounts /api/repositories/*).
func TestInjectRouteParams_DecodesPercentEncodedValues(t *testing.T) {
	r := chiRouteContextReq("/api/repositories/*", "mcr.microsoft.com%2Fplaywright/tags/v1.61.0-jammy/export")

	injectRouteParams(r, "repo", "mcr.microsoft.com%2Fplaywright", "tag", "v1.61.0-jammy")

	if got := chi.URLParam(r, "repo"); got != "mcr.microsoft.com/playwright" {
		t.Errorf("chi.URLParam(repo) = %q, want %q", got, "mcr.microsoft.com/playwright")
	}
	if got := chi.URLParam(r, "tag"); got != "v1.61.0-jammy" {
		t.Errorf("chi.URLParam(tag) = %q, want %q", got, "v1.61.0-jammy")
	}
}

// injectRouteParams must leave values that have no %XX sequence untouched.
func TestInjectRouteParams_LeavesPlainValuesUnchanged(t *testing.T) {
	r := chiRouteContextReq("/api/repositories/*", "library/alpine/tags/3.19/manifest")

	injectRouteParams(r, "repo", "library/alpine", "tag", "3.19")

	if got := chi.URLParam(r, "repo"); got != "library/alpine" {
		t.Errorf("chi.URLParam(repo) = %q, want %q", got, "library/alpine")
	}
	if got := chi.URLParam(r, "tag"); got != "3.19" {
		t.Errorf("chi.URLParam(tag) = %q, want %q", got, "3.19")
	}
}

// injectRouteParams must tolerate invalid % escapes by leaving the value
// as-is. This keeps the dispatcher from panicking on adversarial input.
//
// Note: we can't put a literal "%ZZ" in the request path because httptest
// validates URL syntax at construction. We bypass it by pushing the raw
// value directly through injectRouteParams, which is the only path that
// reaches url.PathUnescape.
func TestInjectRouteParams_ToleratesInvalidEscape(t *testing.T) {
	r := chiRouteContextReq("/api/repositories/*", "anything/tags/v1/export")

	// Manually inject a raw, malformed escape — exactly the shape a
	// pathological upstream might produce before reaching injectRouteParams.
	injectRouteParams(r, "repo", "x%ZZ", "tag", "v1")

	if got := chi.URLParam(r, "repo"); got != "x%ZZ" {
		t.Errorf("chi.URLParam(repo) = %q, want %q (PathUnescape error → unchanged)", got, "x%ZZ")
	}
	if got := chi.URLParam(r, "tag"); got != "v1" {
		t.Errorf("chi.URLParam(tag) = %q, want %q", got, "v1")
	}
}

// chiRouteContextReq builds a request whose RouteContext has the given
// wildcard value already populated, mirroring how NewRouterWithExtras
// mounts /api/repositories/* in production.
func chiRouteContextReq(pattern, wildcardValue string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/repositories/"+wildcardValue, nil)
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{pattern}
	rctx.URLParams.Add("*", wildcardValue)
	r = r.WithContext(r.Context())
	// NewRouter / dispatchRepositoriesRoute both call chi.RouteContext on
	// r.Context(), so we attach the params via the conventional shim.
	type ctxKey struct{}
	r = r.WithContext(attachChiCtx(r.Context(), rctx))
	return r
}

// attachChiCtx stores a chi.RouteContext inside the request's context under
// the key chi uses internally. The shim is local to this test file.
func attachChiCtx(ctx context.Context, rctx *chi.Context) context.Context {
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}
