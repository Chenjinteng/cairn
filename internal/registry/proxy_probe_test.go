package registry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Chenjinteng/cairn/internal/registry"
)

// recordingProxy is a minimal HTTP forward proxy that records the
// absolute-URI of every plain-HTTP request it relays. It answers 502 for
// anything it does not know, so a request that bypasses the proxy fails
// loudly instead of silently succeeding.
type recordingProxy struct {
	mu       sync.Mutex
	seen     []string
	upstream map[string]bool // host -> allowed
}

func (p *recordingProxy) record(u string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, u)
}

func (p *recordingProxy) seenList() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.seen))
	copy(out, p.seen)
	return out
}

func (p *recordingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.record(r.URL.String())
	host := r.URL.Hostname()
	p.mu.Lock()
	ok := p.upstream[host]
	p.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	// Relay to the real upstream.
	outReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
		}
		if rerr != nil {
			return
		}
	}
}

// TestBearerTokenFetchGoesThroughProxy is the regression test for the
// "auth.docker.io 不走 proxy" report.
//
// Docker Hub's pull flow is two hops:
//  1. GET registry-1.docker.io/v2/... -> 401 + WWW-Authenticate: Bearer realm="https://auth.docker.io/token"
//  2. GET auth.docker.io/token?...   -> {"token": "..."}
//  3. retry hop 1 with Authorization: Bearer <token>
//
// Both hops MUST egress through the configured proxy -- the token endpoint is
// a DIFFERENT host than the registry, and in a restricted network it is
// frequently the one that gets blocked.
//
// registry.NewClient shares one *http.Transport between the registry client
// and the Bearer token client, so the proxy should apply to both. This test
// pins that behaviour so a future refactor that gives the token client its
// own transport cannot silently regress it.
func TestBearerTokenFetchGoesThroughProxy(t *testing.T) {
	// The token realm.
	var realmHits int
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realmHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "anon-token",
			"expires_in": 300,
		})
	}))
	defer realm.Close()

	// The registry: always 401 with a Bearer challenge pointing at the realm.
	var registryHits int
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryHits++
		if r.Header.Get("Authorization") == "Bearer anon-token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:aaa","size":1},"layers":[]}`))
			return
		}
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer realm="%s/token",service="registry.docker.io",scope="repository:library/alpine:pull"`, realm.URL))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer reg.Close()

	// The proxy. Only the two fake hosts are relayable; anything else 502s.
	proxy := &recordingProxy{upstream: map[string]bool{}}
	regURL, _ := url.Parse(reg.URL)
	realmURL, _ := url.Parse(realm.URL)
	proxy.upstream[regURL.Hostname()] = true
	proxy.upstream[realmURL.Hostname()] = true
	px := httptest.NewServer(proxy)
	defer px.Close()

	c, err := registry.NewClient(registry.Config{
		BaseURL: reg.URL,
		Proxy:   px.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := c.GetManifest(context.Background(), "library/alpine", "3.19"); err != nil {
		t.Fatalf("GetManifest through proxy: %v", err)
	}

	if registryHits == 0 {
		t.Fatal("registry was never hit")
	}
	if realmHits == 0 {
		t.Fatal("token realm was never hit")
	}

	seen := proxy.seenList()
	t.Logf("proxy saw %d request(s):", len(seen))
	for _, s := range seen {
		t.Logf("  %s", s)
	}

	// The token endpoint must have gone through the proxy.
	var tokenViaProxy bool
	for _, s := range seen {
		if strings.Contains(s, realmURL.Hostname()) {
			tokenViaProxy = true
		}
	}
	if !tokenViaProxy {
		t.Errorf("token request to %s did NOT go through the proxy (proxy saw: %v)",
			realmURL.Hostname(), seen)
	}
}
