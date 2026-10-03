// 0.5.8: IngestLocal is the path the built-in registryd uses to feed the
// heat aggregator. The webhook ServeHTTP path uses the same processOne
// helper, so these tests cover the shared pipeline from a different
// entry point (in-process, no HMAC, no body parsing).
package events

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Chenjinteng/cairn/internal/db"
)

// newTestHandler wires a Handler backed by a fresh on-disk SQLite db so we
// can assert ActivityIncrement landed (or didn't). ignore is the UA
// ignore list; enabled==false exercises the kill-switch path.
func newTestHandler(t *testing.T, ignore []string, enabled bool) (*Handler, *db.Db) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "heat.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	h := NewHandler(d, "", ignore, 200)
	h.SetEnabled(func() bool { return enabled })
	return h, d
}

func localEvent(repo, tag, ua, method string) Event {
	ev := Event{Action: "pull"}
	ev.Target.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
	ev.Target.Repository = repo
	ev.Target.Tag = tag
	ev.Request.Method = method
	ev.Request.UserAgent = ua
	ev.Request.Host = "test"
	ev.Request.RemoteAddr = "127.0.0.1:0"
	return ev
}

// CountsPull verifies a normal HEAD manifest event lands in both the
// in-memory accepted counter and the SQLite heat table.
func TestIngestLocal_CountsPull(t *testing.T) {
	h, d := newTestHandler(t, nil, true)
	ev := localEvent("library/alpine", "3.19", "docker/26.0", "HEAD")
	if !h.IngestLocal(ev) {
		t.Fatalf("IngestLocal returned false on a counted event")
	}
	// v0.7.20: Accepted / Rejected now live in event_log, derived via
	// COUNT(*) / SUM(counted) on each /api/stats/events call.
	counts, err := d.EventLogCount(context.Background())
	if err != nil {
		t.Fatalf("EventLogCount: %v", err)
	}
	if counts.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", counts.Accepted)
	}
	rows, err := d.GetSeries(context.Background(), time.Unix(0, 0), 100)
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("GetSeries returned %d rows, want 1", len(rows))
	}
	if rows[0].Repo != "library/alpine" || rows[0].Tag != "3.19" || rows[0].Action != "pull" {
		t.Errorf("unexpected row shape: %+v", rows[0])
	}
}

// RespectsKillSwitch confirms the live SetEnabled predicate (env or
// panel override) gates IngestLocal at request time, not just at
// construction time — so an operator flipping allow.registry_events on
// the panel stops heat immediately without a restart.
func TestIngestLocal_RespectsKillSwitch(t *testing.T) {
	h, d := newTestHandler(t, nil, false) // kill switch ON
	ev := localEvent("library/alpine", "3.19", "docker/26.0", "HEAD")
	if h.IngestLocal(ev) {
		t.Fatalf("IngestLocal returned true while kill switch was on")
	}
	tot, err := d.EventLogCount(context.Background())
	if err != nil {
		t.Fatalf("EventLogCount: %v", err)
	}
	if tot.Accepted != 0 {
		t.Errorf("accepted = %d, want 0 (kill switch on)", tot.Accepted)
	}
	rows, err := d.GetSeries(context.Background(), time.Unix(0, 0), 100)
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("SQLite has %d rows, want 0 (kill switch on)", len(rows))
	}
}

// NilHandler is the safe default registryd uses — every handler call
// guards with `if h.Events == nil`, but defence-in-depth says IngestLocal
// on a nil *Handler must not panic (e.g. when registryd is wired before
// the events handler in tests).
func TestIngestLocal_NilHandler(t *testing.T) {
	var h *Handler
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	if h.IngestLocal(localEvent("r", "t", "u", "HEAD")) {
		t.Errorf("nil handler returned true")
	}
}

// IgnoreRuleFolds: UA matches the ignore list → folded into
// ignoredFolded, NOT counted, NOT in SQLite. The recent ring stays free
// for the next real client.
func TestIngestLocal_IgnoreRuleFolds(t *testing.T) {
	h, d := newTestHandler(t, []string{"wget"}, true)
	ev := localEvent("library/alpine", "3.19", "wget/1.21", "HEAD")
	if h.IngestLocal(ev) {
		t.Fatalf("IngestLocal returned true on an ignored UA")
	}
	// v0.7.20: ignored events do not land in event_log (was previously
	// folded into an atomic counter that the new SQLite path doesn't
	// replace yet). Verify both that the SQLite heat table is empty
	// AND that event_log got no row.
	counts, err := d.EventLogCount(context.Background())
	if err != nil {
		t.Fatalf("EventLogCount: %v", err)
	}
	if counts.Total != 0 {
		t.Errorf("event_log total = %d, want 0 (ignored UA must NOT land)", counts.Total)
	}
	rows, err := d.GetSeries(context.Background(), time.Unix(0, 0), 100)
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("ignored event still landed in SQLite: %d rows", len(rows))
	}
}

// SelfPushCounted: built-in registry pushing its own image (UA starts
// with "cairn/", method PUT) DOES count — the package doc is explicit
// that self WRITES change heat so they must stay auditable. Compare with
// SelfReadFolded below: a rescan (HEAD) must NOT count.
func TestIngestLocal_SelfPushCounted(t *testing.T) {
	h, d := newTestHandler(t, nil, true)
	ev := localEvent("library/alpine", "3.19", "cairn/0.5.8", "PUT")
	ev.Action = "push"
	if !h.IngestLocal(ev) {
		t.Fatalf("self PUT should be counted")
	}
	// v0.7.20: self PUT still counts (event_log.counted = 1); the
	// separate `self` field on the old atomic counter is gone for
	// now (see CHANGELOG v0.7.20 — needs event_log schema bump).
	counts, err := d.EventLogCount(context.Background())
	if err != nil {
		t.Fatalf("EventLogCount: %v", err)
	}
	if counts.Accepted != 1 {
		t.Errorf("accepted = %d, want 1 (self PUT counts)", counts.Accepted)
	}
	rows, err := d.GetSeries(context.Background(), time.Unix(0, 0), 100)
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != "push" {
		t.Errorf("push row missing or wrong: %+v", rows)
	}
}

// WrongMethodRejected: GET on a manifest (the actual blob fetch path)
// must NOT count — only HEAD/PUT carry the tag, and counting GET would
// amplify every pull by ~blob-count. The reason field is a stable code
// the UI can grep for.
func TestIngestLocal_WrongMethodRejected(t *testing.T) {
	h, d := newTestHandler(t, nil, true)
	ev := localEvent("library/alpine", "3.19", "docker/26.0", "GET")
	if h.IngestLocal(ev) {
		t.Fatalf("IngestLocal returned true on a GET event")
	}
	tot, err := d.EventLogCount(context.Background())
	if err != nil {
		t.Fatalf("EventLogCount: %v", err)
	}
	if tot.Accepted != 0 || tot.Rejected != 1 {
		t.Errorf("counts = %+v, want Accepted=0 Rejected=1", tot)
	}
	rows, err := d.GetSeries(context.Background(), time.Unix(0, 0), 100)
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("GET still landed in SQLite: %d rows", len(rows))
	}
}

// sanity: httptest import used? silence unused if a refactor drops the
// only user. Cheap import-only test that also asserts NewHandler's
// construction path doesn't crash on empty ignore list.
func TestNewHandler_EmptyIgnore(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "heat.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	h := NewHandler(d, "", nil, 50)
	if h == nil {
		t.Fatalf("NewHandler returned nil")
	}
	// touch httptest so the import isn't flagged in code-rotation.
	_ = httptest.NewRecorder
}

// ──────────────────────────────────────────────────────────────────────
// v0.5.52: event_seen persistence tests
//
// The "seen clients" panel previously lived only in h.clients (memory,
// reset on restart). These tests pin the new behaviour:
//   - startup loads persisted rows
//   - flushSeen writes dirty rows
//   - ignore-rule UAs and self UAs never reach event_seen
// ──────────────────────────────────────────────────────────────────────

// eventSeenCount is a tiny helper for the v0.5.52 tests: returns the
// row count of event_seen via the only API the package exposes for
// bulk reads (LoadAllEventSeen).
func eventSeenCount(t *testing.T, d *db.Db) int {
	t.Helper()
	rows, err := d.LoadAllEventSeen(context.Background())
	if err != nil {
		t.Fatalf("LoadAllEventSeen: %v", err)
	}
	return len(rows)
}

// LoadersPersistedSeen asserts the "seen clients" panel survives
// restarts: a row written directly via BatchUpsertEventSeen must
// appear in SnapshotClients() right after NewHandler, before any
// request has been observed.
func TestNewHandler_LoadsPersistedSeen(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "heat.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	now := time.Now().UTC()
	if err := d.BatchUpsertEventSeen(context.Background(), []db.EventSeenRow{
		{UserAgent: "kubelet/v1.30", LastSeenAt: now, FirstSeenAt: now, Events: 5, Counted: 5},
	}); err != nil {
		t.Fatalf("BatchUpsertEventSeen: %v", err)
	}

	h := NewHandler(d, "", nil, 50)
	stats := h.SnapshotClients()
	if len(stats) != 1 {
		t.Fatalf("SnapshotClients returned %d rows, want 1", len(stats))
	}
	if stats[0].UserAgent != "kubelet/v1.30" || stats[0].Events != 5 {
		t.Errorf("loaded stat mismatch: %+v", stats[0])
	}
}

// FlushSeenPersists asserts dirty per-UA aggregates survive a simulated
// restart: recordClient marks dirty, flushSeen writes to SQLite, a fresh
// NewHandler on the same DB loads them back. This is the load-bearing
// property of the whole 0.5.52 change.
func TestFlushSeen_Persists(t *testing.T) {
	h, d := newTestHandler(t, nil, true)
	// Two events from the same UA → non-trivial count.
	if !h.IngestLocal(localEvent("library/alpine", "3.19", "containerd/v1.7", "HEAD")) {
		t.Fatalf("first IngestLocal returned false")
	}
	if !h.IngestLocal(localEvent("library/alpine", "3.19", "containerd/v1.7", "HEAD")) {
		t.Fatalf("second IngestLocal returned false")
	}

	// Synchronous flush (no goroutine timing in tests).
	h.flushSeen(context.Background())
	if n := eventSeenCount(t, d); n != 1 {
		t.Fatalf("event_seen rows after flush = %d, want 1", n)
	}

	// Simulate restart: fresh handler on the same DB.
	h2 := NewHandler(d, "", nil, 50)
	stats := h2.SnapshotClients()
	if len(stats) != 1 {
		t.Fatalf("after restart: %d rows, want 1", len(stats))
	}
	if stats[0].Events != 2 {
		t.Errorf("after restart: events = %d, want 2", stats[0].Events)
	}
}

// IgnoreUANotPersisted asserts ignored UAs never reach event_seen. They
// get folded by ShouldCount upstream of recordClient, so the dirty map
// never even sees them — saving a write that would just be filtered at
// read time anyway.
func TestIgnoreUA_NotPersisted(t *testing.T) {
	h, d := newTestHandler(t, []string{"kubelet"}, true)
	// Ignore UA → IngestLocal returns false (dec.Count=false), but we
	// don't gate the test on that; we only care about post-flush state.
	h.IngestLocal(localEvent("library/alpine", "3.19", "kubelet/v1.30", "HEAD"))
	h.flushSeen(context.Background())
	if n := eventSeenCount(t, d); n != 0 {
		t.Fatalf("event_seen rows for ignored UA = %d, want 0", n)
	}
}

// SelfUANotPersisted asserts cairn-internal UAs (prefix "cairn/") never
// reach event_seen. They're folded by processOne — the operator-visible
// "seen clients" panel should never include the server's own requests.
func TestSelfUA_NotPersisted(t *testing.T) {
	h, d := newTestHandler(t, nil, true)
	if !h.IngestLocal(localEvent("library/alpine", "3.19", "cairn/0.5.52", "HEAD")) {
		t.Fatalf("IngestLocal returned false")
	}
	h.flushSeen(context.Background())
	if n := eventSeenCount(t, d); n != 0 {
		t.Fatalf("event_seen rows for self UA = %d, want 0", n)
	}
}
