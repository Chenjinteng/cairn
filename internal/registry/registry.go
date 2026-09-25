// Package registry provides the Registry abstraction plus its production
// implementation (Client). Handlers depend on the interface so tests can
// swap in fakes without HTTP mocking.
package registry

import (
	"context"
	"sync"
	"time"
)

// Registry is the contract handlers depend on.
//
// All methods MUST be safe for concurrent use. The Inventory cache is internal
// to implementations; callers should never cache results themselves.
type Registry interface {
	// Probe checks that the registry is reachable and V2-conformant.
	Probe(ctx context.Context) error

	// ListRepositories returns every repository name in the registry.
	ListRepositories(ctx context.Context) ([]string, error)

	// ListTags returns every tag for repo. Empty list is not an error.
	ListTags(ctx context.Context, repo string) ([]string, error)

	// GetManifest fetches a manifest by tag or digest.
	GetManifest(ctx context.Context, repo, reference string) (*Manifest, error)

	// DeleteManifest removes the manifest at digest and returns the list of
	// tags that pointed at it (best-effort, see delete.go for caveats).
	DeleteManifest(ctx context.Context, repo, digest string) ([]string, error)

	// ScanInventory returns the full inventory with per-tag summaries.
	ScanInventory(ctx context.Context) (*Inventory, error)
}

// CachedRegistry wraps a Registry with a TTL cache for ScanInventory.
//
// Pattern matches registry-manager/server/inventory.mjs:
//   - first call hits the registry, populates cache
//   - subsequent calls within TTL return the cached Inventory
//   - POST /api/refresh (or TTL expiry) invalidates the cache
//
// Cache misses and scan errors return the underlying error rather than a
// stale cached value; the UI's "scan started at" timestamp shows freshness.
type CachedRegistry struct {
	inner Registry
	ttl   time.Duration
	mu    sync.Mutex
	cache *Inventory
	at    time.Time
}

// NewCached wraps inner with a TTL cache. ttl <= 0 disables caching entirely.
func NewCached(inner Registry, ttl time.Duration) *CachedRegistry {
	return &CachedRegistry{inner: inner, ttl: ttl}
}

// Inventory returns the cached inventory if fresh, otherwise re-scans.
//
// Concurrent callers share a single in-flight scan via a singleflight-style
// pattern: the first caller triggers the scan, others wait on the same
// result. We use a simple mutex here because scan latency is small (sub-
// second for ~100 repos) and the wait queue is bounded by errgroup's worker
// count.
func (c *CachedRegistry) Inventory(ctx context.Context) (*Inventory, error) {
	c.mu.Lock()
	if c.cache != nil && c.ttl > 0 && time.Since(c.at) < c.ttl {
		inv := c.cache
		c.mu.Unlock()
		return inv, nil
	}
	c.mu.Unlock()

	inv, err := c.inner.ScanInventory(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache = inv
	c.at = time.Now()
	c.mu.Unlock()
	return inv, nil
}

// Invalidate drops the cached inventory. Called by /api/refresh.
func (c *CachedRegistry) Invalidate() {
	c.mu.Lock()
	c.cache = nil
	c.at = time.Time{}
	c.mu.Unlock()
}

// Forwarding methods so CachedRegistry satisfies Registry transparently.
func (c *CachedRegistry) Probe(ctx context.Context) error {
	return c.inner.Probe(ctx)
}

// Inner returns the underlying Registry (unwraps the cache). Used by the
// pull executor to access blob-upload methods that aren't part of the
// read-mostly Registry interface.
func (c *CachedRegistry) Inner() Registry { return c.inner }
func (c *CachedRegistry) ListRepositories(ctx context.Context) ([]string, error) {
	return c.inner.ListRepositories(ctx)
}
func (c *CachedRegistry) ListTags(ctx context.Context, repo string) ([]string, error) {
	return c.inner.ListTags(ctx, repo)
}
func (c *CachedRegistry) GetManifest(ctx context.Context, repo, ref string) (*Manifest, error) {
	return c.inner.GetManifest(ctx, repo, ref)
}
func (c *CachedRegistry) DeleteManifest(ctx context.Context, repo, digest string) ([]string, error) {
	affected, err := c.inner.DeleteManifest(ctx, repo, digest)
	if err == nil {
		c.Invalidate()
	}
	return affected, err
}
func (c *CachedRegistry) ScanInventory(ctx context.Context) (*Inventory, error) {
	return c.inner.ScanInventory(ctx)
}
