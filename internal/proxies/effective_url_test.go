package proxies

import (
	"strings"
	"testing"
)

// TestEffectiveURLBakesCredentials is the v0.7.54 regression test.
//
// The proxy library stores credentials in dedicated Username / Password
// fields, but net/http's http.ProxyURL only reads them from the URL's
// userinfo. EffectiveURL is the single conversion point every caller that
// actually dials through a proxy must go through.
//
// Before it existed, only the "测试连接" button converted; the real pull
// path took Proxy.URL verbatim and dropped the credentials, so a proxy that
// requires basic auth passed its connectivity test and then failed every
// pull with 407.
func TestEffectiveURLBakesCredentials(t *testing.T) {
	u, err := EffectiveURL("http://proxy.example.com:8080", "bob", "s3cr3t")
	if err != nil {
		t.Fatalf("EffectiveURL: %v", err)
	}
	if u.User == nil {
		t.Fatal("userinfo not attached")
	}
	gotUser := u.User.Username()
	if gotUser != "bob" {
		t.Errorf("username = %q, want bob", gotUser)
	}
	pass, ok := u.User.Password()
	if !ok || pass != "s3cr3t" {
		t.Errorf("password = %q (ok=%v), want s3cr3t", pass, ok)
	}
	// The rendered URL must carry the userinfo — that is what http.ProxyURL
	// actually reads.
	if !strings.Contains(u.String(), "bob:s3cr3t@") {
		t.Errorf("rendered URL %q has no userinfo", u.String())
	}
}

// TestEffectiveURLForReadsStoredEntry covers the stored-entry variant used
// by the pull path.
func TestEffectiveURLForReadsStoredEntry(t *testing.T) {
	p := Proxy{
		ID:       "p1",
		Name:     "corp-proxy",
		URL:      "http://proxy.example.com:8080",
		Username: "alice",
		Password: "pw",
	}
	u, err := EffectiveURLFor(p)
	if err != nil {
		t.Fatalf("EffectiveURLFor: %v", err)
	}
	if u.User == nil || u.User.Username() != "alice" {
		t.Fatalf("userinfo not attached from stored entry: %v", u.User)
	}
	if pw, _ := u.User.Password(); pw != "pw" {
		t.Errorf("password = %q, want pw", pw)
	}
}

// TestEffectiveURLAnonymousKeepsURLCredentials: an anonymous proxy (no
// username) must not have its URL's own userinfo stripped.
func TestEffectiveURLAnonymousKeepsURLCredentials(t *testing.T) {
	u, err := EffectiveURL("http://carol:pw@proxy.example.com:8080", "", "")
	if err != nil {
		t.Fatalf("EffectiveURL: %v", err)
	}
	if u.User == nil || u.User.Username() != "carol" {
		t.Errorf("URL-embedded credentials were dropped: %v", u.User)
	}
}

// TestEffectiveURLRejectsBadInput keeps the form-facing error messages,
// which the UI form validator mirrors.
func TestEffectiveURLRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no scheme", "proxy.corp:8080"},
		{"unsupported scheme", "ftp://proxy.corp:8080"},
		{"no host", "http://"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := EffectiveURL(c.raw, "", ""); err == nil {
				t.Fatalf("EffectiveURL(%q) = nil error, want rejection", c.raw)
			}
		})
	}
}

// TestEffectiveURLAcceptsSocks5: socks5 proxies are a first-class option in
// the proxies page, and Go's net/http reads the userinfo for them too.
func TestEffectiveURLAcceptsSocks5(t *testing.T) {
	u, err := EffectiveURL("socks5://proxy.example.com:1080", "bob", "pw")
	if err != nil {
		t.Fatalf("EffectiveURL: %v", err)
	}
	if u.Scheme != "socks5" {
		t.Errorf("scheme = %q, want socks5", u.Scheme)
	}
	if u.User == nil {
		t.Error("userinfo not attached for socks5")
	}
}
