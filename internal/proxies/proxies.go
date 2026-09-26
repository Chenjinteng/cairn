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
	"net/http"
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
// v0.5.9: the last three fields are populated by Store.Probe and surfaced
// in the proxy-management page so operators can spot dead entries without
// waiting for a pull to fail. LastProbeStatus is one of:
//
//	"ok"      last probe succeeded (proxy reachable)
//	"failed"  last probe failed (timeout / connection refused / TCP error)
//	"unknown" never probed yet (process just started, or entry was just added)
type Proxy struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	Username        string    `json:"username,omitempty"`
	Password        string    `json:"password,omitempty"`
	Note            string    `json:"note,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	LastProbeAt     time.Time `json:"lastProbeAt,omitempty"`
	LastProbeStatus string    `json:"lastProbeStatus,omitempty"`
	LastProbeError  string    `json:"lastProbeError,omitempty"`
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

// Probe runs a quick TCP+HTTP probe against the proxy endpoint itself
// (NOT through the proxy to a target). It tries a HEAD on the proxy URL
// and treats any 2xx/3xx/4xx as "reachable" — only connection refused,
// timeout, or 5xx-with-no-response count as "failed". Records the
// outcome (LastProbeAt / LastProbeStatus / LastProbeError) back into the
// entry in memory and on disk.
//
// 5-second total budget per probe. Returns the error if the probe
// failed; nil if it succeeded (even when the proxy returned a non-2xx —
// a misconfigured proxy that accepts the TCP connection is still better
// than one that's not listening).
func (s *Store) Probe(id string) error {
	p, err := s.Get(id)
	if err != nil {
		return err
	}
	pu, perr := url.Parse(p.URL)
	if perr != nil {
		s.recordProbe(id, time.Now().UTC(), "failed", "parse url: "+perr.Error())
		return perr
	}
	// http:// or https:// → dial + small request; socks5:// → TCP dial only.
	scheme := pu.Scheme
	if scheme != "http" && scheme != "https" && scheme != "socks5" {
		s.recordProbe(id, time.Now().UTC(), "failed", "unsupported scheme: "+scheme)
		return fmt.Errorf("proxies: unsupported scheme %q", scheme)
	}
	addr := pu.Host
	if addr == "" {
		s.recordProbe(id, time.Now().UTC(), "failed", "missing host")
		return errors.New("proxies: missing host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Step 1: TCP reachability. If the proxy endpoint isn't listening /
	// firewalled / DNS-broken, fail fast.
	var d net.Dialer
	conn, derr := d.DialContext(ctx, "tcp", addr)
	if derr != nil {
		s.recordProbe(id, time.Now().UTC(), "failed", "dial "+addr+": "+derr.Error())
		return derr
	}
	conn.Close()
	if scheme == "socks5" {
		// TCP reachability is the meaningful signal for SOCKS — the actual
		// handshake happens per-request in the client, so we don't try it
		// here (would require writing the SOCKS5 greeting bytes).
		s.recordProbe(id, time.Now().UTC(), "ok", "")
		return nil
	}
	// Step 2: HTTP-level sanity. Try a short HEAD with ResponseHeaderTimeout
	// so a stuck / half-open TCP accept doesn't hang us forever. We treat
	// any non-5xx as "proxy is alive enough" — 4xx means the proxy rejected
	// our no-target request, but the wire is up.
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(pu),
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       3 * time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, p.URL, nil)
	resp, herr := client.Do(req)
	if herr != nil {
		// Couldn't complete the HTTP exchange (CONNECT failed, TLS broken,
		// header timeout). Dial was OK but the proxy isn't usable end-to-end.
		s.recordProbe(id, time.Now().UTC(), "failed", "http: "+herr.Error())
		return herr
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		err := fmt.Errorf("proxy returned %d", resp.StatusCode)
		s.recordProbe(id, time.Now().UTC(), "failed", err.Error())
		return err
	}
	s.recordProbe(id, time.Now().UTC(), "ok", "")
	return nil
}

// recordProbe writes the probe outcome into the entry and persists
// proxies.json. It owns its own locking window (does NOT require the
// caller to hold a lock). Disk I/O runs while no lock is held, so a slow
// filesystem can't wedge concurrent readers.
func (s *Store) recordProbe(id string, at time.Time, status, errMsg string) {
	s.mu.Lock()
	p, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	p.LastProbeAt = at
	p.LastProbeStatus = status
	p.LastProbeError = errMsg
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
