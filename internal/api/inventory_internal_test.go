package api

// v0.7.3: regression coverage for the Platforms field that the inventory
// shape gained so the UI can show a multi-arch tag as "linux/amd64,
// linux/arm64" instead of the historical "linux/amd64 +1" form, and
// offer a per-platform download menu.
//
// buildTagInfo is unexported, so this test lives in package api (alongside
// dispatch_test.go). We seed the filesystem storage directly because
// PutManifest accepts arbitrary body + mediaType — an OCI image index
// is just another manifest on disk as far as the storage layer cares.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chenjinteng/cairn/internal/storage"
)

// multiArchIndexBody is a minimal OCI image index that references two
// single-arch child manifests. The child digest values are arbitrary —
// buildTagInfo does not dereference them for the Platforms list, only
// the index's own manifests[].platform entries.
const multiArchIndexBody = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1000,"platform":{"architecture":"amd64","os":"linux"}},
    {"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1100,"platform":{"architecture":"arm64","os":"linux","variant":"v8"}},
    {"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":1200,"platform":{"architecture":"arm","os":"linux","variant":"v7"}}
  ]
}`

// singleArchManifestBody is the smallest possible OCI image manifest —
// one config blob + one layer, no child entries. buildTagInfo should
// surface its (os, architecture) as a single-element Platforms list
// (read from the config blob).
const singleArchManifestBody = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
    "size": 702
  },
  "layers": [
    {"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","size":3000000}
  ]
}`

// singleArchConfigBody is the OCI image config blob referenced by
// singleArchManifestBody. buildTagInfo reads arch/os/created from here
// for single-arch manifests (it doesn't parse the manifest body for
// those fields, only the config blob).
const singleArchConfigBody = `{
  "architecture":"amd64","os":"linux","created":"2026-10-02T12:00:00Z","rootfs":{}
}`

// seedRepo writes a single repository containing one image-index tag
// and one single-arch tag, plus the config blob the single-arch tag
// points at. Returns the inventory.Repository view so callers can
// inspect Platforms / Architecture / OS without re-parsing the body.
//
// Returns *storage.Filesystem (not storage.Storage) because PutBlob is
// not on the interface — it's the upload-session shape exposed by the
// V2 protocol handlers. buildRepoView is happy with the interface so we
// pass it through without a cast.
func seedRepo(t *testing.T, repo string) (Repository, *storage.Filesystem) {
	t.Helper()
	dir := t.TempDir()
	fs, err := storage.NewFilesystem(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}
	ctx := context.Background()

	if _, err := fs.PutManifest(ctx, repo, "multi", "application/vnd.oci.image.index.v1+json", []byte(multiArchIndexBody)); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if _, err := fs.PutManifest(ctx, repo, "single", "application/vnd.oci.image.manifest.v1+json", []byte(singleArchManifestBody)); err != nil {
		t.Fatalf("seed single-arch manifest: %v", err)
	}
	// Land the config blob directly on disk so we don't have to round-trip
	// a sha256 through PutUpload — this test cares about the inventory
	// view, not the upload protocol. The blob goes under <root>/blobs/
	// (NOT _blobs) — that's where Filesystem.blobPath looks for it.
	// The digest in singleArchManifestBody is sha256:1111…1111 (64 hex
	// digits); Filesystem splits that into algo=sha256, prefix=11, and
	// reads blobs/sha256/11/<hex>/data.
	const configDigest = "1111111111111111111111111111111111111111111111111111111111111111"
	configDir := filepath.Join(dir, "registry", "blobs", "sha256", configDigest[:2], configDigest)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "data"), []byte(singleArchConfigBody), 0o644); err != nil {
		t.Fatalf("WriteFile config blob: %v", err)
	}

	view, errs := buildRepoView(ctx, fs, repo)
	if len(errs) > 0 {
		t.Fatalf("buildRepoView errors: %+v", errs)
	}
	return view, fs
}

// TestInventoryPlatforms_MultiArch verifies an image index produces
// Platforms in the "<os>/<arch>[/<variant>]" shape the UI uses to
// render the architecture column and the download Dropdown menu.
// arm/v7 must NOT collapse into arm64 — the variant is the disambiguator.
func TestInventoryPlatforms_MultiArch(t *testing.T) {
	view, _ := seedRepo(t, "test/multi")
	var multi *TagInfo
	for i := range view.Tags {
		if view.Tags[i].Tag == "multi" {
			multi = &view.Tags[i]
			break
		}
	}
	if multi == nil {
		t.Fatalf("multi tag not built: %+v", view.Tags)
	}
	want := []string{"linux/amd64", "linux/arm64/v8", "linux/arm/v7"}
	if !equalStringSlice(multi.Platforms, want) {
		t.Errorf("Platforms = %v, want %v", multi.Platforms, want)
	}
	if multi.PlatformCount != 3 {
		t.Errorf("PlatformCount = %d, want 3", multi.PlatformCount)
	}
	// Architecture / OS stay populated for back-compat: pick the first
	// index entry that has a platform.
	if multi.Architecture != "amd64" || multi.OS != "linux" {
		t.Errorf("Architecture/OS back-compat = (%q, %q), want (amd64, linux)",
			multi.Architecture, multi.OS)
	}
}

// TestInventoryPlatforms_SingleArch verifies a non-index manifest
// still emits a one-element Platforms array so the download button
// renders identically regardless of how many platforms the tag has.
func TestInventoryPlatforms_SingleArch(t *testing.T) {
	view, _ := seedRepo(t, "test/single")
	var single *TagInfo
	for i := range view.Tags {
		if view.Tags[i].Tag == "single" {
			single = &view.Tags[i]
			break
		}
	}
	if single == nil {
		t.Fatalf("single tag not built: %+v", view.Tags)
	}
	if !equalStringSlice(single.Platforms, []string{"linux/amd64"}) {
		t.Errorf("Platforms = %v, want [linux/amd64]", single.Platforms)
	}
	if single.PlatformCount != 1 {
		t.Errorf("PlatformCount = %d, want 1", single.PlatformCount)
	}
}

// TestFormatPlatformKey covers the small helper that drives both the
// inventory Platforms list and the filename suffix.
func TestFormatPlatformKey(t *testing.T) {
	cases := []struct {
		os, arch, variant string
		want              string
	}{
		{"linux", "amd64", "", "linux/amd64"},
		{"linux", "arm64", "v8", "linux/arm64/v8"},
		{"linux", "arm", "v7", "linux/arm/v7"},
		{"LINUX", "ARM64", "V8", "linux/arm64/v8"}, // case-fold
		{"", "amd64", "", ""},                       // missing os → skipped
		{"linux", "", "", ""},                       // missing arch → skipped
		{"", "", "", ""},
		{"  linux  ", "  amd64  ", "", "linux/amd64"}, // trimmed
	}
	for _, c := range cases {
		got := formatPlatformKey(c.os, c.arch, c.variant)
		if got != c.want {
			t.Errorf("formatPlatformKey(%q, %q, %q) = %q, want %q",
				c.os, c.arch, c.variant, got, c.want)
		}
	}
}

// TestPlatformArchForFilename covers the suffix the download handler
// appends to the on-disk filename so multi-arch downloads don't collide
// in the user's Downloads folder.
func TestPlatformArchForFilename(t *testing.T) {
	cases := []struct {
		platform, want string
	}{
		{"linux/amd64", "amd64"},
		{"linux/arm64", "arm64"},
		{"linux/arm64/v8", "arm64v8"},
		{"linux/arm/v7", "armv7"},
		{"linux/arm/v6", "armv6"},
		{"", "amd64"},   // empty → safe fallback
		{"linux", "amd64"},
		{"LINUX/ARM64/V8", "arm64v8"}, // case-fold
	}
	for _, c := range cases {
		got := platformArchForFilename(c.platform)
		if got != c.want {
			t.Errorf("platformArchForFilename(%q) = %q, want %q",
				c.platform, got, c.want)
		}
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
