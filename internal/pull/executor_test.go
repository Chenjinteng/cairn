package pull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Chenjinteng/cairn/internal/config"
	"github.com/Chenjinteng/cairn/internal/registry"
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

// TestPlatformMatchAny covers the (os, arch)-only matching that
// matchAny uses after v0.7.1. Variant is intentionally ignored: the
// upstream always populates variant="v8" on arm64 and "v7" on armv7,
// but the settings chip writes bare "linux/arm64" / "linux/arm". A
// strict os/arch/variant comparison would silently filter arm64 out.
func TestPlatformMatchAny(t *testing.T) {
	// arm64v8 is the registry's normal arm64 — variant "v8" must NOT
	// disqualify it from matching the chip's "linux/arm64" token.
	arm64 := &sourcePlatformRef{OS: "linux", Architecture: "arm64", Variant: "v8"}
	if !arm64.matchAny(nil) {
		t.Fatal("empty allow-list should match everything")
	}
	if !arm64.matchAny([]string{"linux/arm64"}) {
		t.Fatal("linux/arm64 must match arm64/v8 — variant is ignored by design")
	}
	if !arm64.matchAny([]string{"linux/arm64/v8"}) {
		t.Fatal("explicit-variant allow token still matches arm64/v8")
	}
	if arm64.matchAny([]string{"linux/amd64"}) {
		t.Fatal("non-matching arch must not match")
	}

	// arm/v7 must match the bare "linux/arm" chip token (the operator
	// probably doesn't care about v7 vs v6, just arch).
	armv7 := &sourcePlatformRef{OS: "linux", Architecture: "arm", Variant: "v7"}
	if !armv7.matchAny([]string{"linux/arm"}) {
		t.Fatal("linux/arm must match arm/v7 — variant is ignored by design")
	}
	if armv7.matchAny([]string{"linux/arm64"}) {
		t.Fatal("arm/v7 must NOT match arm64 — different arch")
	}

	// amd64 has no variant; the old and new semantics both match.
	amd64 := &sourcePlatformRef{OS: "linux", Architecture: "amd64"}
	if !amd64.matchAny([]string{"linux/amd64"}) {
		t.Fatal("linux/amd64 must match amd64 (no variant)")
	}

	// Multiple tokens in the allow-list: amd64 + arm64 must select both.
	both := []string{"linux/amd64", "linux/arm64"}
	if !amd64.matchAny(both) {
		t.Fatal("amd64 must be selected by [amd64, arm64]")
	}
	if !arm64.matchAny(both) {
		t.Fatal("arm64/v8 must be selected by [amd64, arm64] — this is the v0.7.0 regression")
	}
	ppc64le := &sourcePlatformRef{OS: "linux", Architecture: "ppc64le"}
	if ppc64le.matchAny(both) {
		t.Fatal("ppc64le must NOT be selected by [amd64, arm64]")
	}

	// No platform info at all: keep it (better to pull an unknown than
	// drop the only manifest the upstream gave us).
	if !((&sourcePlatformRef{}).matchAny([]string{"linux/amd64"})) {
		t.Fatal("no-platform ref should be kept even when allow-list is non-empty")
	}
}

// TestSplitAllowToken covers the small parser that matchAny uses to
// normalise a chip entry into its (os, arch) halves.
func TestSplitAllowToken(t *testing.T) {
	cases := []struct {
		in         string
		wantOS     string
		wantArch   string
	}{
		{"linux/amd64", "linux", "amd64"},
		{"linux/arm64", "linux", "arm64"},
		{"linux/arm64/v8", "linux", "arm64"}, // variant stripped
		{"linux/arm/v7", "linux", "arm"},
		{"  LINUX/ARM64  ", "linux", "arm64"},
		{"", "", ""},
		{"linux", "", ""},
		{"linux/amd64/extra/junk", "", ""},
	}
	for _, c := range cases {
		os, arch := splitAllowToken(c.in)
		if os != c.wantOS || arch != c.wantArch {
			t.Errorf("splitAllowToken(%q) = (%q, %q), want (%q, %q)",
				c.in, os, arch, c.wantOS, c.wantArch)
		}
	}
}

// --- planTransfer end-to-end --------------------------------------------------

// TestPlanTransferIndexFiltering is the platform allow-list regression: a
// multi-arch index with [linux/amd64, linux/arm64, linux/arm/v7] and an
// allow-list of ["linux/amd64"] must fetch only the amd64 child manifest
// and aggregate
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

// TestPlatformAllowNilSafe pins the v0.5.16 fix. The allow-list used to be
// a func field on Orchestrator that the single construction site forgot to
// assign, so calling it panicked with a nil dereference and killed the
// process (every in-memory job disappeared with it). It now reads live
// config, and must stay nil-safe at every step of the chain.
func TestPlatformAllowNilSafe(t *testing.T) {
	var nilOrch *Orchestrator
	if got := nilOrch.platformAllow(); got != nil {
		t.Fatalf("nil orchestrator: got %v, want nil", got)
	}
	// A zero-value Orchestrator has no Cfg at all — must not panic.
	if got := (&Orchestrator{}).platformAllow(); got != nil {
		t.Fatalf("nil Cfg: got %v, want nil", got)
	}
	// A Cfg whose Mutable is nil (boot-before-settings) is equally fine.
	if got := (&Orchestrator{Cfg: &config.Config{}}).platformAllow(); got != nil {
		t.Fatalf("nil Mutable: got %v, want nil", got)
	}
}

// TestPlatformAllowReadsLiveConfig: the list is normalised (trim +
// lowercase) and re-read on every call, so a settings-page change applies
// to the next queued job without a restart. An empty value means "pull
// every platform", hence nil rather than an empty non-nil slice.
func TestPlatformAllowReadsLiveConfig(t *testing.T) {
	mut := &config.Mutable{}
	mut.Set("pull.platforms", " Linux/AMD64 , linux/arm64 ")
	o := &Orchestrator{Cfg: &config.Config{Mutable: mut}}

	got := o.platformAllow()
	if len(got) != 2 || got[0] != "linux/amd64" || got[1] != "linux/arm64" {
		t.Fatalf("platformAllow = %v", got)
	}
	mut.Set("pull.platforms", "")
	if got := o.platformAllow(); got != nil {
		t.Fatalf("cleared allow-list: got %v, want nil", got)
	}
}

// --- synthesizeFilteredIndex --------------------------------------------------
//
// v0.5.25: when a platform filter matches multiple children, the tag must
// point at a *new* index that contains only the filtered children, not the
// upstream one. These tests pin that synthesis.

func TestSynthesizeFilteredIndexKeepsOnlyFiltered(t *testing.T) {
	idxRaw := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": [
			{"digest":"sha256:aaa","mediaType":"application/vnd.oci.image.manifest.v1+json","size":1022,"platform":{"architecture":"amd64","os":"linux"}},
			{"digest":"sha256:bbb","mediaType":"application/vnd.oci.image.manifest.v1+json","size":1023,"platform":{"architecture":"arm64","os":"linux"}},
			{"digest":"sha256:ccc","mediaType":"application/vnd.oci.image.manifest.v1+json","size":1024,"platform":{"architecture":"ppc64le","os":"linux"}}
		]
	}`)
	keep := []plannedManifest{
		{Ref: "sha256:aaa"},
		{Ref: "sha256:ccc"},
	}
	out, err := synthesizeFilteredIndex(idxRaw, keep)
	if err != nil {
		t.Fatalf("synthesizeFilteredIndex: %v", err)
	}
	var got struct {
		SchemaVersion int              `json:"schemaVersion"`
		MediaType     string           `json:"mediaType"`
		Manifests     []map[string]any `json:"manifests"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode synthesised index: %v", err)
	}
	if got.MediaType != "application/vnd.oci.image.index.v1+json" {
		t.Fatalf("mediaType lost in round-trip: got %q", got.MediaType)
	}
	if got.SchemaVersion != 2 {
		t.Fatalf("schemaVersion lost: got %d", got.SchemaVersion)
	}
	if len(got.Manifests) != 2 {
		t.Fatalf("len(manifests) = %d, want 2 (the two filtered)", len(got.Manifests))
	}
	digests := []string{}
	for _, m := range got.Manifests {
		digests = append(digests, m["digest"].(string))
	}
	want := []string{"sha256:aaa", "sha256:ccc"}
	for i, w := range want {
		if digests[i] != w {
			t.Fatalf("manifest[%d].digest = %q, want %q", i, digests[i], w)
		}
	}
}

// TestSynthesizeFilteredIndexPreservesAnnotations covers the case the
// field exists for a reason: Docker's attestation manifests are linked
// to their subjects via the `vnd.docker.reference.digest` annotation.
// Dropping it on the filtered re-emit would break the standard lookup
// path for SBOM/signature manifests, so the round-trip must be lossless.
func TestSynthesizeFilteredIndexPreservesAnnotations(t *testing.T) {
	idxRaw := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": [
			{"digest":"sha256:attest","mediaType":"application/vnd.oci.image.manifest.v1+json","size":838,
			 "annotations":{"vnd.docker.reference.digest":"sha256:subject","vnd.docker.reference.type":"attestation-manifest"},
			 "platform":{"architecture":"unknown","os":"unknown"}},
			{"digest":"sha256:subject","mediaType":"application/vnd.oci.image.manifest.v1+json","size":1022,
			 "platform":{"architecture":"amd64","os":"linux"}}
		]
	}`)
	// Filter to "linux/amd64" — we want to keep the SUBJECT only, drop
	// the attestation. But the function takes a digest list, so test
	// both directions.
	for _, keepDigest := range []string{"sha256:subject", "sha256:attest"} {
		out, err := synthesizeFilteredIndex(idxRaw, []plannedManifest{{Ref: keepDigest}})
		if err != nil {
			t.Fatalf("synthesizeFilteredIndex: %v", err)
		}
		var got struct {
			Manifests []map[string]any `json:"manifests"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Manifests) != 1 {
			t.Fatalf("keep=%s: len(manifests) = %d, want 1", keepDigest, len(got.Manifests))
		}
		m := got.Manifests[0]
		if d, _ := m["digest"].(string); d != keepDigest {
			t.Fatalf("kept wrong digest: got %q, want %q", d, keepDigest)
		}
		if keepDigest == "sha256:attest" {
			ann, _ := m["annotations"].(map[string]any)
			if ann == nil || ann["vnd.docker.reference.digest"] != "sha256:subject" {
				t.Fatalf("attest: %v / vnd.docker.reference.digest lost or wrong", ann)
			}
		}
	}
}
