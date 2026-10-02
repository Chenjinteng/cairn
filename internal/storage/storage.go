// Package storage defines the on-disk blob/manifest store used by the
// registry server (internal/registryd) and the admin UI (internal/api).
//
// Both cairn's data plane (/v2/*) and its control plane (/api/*) share
// one Storage instance backed by one filesystem tree, so an admin "delete"
// is the same operation as a /v2 DELETE — no two sources of truth.
//
// Layout (CNCF Distribution-compatible enough for tools like skopeo to
// recognise; simplified for v0.3):
//
//	<root>/repos/<repo>/tags/<tag>           <- file containing "sha256:abc..."
//	<root>/repos/<repo>/manifests/sha256/<digest>/data  <- raw manifest JSON
//	<root>/blobs/sha256/<aa>/<bb>/<digest>/data          <- raw blob bytes
//	<root>/uploads/<uuid>/data                            <- in-progress upload body
//
// The two-level prefix under blobs/ is the Distribution convention that
// keeps any single directory's child count bounded; FS operations on
// millions of blobs stay O(1)-ish on directory traversal.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is the canonical "this thing doesn't exist" error.
// Handlers map it to HTTP 404; tests assert on errors.Is(err, ErrNotFound).
var ErrNotFound = errors.New("storage: not found")

// ErrInvalidDigest is returned when a digest parameter doesn't match
// "sha256:<64hex>".
var ErrInvalidDigest = errors.New("storage: invalid digest")

// Manifest is the on-disk representation of a stored manifest.
// Digests are always the registry-computed sha256 from the body bytes;
// references (tags) map to digests via the tags/ tree.
type Manifest struct {
	Repo      string    `json:"-"`      // path component, not stored
	Digest    string    `json:"digest"` // "sha256:..."
	MediaType string    `json:"mediaType"`
	Body      []byte    `json:"-"` // raw manifest JSON
	CreatedAt time.Time `json:"createdAt"`
}

// BlobMeta is the metadata for a stored blob. Blobs are content-addressed,
// so Repo is irrelevant (a blob is shared across repos). Size is from
// stat(); we don't store it separately to avoid drift.
type BlobMeta struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Upload tracks one in-progress blob upload session. Sessions are not
// persisted across restarts (registry-manager / CNCF Distribution don't
// either); orphan sessions get GC'd by the next sweep.
type Upload struct {
	UUID      string    `json:"uuid"`
	Repo      string    `json:"repo"`
	StartedAt time.Time `json:"startedAt"`
	Size      int64     `json:"size"` // bytes written so far
}

// Storage is the abstraction handlers depend on. One impl per backend;
// the production backend is Filesystem.
type Storage interface {
	// Catalog
	Repositories(ctx context.Context) ([]string, error)
	Tags(ctx context.Context, repo string) ([]string, error)
	// TagDigest returns the digest a tag currently points at, or ErrNotFound.
	TagDigest(ctx context.Context, repo, tag string) (string, error)

	// TagsForDigest returns every tag in repo that currently points at digest.
	// Used by the v0.5.0 "delete by digest" endpoint so callers can see which
	// tags the delete will detach. ErrNotFound is NOT returned when no tag
	// points at digest -- the result is simply []string{}.
	TagsForDigest(ctx context.Context, repo, digest string) ([]string, error)

	// Manifest
	GetManifest(ctx context.Context, repo, ref string) (*Manifest, error)
	PutManifest(ctx context.Context, repo, ref string, mediaType string, body []byte) (digest string, err error)
	// DeleteManifest removes the manifest at digest under repo and returns the
	// tags that previously pointed at it (may be empty). ErrNotFound if the
	// manifest doesn't exist; ErrInvalidDigest for malformed digest values.
	DeleteManifest(ctx context.Context, repo, digest string) ([]string, error)

	// Blob
	BlobExists(ctx context.Context, repo, digest string) (bool, error)
	GetBlob(ctx context.Context, repo, digest string) (io.ReadCloser, int64, error)
	StatBlob(ctx context.Context, digest string) (int64, error)

	// Upload (chunked)
	StartUpload(ctx context.Context, repo string) (string, error)
	// PatchUpload appends chunk to an in-progress upload. offset=-1 means
	// append at current end (the registry spec allows Content-Range; we
	// tolerate either).
	PatchUpload(ctx context.Context, repo, uuid string, offset int64, body io.Reader) (newSize int64, err error)
	// PutUpload finalises the upload, verifies the body digest, and moves
	// the bytes into blob storage. After this the digest is globally
	// reachable as a blob (regardless of repo).
	PutUpload(ctx context.Context, repo, uuid, digest string) error
	// GetUpload returns the current state (size only — body is opaque).
	GetUpload(ctx context.Context, repo, uuid string) (*Upload, error)
	// CancelUpload removes the upload session without committing.
	CancelUpload(ctx context.Context, repo, uuid string) error

	// Admin actions. These are the same operations the /v2 endpoints perform,
	// exposed so the UI's delete button and a `docker push` can never disagree
	// about what "delete" means.

	// ManifestDigests lists every manifest stored under repo, including ones
	// no tag points at any more (a tag move leaves the old manifest dangling).
	ManifestDigests(ctx context.Context, repo string) ([]string, error)
	// DeleteRepository drops a whole repository: manifests, tags, and any
	// in-flight upload sessions. Blobs are shared and survive; run GC to
	// reclaim their disk.
	DeleteRepository(ctx context.Context, repo string) error
	// GC reclaims blobs that no manifest references and abandons upload
	// sessions older than 24h. Deleting a manifest never frees disk on its
	// own — same semantics as `registry garbage-collect`.
	//
	// opts.CleanEmptyRepos additionally drops the directory tree of any
	// repository whose tags/ is empty AND that has no upload session newer
	// than the 24h cutoff (a concurrent push in flight would otherwise be
	// nuked mid-flight). Off by default — operators must opt in because
	// whole-repo deletion is harder to recover than a stray blob.
	GC(ctx context.Context, opts GCOption) (*GCResult, error)

	// Stats aggregates counts across repos / tags / manifests / blobs.
	// Best-effort: may be slow on very large trees; cached for a few seconds
	// upstream if needed.
	Stats(ctx context.Context) (*StorageStats, error)
}

// GCOption tunes one GC sweep. The zero value reproduces the v0.5.18
// behaviour exactly; new fields are always opt-in.
type GCOption struct {
	// CleanEmptyRepos (v0.5.20): when true, after the standard blob +
	// orphan-upload passes, drop the entire repos/<repo>/ + uploads/<repo>/
	// tree for every repository whose tags/ is empty AND that has no upload
	// session younger than 24h. The freed bytes from those deleted
	// directories are reported in GCResult.EmptyRepoFreedBytes so the UI
	// can credit them to the user.
	CleanEmptyRepos bool
	// DryRun (v0.6.32): when true, every pass still walks the tree and
	// counts what *would* be removed, but performs no filesystem writes.
	// Pass 1 returns ScannedBlobs / OrphanBlobs / OrphanBytes; Pass 2 returns
	// AbandonedUploads; Pass 3 returns WouldRemoveEmptyRepos /
	// WouldEmptyRepoFreedBytes (the byte count it would have credited).
	// Pass 4 is skipped because it depends on Pass 3 actually deleting.
	//
	// The standard RemovedBlobs / FreedBytes / RemovedEmptyRepos /
	// EmptyRepoFreedBytes fields stay at zero (omitempty) in dry-run mode
	// so the v0.5.18 wire shape for non-dry-run callers is unchanged.
	DryRun bool
}

// GCResult reports what one garbage-collection sweep reclaimed.
//
// v0.5.20: RemovedEmptyRepos / EmptyRepoFreedBytes are populated only when
// the caller asked for CleanEmptyRepos; otherwise they stay at the zero
// value (nil / 0) and the JSON shape stays identical to v0.5.18.
//
// v0.6.32: when GCOption.DryRun is set, the Would* / Scanned* / Orphan*
// fields carry the "what would have happened" counts and the standard
// Removed* fields stay at zero. All new fields are omitempty so the
// non-dry-run JSON shape is byte-identical to v0.5.18.
type GCResult struct {
	RemovedBlobs        int      `json:"removedBlobs"`
	FreedBytes          int64    `json:"freedBytes"`
	RemovedEmptyRepos   []string `json:"removedEmptyRepos,omitempty"`
	EmptyRepoFreedBytes int64    `json:"emptyRepoFreedBytes,omitempty"`

	// DryRun-only fields. Populated only when GCOption.DryRun=true.
	// ScannedBlobs        — every blob data file the sweep walked past.
	// OrphanBlobs         — ScannedBlobs minus blobs referenced by any
	//                        manifest body (would be RemovedBlobs in a
	//                        non-dry-run sweep with the same input).
	// OrphanBytes         — total bytes the orphans would free.
	// AbandonedUploads    — upload sessions older than 24h that Pass 2
	//                        would rm-rf.
	// WouldRemoveEmptyRepos — repos that Pass 3 would delete (empty tags/
	//                        + no in-flight upload).
	// WouldEmptyRepoFreedBytes — bytes Pass 3 would free from those.
	ScannedBlobs            int      `json:"scannedBlobs,omitempty"`
	OrphanBlobs             int      `json:"orphanBlobs,omitempty"`
	OrphanBytes             int64    `json:"orphanBytes,omitempty"`
	AbandonedUploads        int      `json:"abandonedUploads,omitempty"`
	WouldRemoveEmptyRepos   []string `json:"wouldRemoveEmptyRepos,omitempty"`
	WouldEmptyRepoFreedBytes int64   `json:"wouldEmptyRepoFreedBytes,omitempty"`
}

// StorageStats is a summary used by the admin UI's "清单概览" panel.
type StorageStats struct {
	RepoCount     int   `json:"repoCount"`
	TagCount      int   `json:"tagCount"`
	ManifestCount int   `json:"manifestCount"`
	BlobCount     int   `json:"blobCount"`
	TotalSize     int64 `json:"totalSize"`
}
