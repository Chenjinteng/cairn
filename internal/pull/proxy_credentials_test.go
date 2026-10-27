package pull

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chenjinteng/cairn/internal/proxies"
)

// TestResolveSourceBakesProxyCredentials is the v0.7.54 regression test.
//
// The proxy library keeps Username / Password in dedicated fields, but
// net/http's http.ProxyURL only reads credentials embedded in the URL.
// resolveSource used to hand Proxy.URL straight through, dropping them — so
// a proxy requiring basic auth passed the proxies-page connectivity test
// (which did attach the credentials) and then failed every real pull with
// "407 Proxy Authentication Required".
//
// resolveSource must now return a URL that carries the userinfo, because
// that string is what ends up in registry.Config.Proxy.
func TestResolveSourceBakesProxyCredentials(t *testing.T) {
	store, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatalf("proxies.Open: %v", err)
	}
	if err := store.Put(proxies.Proxy{
		ID:       "p1",
		Name:     "corp-proxy",
		URL:      "http://proxy.example.com:8080",
		Username: "alice",
		Password: "s3cr3t",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	o := &Orchestrator{Proxies: store}
	job := JobView{
		SourceRef: "library/alpine:3.19",
		Proxy:     "p1",
	}

	_, _, _, proxyURL, err := o.resolveSource(job)
	if err != nil {
		t.Fatalf("resolveSource: %v", err)
	}
	if proxyURL == "" {
		t.Fatal("resolveSource returned an empty proxy URL")
	}
	if !strings.Contains(proxyURL, "alice:s3cr3t@") {
		t.Errorf("proxy URL %q has no credentials — the proxy would answer 407", proxyURL)
	}
	if !strings.Contains(proxyURL, "proxy.example.com:8080") {
		t.Errorf("proxy URL %q lost the host", proxyURL)
	}
}

// TestResolveSourceAnonymousProxyKeepsURL: a proxy without credentials must
// come back unchanged (no bogus userinfo appended).
func TestResolveSourceAnonymousProxyKeepsURL(t *testing.T) {
	store, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatalf("proxies.Open: %v", err)
	}
	if err := store.Put(proxies.Proxy{
		ID:   "p2",
		Name: "anon-proxy",
		URL:  "http://proxy.example.com:8080",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	o := &Orchestrator{Proxies: store}
	_, _, _, proxyURL, err := o.resolveSource(JobView{SourceRef: "library/alpine:3.19", Proxy: "p2"})
	if err != nil {
		t.Fatalf("resolveSource: %v", err)
	}
	if proxyURL != "http://proxy.example.com:8080" {
		t.Errorf("proxy URL = %q, want the stored URL unchanged", proxyURL)
	}
}

// TestResolveSourceInlineProxyWins: an inline proxy URL on the job takes
// precedence over the stored proxy entry, and is passed through verbatim
// (the caller supplies it already-formed).
func TestResolveSourceInlineProxyWins(t *testing.T) {
	store, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatalf("proxies.Open: %v", err)
	}
	if err := store.Put(proxies.Proxy{
		ID:       "p3",
		Name:     "corp-proxy",
		URL:      "http://proxy.example.com:8080",
		Username: "alice",
		Password: "s3cr3t",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	o := &Orchestrator{Proxies: store}
	_, _, _, proxyURL, err := o.resolveSource(JobView{
		SourceRef: "library/alpine:3.19",
		Proxy:     "p3",
		ProxyURL:  "http://inline.example.com:3128",
	})
	if err != nil {
		t.Fatalf("resolveSource: %v", err)
	}
	if proxyURL != "http://inline.example.com:3128" {
		t.Errorf("proxy URL = %q, want the inline URL to win", proxyURL)
	}
}
