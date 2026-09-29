package registry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Chenjinteng/cairn/internal/registry"
)

// fakeRegistry is a minimal CNCF Distribution server sufficient for the
// inventory scanner tests: /v2/, /v2/_catalog, /v2/<name>/tags/list,
// /v2/<name>/manifests/<ref>. Anything else 404s.
type fakeRegistry struct {
	catalog   []string
	tags      map[string][]string
	manifests map[string]string // "repo@ref" -> JSON body
	digestOf  map[string]string // "repo@ref" -> Docker-Content-Digest
}

func (f *fakeRegistry) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": f.catalog})
		case strings.HasPrefix(r.URL.Path, "/v2/") && strings.HasSuffix(r.URL.Path, "/tags/list"):
			name := strings.TrimPrefix(r.URL.Path, "/v2/")
			name = strings.TrimSuffix(name, "/tags/list")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "tags": f.tags[name]})
		case strings.Contains(r.URL.Path, "/manifests/"):
			key := strings.TrimPrefix(r.URL.Path, "/v2/")
			body, ok := f.manifests[key]
			if !ok {
				http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, 404)
				return
			}
			if d, ok := f.digestOf[key]; ok {
				w.Header().Set("Docker-Content-Digest", d)
			}
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func TestProbe(t *testing.T) {
	fr := &fakeRegistry{}
	srv := httptest.NewServer(fr.handler())
	defer srv.Close()

	c, err := registry.NewClient(registry.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestListRepositories(t *testing.T) {
	fr := &fakeRegistry{catalog: []string{"alpine", "redis", "postgres"}}
	srv := httptest.NewServer(fr.handler())
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	repos, err := c.ListRepositories(context.Background())
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if got, want := len(repos), 3; got != want {
		t.Fatalf("got %d repos, want %d (%v)", got, want, repos)
	}
}

func TestListTagsHandlesNullTags(t *testing.T) {
	fr := &fakeRegistry{
		catalog: []string{"empty-repo"},
		tags:    map[string][]string{"empty-repo": nil},
	}
	srv := httptest.NewServer(fr.handler())
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	tags, err := c.ListTags(context.Background(), "empty-repo")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("got %d tags, want 0", len(tags))
	}
}

func TestGetManifestSendsAcceptHeader(t *testing.T) {
	// Verify the fake registry routes manifest fetches correctly only when
	// the client sends Accept matching one of the four media types.
	var sawAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			sawAccept = r.Header.Get("Accept")
		}
		w.Header().Set("Docker-Content-Digest", "sha256:abc")
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:c","size":10},"layers":[]}`))
	}))
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	_, err := c.GetManifest(context.Background(), "alpine", "3.19")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if !strings.Contains(sawAccept, "manifest.v2+json") ||
		!strings.Contains(sawAccept, "image.manifest.v1+json") ||
		!strings.Contains(sawAccept, "image.index.v1+json") ||
		!strings.Contains(sawAccept, "manifest.list.v2+json") {
		t.Fatalf("Accept header missing required media types: %q", sawAccept)
	}
}

func TestScanInventory(t *testing.T) {
	fr := &fakeRegistry{
		catalog: []string{"alpine", "redis"},
		tags: map[string][]string{
			"alpine": {"3.19", "3.20"},
			"redis":  {"7.2"},
		},
		manifests: map[string]string{
			"alpine/manifests/3.19": `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:c1","size":100},"layers":[{"digest":"sha256:l1","size":1000,"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip"}]}`,
			"alpine/manifests/3.20": `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:c2","size":110},"layers":[{"digest":"sha256:l2","size":1100,"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip"}]}`,
			"redis/manifests/7.2":   `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:c3","size":200},"layers":[{"digest":"sha256:l3","size":2000,"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip"}]}`,
		},
		digestOf: map[string]string{
			"alpine/manifests/3.19": "sha256:aaa",
			"alpine/manifests/3.20": "sha256:bbb",
			"redis/manifests/7.2":   "sha256:ccc",
		},
	}
	srv := httptest.NewServer(fr.handler())
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	inv, err := c.ScanInventory(context.Background())
	if err != nil {
		t.Fatalf("ScanInventory: %v", err)
	}
	if inv.Totals.RepoCount != 2 {
		t.Errorf("RepoCount = %d, want 2", inv.Totals.RepoCount)
	}
	if inv.Totals.TagCount != 3 {
		t.Errorf("TagCount = %d, want 3", inv.Totals.TagCount)
	}
	// Sizes include image config size + layer sizes
	wantSize := int64(100 + 1000 + 110 + 1100 + 200 + 2000)
	if inv.Totals.TotalSize != wantSize {
		t.Errorf("TotalSize = %d, want %d", inv.Totals.TotalSize, wantSize)
	}
	if len(inv.FailedTags) != 0 {
		t.Errorf("FailedTags = %v, want empty", inv.FailedTags)
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  *registry.Error
		want bool
	}{
		{"404", &registry.Error{Status: 404}, true},
		{"MANIFEST_UNKNOWN", &registry.Error{Status: 404, Code: "MANIFEST_UNKNOWN"}, true},
		{"NAME_UNKNOWN", &registry.Error{Status: 404, Code: "NAME_UNKNOWN"}, true},
		{"500", &registry.Error{Status: 500}, false},
		{"DIGEST_INVALID", &registry.Error{Status: 400, Code: "DIGEST_INVALID"}, false},
	}
	for _, tc := range cases {
		if got := tc.err.IsNotFound(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
