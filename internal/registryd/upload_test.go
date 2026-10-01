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

	"github.com/Chenjinteng/cairn/internal/storage"
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
	if got, want := resp.Header.Get("Range"), "0--1"; got != want {
		// v0.5.50: empty range after POST upload-start is "bytes 0--1"
		// (suffix range of zero length per RFC 7233). Old code used
		// "0-0" which advertised one byte more than actually received.
		t.Errorf("POST upload start: Range = %q, want %q", got, want)
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
	// Range semantics match CNCF distribution: "0-<last byte received>",
	// where byte positions are inclusive on both ends (RFC 7233).
	// PatchUpload returns the accumulated size; the header end is
	// size-1. (v0.5.50: previously this used `size` itself, advertising
	// one byte MORE than received — fine for skopeo which doesn't read
	// the Range header, but regsync took it as authoritative and
	// computed a +1-byte Content-Range start, breaking every subsequent
	// chunk.)
	if got, want := resp.Header.Get("Range"), fmt.Sprintf("0-%d", len(payload)-1); got != want {
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

// --- v0.6.13 (REG-3/5/6) regression ----------------------------------------

// TestREG3_RootVersionHeaderAndHEAD covers REG-3: GET /v2/ must carry the
// Docker-Distribution-Api-Version header, and HEAD /v2/ must also be 200
// (not 404). Before this fix, HEAD fell through to chi's default 404 and
// docker daemon / monitoring probes that only send HEAD misreported the
// service as down.
func TestREG3_RootVersionHeaderAndHEAD(t *testing.T) {
	store, err := storage.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	root := chi.NewRouter()
	root.Mount("/v2", New(store, nil, nil))
	srv := httptest.NewServer(root)
	defer srv.Close()

	// GET /v2/
	resp, err := http.Get(srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v2/ status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Errorf("GET /v2/ Docker-Distribution-Api-Version = %q, want %q", got, "registry/2.0")
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("GET /v2/ Content-Type = %q, want application/json*", got)
	}

	// HEAD /v2/
	resp, err = http.Head(srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("HEAD /v2/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD /v2/ status = %d, want 200 (was 404 before REG-3)", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Errorf("HEAD /v2/ Docker-Distribution-Api-Version = %q, want %q", got, "registry/2.0")
	}
}

// TestREG5_BlobContentTypeAndAcceptRanges covers REG-5: GET/HEAD on a blob
// must carry explicit Content-Type: application/octet-stream and
// Accept-Ranges: none (the contract declaring we don't honour Range).
//
// Before this fix the Content-Type came from Go's content sniffer and was
// "text/plain; charset=utf-8" depending on the layer's first bytes; Range
// was silently ignored (always 200 + full body).
func TestREG5_BlobContentTypeAndAcceptRanges(t *testing.T) {
	store, err := storage.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	root := chi.NewRouter()
	root.Mount("/v2", New(store, nil, nil))
	srv := httptest.NewServer(root)
	defer srv.Close()

	// Push a tiny blob so we can probe its headers.
	const repo = "reg5"
	payload := []byte("REG-5 payload")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// Start upload + PATCH chunk + PUT finalize, like TestUploadFlowLocationHeaders.
	startResp, err := http.Post(srv.URL+"/v2/"+repo+"/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	loc := startResp.Header.Get("Location")
	startResp.Body.Close()
	if loc == "" {
		t.Fatalf("StartUpload: missing Location header")
	}
	patchReq, _ := http.NewRequest(http.MethodPatch, loc, bytes.NewReader(payload))
	patchReq.Header.Set("Content-Type", "application/octet-stream")
	patchReq.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d", len(payload)-1))
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PatchUpload: %v", err)
	}
	patchResp.Body.Close()
	putReq, _ := http.NewRequest(http.MethodPut, loc+"?digest="+digest, nil)
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PutUpload: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PutUpload status = %d, want 201", putResp.StatusCode)
	}

	// Now GET the blob back and check headers.
	getResp, err := http.Get(srv.URL + "/v2/" + repo + "/blobs/" + digest)
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET blob status = %d, want 200", getResp.StatusCode)
	}
	if got := getResp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("GET blob Content-Type = %q, want application/octet-stream", got)
	}
	if got := getResp.Header.Get("Accept-Ranges"); got != "none" {
		t.Errorf("GET blob Accept-Ranges = %q, want none", got)
	}

	// Range request: must be a no-op (200 + full body, NOT 206).
	rangeReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/v2/"+repo+"/blobs/"+digest, nil)
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeResp, err := http.DefaultClient.Do(rangeReq)
	if err != nil {
		t.Fatalf("GET blob with Range: %v", err)
	}
	defer rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusOK {
		t.Errorf("Range request status = %d, want 200 (no Range support yet)", rangeResp.StatusCode)
	}
	body, _ := io.ReadAll(rangeResp.Body)
	if !bytes.Equal(body, payload) {
		t.Errorf("Range request body = %q, want full payload %q", body, payload)
	}

	// HEAD should mirror the headers too.
	headResp, err := http.Head(srv.URL + "/v2/" + repo + "/blobs/" + digest)
	if err != nil {
		t.Fatalf("HEAD blob: %v", err)
	}
	headResp.Body.Close()
	if got := headResp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("HEAD blob Content-Type = %q, want application/octet-stream", got)
	}
	if got := headResp.Header.Get("Accept-Ranges"); got != "none" {
		t.Errorf("HEAD blob Accept-Ranges = %q, want none", got)
	}
}

// TestREG6_CancelUploadSucceeds covers REG-6: DELETE /v2/<repo>/blobs/uploads/<uuid>
// must now be 204 (was 405 before), must remove the session directory, and
// must be idempotent (second DELETE returns 404).
//
// Without a cancel endpoint, docker CLI's Ctrl-C during push leaves a stale
// uploads/<repo>/<uuid>/ directory until the next GC sweep (up to 24h).
func TestREG6_CancelUploadSucceeds(t *testing.T) {
	store, err := storage.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	root := chi.NewRouter()
	root.Mount("/v2", New(store, nil, nil))
	srv := httptest.NewServer(root)
	defer srv.Close()

	const repo = "reg6"

	// 1. Start an upload session.
	startResp, err := http.Post(srv.URL+"/v2/"+repo+"/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	startResp.Body.Close()
	loc := startResp.Header.Get("Location")
	uuid := startResp.Header.Get("Docker-Upload-UUID")
	if loc == "" || uuid == "" {
		t.Fatalf("StartUpload: missing Location or Docker-Upload-UUID header")
	}

	// 2. PATCH a chunk so the session has data on disk.
	patchReq, _ := http.NewRequest(http.MethodPatch, loc, bytes.NewReader([]byte("REG-6 chunk")))
	patchReq.Header.Set("Content-Type", "application/octet-stream")
	patchReq.Header.Set("Content-Range", "bytes 0-8")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PatchUpload: %v", err)
	}
	patchResp.Body.Close()

	// 3. DELETE the session.
	req, _ := http.NewRequest(http.MethodDelete, loc, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE upload: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE upload status = %d, want 204 (was 405 before REG-6)", delResp.StatusCode)
	}
	if got := delResp.Header.Get("Docker-Upload-UUID"); got != uuid {
		t.Errorf("DELETE response Docker-Upload-UUID = %q, want %q", got, uuid)
	}

	// 4. Idempotency: a second DELETE on an already-cancelled session
	// still returns 204 (Storage.CancelUpload's os.RemoveAll is silent
	// when the dir doesn't exist — no error — so the handler treats
	// both calls as success). This matches typical REST DELETE
	// semantics ("delete is idempotent") and avoids racing between two
	// client retries (e.g. an idle browser tab + a fresh one).
	req, _ = http.NewRequest(http.MethodDelete, loc, nil)
	delResp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE upload (second): %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("second DELETE status = %d, want 204 (idempotent)", delResp.StatusCode)
	}
}

// TestREG6_CancelUploadAllowHeader verifies the Allow response header on a
// 405 path (method not in {GET, PATCH, PUT}) includes DELETE now that REG-6
// is wired in.
func TestREG6_CancelUploadAllowHeader(t *testing.T) {
	store, err := storage.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	root := chi.NewRouter()
	root.Mount("/v2", New(store, nil, nil))
	srv := httptest.NewServer(root)
	defer srv.Close()

	// Start a session just to have a valid uuid to probe.
	startResp, err := http.Post(srv.URL+"/v2/probe/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	startResp.Body.Close()
	loc := startResp.Header.Get("Location")
	if loc == "" {
		t.Fatalf("StartUpload: missing Location")
	}

	// OPTIONS is not in {GET, PATCH, PUT, DELETE} so we get 405 with Allow.
	optReq, _ := http.NewRequest(http.MethodOptions, loc, nil)
	optResp, err := http.DefaultClient.Do(optReq)
	if err != nil {
		t.Fatalf("OPTIONS upload: %v", err)
	}
	optResp.Body.Close()
	if optResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("OPTIONS status = %d, want 405", optResp.StatusCode)
	}
	allow := optResp.Header.Get("Allow")
	for _, want := range []string{"GET", "PATCH", "PUT", "DELETE"} {
		if !strings.Contains(allow, want) {
			t.Errorf("Allow header %q missing %s", allow, want)
		}
	}
}
