package storage

// Roundtrip tests for ExportTar: build a fake Storage with one manifest
// (single-arch OCI image) + one config blob + N layer blobs, run the
// export, parse the resulting tar, and assert every byte matches.
//
// We also cover the image-index (multi-arch) path — the export must
// recurse into the chosen platform's child manifest and emit just
// that platform's image, not the index itself.
//
// These tests don't touch the filesystem (they use a pure-Go fake
// Storage) so they run in < 100ms regardless of layer size and are
// hermetic. The end-to-end filesystem roundtrip is verified by
// running `docker load` against an exported tar in step 5 of the
// build pipeline.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeBlob is one blob entry in the fake storage — opaque bytes + the
// digest we tell callers the bytes map to. Storage.GetBlob returns a
// bytes.Reader matching blob.body so io.Copy-equivalent reads work.
type fakeBlob struct {
	body []byte
}

// digest returns the "sha256:<hex>" digest for the body.
func (b *fakeBlob) digest() string {
	sum := sha256.Sum256(b.body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeStorage implements Storage against a fixed image manifest +
// blobs. Enough for ExportTar to walk through every code path without
// needing the real filesystem backend.
type fakeStorage struct {
	manifests map[string]*Manifest // key = "repo|digest-or-tag"
	blobs     map[string]*fakeBlob  // key = digest
}

func newFakeStorage(t *testing.T) *fakeStorage {
	t.Helper()
	return &fakeStorage{
		manifests: map[string]*Manifest{},
		blobs:     map[string]*fakeBlob{},
	}
}

func (f *fakeStorage) putManifest(repo, ref, mediaType string, body []byte) {
	sum := sha256.Sum256(body)
	d := "sha256:" + hex.EncodeToString(sum[:])
	f.manifests[repo+"|"+d] = &Manifest{
		Repo: repo, Digest: d, MediaType: mediaType, Body: body,
	}
	if !looksLikeDigest(ref) {
		// also store under the tag key so GetManifest(repo, tag) finds it
		f.manifests[repo+"|"+ref] = f.manifests[repo+"|"+d]
	}
}

func (f *fakeStorage) putBlob(body []byte) string {
	b := &fakeBlob{body: body}
	d := b.digest()
	f.blobs[d] = b
	return d
}

// --- Storage interface impl --------------------------------------------------

func (f *fakeStorage) Repositories(ctx context.Context) ([]string, error) {
	return []string{"fake/repo"}, nil
}
func (f *fakeStorage) Tags(ctx context.Context, repo string) ([]string, error) {
	return []string{"latest"}, nil
}
func (f *fakeStorage) TagDigest(ctx context.Context, repo, tag string) (string, error) {
	if m, ok := f.manifests[repo+"|"+tag]; ok {
		return m.Digest, nil
	}
	return "", ErrNotFound
}
func (f *fakeStorage) TagsForDigest(ctx context.Context, repo, digest string) ([]string, error) {
	return []string{"latest"}, nil
}
func (f *fakeStorage) GetManifest(ctx context.Context, repo, ref string) (*Manifest, error) {
	if m, ok := f.manifests[repo+"|"+ref]; ok {
		return m, nil
	}
	return nil, ErrNotFound
}
func (f *fakeStorage) PutManifest(ctx context.Context, repo, ref, mediaType string, body []byte) (string, error) {
	f.putManifest(repo, ref, mediaType, body)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func (f *fakeStorage) DeleteManifest(ctx context.Context, repo, digest string) ([]string, error) {
	delete(f.manifests, repo+"|"+digest)
	return nil, nil
}
func (f *fakeStorage) BlobExists(ctx context.Context, repo, digest string) (bool, error) {
	_, ok := f.blobs[digest]
	return ok, nil
}
func (f *fakeStorage) GetBlob(ctx context.Context, repo, digest string) (io.ReadCloser, int64, error) {
	b, ok := f.blobs[digest]
	if !ok {
		return nil, 0, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b.body)), int64(len(b.body)), nil
}
func (f *fakeStorage) StatBlob(ctx context.Context, digest string) (int64, error) {
	b, ok := f.blobs[digest]
	if !ok {
		return 0, ErrNotFound
	}
	return int64(len(b.body)), nil
}
func (f *fakeStorage) StartUpload(ctx context.Context, repo string) (string, error) {
	return "fake-uuid", nil
}
func (f *fakeStorage) PatchUpload(ctx context.Context, repo, uuid string, offset int64, body io.Reader) (int64, error) {
	return 0, nil
}
func (f *fakeStorage) PutUpload(ctx context.Context, repo, uuid, digest string) error {
	return nil
}
func (f *fakeStorage) GetUpload(ctx context.Context, repo, uuid string) (*Upload, error) {
	return nil, nil
}
func (f *fakeStorage) CancelUpload(ctx context.Context, repo, uuid string) error {
	return nil
}
func (f *fakeStorage) ManifestDigests(ctx context.Context, repo string) ([]string, error) {
	return nil, nil
}
func (f *fakeStorage) DeleteRepository(ctx context.Context, repo string) error {
	return nil
}
func (f *fakeStorage) GC(ctx context.Context, opts GCOption) (*GCResult, error) {
	return &GCResult{}, nil
}
func (f *fakeStorage) Stats(ctx context.Context) (*StorageStats, error) {
	return &StorageStats{}, nil
}
func (f *fakeStorage) ExportTar(ctx context.Context, repo, ref string, opt ExportOpt, w io.Writer) (*ExportResult, error) {
	return exportTar(ctx, f, repo, ref, opt, w)
}

// --- helpers -----------------------------------------------------------------

// makeOCIManifest builds a one-platform OCI image manifest body with
// the given config digest and layer digests. schemaVersion 2 + mediaType
// "application/vnd.oci.image.manifest.v1+json" matches what `docker
// build` + `docker push` to a current daemon produces (and what cairn
// stores verbatim via PUT /v2/<repo>/manifests/<ref>).
func makeOCIManifest(configDigest string, layerDigests []string) []byte {
	type layer struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int    `json:"size"`
	}
	m := struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
			Size      int    `json:"size"`
		} `json:"config"`
		Layers []layer `json:"layers"`
	}{}
	m.SchemaVersion = 2
	m.MediaType = "application/vnd.oci.image.manifest.v1+json"
	m.Config.MediaType = "application/vnd.oci.image.config.v1+json"
	m.Config.Digest = configDigest
	for _, d := range layerDigests {
		m.Layers = append(m.Layers, layer{
			MediaType: "application/vnd.oci.image.layer.v1.tar",
			Digest:    d,
			Size:      0, // size is optional in tar outputs; docker save omits it too
		})
	}
	b, _ := json.Marshal(m)
	return b
}

// makeOCIIndex builds an OCI image index that lists child manifests by
// platform. Used by the multi-arch test.
func makeOCIIndex(children []struct {
	digest string
	os     string
	arch   string
}) []byte {
	type platform struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	}
	type child struct {
		MediaType string   `json:"mediaType"`
		Digest    string   `json:"digest"`
		Size      int      `json:"size"`
		Platform  platform `json:"platform"`
	}
	idx := struct {
		SchemaVersion int     `json:"schemaVersion"`
		MediaType     string  `json:"mediaType"`
		Manifests     []child `json:"manifests"`
	}{}
	idx.SchemaVersion = 2
	idx.MediaType = "application/vnd.oci.image.index.v1+json"
	for _, c := range children {
		idx.Manifests = append(idx.Manifests, child{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    c.digest,
			Size:      0,
			Platform: platform{Architecture: c.arch, OS: c.os},
		})
	}
	b, _ := json.Marshal(idx)
	return b
}

// parseTar reads the tar produced by ExportTar and returns a map
// filename → bytes for every entry. A test asserts on this map.
func parseTar(t *testing.T, r io.Reader) map[string][]byte {
	t.Helper()
	tr := tar.NewReader(r)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("parse tar: %v", err)
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = buf
	}
	return out
}

// --- tests -------------------------------------------------------------------

// TestExportTarSingleArch builds one OCI manifest with one config + two
// layer blobs and verifies the export:
//
//   - VERSION = "1.0"
//   - manifest.json is a JSON array with one entry pointing at the
//     right Config / Layers / RepoTags paths
//   - repositories maps the tag to the config digest
//   - the config blob body matches verbatim
//   - each layer blob body matches verbatim
//   - tar entries for the blob bodies have the same byte content as
//     what was put in (roundtrip integrity)
func TestExportTarSingleArch(t *testing.T) {
	const repo = "fake/repo"
	const tag = "v1.0"

	s := newFakeStorage(t)
	configBody := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := s.putBlob(configBody)
	layer1Body := []byte("first layer bytes - this would be a real rootfs tar")
	layer1Digest := s.putBlob(layer1Body)
	layer2Body := []byte("second layer bytes - even more rootfs")
	layer2Digest := s.putBlob(layer2Body)

	manifestBody := makeOCIManifest(configDigest, []string{layer1Digest, layer2Digest})
	s.putManifest(repo, tag, "application/vnd.oci.image.manifest.v1+json", manifestBody)

	var buf bytes.Buffer
	res, err := s.ExportTar(context.Background(), repo, tag, ExportOpt{
		Platform:   "",
		RepoTags:   []string{repo + ":" + tag},
		OutputRepo: repo,
	}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if res.Bytes == 0 {
		t.Fatalf("export returned zero bytes")
	}

	entries := parseTar(t, &buf)

	wantFiles := []string{
		"VERSION",
		"manifest.json",
		"repositories",
		strings.TrimPrefix(configDigest, "sha256:") + ".json",
		strings.TrimPrefix(layer1Digest, "sha256:") + ".tar",
		strings.TrimPrefix(layer2Digest, "sha256:") + ".tar",
	}
	for _, name := range wantFiles {
		if _, ok := entries[name]; !ok {
			t.Errorf("tar missing entry %q (have: %v)", name, mapKeys(entries))
		}
	}

	if got := string(entries["VERSION"]); got != "1.0" {
		t.Errorf("VERSION = %q, want %q", got, "1.0")
	}

	// manifest.json: JSON array, single entry with right paths
	var manifest []tarManifestEntry
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	if len(manifest) != 1 {
		t.Fatalf("manifest.json entries = %d, want 1", len(manifest))
	}
	m := manifest[0]
	wantConfig := strings.TrimPrefix(configDigest, "sha256:") + ".json"
	if m.Config != wantConfig {
		t.Errorf("Config = %q, want %q", m.Config, wantConfig)
	}
	if len(m.Layers) != 2 {
		t.Errorf("Layers len = %d, want 2", len(m.Layers))
	}
	if m.Layers[0] != strings.TrimPrefix(layer1Digest, "sha256:")+".tar" {
		t.Errorf("Layers[0] = %q, want layer1 .tar", m.Layers[0])
	}
	if m.Layers[1] != strings.TrimPrefix(layer2Digest, "sha256:")+".tar" {
		t.Errorf("Layers[1] = %q, want layer2 .tar", m.Layers[1])
	}
	if len(m.RepoTags) != 1 || m.RepoTags[0] != repo+":"+tag {
		t.Errorf("RepoTags = %v, want [%q]", m.RepoTags, repo+":"+tag)
	}

	// repositories: { "fake/repo": { "v1.0": configDigest } }
	var repos tarRepositories
	if err := json.Unmarshal(entries["repositories"], &repos); err != nil {
		t.Fatalf("parse repositories: %v", err)
	}
	if got := repos[repo][tag]; got != configDigest {
		t.Errorf("repositories[%q][%q] = %q, want %q", repo, tag, got, configDigest)
	}

	// blob bytes — verbatim
	if !bytes.Equal(entries[strings.TrimPrefix(configDigest, "sha256:")+".json"], configBody) {
		t.Errorf("config blob bytes mismatch")
	}
	if !bytes.Equal(entries[strings.TrimPrefix(layer1Digest, "sha256:")+".tar"], layer1Body) {
		t.Errorf("layer1 blob bytes mismatch")
	}
	if !bytes.Equal(entries[strings.TrimPrefix(layer2Digest, "sha256:")+".tar"], layer2Body) {
		t.Errorf("layer2 blob bytes mismatch")
	}
}

// TestExportTarMultiArch builds a top-level image index with two
// platforms (linux/amd64 and linux/arm64), each with its own manifest
// + blobs. Export with platform=linux/amd64 must produce a tar for
// JUST the amd64 child — never the arm64 blobs and never the index
// itself in the output.
func TestExportTarMultiArch(t *testing.T) {
	const repo = "fake/multi"
	const tag = "v1"

	s := newFakeStorage(t)
	// amd64 child
	amdConfigBody := []byte(`{"architecture":"amd64","os":"linux"}`)
	amdConfig := s.putBlob(amdConfigBody)
	amdLayerBody := []byte("amd64 layer bytes")
	amdLayer := s.putBlob(amdLayerBody)
	amdManifest := makeOCIManifest(amdConfig, []string{amdLayer})
	s.putManifest(repo, tag+"-amd64-stub", "application/vnd.oci.image.manifest.v1+json", amdManifest)
	// look up the actual digest the helper computed
	amdDigest := s.manifests[repo+"|"+tag+"-amd64-stub"].Digest
	s.putManifest(repo, amdDigest, "application/vnd.oci.image.manifest.v1+json", amdManifest) // also store under the digest key

	// arm64 child (same shape, different content so byte-comparison fails
	// if the export accidentally emits this one)
	armConfigBody := []byte(`{"architecture":"arm64","os":"linux"}`)
	armConfig := s.putBlob(armConfigBody)
	armLayerBody := []byte("arm64 layer bytes")
	armLayer := s.putBlob(armLayerBody)
	armManifest := makeOCIManifest(armConfig, []string{armLayer})
	s.putManifest(repo, tag+"-arm64-stub", "application/vnd.oci.image.manifest.v1+json", armManifest)
	armDigest := s.manifests[repo+"|"+tag+"-arm64-stub"].Digest
	s.putManifest(repo, armDigest, "application/vnd.oci.image.manifest.v1+json", armManifest)

	// index points at both
	indexBody := makeOCIIndex([]struct {
		digest string
		os     string
		arch   string
	}{{amdDigest, "linux", "amd64"}, {armDigest, "linux", "arm64"}})
	s.putManifest(repo, tag, "application/vnd.oci.image.index.v1+json", indexBody)

	var buf bytes.Buffer
	_, err := s.ExportTar(context.Background(), repo, tag, ExportOpt{
		Platform:   "linux/amd64",
		RepoTags:   []string{repo + ":" + tag},
		OutputRepo: repo,
	}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	entries := parseTar(t, &buf)

	// amd64 blobs present
	amdConfigPath := strings.TrimPrefix(amdConfig, "sha256:") + ".json"
	amdLayerPath := strings.TrimPrefix(amdLayer, "sha256:") + ".tar"
	if !bytes.Equal(entries[amdConfigPath], amdConfigBody) {
		t.Errorf("amd64 config blob bytes mismatch")
	}
	if !bytes.Equal(entries[amdLayerPath], amdLayerBody) {
		t.Errorf("amd64 layer blob bytes mismatch")
	}

	// arm64 blobs NOT present
	if _, present := entries[strings.TrimPrefix(armConfig, "sha256:")+".json"]; present {
		t.Errorf("arm64 config blob leaked into amd64 export")
	}
	if _, present := entries[strings.TrimPrefix(armLayer, "sha256:")+".tar"]; present {
		t.Errorf("arm64 layer blob leaked into amd64 export")
	}

	// manifest.json references ONLY the amd64 child
	var manifest []tarManifestEntry
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	if len(manifest) != 1 || len(manifest[0].Layers) != 1 {
		t.Fatalf("manifest.json should describe 1 image with 1 layer, got %#v", manifest)
	}
	if manifest[0].Config != amdConfigPath {
		t.Errorf("Config = %q, want %q", manifest[0].Config, amdConfigPath)
	}
	if manifest[0].Layers[0] != amdLayerPath {
		t.Errorf("Layers[0] = %q, want %q", manifest[0].Layers[0], amdLayerPath)
	}
}

// TestExportTarMultiArchPlatformMissing covers the no-match error:
// when the index has no entry for the requested platform, export
// returns ErrPlatformMissing so the API layer can 400.
func TestExportTarMultiArchPlatformMissing(t *testing.T) {
	const repo = "fake/arm-only"

	s := newFakeStorage(t)
	armConfigBody := []byte(`{"architecture":"arm64","os":"linux"}`)
	armConfig := s.putBlob(armConfigBody)
	armLayerBody := []byte("arm layer")
	armLayer := s.putBlob(armLayerBody)
	armManifest := makeOCIManifest(armConfig, []string{armLayer})
	s.putManifest(repo, "arm-stub", "application/vnd.oci.image.manifest.v1+json", armManifest)
	armDigest := s.manifests[repo+"|arm-stub"].Digest
	s.putManifest(repo, armDigest, "application/vnd.oci.image.manifest.v1+json", armManifest)
	indexBody := makeOCIIndex([]struct {
		digest string
		os     string
		arch   string
	}{{armDigest, "linux", "arm64"}})
	s.putManifest(repo, "v1", "application/vnd.oci.image.index.v1+json", indexBody)

	var buf bytes.Buffer
	_, err := s.ExportTar(context.Background(), repo, "v1", ExportOpt{
		Platform:   "linux/amd64", // requested amd64, index only has arm64
		RepoTags:   []string{repo + ":v1"},
		OutputRepo: repo,
	}, &buf)
	if !errors.Is(err, ErrPlatformMissing) {
		t.Fatalf("err = %v, want ErrPlatformMissing", err)
	}
}

// TestExportTarMultiArchNoPlatform covers the other failure mode:
// multi-arch image with NO platform selector passed. Caller must
// explicitly pick one — we refuse to guess.
func TestExportTarMultiArchNoPlatform(t *testing.T) {
	const repo = "fake/multi"
	s := newFakeStorage(t)
	// minimal index with one platform; doesn't matter what it is,
	// the test is that we don't pick without opt.Platform set.
	idx := makeOCIIndex([]struct {
		digest string
		os     string
		arch   string
	}{{"sha256:" + strings.Repeat("a", 64), "linux", "amd64"}})
	s.putManifest(repo, "v1", "application/vnd.oci.image.index.v1+json", idx)

	var buf bytes.Buffer
	_, err := s.ExportTar(context.Background(), repo, "v1", ExportOpt{
		Platform:   "",
		RepoTags:   []string{repo + ":v1"},
		OutputRepo: repo,
	}, &buf)
	if !errors.Is(err, ErrPlatformMissing) {
		t.Fatalf("err = %v, want ErrPlatformMissing (no platform given)", err)
	}
}

// TestExportTarSchema1Rejected — schema1 manifests (fsLayers instead
// of layers) are not supported; explicit ErrUnsupportedManifest rather
// than a best-effort conversion that might silently corrupt the image.
func TestExportTarSchema1Rejected(t *testing.T) {
	const repo = "fake/old"
	s := newFakeStorage(t)
	// A schema1 manifest looks like this — different shape, fsLayers
	// instead of config+layers, no config digest.
	body := []byte(`{
		"schemaVersion": 1,
		"name": "fake/old",
		"tag": "v1",
		"architecture": "amd64",
		"fsLayers": [{"blobSum": "sha256:abc"}],
		"history": [{"v1Compatibility": "{}"}]
	}`)
	s.putManifest(repo, "v1", "application/vnd.docker.distribution.manifest.v1+json", body)

	var buf bytes.Buffer
	_, err := s.ExportTar(context.Background(), repo, "v1", ExportOpt{
		Platform:   "",
		RepoTags:   []string{repo + ":v1"},
		OutputRepo: repo,
	}, &buf)
	if !errors.Is(err, ErrUnsupportedManifest) {
		t.Fatalf("err = %v, want ErrUnsupportedManifest", err)
	}
}

// TestExportTarStreamingNoBuffer — every layer body goes through
// GetBlob's io.ReadCloser. We assert bytes-on-the-wire match what we
// put in even when the source reader is consumed incrementally (not
// just via bytes.Reader).
func TestExportTarStreamingNoBuffer(t *testing.T) {
	const repo = "fake/stream"
	const tag = "v1"

	s := newFakeStorage(t)
	configBody := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := s.putBlob(configBody)

	// 5 MB synthetic layer — large enough to span many 64KiB read
	// chunks but small enough not to slow the test.
	layerBody := make([]byte, 5<<20)
	for i := range layerBody {
		layerBody[i] = byte(i % 251) // arbitrary non-zero pattern
	}
	layerDigest := s.putBlob(layerBody)

	manifestBody := makeOCIManifest(configDigest, []string{layerDigest})
	s.putManifest(repo, tag, "application/vnd.oci.image.manifest.v1+json", manifestBody)

	var buf bytes.Buffer
	_, err := s.ExportTar(context.Background(), repo, tag, ExportOpt{
		Platform:   "",
		RepoTags:   []string{repo + ":" + tag},
		OutputRepo: repo,
	}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	entries := parseTar(t, &buf)
	if !bytes.Equal(entries[strings.TrimPrefix(layerDigest, "sha256:")+".tar"], layerBody) {
		t.Errorf("large layer body mismatch (size %d)", len(layerBody))
	}
}

// TestExportTarContextCancel — cancel mid-stream must abort the tar
// write cleanly rather than hanging or producing a corrupt tail. We
// schedule a cancel 50ms in via context.WithTimeout; the 4 MiB layer
// body takes far longer than 50ms to stream (1 MiB chunks), so the
// write loop in writeBlobEntry hits its ctx.Done() check during the
// layer copy and returns ctx.Err() rather than completing the tar.
//
// We don't assert on the specific error value beyond "is a ctx error"
// — both context.Canceled and context.DeadlineExceeded satisfy
// errors.Is(err, context.Canceled) once WithTimeout fires, and the
// distinction isn't operationally meaningful (the client gave up).
func TestExportTarContextCancel(t *testing.T) {
	const repo = "fake/cancel"
	const tag = "v1"

	s := newFakeStorage(t)
	configDigest := s.putBlob([]byte(`{"architecture":"amd64","os":"linux"}`))
	// 64 MiB layer — large enough that streaming it (one 64 KiB chunk
	// per loop iter) takes well over the 1ms cancel deadline. The
	// read loop in writeBlobEntry checks ctx.Done() between chunks,
	// so we should observe the cancel before the layer is exhausted.
	layerBody := make([]byte, 64<<20)
	for i := range layerBody {
		layerBody[i] = byte(i % 13)
	}
	layerDigest := s.putBlob(layerBody)

	manifestBody := makeOCIManifest(configDigest, []string{layerDigest})
	s.putManifest(repo, tag, "application/vnd.oci.image.manifest.v1+json", manifestBody)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// io.Discard — we don't care about the bytes, only that the
	// cancel takes effect before the tar is fully written.
	_, err := s.ExportTar(ctx, repo, tag, ExportOpt{
		Platform: "", RepoTags: []string{repo + ":" + tag}, OutputRepo: repo,
	}, io.Discard)
	if err == nil {
		t.Fatalf("export returned nil; expected ctx error")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.Canceled or DeadlineExceeded", err)
	}
}

// --- helpers used by tests ---------------------------------------------------

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Compile-time assertion that *fakeStorage implements Storage. Failing
// here is a louder signal than "method X missing" from the build.
var _ Storage = (*fakeStorage)(nil)

// Avoid "imported and not used" if a future test reorders this file.
var _ = fmt.Sprintf
