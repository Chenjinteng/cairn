package proxies

import (
	"context"
	"net"
	"net/url"
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
	s.recordProbe("a", time.Now().UTC(), "failed", 0, "synthetic")

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
						s.recordProbe(id, time.Now().UTC(), "ok", 0.42, "")
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

// TestProbeSucceedsForPlainTCPListener is the regression test for the
// <proxy> report ("passes the test on create, shows as unavailable on
// probe"): a bare listener that accepts the socket and then waits for a
// CONNECT line — exactly how 3proxy behaves — must be reported as
// reachable. The old probe followed the dial with a HEAD request and
// hung until its deadline, so a healthy proxy was marked "failed".
func TestProbeSucceedsForPlainTCPListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	// Accept, then hold the connection open without ever writing a byte.
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				time.Sleep(2 * time.Second)
				_ = c.Close()
			}(c)
		}
	}()

	s := newTestStore(t)
	id := "plain-tcp"
	if err := s.Put(Proxy{ID: id, Name: id, URL: "http://" + ln.Addr().String()}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Probe(id); err != nil {
		t.Fatalf("Probe against a live TCP listener must succeed, got: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastProbeStatus != "ok" {
		t.Fatalf("LastProbeStatus = %q, want ok", got.LastProbeStatus)
	}
	if got.LastProbeAt.IsZero() {
		t.Fatalf("LastProbeAt was not recorded")
	}
	if got.LastProbeLatencyMs <= 0 {
		t.Fatalf("LastProbeLatencyMs = %v, want > 0", got.LastProbeLatencyMs)
	}
	if got.LastProbeError != "" {
		t.Fatalf("LastProbeError = %q, want empty", got.LastProbeError)
	}
}

// TestProbeReportsDialFailure pins the other half of the contract:
// nothing listening on the port means status "failed", a non-empty
// error message, and no latency.
func TestProbeReportsDialFailure(t *testing.T) {
	// Bind a port, learn its address, then release it so the dial is
	// refused instead of hitting whatever happens to be running locally.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s := newTestStore(t)
	id := "dead"
	if err := s.Put(Proxy{ID: id, Name: id, URL: "http://" + addr}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Probe(id); err == nil {
		t.Fatalf("Probe against a closed port must fail")
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastProbeStatus != "failed" {
		t.Fatalf("LastProbeStatus = %q, want failed", got.LastProbeStatus)
	}
	if got.LastProbeError == "" {
		t.Fatalf("LastProbeError is empty, want a dial error")
	}
	if got.LastProbeLatencyMs != 0 {
		t.Fatalf("LastProbeLatencyMs = %v, want 0 on failure", got.LastProbeLatencyMs)
	}
}

// TestDialAddrDefaultsPort covers the host:port we hand to the dialer,
// including the scheme default ports and the no-host edge case.
func TestDialAddrDefaultsPort(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"http://192.0.2.1:7890", "192.0.2.1:7890"},
		{"http://192.0.2.1", "192.0.2.1:80"},
		{"https://proxy.example.com", "proxy.example.com:443"},
		{"socks5://192.0.2.1", "192.0.2.1:1080"},
		{"socks5://192.0.2.1:1080", "192.0.2.1:1080"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", c.raw, err)
		}
		if got := dialAddr(u); got != c.want {
			t.Errorf("dialAddr(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	// No host at all must yield "" so Probe reports "missing host"
	// instead of dialling something unexpected.
	if got := dialAddr(&url.URL{Scheme: "http"}); got != "" {
		t.Errorf("dialAddr(no host) = %q, want empty", got)
	}
}
