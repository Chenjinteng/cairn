package api

// InventoryCache memoizes one GET /api/inventory result for a short window
// so a busy management page (or anything that hammers /api/inventory
// after a write) doesn't re-walk the storage tree every call.
//
// v0.6.32: introduced as part of the perf round (see CHANGELOG). The
// previous build rebuilt the full inventory from the filesystem on every
// GET /api/inventory; on the UAT reference registry that took ~250ms
// even with a modest tree, and got noticeably worse as repos grew.
//
// Wire-up is in server.go: one InventoryCache instance is shared by the
// Handlers.GetInventory read path and any write path that wants to drop
// the snapshot. V2 protocol handlers (PUT /v2/<repo>/manifests/<ref>,
// DELETE /v2/<repo>/manifests/<digest>, the upload-commit + upload-cancel
// paths) and the /api/* delete handlers all call Invalidate after a
// successful state change so the next read rebuilds immediately. Reads
// inside the TTL window return the cached snapshot without touching disk.
//
// Cache is in-memory only and process-local: there is no cross-process
// invalidation. With cairn's "single binary, single registry" model
// there is only one process so this is fine; if the constraint ever
// loosens, switch to a per-storage-version cache key or a pub/sub bus.
//
// TTL is 30 seconds by default — long enough that consecutive tab
// switches in the management UI all hit the cache, short enough that a
// missed Invalidate path (e.g. a brand new write site we forgot) doesn't
// silently serve stale data forever. Reads return the snapshot as soon
// as Get returns, so write-driven stale windows are bounded by TTL in
// the worst case.

import (
	"sync"
	"time"
)

// inventoryCacheTTL is the default time-to-live for a cached inventory.
// 30s mirrors the previous (pre-F1) user-visible latency profile: with
// caching, the slowest page first-load is the first read after a write
// (cache miss → ~250ms build), and subsequent reads inside the window
// return immediately. Beyond 30s we'd risk the UI showing data older
// than what the operator's mental model expects from "I just clicked
// refresh".
const inventoryCacheTTL = 30 * time.Second

// InventoryCache is the thread-safe memoization layer for /api/inventory.
//
// One instance is created in server.go and shared between the read site
// (Handlers.GetInventory) and every write site (DeleteRepository /
// DeleteManifestByDigest / DeleteTag / RunGC / the V2 protocol write
// paths in internal/registryd).
type InventoryCache struct {
	mu     sync.RWMutex
	snap   *Inventory
	expire time.Time
}

// Get returns the cached snapshot if it's still fresh. The boolean is
// true on a cache hit, false on a miss. On a miss the caller must run
// buildInventory and call Set with the result before serving it —
// otherwise the next request will rebuild again. The cache instance
// itself never invokes buildInventory, keeping this package free of any
// dependency on the storage package's concrete types.
func (c *InventoryCache) Get() (snap *Inventory, hit bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snap == nil || time.Now().After(c.expire) {
		return nil, false
	}
	return c.snap, true
}

// Set stores a freshly built snapshot under the standard TTL. The host
// field is overwritten on read by GetOrBuild (the caller knows the
// current request's host from the X-Forwarded-Host / Host header path)
// so we don't need to thread it through here.
func (c *InventoryCache) Set(snap *Inventory) {
	if c == nil || snap == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap = snap
	c.expire = time.Now().Add(inventoryCacheTTL)
}

// Invalidate drops the cached snapshot. The next Get returns a miss
// and the caller rebuilds. Safe to call from any goroutine, including
// the V2 protocol handlers in internal/registryd (which call it through
// the Handler.OnWrite hook wired in server.go).
//
// Always-invalidate on a successful write: a missed invalidation here
// is the kind of bug that surfaces as "I deleted the repo but it still
// shows up in the UI for 30s". The TTL bounds the damage but a missed
// invalidation in a write handler is still a real defect, not a known
// limitation.
func (c *InventoryCache) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap = nil
	c.expire = time.Time{}
}

// GetOrBuild is a convenience wrapper used by Handlers.GetInventory: it
// tries the cache first, and on a miss calls rebuild and stores the
// result. host is the externally visible registry address — it's the
// only field the cache can't self-derive, so the caller supplies it
// each request. rebuild must return a freshly built *Inventory; any
// error aborts and the cache is left in its prior state (typically an
// already-invalid one).
func (c *InventoryCache) GetOrBuild(host string, rebuild func() (*Inventory, error)) (*Inventory, error) {
	if snap, hit := c.Get(); hit {
		// The cached snapshot's Host may differ from the current
		// request's host (the Host header is request-local, the
		// snapshot is shared). Rewrite it before serving so the
		// response shape matches what the caller would have seen
		// on a fresh build.
		snap.Host = host
		return snap, nil
	}
	snap, err := rebuild()
	if err != nil {
		return nil, err
	}
	c.Set(snap)
	snap.Host = host
	return snap, nil
}
