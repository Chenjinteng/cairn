// verify-export is a standalone tool that roundtrips an ExportTar
// output through docker-load-style schema checks — the same checks
// `docker load` performs internally when it parses a tar archive.
//
// This is a substitute for the real `docker load` we can't run in
// CI environments that don't have a docker daemon. It does NOT load
// into any docker cache; it just validates the tar would be loadable.
//
// What it checks (mirrors docker-load's contract):
//
//   1. The tar is parseable.
//   2. VERSION exists and equals "1.0".
//   3. manifest.json is a JSON array (single-platform tar) and each
//      entry's Config / Layers paths resolve to existing tar entries.
//   4. repositories is a JSON object mapping repo → tag → digest,
//      and every digest it names matches an actual config blob.
//   5. Each <config>.json decodes as an OCI/Docker image config and
//      has a sha256 digest equal to its own filename minus extension.
//   6. Each <layer>.tar's sha256 matches config.rootfs.diff_ids (when
//      diff_ids is present; older images omit it and we skip).
//
// Layer tar byte-streams aren't extracted here — we trust storage to
// have written them verbatim, and the storage-level roundtrip test
// (TestExportTarSingleArch etc.) already covers that. This tool only
// validates the *shape* docker load cares about.
//
// Run after pulling an exported tar:
//
//   verify-export path/to/exported.tar
//
// Exits 0 on success, 1 on any contract violation (with a clear error).
package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type manifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags,omitempty"`
	Layers   []string `json:"Layers"`
}

type imageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		// OCI uses config.digest
		Digest string `json:"digest,omitempty"`
		// Docker schema2 uses config.image; we read whichever is present.
	} `json:"config,omitempty"`
	RootFS struct {
		DiffIDs []string `json:"diff_ids,omitempty"`
	} `json:"rootfs,omitempty"`
	// legacy docker v1 compat — ignored for now.
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: verify-export <tar>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "verify-export FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("verify-export OK")
}

func run(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)

	entries := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		entries[hdr.Name] = buf
	}

	// 1+2: VERSION
	if v := string(entries["VERSION"]); v != "1.0" {
		return fmt.Errorf("VERSION = %q, want \"1.0\"", v)
	}

	// 3: manifest.json
	var manifest []manifestEntry
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		return fmt.Errorf("parse manifest.json: %w", err)
	}
	if len(manifest) == 0 {
		return fmt.Errorf("manifest.json has no entries")
	}
	for i, m := range manifest {
		if m.Config == "" {
			return fmt.Errorf("manifest[%d].Config is empty", i)
		}
		if _, ok := entries[m.Config]; !ok {
			return fmt.Errorf("manifest[%d].Config = %q not in tar", i, m.Config)
		}
		for j, l := range m.Layers {
			if _, ok := entries[l]; !ok {
				return fmt.Errorf("manifest[%d].Layers[%d] = %q not in tar", i, j, l)
			}
		}
	}

	// 4: repositories — every digest it names must resolve.
	type repoMap map[string]map[string]string
	var repos repoMap
	if err := json.Unmarshal(entries["repositories"], &repos); err != nil {
		return fmt.Errorf("parse repositories: %w", err)
	}
	allDigests := map[string]string{} // digest → config blob path
	for repoName, tags := range repos {
		for tag, digest := range tags {
			// The "digest" in repositories is a sha256:<hex> string;
			// the matching config entry is at "<hex-without-prefix>.json".
			if !strings.HasPrefix(digest, "sha256:") {
				return fmt.Errorf("repositories[%q][%q] digest %q: not sha256-prefixed", repoName, tag, digest)
			}
			hexPart := strings.TrimPrefix(digest, "sha256:")
			cfgPath := hexPart + ".json"
			if _, ok := entries[cfgPath]; !ok {
				return fmt.Errorf("repositories[%q][%q] → %q: no %q in tar", repoName, tag, digest, cfgPath)
			}
			allDigests[digest] = cfgPath
		}
	}

	// 5+6: each config blob is well-formed and matches its filename.
	for digest, cfgPath := range allDigests {
		cfgBytes := entries[cfgPath]
		var cfg imageConfig
		if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
			return fmt.Errorf("parse %s: %w", cfgPath, err)
		}
		// sha256 of the config body must equal the digest key from repositories.
		sum := sha256.Sum256(cfgBytes)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if actual != digest {
			return fmt.Errorf("config %s: sha256 mismatch (filename %s, body %s)", cfgPath, digest, actual)
		}
		// OCI config.digest is informational; we don't gate on it.
		// rootfs.diff_ids: if present, each must match a layer tar's
		// sha256. diff_ids are the uncompressed tar's sha256; since
		// cairn stores layer tarballs verbatim, they line up exactly.
		for i, d := range cfg.RootFS.DiffIDs {
			if !strings.HasPrefix(d, "sha256:") {
				return fmt.Errorf("%s rootfs.diff_ids[%d] = %q: not sha256-prefixed", cfgPath, i, d)
			}
			hexPart := strings.TrimPrefix(d, "sha256:")
			layerPath := hexPart + ".tar"
			layerBytes, ok := entries[layerPath]
			if !ok {
				return fmt.Errorf("%s rootfs.diff_ids[%d] → %s: not in tar", cfgPath, i, layerPath)
			}
			sum := sha256.Sum256(layerBytes)
			if "sha256:"+hex.EncodeToString(sum[:]) != d {
				return fmt.Errorf("%s rootfs.diff_ids[%d] = %s: layer sha256 mismatch", cfgPath, i, d)
			}
		}
	}

	return nil
}
