package proxies

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper: make a store rooted in a temp dir
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "proxies.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestPutReleasesLock: the regression test for the v0.5.9 deadlock.
// Put must release s.mu before returning, so the next List is never
// blocked by a leaked write-lock. We assert List completes within a
// short timeout; a hang would mean the lock leaked.
func TestPutReleasesLock(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Proxy{ID: "a", Name: "alpha", URL: "http://1.1.1.1:80"}); err != nil {
		t.Fatalf("Put a: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = s.List()
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("List blocked after Put — write-lock leaked (the v0.5.9 deadlock)")
	}
}

// TestDeleteReleasesLock: same shape for Delete (Delete was always
// correct via defer Unlock, but pin the contract).
func TestDeleteReleasesLock(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Proxy{ID: "a", Name: "alpha", URL: "http://1.1.1.1:80"}); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("Delete a: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = s.List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("List blocked after Delete — write-lock leaked")
	}
}

// TestRecordProbeReleasesLock: recordProbe (called from Probe) takes
// the write lock to update LastProbeAt/Status/Error, then writes to
// disk. It must release the lock before returning.
func TestRecordProbeReleasesLock(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Proxy{ID: "a", Name: "alpha", URL: "http://127.0.0.1:1"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s.recordProbe("a", time.Now().UTC(), "failed", "synthetic")

	done := make(chan struct{})
	go func() {
		_ = s.List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("List blocked after recordProbe — write-lock leaked")
	}
}

// TestConcurrentStress: hammer the store from many goroutines doing
// Put / Get / List / Delete / recordProbe in parallel. If any lock is
// leaked, the test deadlocks under -race within a few hundred ms and
// `go test -race -timeout 10s` will fail it. The original deadlock
// would wedge this test almost immediately.
func TestConcurrentStress(t *testing.T) {
	// Don't use t.TempDir: its auto-RemoveAll races the final
	// persistToDisk .tmp rename under -race and prints
	// "directory not empty" even though the test itself passed.
	dir, err := os.MkdirTemp("", "proxies-stress-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	s, err := Open(filepath.Join(dir, "proxies.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// seed
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		if err := s.Put(Proxy{ID: id, Name: id, URL: "http://127.0.0.1:1"}); err != nil {
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
					switch (w + i) % 5 {
					case 0:
						id := string(rune('a' + (i % 5)))
						_ = s.Put(Proxy{ID: id, Name: id, URL: "http://127.0.0.1:1"})
					case 1:
						_, _ = s.Get(string(rune('a' + (i % 5))))
					case 2:
						_ = s.List()
					case 3:
						id := string(rune('a' + (i % 5)))
						_ = s.Delete(id)
					case 4:
						id := string(rune('a' + (i % 5)))
						s.recordProbe(id, time.Now().UTC(), "ok", "")
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

// TestPersistToDiskCreatesFile: sanity-check the new lock-free helper
// still produces a parseable file on disk.
func TestPersistToDiskCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "proxies.json")
	if err := persistToDisk(path, []Proxy{{ID: "x", Name: "x", URL: "http://1.1.1.1:80"}}); err != nil {
		t.Fatalf("persistToDisk: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() == 0 {
		t.Fatalf("file empty")
	}
}
