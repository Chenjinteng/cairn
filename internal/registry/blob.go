package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
//
// Do NOT route this through doRequest: doRequest is built for small JSON
// responses -- it drains the body into memory (32MB cap) and closes it via
// defer before returning, so a resp.Body handed back to callers would be
// dead on arrival ("http2: response body closed" on the first read). Blobs
// can be hundreds of MB and must stream. The 401 -> Bearer token retry
// below mirrors doRequest's, including stale-token invalidation on a
// second 401.
func (c *Client) GetBlob(ctx context.Context, repo, digest string) (io.ReadCloser, int64, error) {
	if repo == "" || digest == "" {
		return nil, 0, fmt.Errorf("registry: GetBlob: repo and digest required")
	}
	path := fmt.Sprintf("/v2/%s/blobs/%s", escapeRepo(repo), url.PathEscape(digest))
	full := *c.baseURL
	full.Path = strings.TrimRight(full.Path, "/") + "/" + strings.TrimLeft(path, "/")

	do := func(bearerToken string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("registry: build request: %w", err)
		}
		req.Header.Set("User-Agent", UserAgent)
		if bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+bearerToken)
		} else if c.user != "" {
			req.SetBasicAuth(c.user, c.pass)
		}
		return c.http.Do(req)
	}

	resp, err := do("")
	if err != nil {
		return nil, 0, fmt.Errorf("registry: GET %s: %w", full.Path, err)
	}

	// V2 Bearer flow, same shape as doRequest: on 401 with a parseable
	// challenge, fetch a token and retry once.
	if resp.StatusCode == http.StatusUnauthorized && c.bearer != nil {
		if ch, ok := parseChallenge(resp.Header.Get("WWW-Authenticate")); ok {
			token, terr := c.bearer.FetchToken(ctx, ch, &BasicAuth{Username: c.user, Password: c.pass})
			resp.Body.Close()
			if terr != nil {
				return nil, 0, fmt.Errorf("registry: GET %s: 401 unauthorized (bearer token fetch failed: %v)", full.Path, terr)
			}
			if token == "" {
				return nil, 0, fmt.Errorf("registry: GET %s: 401 unauthorized (bearer token response empty)", full.Path)
			}
			resp, err = do(token)
			if err != nil {
				return nil, 0, fmt.Errorf("registry: GET %s (bearer retry): %w", full.Path, err)
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		// Second 401: stale cached token -- invalidate so the next call
		// fetches a fresh one (same as doRequest).
		if resp.StatusCode == http.StatusUnauthorized && c.bearer != nil {
			if ch, ok := parseChallenge(resp.Header.Get("WWW-Authenticate")); ok {
				c.bearer.Invalidate(ch, &BasicAuth{Username: c.user, Password: c.pass})
			}
		}
		body := readLimited(resp.Body, 4096)
		resp.Body.Close()
		return nil, 0, decodeError(resp.StatusCode, full.String(), body)
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
