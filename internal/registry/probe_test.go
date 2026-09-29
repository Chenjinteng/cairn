package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// quayLikeServer mimics quay.io's auth topology — the shape that broke
// anonymous Probe before v0.5.47:
//
//	GET /v2/                            → 401 + Bearer challenge WITHOUT scope
//	GET /v2/ (Bearer token)             → 200
//	GET /v2/auth  anonymous, no scope   → 401 {"error":"Requires authentication"}  ← quay quirk
//	GET /v2/auth  anonymous + scope     → 200 {"token":"anon-token"}
//	GET /v2/auth  valid Basic           → 200 {"token":"basic-token"}
//	GET /v2/auth  wrong Basic           → 401
//	GET /v2/<repo>/manifests/<tag>      → 401 + challenge WITH scope; with Bearer → 200
//
// Docker Hub differs in exactly one cell: auth.docker.io mints anonymous
// tokens even without a scope, which is why the old "ping must reach 200"
// Probe worked there and failed here.
func quayLikeServer(t *testing.T, validUser, validPass string) *httptest.Server {
	t.Helper()
	const manifestBody = `{
	  "schemaVersion": 2,
	  "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
	  "config": {"mediaType":"application/vnd.docker.container.image.v1+json","size":7023,"digest":"sha256:b5b2b2c507a0944348e03131147403ad0c4f38bced3ffd34cc0fe6c2bfd4c2a8"},
	  "layers": [{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":32654,"digest":"sha256:e692418e4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f"}]
	}`
	host := "" // filled in after the listener exists; handlers read it per request
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if u, p, ok := r.BasicAuth(); ok {
			if u == validUser && p == validPass {
				_, _ = w.Write([]byte(`{"token":"basic-token","expires_in":300}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("scope") == "" {
			// The quay.io quirk: no scope + no credentials → 401.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Requires authentication"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"anon-token","expires_in":300}`))
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+host+`/v2/auth",service="quay.test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
				w.Header().Set("Docker-Content-Digest", "sha256:b5b2b2c507a0944348e03131147403ad0c4f38bced3ffd34cc0fe6c2bfd4c2a8")
				_, _ = w.Write([]byte(manifestBody))
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+host+`/v2/auth",service="quay.test",scope="repository:prometheus/node-exporter:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	host = srv.Listener.Addr().String()
	t.Cleanup(srv.Close)
	return srv
}

func quayLikeClient(t *testing.T, srv *httptest.Server, user, pass string) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: srv.URL, Username: user, Password: pass, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// TestProbeAnonymousQuayLike is the v0.5.47 regression test: an anonymous
// Probe against a quay.io-shaped registry must succeed on the strength of
// the 401+Bearer challenge alone. The old code escalated the challenge
// into a scope-less token fetch, quay answered 401, and the whole pull
// preflight died with "TOKEN_FETCH_FAILED: token endpoint returned 401".
func TestProbeAnonymousQuayLike(t *testing.T) {
	srv := quayLikeServer(t, "quayuser", "quaypass")
	c := quayLikeClient(t, srv, "", "")
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("anonymous Probe against quay-like registry: %v", err)
	}
}

// TestAnonymousManifestAfterProbe walks the full anonymous preflight chain:
// Probe passes, then the real resource request (whose challenge DOES carry
// a scope) mints an anonymous token and fetches the manifest.
func TestAnonymousManifestAfterProbe(t *testing.T) {
	srv := quayLikeServer(t, "quayuser", "quaypass")
	c := quayLikeClient(t, srv, "", "")
	ctx := context.Background()
	if err := c.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	m, err := c.GetManifest(ctx, "prometheus/node-exporter", "v1.8.2")
	if err != nil {
		t.Fatalf("GetManifest (anonymous, scoped challenge): %v", err)
	}
	if len(m.Layers) != 1 {
		t.Errorf("layers = %d, want 1", len(m.Layers))
	}
	if want := int64(32654 + 7023); m.Size != want {
		t.Errorf("size = %d, want %d", m.Size, want)
	}
}

// TestProbeCredentialedStillValidates keeps the credential-check semantics:
// with a username configured, Probe must go through the token exchange, so
// valid creds pass and wrong creds still fail (a credentials test that
// cannot fail is worthless).
func TestProbeCredentialedStillValidates(t *testing.T) {
	srv := quayLikeServer(t, "quayuser", "quaypass")
	ctx := context.Background()

	good := quayLikeClient(t, srv, "quayuser", "quaypass")
	if err := good.Probe(ctx); err != nil {
		t.Fatalf("Probe with valid credentials: %v", err)
	}

	bad := quayLikeClient(t, srv, "quayuser", "wrong")
	err := bad.Probe(ctx)
	if err == nil {
		t.Fatal("Probe with wrong credentials must fail")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong-credentials error should mention the 401, got: %v", err)
	}
}

// TestProbePlain200 pins the no-auth path (intranet registries,
// mcr.microsoft.com, registry.k8s.io): GET /v2/ → 200, Probe → nil.
func TestProbePlain200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := quayLikeClient(t, srv, "", "")
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe against plain-200 registry: %v", err)
	}
}

// TestProbe401NoChallenge: a bare 401 without a parseable Bearer challenge
// is NOT proof of a live registry (could be a misconfigured reverse proxy)
// — Probe must fail with PROBE_FAILED.
func TestProbe401NoChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := quayLikeClient(t, srv, "", "")
	err := c.Probe(context.Background())
	if err == nil {
		t.Fatal("Probe must fail on 401 without a Bearer challenge")
	}
	var re *Error
	if !errors.As(err, &re) || re.Code != "PROBE_FAILED" {
		t.Fatalf("want *Error{Code: PROBE_FAILED}, got %v", err)
	}
}
