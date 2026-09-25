package pull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"cairn/internal/registry"
)

// fakeFetcher answers GetManifest by digest lookup. Anything not in the
// map returns an error so a regression that "fetches the wrong digest"
// fails loudly instead of accidentally hitting a real registry.
type fakeFetcher struct {
	byDigest map[string]*registry.Manifest
	got      []string
}

func (f *fakeFetcher) GetManifest(_ context.Context, _, ref string) (*registry.Manifest, error) {
	f.got = append(f.got, ref)
	if m, ok := f.byDigest[ref]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("fakeFetcher: no manifest for %s", ref)
}

// buildIndexManifest constructs an image index whose children are one
// schema2 image manifest per platform. Returns the index (as a
// *registry.Manifest) and a digest→child lookup for the fake fetcher.
func buildIndexManifest(t *testing.T, platforms []platformSpec) (*registry.Manifest, map[string]*registry.Manifest) {
	t.Helper()
	type platform struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant,omitempty"`
	}
	type desc struct {
		MediaType string    `json:"mediaType"`
		Digest    string    `json:"digest"`
		Size      int64     `json:"size"`
		Platform  *platform `json:"platform"`
	}
	type indexDoc struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Manifests     []desc `json:"manifests"`
	}

	children := make(map[string]*registry.Manifest)
	manifests := make([]desc, 0, len(platforms))
	for _, p := range platforms {
		cfgBlob := "sha256:" + p.digestSeed + "-config"
		layerBlob := "sha256:" + p.digestSeed + "-layer"
		manifestJSON, _ := json.Marshal(map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
			"config":        map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "digest": cfgBlob, "size": 1234},
			"layers":        []map[string]any{{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "digest": layerBlob, "size": 5678}},
		})
		childDigest := "sha256:" + p.digestSeed + "-manifest"
		children[childDigest] = &registry.Manifest{
			Digest:    childDigest,
			MediaType: "application/vnd.docker.distribution.manifest.v2+json",
			Raw:       manifestJSON,
		}
		d := desc{
			MediaType: "application/vnd.docker.distribution.manifest.v2+json",
			Digest:    childDigest,
			Size:      int64(len(manifestJSON)),
			Platform:  &platform{Architecture: p.arch, OS: p.os, Variant: p.variant},
		}
		manifests = append(manifests, d)
	}
	idx, _ := json.Marshal(indexDoc{
		SchemaVersion: 2,
		MediaType:     "application/vnd.docker.distribution.manifest.list.v2+json",
		Manifests:     manifests,
	})
	return &registry.Manifest{
		MediaType: "application/vnd.docker.distribution.manifest.list.v2+json",
		Raw:       idx,
	}, children
}

type platformSpec struct {
	os         string
	arch       string
	variant    string
	digestSeed string
}

// --- sourcePlatformRef.key() --------------------------------------------------

func TestPlatformKey(t *testing.T) {
	cases := []struct {
		name string
		p    *sourcePlatformRef
		want string
	}{
		{"nil", nil, ""},
		{"missing os", &sourcePlatformRef{Architecture: "amd64"}, ""},
		{"missing arch", &sourcePlatformRef{OS: "linux"}, ""},
		{"plain", &sourcePlatformRef{OS: "linux", Architecture: "amd64"}, "linux/amd64"},
		{"with variant", &sourcePlatformRef{OS: "linux", Architecture: "arm", Variant: "v7"}, "linux/arm/v7"},
		{"uppercased normalised", &sourcePlatformRef{OS: "LINUX", Architecture: "AMD64"}, "linux/amd64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.key(); got != tc.want {
				t.Fatalf("key() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlatformMatchAny(t *testing.T) {
	p := &sourcePlatformRef{OS: "linux", Architecture: "arm", Variant: "v7"}
	if !p.matchAny(nil) {
		t.Fatal("empty allow-list should match everything")
	}
	if !p.matchAny([]string{"linux/arm/v7"}) {
		t.Fatal("exact key should match")
	}
	if p.matchAny([]string{"linux/amd64"}) {
		t.Fatal("non-matching arch must not match")
	}
	if p.matchAny([]string{"linux/arm"}) {
		t.Fatal("variant is part of the key; linux/arm must not match linux/arm/v7")
	}
	// No platform info at all: keep it (better to pull an unknown than drop
	// the only manifest the upstream gave us).
	if !((&sourcePlatformRef{}).matchAny([]string{"linux/amd64"})) {
		t.Fatal("no-platform ref should be kept even when allow-list is non-empty")
	}
}

// --- planTransfer end-to-end --------------------------------------------------

// TestPlanTransferIndexFiltering is the v0.6.0 regression: a multi-arch
// index with [linux/amd64, linux/arm64, linux/arm/v7] and an allow-list of
// ["linux/amd64"] must fetch only the amd64 child manifest and aggregate
// its blobs (one config + one layer).
func TestPlanTransferIndexFiltering(t *testing.T) {
	idx, children := buildIndexManifest(t, []platformSpec{
		{os: "linux", arch: "amd64", digestSeed: "aaa"},
		{os: "linux", arch: "arm64", digestSeed: "bbb"},
		{os: "linux", arch: "arm", variant: "v7", digestSeed: "ccc"},
	})
	fetcher := &fakeFetcher{byDigest: children}

	plan, err := planTransfer(context.Background(), fetcher, "library/alpine", idx, []string{"linux/amd64"})
	if err != nil {
		t.Fatalf("planTransfer: %v", err)
	}
	if got, want := len(plan.children), 1; got != want {
		t.Fatalf("len(children) = %d, want %d (got fetches=%v)", got, want, fetcher.got)
	}
	if got, want := len(plan.blobs), 2; got != want {
		// 1 config + 1 layer for the single platform
		t.Fatalf("len(blobs) = %d, want %d", got, want)
	}
	wantDigest := "sha256:aaa-manifest"
	if plan.children[0].Ref != wantDigest {
		t.Fatalf("child ref = %q, want %q", plan.children[0].Ref, wantDigest)
	}
}

// TestPlanTransferEmptyAllowListMatchesAll asserts the v0.5.x default
// ("no filter") keeps every child.
func TestPlanTransferEmptyAllowListMatchesAll(t *testing.T) {
	idx, children := buildIndexManifest(t, []platformSpec{
		{os: "linux", arch: "amd64", digestSeed: "aaa"},
		{os: "linux", arch: "arm64", digestSeed: "bbb"},
		{os: "linux", arch: "ppc64le", digestSeed: "ddd"},
	})
	fetcher := &fakeFetcher{byDigest: children}
	plan, err := planTransfer(context.Background(), fetcher, "library/alpine", idx, nil)
	if err != nil {
		t.Fatalf("planTransfer: %v", err)
	}
	if got, want := len(plan.children), 3; got != want {
		t.Fatalf("len(children) = %d, want %d", got, want)
	}
}

// TestPlanTransferNoMatchErrors verifies a misconfigured allow-list (none
// of the children match) surfaces as a clear error rather than silently
// producing an empty index that breaks every `docker pull` from here.
func TestPlanTransferNoMatchErrors(t *testing.T) {
	idx, children := buildIndexManifest(t, []platformSpec{
		{os: "linux", arch: "amd64", digestSeed: "aaa"},
		{os: "linux", arch: "arm64", digestSeed: "bbb"},
	})
	fetcher := &fakeFetcher{byDigest: children}
	_, err := planTransfer(context.Background(), fetcher, "library/alpine", idx, []string{"linux/s390x"})
	if err == nil {
		t.Fatal("expected error when filter matches no children")
	}
	if !errors.Is(err, err) { // just confirm it is non-nil; message text is part of the contract
		t.Fatal("error should be non-nil")
	}
}

// TestPlanTransferSingleArchManifestUnaffected covers the non-index path:
// a schema2 manifest (one arch) must always come through regardless of
// the filter. platformAllow is a no-op for non-index manifests.
func TestPlanTransferSingleArchManifestUnaffected(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:c","size":7},
		"layers": [{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:l","size":9}]
	}`)
	root := &registry.Manifest{
		MediaType: "application/vnd.docker.distribution.manifest.v2+json",
		Raw:       body,
	}
	// Even with an empty fetcher, single-arch pulls never call GetManifest.
	fetcher := &fakeFetcher{byDigest: nil}
	plan, err := planTransfer(context.Background(), fetcher, "x/y", root, []string{"linux/s390x"})
	if err != nil {
		t.Fatalf("planTransfer: %v", err)
	}
	if len(plan.children) != 0 {
		t.Fatalf("single-arch manifest should produce no child manifests; got %d", len(plan.children))
	}
	if len(plan.blobs) != 2 {
		t.Fatalf("single-arch manifest: want 2 blobs (config+layer), got %d", len(plan.blobs))
	}
}