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
	Repo      string    `json:"-"`        // path component, not stored
	Digest    string    `json:"digest"`   // "sha256:..."
	MediaType string    `json:"mediaType"`
	Body      []byte    `json:"-"`        // raw manifest JSON
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
	Size      int64     `json:"size"`     // bytes written so far
}

// Storage is the abstraction handlers depend on. One impl per backend;
// the production backend is Filesystem.
type Storage interface {
	// Catalog
	Repositories(ctx context.Context) ([]string, error)
	Tags(ctx context.Context, repo string) ([]string, error)
	// TagDigest returns the digest a tag currently points at, or ErrNotFound.
	TagDigest(ctx context.Context, repo, tag string) (string, error)

	// Manifest
	GetManifest(ctx context.Context, repo, ref string) (*Manifest, error)
	PutManifest(ctx context.Context, repo, ref string, mediaType string, body []byte) (digest string, err error)
	DeleteManifest(ctx context.Context, repo, digest string) error

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

	// Stats aggregates counts across repos / tags / manifests / blobs.
	// Best-effort: may be slow on very large trees; cached for a few seconds
	// upstream if needed.
	Stats(ctx context.Context) (*StorageStats, error)
}

// StorageStats is a summary used by the admin UI's "清单概览" panel.
type StorageStats struct {
	RepoCount   int   `json:"repoCount"`
	TagCount    int   `json:"tagCount"`
	ManifestCount int  `json:"manifestCount"`
	BlobCount   int   `json:"blobCount"`
	TotalSize   int64 `json:"totalSize"`
}