package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cairn/internal/version"
)

// UserAgent identifies cairn to upstream registries. Some allowlists in
// private registries match on User-Agent to permit scripts; "cairn/<ver>"
// is honest and easy to filter on. Sourced from internal/version so a single
// version bump propagates everywhere.
var UserAgent = version.UserAgent

// manifestAccept is the Accept header sent on every manifest fetch.
// The V2 spec returns 404 (not 400, not 406) if Accept doesn't list a known
// media type. We list all four so Docker v2 + OCI manifest + OCI index +
// Docker list manifests all resolve. Mirrors registry-manager's MANIFEST_ACCEPT.
const manifestAccept = "application/vnd.oci.image.manifest.v1+json,application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json"

// Client is a thin HTTP wrapper around a V2 registry endpoint.
//
// One Client is safe for concurrent use; it owns a single http.Client and
// immutable config. The Registry interface in registry.go is the abstraction
// handlers depend on; Client is the production implementation.
type Client struct {
	baseURL *url.URL
	http    *http.Client
	user    string
	pass    string
}

// GetBaseURL returns the registry base URL as a string. Used by the pull
// orchestrator to inspect the source URL (e.g. for Docker Hub detection).
func (c *Client) GetBaseURL() string { return c.baseURL.String() }

// HTTP returns the underlying http.Client. Used by handlers that need to
// issue arbitrary requests through the configured transport (e.g. proxy
// connectivity tests that hit an unrelated endpoint).
func (c *Client) HTTP() *http.Client { return c.http }

// PutRaw issues an arbitrary method+path with an explicit Content-Type and
// raw JSON body. Used by the pull orchestrator to PUT a manifest verbatim.
func (c *Client) PutRaw(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	full := c.absURL(path)
	req, err := http.NewRequestWithContext(ctx, method, full, ioReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	return c.http.Do(req)
}

func ioReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct {
	b []byte
	i int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// Config is what NewClient requires. All fields except BaseURL are optional.
type Config struct {
	BaseURL  string        // required, must parse as URL
	Username string        // optional basic-auth user
	Password string        // optional basic-auth pass
	Proxy    string        // optional HTTP proxy URL for reaching the registry
	Timeout  time.Duration // per-request timeout; default 30s
	// InsecureTLS skips verification of the registry's certificate.
	// Useful for self-signed internal registries; do NOT enable in prod.
	InsecureTLS bool
}

// NewClient validates cfg and returns a Client. The returned client owns no
// resources beyond the http.Client's idle connection pool.
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("registry: BaseURL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("registry: parse BaseURL %q: %w", cfg.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("registry: BaseURL must be http(s); got %q", u.Scheme)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if cfg.Proxy != "" {
		pu, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("registry: parse Proxy %q: %w", cfg.Proxy, err)
		}
		transport.Proxy = http.ProxyURL(pu)
	}
	if cfg.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // intentional, opt-in
	}

	return &Client{
		baseURL: u,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		user: cfg.Username,
		pass: cfg.Password,
	}, nil
}

// doRequest performs an HTTP request and decodes a V2 error document on failure.
//
// The AcceptManifest header is set automatically when accept is empty and
// the URL points at /v2/<name>/manifests/; otherwise we send whatever the
// caller asked for. responseInto, if non-nil, is JSON-decoded into on 2xx.
func (c *Client) doRequest(ctx context.Context, method, path, accept string, responseInto any) (*http.Response, []byte, error) {
	full := *c.baseURL
	joined := strings.TrimRight(full.Path, "/") + "/" + strings.TrimLeft(path, "/")
	// Split any "?query" off joined into RawQuery so url.URL.String() encodes
	// it correctly. We used to put it in Path, which caused ?n=100 to become
	// %3Fn=100 in the outgoing URL — silent 404 in tests.
	if i := strings.Index(joined, "?"); i >= 0 {
		full.RawQuery = joined[i+1:]
		joined = joined[:i]
	}
	full.Path = joined

	req, err := http.NewRequestWithContext(ctx, method, full.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else if strings.Contains(full.Path, "/manifests/") {
		req.Header.Set("Accept", manifestAccept)
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: %s %s: %w", method, full.Path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32MB cap; manifest JSON is small
	if err != nil {
		return nil, nil, fmt.Errorf("registry: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, body, decodeError(resp.StatusCode, full.String(), body)
	}

	if responseInto != nil && len(body) > 0 {
		if err := json.Unmarshal(body, responseInto); err != nil {
			return nil, body, fmt.Errorf("registry: decode response: %w", err)
		}
	}
	return resp, body, nil
}

// decodeError builds an *Error from a V2 problem document.
//
// Per the V2 spec, errors are JSON of the shape:
//
//	{ "errors": [ { "code": "MANIFEST_UNKNOWN", "message": "...", "detail": "..." } ] }
//
// Some endpoints (notably Docker Hub's CDN) return empty bodies with just a
// status code; we tolerate that and synthesize a generic message.
func decodeError(status int, url string, body []byte) error {
	var doc struct {
		// Some legacy endpoints surface code/message at the top level instead
		// of (or in addition to) inside errors[].
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Errors  []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(body, &doc)

	code := doc.Code
	msg := doc.Message
	detail := doc.Detail
	if len(doc.Errors) > 0 {
		first := doc.Errors[0]
		if code == "" {
			code = first.Code
		}
		if msg == "" {
			msg = first.Message
		}
		if detail == "" {
			detail = first.Detail
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(status)
		}
	}
	return &Error{
		Status:  status,
		Code:    code,
		Message: msg,
		URL:     url,
	}
}

// IsRetryable reports whether a network error should be retried.
//
// We only retry on transport-level errors (DNS, connection refused, EOF),
// never on 5xx from the registry itself. The registry may be temporarily
// broken in ways a retry won't fix (corrupt manifest, missing blob).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*Error); ok {
		return false // registry returned a structured error; retrying won't help
	}
	var ne net.Error
	if asErr(err, &ne) {
		return true
	}
	// url.Error wrapping is the most common shape
	if strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "EOF") ||
		strings.Contains(err.Error(), "no such host") ||
		strings.Contains(err.Error(), "i/o timeout") {
		return true
	}
	return false
}

// asErr is a tiny shim so this file doesn't import "errors" just for As.
func asErr(err error, target any) bool {
	type unwrapper interface{ Unwrap() error }
	for cur := err; cur != nil; {
		if u, ok := cur.(unwrapper); ok {
			cur = u.Unwrap()
			continue
		}
		break
	}
	return false
}