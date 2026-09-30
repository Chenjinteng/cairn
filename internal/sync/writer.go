package sync

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Writer is the *outbound* side of the sync engine — it pushes blobs
// and manifests to a remote OCI registry, presenting a bearer token
// on every request. The HTTP shape matches the OCI Distribution Spec,
// so cairn↔cairn works and so does cairn→distribution, etc.
//
// Lifecycle for one blob (EnsureBlob):
//
//	HEAD  /v2/{repo}/blobs/{digest}        — 200 = skip, 404 = upload
//	POST  /v2/{repo}/blobs/uploads/        — 202 + Location (UUID url)
//	PATCH /v2/{repo}/blobs/uploads/{uuid}  — 202 + Range (append body)
//	PUT   /v2/{repo}/blobs/uploads/{uuid}?digest={digest} — 201
//
// Lifecycle for one manifest (PutManifest):
//
//	PUT /v2/{repo}/manifests/{ref}  — 201 + Docker-Content-Digest
//
// Why monolithic upload (no chunked PATCH loop): a single PATCH with
// the full body works against every major registry implementation
// (cairn, distribution, GitLab, Harbor). Chunked would only matter
// for blobs > the HTTP body limit on the receiver (rare in practice;
// v0.6.2+ follow-up if needed).
type Writer struct {
	baseURL *url.URL
	token   string
	hc      *http.Client
}

// NewWriter builds a Writer for the given remote URL with bearer auth.
//
// remoteURL must include scheme + host (e.g. "https://cairn-b.example.com").
// token is the bearer string sent on every request's Authorization
// header; pass "" for anonymous (the remote must allow it).
func NewWriter(remoteURL, token string) (*Writer, error) {
	u, err := url.Parse(remoteURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, ErrInvalidURL
	}
	return &Writer{
		baseURL: u,
		token:   token,
		hc: &http.Client{
			// Blob uploads can move a lot of bytes for big images; 10 min
			// covers a 1 GB blob on a 20 Mbps link. Manifests are small
			// so the per-request context timeout dominates there.
			Timeout: 10 * time.Minute,
		},
	}, nil
}

// EnsureBlob uploads blob to the remote unless it already exists there.
//
// On success, the remote has the blob and a subsequent `GET /v2/{repo}/
// manifests/{ref}` against any node sharing this storage will resolve
// the reference's digest into this blob.
//
// blobSize, when > 0, sets Content-Length on the PATCH so receivers
// can stream-to-disk without buffering. Pass 0 if unknown (most cases).
func (w *Writer) EnsureBlob(ctx context.Context, repo, digest string, body io.Reader, blobSize int64) error {
	exists, err := w.blobExists(ctx, repo, digest)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	uploadURL, err := w.startBlobUpload(ctx, repo)
	if err != nil {
		return err
	}

	if err := w.uploadChunk(ctx, uploadURL, body, blobSize); err != nil {
		return err
	}

	return w.finalizeBlob(ctx, uploadURL, digest)
}

// PutManifest uploads a manifest and returns the digest the registry
// computed (sha256 of the body). reference can be a tag ("v1.0") or a
// digest; the registry stores it under both lookups.
//
// mediaType should be one of the standard OCI/Docker manifest types —
// the registry will only serve it back with the same media type.
func (w *Writer) PutManifest(ctx context.Context, repo, reference, mediaType string, body []byte) (string, error) {
	full := w.repoPath(repo, "manifests", reference)
	req, err := w.newRequest(ctx, http.MethodPut, full, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mediaType)
	resp, err := w.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", &WriterError{Op: "PUT manifest", Status: resp.StatusCode, URL: resp.Request.URL}
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

// blobExists is HEAD /v2/{repo}/blobs/{digest}; returns true on 200,
// false on 404, error otherwise.
func (w *Writer) blobExists(ctx context.Context, repo, digest string) (bool, error) {
	full := w.repoPath(repo, "blobs", digest)
	req, err := w.newRequest(ctx, http.MethodHead, full, nil)
	if err != nil {
		return false, err
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, &WriterError{Op: "HEAD blob", Status: resp.StatusCode, URL: resp.Request.URL}
	}
}

// startBlobUpload is POST /v2/{repo}/blobs/uploads/. Returns the upload
// URL from the Location header (which the registry may relocate, so we
// follow that hint rather than constructing it locally).
func (w *Writer) startBlobUpload(ctx context.Context, repo string) (string, error) {
	full := w.repoPath(repo, "blobs", "uploads", "")
	req, err := w.newRequest(ctx, http.MethodPost, full, nil)
	if err != nil {
		return "", err
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return "", &WriterError{Op: "POST upload-start", Status: resp.StatusCode, URL: resp.Request.URL}
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", &WriterError{Op: "POST upload-start", Status: resp.StatusCode, URL: resp.Request.URL, Msg: "missing Location header"}
	}
	// Location may be relative ("/v2/.../uploads/<uuid>") or absolute.
	// Resolve relative against the request URL so we always end up with
	// an absolute URL for the subsequent PATCH/PUT.
	return resp.Request.URL.ResolveReference(mustParseRef(loc)).String(), nil
}

// uploadChunk is PATCH against the upload URL with the blob body.
// blobSize, when > 0, sets Content-Length so the receiver can stream
// straight to disk instead of buffering.
func (w *Writer) uploadChunk(ctx context.Context, uploadURL string, body io.Reader, blobSize int64) error {
	req, err := w.newRequest(ctx, http.MethodPatch, uploadURL, body)
	if err != nil {
		return err
	}
	if blobSize > 0 {
		req.ContentLength = blobSize
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return &WriterError{Op: "PATCH upload", Status: resp.StatusCode, URL: resp.Request.URL}
	}
	return nil
}

// finalizeBlob is PUT against the upload URL with ?digest=<digest>.
// No body — the chunk(s) are already in the upload session.
func (w *Writer) finalizeBlob(ctx context.Context, uploadURL, digest string) error {
	u, err := url.Parse(uploadURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("digest", digest)
	u.RawQuery = q.Encode()
	req, err := w.newRequest(ctx, http.MethodPut, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return &WriterError{Op: "PUT upload-finalize", Status: resp.StatusCode, URL: resp.Request.URL}
	}
	return nil
}

// newRequest builds an *http.Request, stamping Authorization (when a
// token is set) and our User-Agent on every outbound call. The token
// is the only auth we send — basic auth would only matter against
// registries that don't speak bearer (cairn isn't one of them).
func (w *Writer) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	req.Header.Set("User-Agent", "cairn-sync/0.6.0")
	return req, nil
}

// repoPath joins baseURL + /v2/<repo>/<parts...>. The repository name
// is path-escaped so names containing '/' are correctly encoded; the
// remaining parts (e.g. "blobs", "uploads") are joined literally.
func (w *Writer) repoPath(repo string, parts ...string) string {
	// We want baseURL + /v2/<repo>/<parts...>. Use path.Join against the
	// path segment to get escaping right; then rebuild against baseURL.
	segments := append([]string{strings.TrimRight(w.baseURL.Path, "/"), "v2", repo}, parts...)
	rel := path.Join(segments...)
	u := *w.baseURL
	u.Path = rel
	u.RawQuery = ""
	return u.String()
}

// WriterError is what Writer methods return on a non-2xx HTTP response.
// Op identifies which step failed (HEAD blob, POST upload-start, etc.)
// so the engine's per-repo failure log can be specific.
type WriterError struct {
	Op     string
	Status int
	URL    *url.URL
	Msg    string
}

func (e *WriterError) Error() string {
	if e.Msg != "" {
		return "sync: " + e.Op + ": " + e.Msg + " (status=" + itoa(e.Status) + " url=" + e.URL.String() + ")"
	}
	return "sync: " + e.Op + ": status=" + itoa(e.Status) + " url=" + e.URL.String()
}

// mustParseRef parses a URL reference (relative or absolute). Panics
// only on truly invalid input — the Location header is supposed to be
// produced by the registry, so a parse error here means the registry
// is broken and we should fail loud.
func mustParseRef(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic("sync: registry returned invalid Location: " + s)
	}
	return u
}

// itoa avoids importing strconv just for two call sites.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}