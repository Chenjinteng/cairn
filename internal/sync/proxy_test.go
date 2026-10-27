package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chenjinteng/cairn/internal/proxies"
)

// recordingProxy is a minimal HTTP forward proxy that records the
// absolute-URI of every plain-HTTP request it relays. Anything it does
// not recognise gets a 502, so a request that bypassed the proxy fails
// loudly instead of silently succeeding.
type recordingProxy struct {
	mu       sync.Mutex
	seen     []string
	upstream map[string]bool
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

// newProxyStore builds a proxy library holding one credentialed entry.
func newProxyStore(t *testing.T, id, rawURL, user, pass string) *proxies.Store {
	t.Helper()
	store, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatalf("proxies.Open: %v", err)
	}
	if err := store.Put(proxies.Proxy{
		ID: id, Name: "corp-proxy", URL: rawURL, Username: user, Password: pass,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return store
}

// TestResolveProxyUsesLibraryEntry is the v0.7.54 regression test for the
// sync engine's proxy support.
//
// Before v0.7.54 the sync engine had NO proxy support at all:
// newRemoteClient built a registry.Client with no Proxy field, so every
// sync job went out direct — including the auth.docker.io token fetch
// that a Docker Hub pull needs. resolveProxy must therefore return the
// library entry's URL WITH the stored credentials baked in as userinfo
// (net/http's http.ProxyURL reads credentials only from the URL).
func TestResolveProxyUsesLibraryEntry(t *testing.T) {
	store := newProxyStore(t, "p1", "http://proxy.example.com:8080", "alice", "s3cr3t")
	e := &Engine{proxies: store}

	got, err := e.resolveProxy(SyncTask{RemoteProxyID: "p1"})
	if err != nil {
		t.Fatalf("resolveProxy: %v", err)
	}
	if !strings.Contains(got, "alice:s3cr3t@") {
		t.Errorf("proxy URL %q has no credentials — the proxy would answer 407", got)
	}
	if !strings.Contains(got, "proxy.example.com:8080") {
		t.Errorf("proxy URL %q lost the host", got)
	}
}

// TestResolveProxyInlineWins: an inline proxy URL takes precedence over
// the library reference, matching the pull flow.
func TestResolveProxyInlineWins(t *testing.T) {
	store := newProxyStore(t, "p1", "http://proxy.example.com:8080", "alice", "s3cr3t")
	e := &Engine{proxies: store}

	got, err := e.resolveProxy(SyncTask{
		RemoteProxyID:  "p1",
		RemoteProxyURL: "http://inline.example.com:3128",
	})
	if err != nil {
		t.Fatalf("resolveProxy: %v", err)
	}
	if got != "http://inline.example.com:3128" {
		t.Errorf("proxy URL = %q, want the inline URL to win", got)
	}
}

// TestResolveProxyEmptyIsDirect: a task with no proxy configured must
// resolve to the empty string — the pre-v0.7.54 "direct outbound" contract.
func TestResolveProxyEmptyIsDirect(t *testing.T) {
	e := &Engine{proxies: newProxyStore(t, "p1", "http://proxy.example.com:8080", "", "")}
	got, err := e.resolveProxy(SyncTask{})
	if err != nil {
		t.Fatalf("resolveProxy: %v", err)
	}
	if got != "" {
		t.Errorf("proxy URL = %q, want empty (direct)", got)
	}
}

// TestResolveProxyDanglingReferenceFailsLoudly: a task pointing at a
// deleted proxy entry must fail the run, NOT silently downgrade to a
// direct connection. A silent downgrade looks like "the network is flaky",
// which is exactly the wrong conclusion.
func TestResolveProxyDanglingReferenceFailsLoudly(t *testing.T) {
	store := newProxyStore(t, "p1", "http://proxy.example.com:8080", "", "")
	e := &Engine{proxies: store}

	if _, err := e.resolveProxy(SyncTask{RemoteProxyID: "does-not-exist"}); err == nil {
		t.Fatal("resolveProxy with a dangling reference returned nil error")
	}
}

// TestSyncClientUsesProxy is the end-to-end regression test: a sync task
// that references a proxy must have BOTH the registry request AND the
// Bearer-token request egress through that proxy.
//
// The token hop is the one that matters: Docker Hub answers /v2/ with
// 401 + a challenge pointing at auth.docker.io — a DIFFERENT host — and
// in a restricted network that hop is usually the blocked one. If the
// token fetch bypassed the proxy, the pull would fail at exactly the
// point the operator configured the proxy to fix.
func TestSyncClientUsesProxy(t *testing.T) {
	// The token realm.
	var realmHits int
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realmHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "anon-token", "expires_in": 300})
	}))
	defer realm.Close()

	// The registry: 401 with a Bearer challenge until the token arrives.
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

	// The proxy.
	proxy := &recordingProxy{upstream: map[string]bool{}}
	regURL, _ := url.Parse(reg.URL)
	realmURL, _ := url.Parse(realm.URL)
	proxy.upstream[regURL.Hostname()] = true
	proxy.upstream[realmURL.Hostname()] = true
	px := httptest.NewServer(proxy)
	defer px.Close()

	store := newProxyStore(t, "p1", px.URL, "", "")

	// Resolve the proxy exactly like the engine does, then build the
	// client exactly like newRemoteClient does.
	e := &Engine{proxies: store}
	proxyURL, err := e.resolveProxy(SyncTask{RemoteProxyID: "p1"})
	if err != nil {
		t.Fatalf("resolveProxy: %v", err)
	}
	client, err := newRemoteClient(reg.URL, "", "", proxyURL, DefaultSyncTimeout)
	if err != nil {
		t.Fatalf("newRemoteClient: %v", err)
	}

	if _, err := client.GetManifest(context.Background(), "library/alpine", "3.19"); err != nil {
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
