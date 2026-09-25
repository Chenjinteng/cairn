// Package credentials implements the encrypted vault for upstream registry
// credentials (basic auth pairs).
//
// Storage format (whole file is one JSON document, AES-256-GCM encrypted):
//
//	{
//	  "version": 1,
//	  "salt":    "<base64>",
//	  "nonce":   "<base64>",
//	  "ciphertext": "<base64>"
//	}
//
// Key derivation: REGISTRY_CREDENTIAL_KEY (raw string) is hashed with
// SHA-256 to produce a 32-byte AES-256 key. SHA-256 is acceptable here
// because the input is a high-entropy random key (recommended 32+ chars),
// not a user-chosen password — no need for Argon2/scrypt.
//
// Why one whole-file blob, not per-record encryption:
//   - registry-manager's design (AGENTS.md §4.1) explicitly rejects putting
//     credentials in SQLite: "from 'whole file unreadable' to 'metadata
//     all exposed'". Same logic applies to per-row encryption — the table
//     schema (name, registry_url, username) would leak structure even if
//     values were encrypted.
//
// Concurrency: safe for concurrent use. File updates use an atomic
// write-rename pattern; reads are lock-free after the initial load.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Credential is one upstream-registry basic-auth entry.
//
// URL is the registry endpoint we authenticate against (e.g.
// "https://registry-1.docker.io"). Username / Password are the basic-auth
// pair. We never expose Password in API responses (handlers must filter it).
type Credential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Username  string    `json:"username"`
	Password  string    `json:"password,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Vault is the encrypted credential store.
type Vault struct {
	path    string
	key     []byte // 32-byte AES-256 key derived from cfg.Key

	mu    sync.RWMutex
	items map[string]Credential // id → Credential
}

// Open loads (or creates) the vault at path using key as the AES key source.
// Key must be at least 32 chars; if shorter we refuse (registry-manager does
// the same check at config-load time).
func Open(path, key string) (*Vault, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("credentials: key must be >=32 chars (got %d)", len(key))
	}
	sum := sha256.Sum256([]byte(key))
	v := &Vault{
		path:  path,
		key:   sum[:],
		items: make(map[string]Credential),
	}
	if err := v.load(); err != nil {
		return nil, err
	}
	return v, nil
}

// List returns shallow copies of all credentials (Passwords included —
// handlers MUST strip them before responding).
func (v *Vault) List() []Credential {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]Credential, 0, len(v.items))
	for _, c := range v.items {
		out = append(out, c)
	}
	return out
}

// Get returns a shallow copy of one credential by id, or ErrNotFound.
func (v *Vault) Get(id string) (Credential, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	c, ok := v.items[id]
	if !ok {
		return Credential{}, ErrNotFound
	}
	return c, nil
}

// Put inserts or replaces a credential by ID. Updates UpdatedAt. Persists
// atomically; returns the first persistence error if any.
func (v *Vault) Put(c Credential) error {
	if c.ID == "" {
		return errors.New("credentials: ID required")
	}
	if c.Name == "" {
		return errors.New("credentials: Name required")
	}
	if c.URL == "" {
		return errors.New("credentials: URL required")
	}

	v.mu.Lock()
	now := time.Now().UTC()
	existing, hadExisting := v.items[c.ID]
	if hadExisting {
		c.CreatedAt = existing.CreatedAt
	} else {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	v.items[c.ID] = c
	return v.persistLocked()
}

// Delete removes a credential by id. ErrNotFound if it didn't exist.
func (v *Vault) Delete(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.items[id]; !ok {
		return ErrNotFound
	}
	delete(v.items, id)
	return v.persistLocked()
}

// ErrNotFound is returned when an ID doesn't exist.
var ErrNotFound = errors.New("credential not found")

// persistLocked writes the encrypted file. Caller holds v.mu (write lock).
func (v *Vault) persistLocked() error {
	plaintext, err := json.MarshalIndent(v.items, "", "  ")
	if err != nil {
		return fmt.Errorf("credentials: marshal: %w", err)
	}
	nonce := make([]byte, 12) // GCM standard
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("credentials: nonce: %w", err)
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return fmt.Errorf("credentials: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("credentials: gcm: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	doc := struct {
		Version    int    `json:"v"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ct"`
	}{
		Version:    1,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	// Atomic write: tmp file in same dir, then rename. Avoids leaving a
	// half-written credentials.json if the process is killed mid-write.
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, v.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// load reads and decrypts the vault file. Caller does NOT hold v.mu.
func (v *Vault) load() error {
	body, err := os.ReadFile(v.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh vault; ok
		}
		return fmt.Errorf("credentials: read %s: %w", v.path, err)
	}
	var doc struct {
		Version    int    `json:"v"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ct"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("credentials: parse envelope: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(doc.Nonce)
	if err != nil {
		return fmt.Errorf("credentials: nonce b64: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(doc.Ciphertext)
	if err != nil {
		return fmt.Errorf("credentials: ct b64: %w", err)
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return fmt.Errorf("credentials: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return fmt.Errorf("credentials: decrypt (wrong key or corrupted file): %w", err)
	}
	var items map[string]Credential
	if err := json.Unmarshal(plaintext, &items); err != nil {
		return fmt.Errorf("credentials: parse plaintext: %w", err)
	}
	v.mu.Lock()
	v.items = items
	v.mu.Unlock()
	return nil
}