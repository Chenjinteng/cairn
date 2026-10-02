package storage

// docker save / load tar format reference + cairn-side implementation.
//
// Output layout (compatible with `docker load` since 18.03 and
// `skopeo copy docker-archive:...`):
//
//	manifest.json     — JSON array; one entry per image (we always emit
//	                    one; multi-platform image indexes collapse to a
//	                    single platform chosen via opt.Platform)
//	repositories      — JSON object: { repoName: { tagName: configDigest } }
//	VERSION           — "1.0" (mandatory for older `docker load`; harmless for new)
//	<configDigest>.json — image config blob contents (verbatim)
//	<layerDigest>.tar   — layer blob contents (verbatim; layers are
//	                      already rootfs tarballs, NOT re-tarred)
//
// Why short paths (just "<digest>.json" / "<digest>.tar") and not the
// `<repo>/<layerid>/...` legacy layout: the short-path layout is what
// `docker save` itself emits on a current Docker (>= 18.03) and is the
// one `docker load` accepts. The long-form `<image-id>/VERSION` layout
// would require us to know the legacy image-id concept, which Docker
// itself deprecated — the short form is the right target.
//
// No re-checksumming, no rewriting: every blob is written verbatim from
// disk via Storage.GetBlob (which already verified the sha256 matches
// the digest on Put). A corrupted blob would fail at storage layer
// well before ExportTar gets to copy it.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

// manifestDoc is the subset of an OCI / Docker image manifest we need
// for export — schema2 and OCI share the same shape so one struct
// covers both. image indexes use indexDoc (below) and have manifests[]
// instead of config + layers[].
type manifestDoc struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Config        struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	} `json:"layers"`
}

// indexDoc is an OCI image index / Docker manifest list. cairn stores
// these as the manifest body when a registry push lands on a multi-arch
// tag (e.g. `library/postgres:15` is a list of per-arch manifests).
//
// A multi-platform tar can technically list multiple platforms in the
// same manifest.json; we DON'T do that — cairn's product surface is
// single-platform export (chip defaults to amd64), so the export
// endpoint resolves the platform, then exports the chosen platform's
// image manifest exactly like a single-arch tag would.
type indexDoc struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Manifests     []struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
		Platform  struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant,omitempty"`
		} `json:"platform"`
	} `json:"manifests"`
}

// tarManifestEntry is one element of the top-level manifest.json array
// (the v2 / OCI tar layout).
type tarManifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags,omitempty"`
	Layers   []string `json:"Layers"`
}

// tarRepositories is the auxiliary `repositories` file mapping repo
// + tag → config blob digest. Optional for some docker-load paths but
// every modern docker / skopeo reads it, so we always emit it.
type tarRepositories map[string]map[string]string

// exportTar reads one image manifest (after resolving any image index)
// and writes the tar layout into w.
//
// split out from the Filesystem methods so the tar layout is easy to
// unit-test against a fake Storage (see export_test.go).
func exportTar(ctx context.Context, s Storage, repo, ref string, opt ExportOpt, w io.Writer) (*ExportResult, error) {
	if opt.OutputRepo == "" {
		opt.OutputRepo = repo
	}

	m, err := s.GetManifest(ctx, repo, ref)
	if err != nil {
		return nil, err
	}

	// Resolve image indexes: pick the platform, recurse into that
	// platform's image manifest, and continue as if the caller had
	// passed the resolved digest directly. This means everything
	// below this block can assume it's working with a leaf manifest.
	body := m.Body
	if isImageIndex(body) {
		if opt.Platform == "" {
			return nil, ErrPlatformMissing
		}
		childDigest, err := pickPlatform(body, opt.Platform)
		if err != nil {
			return nil, err
		}
		// GetManifest accepts a digest ref directly (looksLikeDigest
		// short-circuits to the file lookup), so we can recurse by
		// passing the child digest as ref.
		m, err = s.GetManifest(ctx, repo, childDigest)
		if err != nil {
			return nil, err
		}
		body = m.Body
	}

	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("export: parse manifest: %w", err)
	}
	// schema1 has a fundamentally different shape (fsLayers + history
	// + a signed JWS payload) and no config digest — emulating it
	// would be 100+ lines of complexity for a format docker itself
	// deprecated in 2017. Reject with a clear error.
	if doc.SchemaVersion < 2 || doc.Config.Digest == "" {
		return nil, ErrUnsupportedManifest
	}
	if doc.Config.Digest == "" {
		return nil, fmt.Errorf("export: manifest has no config descriptor")
	}

	// Lay out the per-image tar entries. Each entry's Config / Layers
	// paths are just the digest (with the standard "sha256:<hex>.json"
	// / ".tar" suffix); the actual blob bytes are written as
	// sibling entries with matching names.
	entry := tarManifestEntry{
		Config:   blobFilename(doc.Config.Digest, ".json"),
		RepoTags: opt.RepoTags,
		Layers:   make([]string, 0, len(doc.Layers)),
	}
	for _, l := range doc.Layers {
		entry.Layers = append(entry.Layers, blobFilename(l.Digest, ".tar"))
	}

	// We accumulate everything before opening the tar writer so any
	// read error fails fast and the caller sees a clean 500 instead of
	// a half-written tar. Bytes flow once we start writing.
	tw := tar.NewWriter(w)
	n := int64(0)

	// VERSION — required by some older `docker load` paths. Bytes-only
	// entry, no fs metadata beyond a fixed filename.
	if err := writeStringEntry(tw, "VERSION", "1.0", &n); err != nil {
		return nil, err
	}

	// manifest.json — JSON array. We always emit one image per tar
	// (multi-platform collapse is the caller's job — opt.Platform
	// picks one); single-element array is the standard docker save
	// output for a non-multi-arch pull.
	mb, err := json.Marshal([]tarManifestEntry{entry})
	if err != nil {
		return nil, fmt.Errorf("export: marshal manifest.json: %w", err)
	}
	if err := writeStringEntry(tw, "manifest.json", string(mb), &n); err != nil {
		return nil, err
	}

	// repositories — map[repo]map[tag]configDigest. Only emit entries
	// for tags the caller said belong to this image; multi-tag images
	// are rare in cairn (a tag points at exactly one digest) so this
	// is normally one repo → one tag → one config.
	if len(opt.RepoTags) > 0 {
		repos := tarRepositories{}
		tags := repos[opt.OutputRepo]
		if tags == nil {
			tags = map[string]string{}
			repos[opt.OutputRepo] = tags
		}
		for _, tagRef := range opt.RepoTags {
			// RepoTags entries are "<repo>:<tag>" — split out the tag
			// half so the file is the canonical repo→tag→digest shape.
			// (A cairn pull typically has one tag, but the file shape
			// supports N if it ever came up.)
			tagName := tagRef
			if i := strings.LastIndex(tagRef, ":"); i >= 0 {
				tagName = tagRef[i+1:]
			}
			tags[tagName] = doc.Config.Digest
		}
		rb, err := json.Marshal(repos)
		if err != nil {
			return nil, fmt.Errorf("export: marshal repositories: %w", err)
		}
		if err := writeStringEntry(tw, "repositories", string(rb), &n); err != nil {
			return nil, err
		}
	}

	// Config blob — verbatim from storage. GetBlob streams, so even
	// large configs (rare, but a few KB) don't materialise as a Go
	// string. config.MediaType on the source manifest is irrelevant
	// here — what goes into the tar entry is the blob body.
	if err := writeBlobEntry(ctx, s, tw, repo, doc.Config.Digest, entry.Config, &n); err != nil {
		return nil, err
	}

	// Layer blobs — same pattern, streamed one at a time. Layer order
	// in the manifest matters (docker load rebuilds the image in this
	// order), so we trust the parsed Layers[] order and don't sort.
	for i, l := range doc.Layers {
		if err := writeBlobEntry(ctx, s, tw, repo, l.Digest, entry.Layers[i], &n); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("export: close tar: %w", err)
	}
	return &ExportResult{Bytes: n}, nil
}

// writeStringEntry writes a tar entry whose body fits in memory. Used
// for the tiny JSON / VERSION entries — config + layers are big enough
// to warrant the streaming path below.
func writeStringEntry(tw *tar.Writer, name, body string, n *int64) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("export: write header %s: %w", name, err)
	}
	w, err := tw.Write([]byte(body))
	*n += int64(w)
	if err != nil {
		return fmt.Errorf("export: write body %s: %w", name, err)
	}
	return nil
}

// writeBlobEntry copies a single blob from storage into a tar entry.
// The reader from GetBlob is consumed entirely (or until ctx cancels);
// any read error becomes the export error and the tar is left for the
// caller to discard (the failed Close above makes it non-loadable).
//
// n is bumped with every Write so the returned ExportResult is a true
// byte count of what made it onto the wire (not "what we wanted to
// write").
func writeBlobEntry(ctx context.Context, s Storage, tw *tar.Writer, repo, digest, name string, n *int64) error {
	rc, size, err := s.GetBlob(ctx, repo, digest)
	if err != nil {
		return fmt.Errorf("export: get blob %s: %w", digest, err)
	}
	defer rc.Close()

	hdr := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: size,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("export: write header %s: %w", name, err)
	}
	// Copy with ctx-cancellation check every 64 KiB. CopyBuffer would
	// be marginally faster, but the simple loop keeps the error
	// propagation obvious and the 64 KiB granularity is well under
	// typical layer chunk sizes (5+ MiB) so the cancel latency is
	// already bounded by network round-trip on a real disconnect.
	buf := make([]byte, 64<<10)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		nr, er := rc.Read(buf)
		if nr > 0 {
			nw, ew := tw.Write(buf[:nr])
			*n += int64(nw)
			if ew != nil {
				return fmt.Errorf("export: write blob %s: %w", digest, ew)
			}
		}
		if er == io.EOF {
			return nil
		}
		if er != nil {
			return fmt.Errorf("export: read blob %s: %w", digest, er)
		}
	}
}

// isImageIndex reports whether body looks like an OCI image index /
// Docker manifest list — those have a manifests[] array instead of
// config + layers. We distinguish from a schema1 image manifest by
// presence of the manifests key; schema1 has fsLayers instead.
func isImageIndex(body []byte) bool {
	var probe struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Manifests) > 0
}

// pickPlatform returns the digest of the index entry whose OS/arch
// matches want ("linux/amd64"). Variant is optional (matches both
// "arm/v8" with and without a variant); an empty Variant in the
// manifest entry is also a positive match for "linux/arm64".
//
// If no entry matches, returns ErrPlatformMissing so the API layer can
// surface a clear 400 to the user (instead of guessing what they meant).
func pickPlatform(body []byte, want string) (string, error) {
	var idx indexDoc
	if err := json.Unmarshal(body, &idx); err != nil {
		return "", fmt.Errorf("export: parse index: %w", err)
	}
	for _, m := range idx.Manifests {
		got := m.Platform.OS + "/" + m.Platform.Architecture
		if m.Platform.Variant != "" {
			got += "/" + m.Platform.Variant
		}
		if got == want {
			if !looksLikeDigest(m.Digest) {
				return "", fmt.Errorf("export: platform %s has malformed digest %q", want, m.Digest)
			}
			return m.Digest, nil
		}
	}
	return "", ErrPlatformMissing
}

// blobFilename formats a blob digest as "<digest-without-prefix>.json"
// (or ".tar" for layers). Matches what we put in manifest.json's Config
// / Layers fields so `docker load` resolves the path correctly.
//
// Path is also anchored at the tar root (no leading slash) per the
// ustar convention.
func blobFilename(digest, suffix string) string {
	return path.Clean(strings.TrimPrefix(digest, "sha256:") + suffix)
}
