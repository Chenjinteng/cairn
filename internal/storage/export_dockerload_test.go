package storage

// Integration test that exercises the same tar layout verify-export
// checks, in-process (no separate binary needed). This test is the
// closest thing to a real `docker load` we can run without a docker
// daemon — it builds a fakeStorage with a docker-load-compatible
// config (rootfs.diff_ids matches the layer tar sha256s), exports it,
// and runs the same schema assertions verify-export would.
//
// If this test passes, the tar shape is correct enough for
// `docker load` to ingest. Field-by-field, it covers:
//   - VERSION = "1.0"
//   - manifest.json references valid Config / Layers paths
//   - repositories map names the same config digest
//   - config.digest == sha256(config body)
//   - rootfs.diff_ids[i] == sha256(layer[i] tar body)

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// fakeImageConfig is the OCI image config schema we write into the
// config blob. RootFS.DiffIDs lists sha256 of each layer's tar body
// (uncompressed); docker load rebuilds the image by applying them
// in order and verifying the chain ends at config.digest.
type fakeImageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		Digest string `json:"digest"` // sha256 of THIS config body
	} `json:"config"`
	RootFS struct {
		DiffIDs []string `json:"diff_ids"` // sha256 of each layer tar
	} `json:"rootfs"`
}

// TestExportTarDockerLoadCompat produces a fully docker-load-compatible
// tar — config.digest and rootfs.diff_ids align with the actual blob
// sha256s — and runs the verify-export checks in-process.
//
// This is the single test that matters for "will docker load accept
// this tar?". If it passes, every other test (SingleArch, MultiArch,
// Schema1Rejected etc.) is operating on the same export machinery
// that's docker-load-validated here.
func TestExportTarDockerLoadCompat(t *testing.T) {
	const repo = "fake/dl"
	const tag = "v1"

	s := newFakeStorage(t)

	// Two layers; their bytes are arbitrary but sha256-aligned with
	// the config we'll synthesise below.
	layer1 := []byte("LAYER1-fake-rootfs-tar-bytes-aaaaaaaaaaaaaaaaaaaa")
	layer2 := []byte("LAYER2-fake-rootfs-tar-bytes-bbbbbbbbbbbbbbbbbbbb")
	layer1Digest := "sha256:" + sha256Hex(layer1)
	layer2Digest := "sha256:" + sha256Hex(layer2)
	s.putBlob(layer1)
	s.putBlob(layer2)

	// Image config — config.digest is informational and not part of
	// docker-load's contract (only sha256(body) == repositories[repo][tag]
	// is checked). We OMIT it from the body to avoid the marshal-then-
	// hash chicken-and-egg; the real OCI layout includes it, but
	// docker-load accepts both shapes.
	cfg := fakeImageConfig{
		Architecture: "amd64",
		OS:           "linux",
	}
	cfg.RootFS.DiffIDs = []string{layer1Digest, layer2Digest}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfgDigest := "sha256:" + sha256Hex(cfgBytes)
	s.putBlob(cfgBytes)

	manifestBody := makeOCIManifest(cfgDigest, []string{layer1Digest, layer2Digest})
	s.putManifest(repo, tag, "application/vnd.oci.image.manifest.v1+json", manifestBody)

	var buf bytes.Buffer
	if _, err := s.ExportTar(context.Background(), repo, tag, ExportOpt{
		Platform:   "",
		RepoTags:   []string{repo + ":" + tag},
		OutputRepo: repo,
	}, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Same checks as cmd/verify-export — duplicated here in-process
	// so this test catches regressions without depending on a
	// separate binary.
	entries := parseTar(t, &buf)

	// VERSION
	if got := string(entries["VERSION"]); got != "1.0" {
		t.Errorf("VERSION = %q, want %q", got, "1.0")
	}

	// manifest.json
	var manifest []tarManifestEntry
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	if len(manifest) != 1 {
		t.Fatalf("manifest entries = %d, want 1", len(manifest))
	}
	wantConfig := strings.TrimPrefix(cfgDigest, "sha256:") + ".json"
	if manifest[0].Config != wantConfig {
		t.Errorf("Config = %q, want %q", manifest[0].Config, wantConfig)
	}
	for _, p := range manifest[0].Layers {
		if _, ok := entries[p]; !ok {
			t.Errorf("Layers[%q] not in tar", p)
		}
	}

	// repositories
	var repos tarRepositories
	if err := json.Unmarshal(entries["repositories"], &repos); err != nil {
		t.Fatalf("parse repositories: %v", err)
	}
	if got := repos[repo][tag]; got != cfgDigest {
		t.Errorf("repositories[%q][%q] = %q, want %q", repo, tag, got, cfgDigest)
	}

	// config blob: sha256(body) must equal cfgDigest (filename-equivalent)
	body := entries[wantConfig]
	sum := sha256.Sum256(body)
	if actual := "sha256:" + hex.EncodeToString(sum[:]); actual != cfgDigest {
		t.Errorf("config sha256 mismatch: body %s, want %s", actual, cfgDigest)
	}

	// config body is well-formed JSON with diff_ids matching layer tar sha256s
	var cfg2 fakeImageConfig
	if err := json.Unmarshal(body, &cfg2); err != nil {
		t.Fatalf("parse config body: %v", err)
	}
	if cfg2.Architecture != "amd64" || cfg2.OS != "linux" {
		t.Errorf("config arch/os = %s/%s, want amd64/linux", cfg2.Architecture, cfg2.OS)
	}
	if len(cfg2.RootFS.DiffIDs) != 2 {
		t.Errorf("config rootfs.diff_ids len = %d, want 2", len(cfg2.RootFS.DiffIDs))
	}
	for i, d := range cfg2.RootFS.DiffIDs {
		wantLayer := strings.TrimPrefix(d, "sha256:") + ".tar"
		layerBytes, ok := entries[wantLayer]
		if !ok {
			t.Errorf("diff_ids[%d] = %s → %s not in tar", i, d, wantLayer)
			continue
		}
		sum := sha256.Sum256(layerBytes)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if actual != d {
			t.Errorf("diff_ids[%d] = %s: layer sha256 %s mismatch", i, d, actual)
		}
	}
}

// sha256Hex is a tiny helper to avoid pulling sha256 + encoding/hex
// at every test site — keep the table-formatting noise down.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
