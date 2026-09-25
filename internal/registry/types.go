// Package registry wraps a CNCF Distribution (Docker Registry V2) endpoint.
//
// Behavior is modeled on registry-manager/server/registry-client.mjs, with
// every V2 protocol quirk documented there ported here as code + comments:
//
//   - GET /v2/<name>/manifests/<ref> MUST send an Accept header covering the
//     four manifest media types; otherwise the registry returns 404.
//   - /v2/_catalog paginates via ?n=&last=; we keep paginating while the
//     response length equals the page size.
//   - /v2/<name>/tags/list pagination is version-dependent and we intentionally
//     omit ?n= so v2.8.3 and v3.x behave identically.
//   - DELETE /v2/<name>/manifests/<ref> requires the digest, not a tag.
//   - _catalog may contain repositories with tags=null; we keep them in the
//     list (registry-manager's readRepository is tolerant on purpose).
package registry

import (
	"encoding/json"
	"time"
)

// Manifest describes a single tag's manifest, normalized across Docker v2 /
// OCI media types. Raw holds the original JSON so unknown fields round-trip.
type Manifest struct {
	Digest       string          `json:"digest"`
	MediaType    string          `json:"mediaType"`
	Tag          string          `json:"tag,omitempty"`
	Created      *time.Time      `json:"created,omitempty"`
	Architecture string          `json:"architecture,omitempty"`
	OS           string          `json:"os,omitempty"`
	Layers       []Layer         `json:"layers"`
	Size         int64           `json:"size"` // sum of layer sizes; NOT disk usage (blobs are shared across tags)
	Raw          json.RawMessage `json:"raw,omitempty"`
}

// Layer is one entry from a manifest's layers[] or image config's rootfs.diff_ids.
type Layer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

// Repository is one entry from /v2/_catalog plus its tag list.
type Repository struct {
	Name      string    `json:"name"`
	TagCount  int       `json:"tagCount"`
	Tags      []TagInfo `json:"tags"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TagInfo is the lightweight per-tag summary surfaced in the inventory list.
// Full Manifest is fetched on demand via /api/repositories/{name}/tags/{tag}.
type TagInfo struct {
	Name         string     `json:"name"`
	Digest       string     `json:"digest,omitempty"`
	Size         int64      `json:"size,omitempty"`
	Created      *time.Time `json:"created,omitempty"`
	Architecture string     `json:"architecture,omitempty"`
}

// Inventory is the full payload returned by GET /api/inventory.
// Totals reflect what's currently in the in-memory cache; failedTags lists
// tags whose manifest fetch errored (so the UI can show "12 of 77 read failed").
type Inventory struct {
	Repositories []Repository `json:"repositories"`
	Totals       Totals       `json:"totals"`
	RefreshAt    time.Time    `json:"refreshAt"`
	FailedTags   []FailedTag  `json:"failedTags,omitempty"`
}

type Totals struct {
	RepoCount  int   `json:"repoCount"`
	TagCount   int   `json:"tagCount"`
	LayerCount int   `json:"layerCount"`
	TotalSize  int64 `json:"totalSize"`
}

type FailedTag struct {
	Repo  string `json:"repo"`
	Tag   string `json:"tag"`
	Error string `json:"error"`
}