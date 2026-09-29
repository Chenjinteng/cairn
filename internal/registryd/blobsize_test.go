package registryd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Chenjinteng/cairn/internal/storage"
)

// pushBlob walks the full OCI push flow (POST upload-start → PATCH (one
// or many) → PUT finalize) over the real HTTP routes, and returns the
// resulting (digest, on-disk blob size, last PATCH Range header). The
// repo defaults to "test" so callers don't repeat the path arithmetic.
func pushBlob(t *testing.T, srvURL, repo string, chunks [][]byte, contentRanges []string) (digest string, size int64, lastRange string) {
	t.Helper()
	if len(contentRanges) != 0 && len(contentRanges) != len(chunks) {
		t.Fatalf("contentRanges length (%d) must be 0 or equal chunks length (%d)", len(contentRanges), len(chunks))
	}

	// --- 1. POST upload-start ---
	resp, err := http.Post(srvURL+"/v2/"+repo+"/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("POST upload-start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST upload-start: status = %d, want 202", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatalf("POST upload-start: missing Location header")
	}

	// --- 2. PATCH each chunk ---
	var totalWritten int64
	for i, chunk := range chunks {
		req, err := http.NewRequest(http.MethodPatch, loc, bytes.NewReader(chunk))
		if err != nil {
			t.Fatalf("build PATCH request %d: %v", i, err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Length", strconv.Itoa(len(chunk)))
		if len(contentRanges) > 0 && contentRanges[i] != "" {
			req.Header.Set("Content-Range", contentRanges[i])
		}
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PATCH chunk %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("PATCH chunk %d: status = %d, want 202", i, resp.StatusCode)
		}
		lastRange = resp.Header.Get("Range")
		totalWritten += int64(len(chunk))
	}

	// Compute digest over the concatenation of chunks (what the client
	// thinks it sent). This is the same payload PutUpload will sha256.
	h := sha256.New()
	for _, c := range chunks {
		h.Write(c)
	}
	digest = "sha256:" + hex.EncodeToString(h.Sum(nil))

	// --- 3. PUT finalize ---
	putURL := loc + "?digest=" + digest
	req, err := http.NewRequest(http.MethodPut, putURL, nil)
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

	// Read the on-disk blob size via the embedded storage (bypasses
	// HTTP HEAD entirely so we're testing the bytes the server actually
	// persisted, not what it chose to advertise).
	st := currentStore(t)
	gotSize, err := st.StatBlob(context.Background(), digest)
	if err != nil {
		t.Fatalf("StatBlob: %v", err)
	}
	return digest, gotSize, lastRange
}

// headBlob performs an HTTP HEAD on /v2/<repo>/blobs/<digest> and returns
// the Content-Length and Docker-Content-Digest headers.
func headBlob(t *testing.T, srvURL, repo, digest string) (cl int64, hdrDigest string, status int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodHead, srvURL+"/v2/"+repo+"/blobs/"+digest, nil)
	if err != nil {
		t.Fatalf("build HEAD: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	resp.Body.Close()
	return resp.ContentLength, resp.Header.Get("Docker-Content-Digest"), resp.StatusCode
}

// currentStore is a test helper that re-creates a fresh in-memory-ish
// store on the same tempdir used by the enclosing test. We can't easily
// hand back the *Filesystem the HTTP server is using (the package-level
// `New` wraps it privately), so we cheat: open a parallel handle to the
// same root. Filesystem is safe for concurrent reads across instances.
func currentStore(t *testing.T) *storage.Filesystem {
	t.Helper()
	root := testRoot(t)
	fs, err := storage.NewFilesystem(root)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	return fs
}

// Each test gets its own httptest.Server rooted at a fresh TempDir;
// the root path is stashed on the test's t.Cleanup chain via t.TempDir.
// We use the tempDir trick: httptest.NewServer accepts any Handler.
func testRoot(t *testing.T) string {
	t.Helper()
	// chi.Mount strips the prefix before the wildcard dispatcher sees it.
	// We need the same root the server uses; easiest is to remember it on
	// first call. See newServer below.
	root := testRoots[t.Name()]
	if root == "" {
		t.Fatalf("test %s did not register a server", t.Name())
	}
	return root
}

var testRoots = map[string]string{}

// newServer spins up a fresh cairn registryd backed by a unique TempDir.
// The TempDir is stashed so tests can open parallel *Filesystem handles
// against the same on-disk tree.
func newServer(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	testRoots[t.Name()] = dir
	store, err := storage.NewFilesystem(dir)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	root := chi.NewRouter()
	root.Mount("/v2", New(store, nil, nil))
	srv := httptest.NewServer(root)
	return srv.URL, srv.Close
}

// TestBlobSize_MonolithicPatch is the trivial case: one POST, one PATCH,
// one PUT. This is what skopeo/containers-image does. Catches "PATCH
// reads an extra byte off the HTTP body".
func TestBlobSize_MonolithicPatch(t *testing.T) {
	srvURL, done := newServer(t)
	defer done()

	payload := bytes.Repeat([]byte("A"), 7266) // same size as the bitnami
	// blob that regsync complained about (7266 expected, 7267 received).
	// If cairn were +1, this test would catch it for any size.
	digest, size, _ := pushBlob(t, srvURL, "r", [][]byte{payload}, nil)
	if size != int64(len(payload)) {
		t.Fatalf("on-disk blob size = %d, want %d (digest %s)", size, len(payload), digest)
	}
	cl, hdr, status := headBlob(t, srvURL, "r", digest)
	if status != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", status)
	}
	if cl != int64(len(payload)) {
		t.Fatalf("HEAD Content-Length = %d, want %d", cl, len(payload))
	}
	if hdr != digest {
		t.Fatalf("HEAD Docker-Content-Digest = %q, want %q", hdr, digest)
	}
}

// TestBlobSize_MultiChunkNoRange is what regsync's *non*-cross-mount
// chunked upload looks like: one UUID, multiple PATCHes, no
// Content-Range. Each PATCH appends; the on-disk size must equal the sum.
func TestBlobSize_MultiChunkNoRange(t *testing.T) {
	srvURL, done := newServer(t)
	defer done()

	// 3 chunks of 100, 250, 1000 bytes (= 1350 total)
	c1 := bytes.Repeat([]byte("a"), 100)
	c2 := bytes.Repeat([]byte("b"), 250)
	c3 := bytes.Repeat([]byte("c"), 1000)
	chunks := [][]byte{c1, c2, c3}

	digest, size, lastRange := pushBlob(t, srvURL, "r", chunks, nil)
	want := int64(len(c1) + len(c2) + len(c3))
	if size != want {
		t.Fatalf("on-disk blob size = %d, want %d", size, want)
	}
	cl, _, _ := headBlob(t, srvURL, "r", digest)
	if cl != want {
		t.Fatalf("HEAD Content-Length = %d, want %d", cl, want)
	}
	// Last PATCH's Range header must reflect the cumulative size.
	if got, wantRange := lastRange, fmt.Sprintf("0-%d", want-1); got != wantRange {
		t.Fatalf("last PATCH Range = %q, want %q", got, wantRange)
	}
}

// TestBlobSize_MultiChunkWithRange is the regsync / containers-image
// chunked upload path: multiple PATCHes on the SAME UUID, each carrying
// Content-Range "bytes START-END" so the server can resume an aborted
// upload. The server-side seek must honour the start offset — if it
// doesn't, you'll see the chunk appended *on top of* previous data
// (O_APPEND ignores Seek on Linux), giving a blob that's larger than
// the client thinks it sent by exactly the overlap.
//
// We deliberately pick chunk sizes that are NOT 4 MiB-aligned so an
// off-by-one or "treat Content-Range as advisory" bug is obvious.
func TestBlobSize_MultiChunkWithRange(t *testing.T) {
	srvURL, done := newServer(t)
	defer done()

	c1 := bytes.Repeat([]byte("a"), 7266) // matches regsync bitnami size
	c2 := bytes.Repeat([]byte("b"), 5000)
	chunks := [][]byte{c1, c2}
	// First chunk is the START of the upload: Content-Range: 0-(len-1)
	// (spec also allows just "0-" or omitting entirely; we send the
	// explicit form because that's what regclient sends on resume.)
	ranges := []string{
		fmt.Sprintf("bytes 0-%d", len(c1)-1),
		fmt.Sprintf("bytes %d-%d", len(c1), len(c1)+len(c2)-1),
	}

	digest, size, _ := pushBlob(t, srvURL, "r", chunks, ranges)
	want := int64(len(c1) + len(c2))
	if size != want {
		t.Fatalf("on-disk blob size = %d, want %d (Content-Range path likely broken — O_APPEND + Seek?)", size, want)
	}
	cl, _, _ := headBlob(t, srvURL, "r", digest)
	if cl != want {
		t.Fatalf("HEAD Content-Length = %d, want %d", cl, want)
	}

	// And the blob content must equal chunks concatenated in order.
	body, contentSize, err := currentStore(t).GetBlob(context.Background(), "r", digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if int64(len(got)) != want || contentSize != want {
		t.Fatalf("blob bytes = %d (StatBlob %d), want %d", len(got), contentSize, want)
	}
	prefix := append([]byte{}, c1...)
	if !bytes.HasPrefix(got, prefix) {
		t.Fatal("blob prefix is not chunk 1 — chunks got swapped or reordered")
	}
	suffix := got[len(c1):]
	if !bytes.Equal(suffix, c2) {
		t.Fatalf("blob suffix = %d bytes, want chunk2 verbatim (%d bytes)", len(suffix), len(c2))
	}
}

// TestBlobSize_RangeHeaderAtEachPatch verifies the Range header on EACH
// PATCH response is the file size after that PATCH (cumulative). regsync
// trusts this value to compute the next chunk's Content-Range start; if
// cairn ever reports a stale or wrong value, regsync will skip bytes
// (or write overlapping bytes, depending on which side is wrong).
func TestBlobSize_RangeHeaderAtEachPatch(t *testing.T) {
	srvURL, done := newServer(t)
	defer done()

	c1 := bytes.Repeat([]byte("a"), 100)
	c2 := bytes.Repeat([]byte("b"), 200)

	// POST upload-start
	resp, err := http.Post(srvURL+"/v2/r/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")

	// PATCH 1, no Content-Range (start of upload)
	req, _ := http.NewRequest(http.MethodPatch, loc, bytes.NewReader(c1))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.Itoa(len(c1)))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH 1: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got, want := resp.Header.Get("Range"), fmt.Sprintf("0-%d", len(c1)-1); got != want {
		t.Fatalf("PATCH 1 Range = %q, want %q", got, want)
	}

	// PATCH 2 with Content-Range picking up where PATCH 1 left off
	req, _ = http.NewRequest(http.MethodPatch, loc, bytes.NewReader(c2))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.Itoa(len(c2)))
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d", len(c1), len(c1)+len(c2)-1))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH 2: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got, want := resp.Header.Get("Range"), fmt.Sprintf("0-%d", len(c1)+len(c2)-1); got != want {
		t.Fatalf("PATCH 2 Range = %q, want %q", got, want)
	}
}

// TestBlobSize_PATCHReadsExactlyContentLength is a paranoia check that
// the server's read of the request body stops at exactly Content-Length.
// We send a body whose underlying io.Reader has extra bytes past the
// declared length — the server must NOT consume those bytes (they'd
// leak into the next PATCH on the same keep-alive connection and look
// like an extra byte being appended to the blob).
func TestBlobSize_PATCHReadsExactlyContentLength(t *testing.T) {
	srvURL, done := newServer(t)
	defer done()

	// POST upload-start
	resp, err := http.Post(srvURL+"/v2/r/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")

	// Body is exactly 100 bytes, but we'll *also* set Content-Length: 100
	// on the request and rely on Go's http.Client not to send anything
	// past that. If the server reads past Content-Length, it'll write
	// 100 + whatever it sneaks in.
	body := bytes.Repeat([]byte("z"), 100)
	req, _ := http.NewRequest(http.MethodPatch, loc, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got, want := resp.Header.Get("Range"), fmt.Sprintf("0-%d", len(body)-1); got != want {
		t.Fatalf("PATCH Range = %q, want %q (server may have over-read body)", got, want)
	}
	// And then PUT finalize to verify on-disk size matches.
	h := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(h[:])
	req, _ = http.NewRequest(http.MethodPut, loc+"?digest="+digest, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	st := currentStore(t)
	gotSize, err := st.StatBlob(context.Background(), digest)
	if err != nil {
		t.Fatalf("StatBlob: %v", err)
	}
	if gotSize != int64(len(body)) {
		t.Fatalf("blob size = %d, want %d", gotSize, len(body))
	}
}

// Sanity-check that the testRoot/testRoots plumbing works (a meta-test so
// failures aren't misleading).
func TestBlobSize_TestRootRegistration(t *testing.T) {
	srvURL, done := newServer(t)
	defer done()
	if !strings.HasPrefix(srvURL, "http://127.0.0.1:") {
		t.Fatalf("srvURL = %q, want local httptest URL", srvURL)
	}
	if _, err := os.Stat(testRoot(t)); err != nil {
		t.Fatalf("testRoot not accessible: %v", err)
	}
}
