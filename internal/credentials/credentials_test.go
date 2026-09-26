package credentials

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testKey is a throwaway 64-char key. Open only requires >=32 chars; it is
// never used outside these tests.
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	v, err := Open(filepath.Join(t.TempDir(), "credentials.json"), testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return v
}

func sample(id string) Credential {
	return Credential{ID: id, Name: id, URL: "https://registry-1.docker.io", Username: "u", Password: "p"}
}

// TestPutReleasesLock is the regression test for the v0.5.x deadlock: Put
// must release v.mu before returning. A leaked write lock shows up as a hung
// Get/List (the lock lives on the mutex, so the goroutine that leaked it does
// not need to still be around), so we assert completion within a timeout.
func TestPutReleasesLock(t *testing.T) {
	v := newTestVault(t)
	if err := v.Put(sample("a")); err != nil {
		t.Fatalf("Put a: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = v.List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("List blocked after Put — write-lock leaked (the v0.5.x deadlock)")
	}
}

// TestPutThenGetReleasesLock pins the exact production call chain:
// CreateCredential does Put immediately followed by Get, which is where the
// original bug wedged ("create credential" never returned at all).
func TestPutThenGetReleasesLock(t *testing.T) {
	v := newTestVault(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := v.Put(sample("a")); err != nil {
			t.Errorf("Put: %v", err)
			return
		}
		if _, err := v.Get("a"); err != nil {
			t.Errorf("Get after Put: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Put->Get wedged — write-lock leaked")
	}
}

// TestDeleteReleasesLock: same contract for Delete.
func TestDeleteReleasesLock(t *testing.T) {
	v := newTestVault(t)
	if err := v.Put(sample("a")); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := v.Delete("a"); err != nil {
		t.Fatalf("Delete a: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = v.List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("List blocked after Delete — write-lock leaked")
	}

	// Delete must not leave a listener that reads-only callers trip over.
	if _, err := v.Get("a"); err != ErrNotFound {
		t.Fatalf("Get deleted id: want ErrNotFound, got %v", err)
	}
}

// TestConcurrentStress hammers the vault and would deadlock within
// milliseconds under -race if any write path leaked a lock.
func TestConcurrentStress(t *testing.T) {
	// Not t.TempDir: its cleanup races the final persistToDisk .tmp rename.
	dir, err := os.MkdirTemp("", "credentials-stress-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	v, err := Open(filepath.Join(dir, "credentials.json"), testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		if err := v.Put(sample(id)); err != nil {
			t.Fatalf("seed Put %s: %v", id, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		const workers = 8
		const iters = 200
		for w := 0; w < workers; w++ {
			w := w
			go func() {
				for i := 0; i < iters; i++ {
					id := string(rune('a' + (i % 5)))
					switch (w + i) % 4 {
					case 0:
						_ = v.Put(sample(id))
					case 1:
						_, _ = v.Get(id)
					case 2:
						_ = v.List()
					case 3:
						_ = v.Delete(id)
					}
				}
			}()
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("concurrent stress deadlock — some operation leaked a lock")
	}
}

// TestOnDiskFormatRoundTrip guards backward compatibility: the file written
// by persistToDisk must reload through load() with the same contents and the
// same header fields, so vaults written by earlier releases keep decrypting.
func TestOnDiskFormatRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")

	v1, err := Open(path, testKey)
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	if err := v1.Put(sample("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vault file: %v", err)
	}
	// Envelope shape must stay {v, nonce, ct} — old binaries must still parse it.
	for _, want := range []string{`"v":`, `"nonce":`, `"ct":`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("vault envelope missing %s (format drift breaks old vaults)", want)
		}
	}
	// Nothing may leak in plaintext.
	if strings.Contains(string(raw), "registry-1.docker.io") || strings.Contains(string(raw), `"username"`) {
		t.Fatalf("vault file leaks plaintext credential fields")
	}
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		t.Fatalf("vault file missing or empty: %v", err)
	}
	// File mode must stay 0600 (it holds secrets).
	if st, err := os.Stat(path); err == nil && st.Mode().Perm() != 0o600 {
		t.Fatalf("vault file mode = %#o, want 0600", st.Mode().Perm())
	}

	v2, err := Open(path, testKey)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := v2.Get("a")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Name != "a" || got.Password != "p" || got.URL != "https://registry-1.docker.io" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps lost across round-trip: %+v", got)
	}

	// Wrong key must fail loudly rather than silently returning an empty vault.
	if _, err := Open(path, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatalf("Open with wrong key succeeded — ciphertext is not authenticated")
	}
}

// TestPutPreservesCreatedAt: update must keep the original CreatedAt and only
// move UpdatedAt (the snapshot refactor must not disturb this bookkeeping).
func TestPutPreservesCreatedAt(t *testing.T) {
	v := newTestVault(t)
	if err := v.Put(sample("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	first, _ := v.Get("a")

	time.Sleep(5 * time.Millisecond)
	upd := sample("a")
	upd.Name = "renamed"
	if err := v.Put(upd); err != nil {
		t.Fatalf("Put update: %v", err)
	}
	second, _ := v.Get("a")
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed on update: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt did not advance: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}
}

// TestPutValidation: the cheap guards must fire before the lock is taken, so a
// rejected Put can never leave the vault locked.
func TestPutValidation(t *testing.T) {
	v := newTestVault(t)
	for _, tc := range []struct {
		name string
		c    Credential
	}{
		{"no id", Credential{Name: "n", URL: "u"}},
		{"no name", Credential{ID: "i", URL: "u"}},
		{"no url", Credential{ID: "i", Name: "n"}},
	} {
		if err := v.Put(tc.c); err == nil {
			t.Fatalf("%s: want error, got nil", tc.name)
		}
	}
	if err := v.Put(sample("ok")); err != nil {
		t.Fatalf("vault unusable after rejected Puts: %v", err)
	}
}

// TestOpenRejectsShortKey: a short key must be refused at boot, not silently
// used to derive a weak AES key.
func TestOpenRejectsShortKey(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "credentials.json"), "tooshort"); err == nil {
		t.Fatalf("Open accepted a key shorter than 32 chars")
	}
}
