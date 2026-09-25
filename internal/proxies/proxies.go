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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Proxy is one outbound proxy configuration.
//
// URL is the proxy endpoint (http://... or socks5://...). If Username /
// Password are set, the proxy is used with basic auth.
type Proxy struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Username  string    `json:"username,omitempty"`
	Password  string    `json:"password,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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

// Put inserts or replaces a proxy by ID. Updates UpdatedAt.
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

	s.mu.Lock()
	now := time.Now().UTC()
	existing, hadExisting := s.items[p.ID]
	if hadExisting {
		p.CreatedAt = existing.CreatedAt
	} else {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	s.items[p.ID] = p
	return s.persistLocked()
}

// Delete removes a proxy by id.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return ErrNotFound
	}
	delete(s.items, id)
	return s.persistLocked()
}

// ErrNotFound is returned when an ID doesn't exist.
var ErrNotFound = errors.New("proxy not found")

func (s *Store) persistLocked() error {
	out := make([]Proxy, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p)
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
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