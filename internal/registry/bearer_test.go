package registry

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchTokenAnonymousSendsNoBasicAuth is the v0.5.5 regression test.
//
// auth.docker.io answers an empty-credential Basic header (base64(":"))
// with 401 "incorrect username or password" instead of ignoring it, so
// anonymous token requests must carry NO Authorization header at all.
// The realm below mimics that behaviour: any Authorization header is
// rejected unless it carries real credentials.
func TestFetchTokenAnonymousSendsNoBasicAuth(t *testing.T) {
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"details":"incorrect username or password"}`))
			return
		}
		if r.URL.Query().Get("service") != "registry.docker.io" {
			t.Errorf("realm got service=%q, want registry.docker.io", r.URL.Query().Get("service"))
		}
		if r.URL.Query().Get("scope") != "repository:library/alpine:pull" {
			t.Errorf("realm got scope=%q, want repository:library/alpine:pull", r.URL.Query().Get("scope"))
		}
		_, _ = w.Write([]byte(`{"token":"anon-token","expires_in":300}`))
	}))
	defer realm.Close()

	b := NewBearerAuth(realm.Client())
	ch := Challenge{
		Scheme:  "Bearer",
		Realm:   realm.URL,
		Service: "registry.docker.io",
		Scope:   "repository:library/alpine:pull",
	}
	// Empty credentials -- the shape client.go passes for anonymous pulls.
	tok, err := b.FetchToken(context.Background(), ch, &BasicAuth{})
	if err != nil {
		t.Fatalf("FetchToken with empty creds: %v", err)
	}
	if tok != "anon-token" {
		t.Fatalf("token = %q, want anon-token", tok)
	}
}

// TestFetchTokenWithCredentialsSendsBasicAuth verifies the other half:
// when a username IS configured, the realm must see the Basic header.
func TestFetchTokenWithCredentialsSendsBasicAuth(t *testing.T) {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"cred-token"}`))
	}))
	defer realm.Close()

	b := NewBearerAuth(realm.Client())
	ch := Challenge{Scheme: "Bearer", Realm: realm.URL, Service: "svc"}
	tok, err := b.FetchToken(context.Background(), ch, &BasicAuth{Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("FetchToken with creds: %v", err)
	}
	if tok != "cred-token" {
		t.Fatalf("token = %q, want cred-token (access_token fallback)", tok)
	}
}

// TestParseChallengeDockerHubShape pins the real-world header shape:
// no spaces after commas, quoted values.
func TestParseChallengeDockerHubShape(t *testing.T) {
	header := `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`
	ch, ok := parseChallenge(header)
	if !ok {
		t.Fatal("parseChallenge returned !ok for the Docker Hub header shape")
	}
	if ch.Realm != "https://auth.docker.io/token" {
		t.Errorf("realm = %q", ch.Realm)
	}
	if ch.Service != "registry.docker.io" {
		t.Errorf("service = %q", ch.Service)
	}
	if ch.Scope != "repository:library/alpine:pull" {
		t.Errorf("scope = %q", ch.Scope)
	}
}
