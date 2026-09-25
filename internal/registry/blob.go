package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// BlobExists checks whether the destination registry already has a blob.
//
// Returns (true, nil) when the registry answers 200 OK on HEAD. 404 is the
// expected "missing" answer; anything else is surfaced as a typed error so
// callers can retry if they want.
func (c *Client) BlobExists(ctx context.Context, repo, digest string) (bool, error) {
	if repo == "" || digest == "" {
		return false, fmt.Errorf("registry: BlobExists: repo and digest required")
	}
	path := fmt.Sprintf("/v2/%s/blobs/%s", escapeRepo(repo), url.PathEscape(digest))
	resp, _, err := c.doRequest(ctx, http.MethodHead, path, "", nil)
	if err != nil {
		var re *Error
		if errors.As(err, &re) && re.IsNotFound() {
			return false, nil
		}
		return false, err
	}
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, &Error{
		Status:  resp.StatusCode,
		Code:    "BLOB_HEAD_FAILED",
		Message: fmt.Sprintf("HEAD %s returned %d", path, resp.StatusCode),
		URL:     path,
	}
}

// GetBlob streams a blob's bytes from the registry.
//
// Caller must close the returned ReadCloser. On error, the body has been
// closed and the error returned. Content-Length is reported via the size
// pointer (may be -1 if unknown).
func (c *Client) GetBlob(ctx context.Context, repo, digest string) (io.ReadCloser, int64, error) {
	if repo == "" || digest == "" {
		return nil, 0, fmt.Errorf("registry: GetBlob: repo and digest required")
	}
	path := fmt.Sprintf("/v2/%s/blobs/%s", escapeRepo(repo), url.PathEscape(digest))
	resp, _, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, &Error{
			Status:  resp.StatusCode,
			Code:    "BLOB_GET_FAILED",
			Message: fmt.Sprintf("GET %s returned %d", path, resp.StatusCode),
			URL:     path,
		}
	}
	return resp.Body, resp.ContentLength, nil
}

// StartBlobUpload opens a new blob upload session and returns its UUID.
//
// POST /v2/<repo>/blobs/uploads/ → 202 Accepted + Location header.
// We extract the UUID (last path segment of Location) for subsequent calls.
func (c *Client) StartBlobUpload(ctx context.Context, repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("registry: StartBlobUpload: repo required")
	}
	path := fmt.Sprintf("/v2/%s/blobs/uploads/", escapeRepo(repo))
	resp, _, err := c.doRequest(ctx, http.MethodPost, path, "", nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusAccepted {
		return "", &Error{
			Status:  resp.StatusCode,
			Code:    "BLOB_UPLOAD_INIT_FAILED",
			Message: fmt.Sprintf("POST %s returned %d (want 202)", path, resp.StatusCode),
			URL:     path,
		}
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("registry: StartBlobUpload: missing Location header")
	}
	u, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("registry: StartBlobUpload: parse Location %q: %w", loc, err)
	}
	parts := splitPath(u.Path)
	if len(parts) == 0 {
		return "", fmt.Errorf("registry: StartBlobUpload: empty Location path %q", loc)
	}
	return parts[len(parts)-1], nil
}

// UploadBlob streams src into a fresh upload session and commits the blob
// under digest. Implementation:
//
//  1. POST .../blobs/uploads/ → upload UUID
//  2. PATCH .../blobs/uploads/<uuid> with 4 MiB chunks (Content-Range)
//  3. PUT .../blobs/uploads/<uuid>?digest=<digest> with empty body to commit
//
// The full body is read from src; caller is responsible for closing src if
// it owns it. ctx is checked between chunks; cancellation surfaces as
// ctx.Err() and the upload session is abandoned.
func (c *Client) UploadBlob(ctx context.Context, repo, digest string, src io.Reader) error {
	if repo == "" || digest == "" {
		return fmt.Errorf("registry: UploadBlob: repo and digest required")
	}
	uuid, err := c.StartBlobUpload(ctx, repo)
	if err != nil {
		return err
	}
	const chunkSize = 4 << 20 // 4 MiB; matches registry default
	buf := make([]byte, chunkSize)
	basePath := fmt.Sprintf("/v2/%s/blobs/uploads/%s", escapeRepo(repo), url.PathEscape(uuid))
	var offset int64

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := io.ReadFull(src, buf)
		if n == 0 {
			if rerr == io.EOF {
				break
			}
			return fmt.Errorf("registry: read source: %w", rerr)
		}
		isLast := rerr == io.EOF || rerr == io.ErrUnexpectedEOF
		chunk := buf[:n]

		// PATCH with Content-Range; last chunk includes the digest so a
		// single PUT (step 3 below) is purely a no-op commit confirmation.
		// Some registries accept the digest on the last PATCH and skip the
		// PUT; we do PUT regardless for compatibility.
		patchPath := basePath
		req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
			c.absURL(patchPath), bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Range", fmt.Sprintf("%d-%d", offset, offset+int64(n)-1))
		req.ContentLength = int64(n)

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("registry: PATCH chunk: %w", err)
		}
		if resp.StatusCode != http.StatusAccepted {
			body := readLimited(resp.Body, 4096)
			resp.Body.Close()
			return &Error{
				Status:  resp.StatusCode,
				Code:    "BLOB_PATCH_FAILED",
				Message: fmt.Sprintf("PATCH returned %d: %s", resp.StatusCode, string(body)),
				URL:     patchPath,
			}
		}
		resp.Body.Close()
		offset += int64(n)

		if isLast {
			break
		}
	}

	// Step 3: commit via PUT with the digest query parameter.
	q := url.Values{}
	q.Set("digest", digest)
	putPath := basePath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.absURL(putPath), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Length", "0")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("registry: PUT commit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body := readLimited(resp.Body, 4096)
		return &Error{
			Status:  resp.StatusCode,
			Code:    "BLOB_PUT_FAILED",
			Message: fmt.Sprintf("PUT returned %d: %s", resp.StatusCode, string(body)),
			URL:     putPath,
		}
	}
	return nil
}

// absURL builds an absolute URL from a path (relative to the client's base).
// Used by UploadBlob to avoid the query-string mangling in doRequest.
func (c *Client) absURL(path string) string {
	full := *c.baseURL
	full.Path = path
	return full.String()
}

// splitPath splits a URL path into non-empty segments (no leading slash).
func splitPath(p string) []string {
	var out []string
	for p != "" {
		i := 0
		for i < len(p) && p[i] == '/' {
			i++
		}
		p = p[i:]
		if p == "" {
			break
		}
		i = 0
		for i < len(p) && p[i] != '/' {
			i++
		}
		out = append(out, p[:i])
		p = p[i:]
	}
	return out
}

func readLimited(r io.Reader, max int) []byte {
	buf := make([]byte, max)
	n, _ := io.ReadFull(r, buf)
	return buf[:n]
}
