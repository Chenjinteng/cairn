// Package proxies implements the plain-JSON proxy store used by the pull
// feature to reach upstream registries.
//
// Plain JSON is intentional: proxy URLs are not secret on the same level
// as credentials (an attacker who can read the file likely already knows
// which egress points are available), and AES-GCM overhead would be wasted.
// The trade-off mirrors registry-manager/server/proxies.mjs.
//
// File format: one JSON document, an array of Proxy entries.
package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Proxy is one outbound proxy configuration.
//
// URL is the proxy endpoint (http://... or socks5://...). If Username /
// Password are set, the proxy is used with basic auth.
//
// v0.5.9: the last four fields are populated by Store.Probe and surfaced
// in the proxy-management page so operators can spot dead entries without
// waiting for a pull to fail. LastProbeStatus is one of:
//
//	"ok"      last probe succeeded (proxy reachable)
//	"failed"  last probe failed (timeout / connection refused / TCP error)
//	"unknown" never probed yet (process just started, or entry was just added)
//
// v0.5.15: LastProbeLatencyMs holds the TCP connect round-trip in
// milliseconds when the probe succeeded; it stays 0 (and is therefore
// omitted from JSON) for failures.
type Proxy struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	URL                string    `json:"url"`
	Username           string    `json:"username,omitempty"`
	Password           string    `json:"password,omitempty"`
	Note               string    `json:"note,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	LastProbeAt        time.Time `json:"lastProbeAt,omitempty"`
	LastProbeStatus    string    `json:"lastProbeStatus,omitempty"`
	LastProbeError     string    `json:"lastProbeError,omitempty"`
	LastProbeLatencyMs float64   `json:"lastProbeLatencyMs,omitempty"`
}

// Store is the on-disk proxy list.
type Store struct {
	path string

	mu    sync.RWMutex
	items map[string]Proxy
}

// Open loads (or creates) the proxy store at path.
func Open(path string) (*Store, error) {
	s := &Store{
		path:  path,
		items: make(map[string]Proxy),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// List returns shallow copies of all proxies.
func (s *Store) List() []Proxy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Proxy, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p)
	}
	return out
}

// Get returns a shallow copy of one proxy by id.
func (s *Store) Get(id string) (Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.items[id]
	if !ok {
		return Proxy{}, ErrNotFound
	}
	return p, nil
}

// Put inserts or replaces a proxy by ID. Updates UpdatedAt. Sets
// CreatedAt on first insert; preserves it on update.
func (s *Store) Put(p Proxy) error {
	if p.ID == "" {
		return errors.New("proxies: ID required")
	}
	if p.Name == "" {
		return errors.New("proxies: Name required")
	}
	if p.URL == "" {
		return errors.New("proxies: URL required")
	}

	now := time.Now().UTC()
	s.mu.Lock()
	existing, hadExisting := s.items[p.ID]
	if hadExisting {
		p.CreatedAt = existing.CreatedAt
	} else {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	s.items[p.ID] = p
	// Snapshot under lock, then release before disk I/O. See persistLocked
	// for the rationale (the previous "caller MUST hold s.mu" contract was
	// what caused the v0.5.9 production deadlock — Put leaked the WLock
	// because it never called Unlock; the leaked state lived on the mutex
	// and wedged every subsequent reader/writer until process restart).
	snap := make([]Proxy, 0, len(s.items))
	for _, x := range s.items {
		snap = append(snap, x)
	}
	s.mu.Unlock()

	return persistToDisk(s.path, snap)
}

// Delete removes a proxy by id. Returns ErrNotFound if the id
// isn't present.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	if _, ok := s.items[id]; !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.items, id)
	snap := make([]Proxy, 0, len(s.items))
	for _, x := range s.items {
		snap = append(snap, x)
	}
	s.mu.Unlock()

	return persistToDisk(s.path, snap)
}

// ErrNotFound is returned when an ID doesn't exist.
var ErrNotFound = errors.New("proxy not found")

// persistToDisk writes the given proxies snapshot to path atomically
// (write-temp + rename). No lock required; callers snapshot under s.mu
// themselves, release the lock, then call this. Keeping disk I/O outside
// any lock means a slow filesystem can't wedge concurrent readers.
func persistToDisk(path string, items []Proxy) error {
	body, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) load() error {
	body, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("proxies: read %s: %w", s.path, err)
	}
	var arr []Proxy
	if err := json.Unmarshal(body, &arr); err != nil {
		return fmt.Errorf("proxies: parse: %w", err)
	}
	m := make(map[string]Proxy, len(arr))
	for _, p := range arr {
		m[p.ID] = p
	}
	s.mu.Lock()
	s.items = m
	s.mu.Unlock()
	return nil
}

// Probe checks TCP reachability of the proxy endpoint and measures the
// connect latency. It stops at "the TCP handshake completed" - no HTTP
// request is sent, and nothing is sent through the proxy.
//
// Rationale (v0.5.15): the previous implementation followed the dial with
// a HEAD request aimed at the proxy URL itself. That works for a
// transparent forward proxy (clash / mihomo), but not for a bare
// CONNECT-only listener such as 3proxy: the socket accepts and then waits
// for a CONNECT line, so a perfectly healthy proxy was reported as
// "failed: context deadline exceeded" while the create-time test (which
// really does go through the proxy to /v2/) passed. Operators only want
// "is this ip:port reachable, and how fast", so we now report exactly
// that. Whether the proxy can really forward traffic is still verified by
// the "test connection" action, which dials through it.
//
// 5-second budget. Records LastProbeAt / LastProbeStatus /
// LastProbeLatencyMs / LastProbeError back into the entry, in memory and
// on disk. Returns the error when the probe failed, nil otherwise.
func (s *Store) Probe(id string) error {
	p, err := s.Get(id)
	if err != nil {
		return err
	}
	pu, perr := url.Parse(p.URL)
	if perr != nil {
		s.recordProbe(id, time.Now().UTC(), "failed", 0, "parse url: "+perr.Error())
		return perr
	}
	scheme := pu.Scheme
	if scheme != "http" && scheme != "https" && scheme != "socks5" {
		s.recordProbe(id, time.Now().UTC(), "failed", 0, "unsupported scheme: "+scheme)
		return fmt.Errorf("proxies: unsupported scheme %q", scheme)
	}
	addr := dialAddr(pu)
	if addr == "" {
		s.recordProbe(id, time.Now().UTC(), "failed", 0, "missing host")
		return errors.New("proxies: missing host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	var d net.Dialer
	conn, derr := d.DialContext(ctx, "tcp", addr)
	latency := msSince(start)
	if derr != nil {
		s.recordProbe(id, time.Now().UTC(), "failed", 0, "dial "+addr+": "+derr.Error())
		return derr
	}
	conn.Close()
	s.recordProbe(id, time.Now().UTC(), "ok", latency, "")
	return nil
}

// probeTimeout bounds a single TCP reachability check.
const probeTimeout = 5 * time.Second

// dialAddr turns a parsed proxy URL into the host:port we should dial. A
// missing port is filled in from the scheme's conventional default so an
// entry written as "http://proxy.example.com" still probes correctly. Returns
// "" when the URL carries no host at all.
func dialAddr(u *url.URL) string {
	if u.Host == "" {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	port := ""
	switch u.Scheme {
	case "http":
		port = "80"
	case "https":
		port = "443"
	case "socks5":
		port = "1080"
	}
	if port == "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// msSince returns the elapsed time as float milliseconds. Sub-millisecond
// resolution is kept on purpose: a proxy on the same LAN answers in
// microseconds and would otherwise always render as "0 ms".
func msSince(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

// recordProbe writes the probe outcome into the entry and persists
// proxies.json. It owns its own locking window (does NOT require the
// caller to hold a lock). Disk I/O runs while no lock is held, so a slow
// filesystem can't wedge concurrent readers.
func (s *Store) recordProbe(id string, at time.Time, status string, latencyMs float64, errMsg string) {
	s.mu.Lock()
	p, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	p.LastProbeAt = at
	p.LastProbeStatus = status
	p.LastProbeError = errMsg
	p.LastProbeLatencyMs = latencyMs
	s.items[id] = p
	// Copy out the snapshot while still holding the lock.
	out := make([]Proxy, 0, len(s.items))
	for _, x := range s.items {
		out = append(out, x)
	}
	s.mu.Unlock()
	// Disk I/O outside the lock (same pattern as Put / Delete above).
	_ = persistToDisk(s.path, out)
}

// ProbeAll probes every entry in parallel and waits for all to finish.
// Errors are logged on each entry; the overall call returns the first
// non-nil error from any single probe (operators typically only care
// about "did at least one fail", which is also visible in the persisted
// status field).
func (s *Store) ProbeAll(ctx context.Context) error {
	proxies := s.List()
	if len(proxies) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(proxies))
	for _, p := range proxies {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := s.Probe(id); err != nil {
				errCh <- fmt.Errorf("probe %s: %w", id, err)
			}
		}(p.ID)
	}
	wg.Wait()
	close(errCh)
	// Return the first error (if any). The rest are persisted in entries.
	select {
	case e := <-errCh:
		return e
	default:
		return nil
	}
}

// StartProbeLoop launches a background goroutine that re-probes every
// `interval`. Returns immediately; the goroutine exits when ctx is
// cancelled. interval <= 0 disables the loop (single probe at boot
// still runs from Boot if ProbeBoot is true).
func (s *Store) StartProbeLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = s.ProbeAll(ctx)
			}
		}
	}()
}
