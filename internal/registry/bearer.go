package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// BearerAuth handles the V2 spec's "Bearer token" flow, used by Docker Hub,
// ghcr.io, quay.io, and most other public registries.
//
// Flow:
//
//  1. Send request without Authorization (or with basic auth).
//  2. On 401 with `WWW-Authenticate: Bearer realm="...",service="...",scope="..."`,
//     parse the challenge.
//  3. POST to realm with `?service=...&scope=...` (form-encoded or query),
//     receive {"token": "..."}.
//  4. Retry the original request with `Authorization: Bearer <token>`.
//
// We cache tokens per (realm, service, scope, basic-auth) tuple until they
// fail with another 401, at which point we evict and re-acquire once.
type BearerAuth struct {
	mu    sync.Mutex
	cache map[string]bearerToken
	// tokenClient is the http.Client used to fetch tokens. Sharing the
	// transport with the main client avoids extra TLS handshakes.
	tokenClient *http.Client
}

type bearerToken struct {
	value     string
	expiresAt time.Time
}

// NewBearerAuth creates a BearerAuth that uses sharedClient's transport for
// token fetches (so connection pools / proxy config are reused).
func NewBearerAuth(sharedClient *http.Client) *BearerAuth {
	return &BearerAuth{
		cache:       make(map[string]bearerToken),
		tokenClient: sharedClient,
	}
}

// Challenge is the parsed WWW-Authenticate header from a 401 response.
type Challenge struct {
	Scheme  string // "Bearer"
	Realm   string // https://auth.docker.io/token
	Service string // registry.docker.io
	Scope   string // repository:library/alpine:pull
}

// parseChallenge extracts a Bearer challenge from a WWW-Authenticate header.
//
// We accept the V2 spec shape: `Bearer realm="...",service="...",scope="..."`.
// Quoted values are unquoted; missing fields leave the struct zero-valued.
func parseChallenge(header string) (Challenge, bool) {
	if header == "" {
		return Challenge{}, false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Challenge{}, false
	}
	c := Challenge{Scheme: "Bearer"}
	for _, kv := range splitCommas(parts[1]) {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(kv[:eq])
		v := strings.TrimSpace(kv[eq+1:])
		v = strings.Trim(v, `"`)
		switch k {
		case "realm":
			c.Realm = v
		case "service":
			c.Service = v
		case "scope":
			c.Scope = v
		}
	}
	if c.Realm == "" {
		return Challenge{}, false
	}
	return c, true
}

// splitCommas splits on commas, but is naive about quoted commas. Sufficient
// for the well-formed challenges registries emit.
func splitCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '"':
			depth ^= 1
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// FetchToken acquires a Bearer token for the given challenge, optionally
// using basic auth (some realms require it).
//
// Results are cached by (realm, service, scope, basicAuth) until they expire
// or a 401 evicts them.
func (b *BearerAuth) FetchToken(ctx context.Context, ch Challenge, basicAuth *BasicAuth) (string, error) {
	key := b.cacheKey(ch, basicAuth)
	if t := b.lookup(key); t != "" {
		return t, nil
	}

	t, err := b.fetchTokenOnce(ctx, ch, basicAuth)
	if err != nil {
		return "", err
	}
	b.store(key, t)
	return t, nil
}

func (b *BearerAuth) fetchTokenOnce(ctx context.Context, ch Challenge, basicAuth *BasicAuth) (string, error) {
	u, err := url.Parse(ch.Realm)
	if err != nil {
		return "", fmt.Errorf("bearer: parse realm %q: %w", ch.Realm, err)
	}
	q := u.Query()
	if ch.Service != "" {
		q.Set("service", ch.Service)
	}
	if ch.Scope != "" {
		q.Set("scope", ch.Scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	if basicAuth != nil {
		req.SetBasicAuth(basicAuth.Username, basicAuth.Password)
	}

	resp, err := b.tokenClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("bearer: token fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &Error{
			Status:  resp.StatusCode,
			Code:    "TOKEN_FETCH_FAILED",
			Message: fmt.Sprintf("token endpoint returned %d", resp.StatusCode),
			URL:     ch.Realm,
		}
	}
	var doc struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("bearer: decode token: %w", err)
	}
	tok := doc.Token
	if tok == "" {
		tok = doc.AccessToken
	}
	if tok == "" {
		return "", fmt.Errorf("bearer: token response had no token field")
	}
	// we don't strictly honor ExpiresIn (would require storing expiry); the
	// 401-eviction path catches stale tokens.
	_ = doc.ExpiresIn
	return tok, nil
}

// Invalidate drops the cached token for this (realm, service, scope, basic)
// tuple. Called when a request with a cached token gets another 401 — the
// upstream realm may have rotated the token.
func (b *BearerAuth) Invalidate(ch Challenge, basicAuth *BasicAuth) {
	key := b.cacheKey(ch, basicAuth)
	b.mu.Lock()
	delete(b.cache, key)
	b.mu.Unlock()
}

func (b *BearerAuth) cacheKey(ch Challenge, basic *BasicAuth) string {
	ba := ""
	if basic != nil {
		ba = basic.Username + ":" + basic.Password
	}
	return ch.Realm + "|" + ch.Service + "|" + ch.Scope + "|" + ba
}

func (b *BearerAuth) lookup(key string) string {
	b.mu.Lock()
	t, ok := b.cache[key]
	b.mu.Unlock()
	if !ok || time.Now().After(t.expiresAt) {
		return ""
	}
	return t.value
}

func (b *BearerAuth) store(key, token string) {
	b.mu.Lock()
	b.cache[key] = bearerToken{value: token, expiresAt: time.Now().Add(2 * time.Minute)}
	b.mu.Unlock()
}

// BasicAuth is the optional credential pair used to acquire a Bearer token.
// Some realms (e.g. ghcr.io) require basic auth to issue any token at all.
type BasicAuth struct {
	Username string
	Password string
}
