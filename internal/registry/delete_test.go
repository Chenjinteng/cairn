package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cairn/internal/registry"
)

// deleteRegistry tracks DELETE calls so tests can assert the right manifest
// was targeted. Tag-list and manifest fetches still come from fakeRegistry.
type deleteRegistry struct {
	fakeRegistry
	deleted []string // paths like "alpine/manifests/sha256:abc"
}

func (d *deleteRegistry) handler() http.Handler {
	inner := d.fakeRegistry.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			d.deleted = append(d.deleted, strings.TrimPrefix(r.URL.Path, "/v2/"))
			w.WriteHeader(http.StatusAccepted)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func TestDeleteManifestSuccess(t *testing.T) {
	fr := &deleteRegistry{
		fakeRegistry: fakeRegistry{
			catalog:   []string{"alpine"},
			tags:      map[string][]string{"alpine": {"3.19"}},
			manifests: map[string]string{"alpine/manifests/3.19": `{"layers":[]}`},
			digestOf:  map[string]string{"alpine/manifests/3.19": "sha256:abc"},
		},
	}
	srv := httptest.NewServer(fr.handler())
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	affected, err := c.DeleteManifest(context.Background(), "alpine", "sha256:abc")
	if err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if len(fr.deleted) != 1 || fr.deleted[0] != "alpine/manifests/sha256:abc" {
		t.Errorf("deleted = %v, want [alpine/manifests/sha256:abc]", fr.deleted)
	}
	// tagsForDigest will try to ListTags + GetManifest post-delete; the fake
	// still serves them, so we should get ["3.19"] back as the affected list.
	if len(affected) != 1 || affected[0] != "3.19" {
		t.Errorf("affected = %v, want [3.19]", affected)
	}
}

func TestDeleteManifestMissingDigest(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	if _, err := c.DeleteManifest(context.Background(), "alpine", ""); err == nil {
		t.Fatal("expected error for empty digest, got nil")
	}
}

func TestDeleteManifestErrorIsTyped(t *testing.T) {
	// Registry returns 400 with code DIGEST_INVALID when given a tag instead
	// of a digest. We expect the error to wrap a *registry.Error so handlers
	// can map it to a stable HTTP code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]string{{"code": "DIGEST_INVALID", "message": "manifest reference must be a digest"}},
		})
	}))
	defer srv.Close()

	c, _ := registry.NewClient(registry.Config{BaseURL: srv.URL})
	_, err := c.DeleteManifest(context.Background(), "alpine", "not-a-digest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *registry.Error
	if !errors.As(err, &re) {
		t.Fatalf("expected *registry.Error, got %T", err)
	}
	if re.Code != "DIGEST_INVALID" {
		t.Errorf("re.Code = %q, want DIGEST_INVALID", re.Code)
	}
}

// errorAs is a tiny shim so the test file doesn't have to import "errors".
func errorAs(err error, target any) bool {
	return errors.As(err, target)
}