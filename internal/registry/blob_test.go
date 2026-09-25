package registry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testBlobDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestGetBlobStreamsLiveBody is the v0.5.6 regression test for the pull
// failure "http2: response body closed": GetBlob used to route through
// doRequest, which drains the body into memory and closes it via defer,
// so callers got a dead stream on first read. GetBlob must hand back a
// live body that reads to completion.
func TestGetBlobStreamsLiveBody(t *testing.T) {
	payload := bytes.Repeat([]byte("layer-bytes-"), 500000) // ~6MB, bigger than any intermediate buffer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/v2/library/alpine/blobs/" + testBlobDigest; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	c, err := NewClient(Config{BaseURL: ts.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	rc, size, err := c.GetBlob(context.Background(), "library/alpine", testBlobDigest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	defer rc.Close()
	if size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", size, len(payload))
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read blob body: %v (pre-fix this was: http2: response body closed)", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestGetBlobBearerRetryStreams verifies the blob download survives the
// 401 -> Bearer token -> retry flow and still returns a live stream, and
// that the anonymous token request carries no Authorization header
// (the v0.5.5 fix, re-pinned at the blob layer).
func TestGetBlobBearerRetryStreams(t *testing.T) {
	payload := []byte("blob-after-token")
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			t.Errorf("anonymous token request must not carry Authorization, got %q", h)
		}
		_, _ = fmt.Fprint(w, `{"token":"tok-blob"}`)
	})
	mux.HandleFunc("/v2/library/alpine/blobs/"+testBlobDigest, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-blob" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry.test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c, err := NewClient(Config{BaseURL: ts.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	rc, _, err := c.GetBlob(context.Background(), "library/alpine", testBlobDigest)
	if err != nil {
		t.Fatalf("GetBlob with bearer retry: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read blob body after bearer retry: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

// TestBearerRetryDoesNotDowngradeToBasic pins the v0.5.6 fix in doRequest:
// for a client configured with username/password, the retried business
// request must carry the Bearer token -- SetBasicAuth must not clobber it
// (SetBasicAuth replaces the whole Authorization header, which downgraded
// the retry to Basic and made Docker Hub-style registries 401 again).
// The token endpoint, in contrast, MUST see the Basic credentials.
func TestBearerRetryDoesNotDowngradeToBasic(t *testing.T) {
	manifestJSON := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":7,"digest":"` + testBlobDigest + `"},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":32,"digest":"` + testBlobDigest + `"}]}`
	sawTokenBasic := false
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "user" && p == "pass" {
			sawTokenBasic = true
		}
		_, _ = fmt.Fprint(w, `{"token":"tok-manifest"}`)
	})
	mux.HandleFunc("/v2/library/alpine/manifests/3.19", func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok-manifest" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry.test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", testBlobDigest)
		_, _ = fmt.Fprint(w, manifestJSON)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c, err := NewClient(Config{BaseURL: ts.URL, Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	m, err := c.GetManifest(context.Background(), "library/alpine", "3.19")
	if err != nil {
		t.Fatalf("GetManifest: %v (pre-fix: retry downgraded to Basic -> second 401)", err)
	}
	if m.Digest != testBlobDigest {
		t.Errorf("digest = %q, want %q", m.Digest, testBlobDigest)
	}
	if !sawTokenBasic {
		t.Error("token endpoint never received the Basic credentials")
	}
}
