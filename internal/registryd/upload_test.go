package registryd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"cairn/internal/storage"
)

// TestUploadFlowLocationHeaders walks the full OCI push flow through the
// embedded registry routes and asserts every response carries the headers
// the spec mandates — in particular the absolute `Location` header that
// skopeo/containers-image reads after *each* step:
//
//	POST /v2/<repo>/blobs/uploads/   → 202 + Location  (v0.5.44 abs URL, v0.5.45 trailing slash)
//	PATCH /v2/<repo>/blobs/uploads/<uuid> → 202 + Location + Range  (v0.5.46 — the skopeo blocker)
//	GET  /v2/<repo>/blobs/uploads/<uuid>  → 204 + Location + Range  (v0.5.46)
//	PUT  /v2/<repo>/blobs/uploads/<uuid>?digest=… → 201 + Location + Docker-Content-Digest
//
// Regression guard: before v0.5.46 uploadPatch returned only
// Docker-Upload-UUID + Range, and skopeo aborted with
// "Error determining upload URL: http: no Location header in response"
// right after the PATCH — no PUT was ever attempted.
func TestUploadFlowLocationHeaders(t *testing.T) {
	store, err := storage.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	// Mount exactly like production (internal/server/server.go): the
	// registryd router lives under /v2 and chi strips that prefix before
	// the wildcard dispatcher sees the path.
	root := chi.NewRouter()
	root.Mount("/v2", New(store, nil, nil))
	srv := httptest.NewServer(root)
	defer srv.Close()

	const repo = "test"
	payload := []byte("cairn upload flow regression payload")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// absoluteLocation must produce URLs rooted at the test server's
	// scheme://host — assert every Location we see carries that prefix.
	wantPrefix := srv.URL + "/v2/" + repo + "/"

	// --- 1. POST upload start (OCI-spec trailing slash) ---
	resp, err := http.Post(srv.URL+"/v2/"+repo+"/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("POST upload start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST upload start: status = %d, want 202", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST upload start: missing Location header")
	}
	if !strings.HasPrefix(loc, wantPrefix+"blobs/uploads/") {
		t.Fatalf("POST upload start: Location %q is not absolute under %q", loc, wantPrefix)
	}
	if resp.Header.Get("Docker-Upload-UUID") == "" {
		t.Error("POST upload start: missing Docker-Upload-UUID header")
	}
	if got := resp.Header.Get("Range"); got != "0-0" {
		t.Errorf("POST upload start: Range = %q, want 0-0", got)
	}

	// --- 2. PATCH the chunk (this is where skopeo needs Location) ---
	req, err := http.NewRequest(http.MethodPatch, loc, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build PATCH request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("PATCH: status = %d, want 202", resp.StatusCode)
	}
	patchLoc := resp.Header.Get("Location")
	if patchLoc == "" {
		t.Fatal("PATCH: missing Location header (skopeo would abort here with 'no Location header in response')")
	}
	if !strings.HasPrefix(patchLoc, wantPrefix+"blobs/uploads/") {
		t.Fatalf("PATCH: Location %q is not absolute under %q", patchLoc, wantPrefix)
	}
	// Range semantics match CNCF distribution: "0-<total bytes so far>"
	// (an empty session reports "0-0", which is also what the OCI spec's
	// POST example shows). PatchUpload returns the accumulated size.
	if got, want := resp.Header.Get("Range"), fmt.Sprintf("0-%d", len(payload)); got != want {
		t.Errorf("PATCH: Range = %q, want %q", got, want)
	}
	if resp.Header.Get("Docker-Upload-UUID") == "" {
		t.Error("PATCH: missing Docker-Upload-UUID header")
	}

	// --- 3. GET upload progress ---
	resp, err = http.Get(patchLoc)
	if err != nil {
		t.Fatalf("GET upload progress: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("GET upload progress: status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); !strings.HasPrefix(got, wantPrefix+"blobs/uploads/") {
		t.Errorf("GET upload progress: Location = %q, want absolute URL under %q", got, wantPrefix+"blobs/uploads/")
	}

	// --- 4. PUT finalize ---
	finalizeURL := patchLoc + "?digest=" + digest
	req, err = http.NewRequest(http.MethodPut, finalizeURL, nil)
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT finalize: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT finalize: status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Errorf("PUT finalize: Docker-Content-Digest = %q, want %q", got, digest)
	}
	if got := resp.Header.Get("Location"); got != wantPrefix+"blobs/"+digest {
		t.Errorf("PUT finalize: Location = %q, want %q", got, wantPrefix+"blobs/"+digest)
	}

	// --- 5. roundtrip: the blob is now fetchable ---
	resp, err = http.Get(srv.URL + "/v2/" + repo + "/blobs/" + digest)
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET blob: status = %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("GET blob: body = %q, want %q", got, payload)
	}
}
