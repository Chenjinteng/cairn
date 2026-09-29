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
	tot := h.SnapshotTotals()
	if tot.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", tot.Accepted)
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
	tot := h.SnapshotTotals()
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
	tot := h.SnapshotTotals()
	if tot.Accepted != 0 || tot.Ignored != 1 {
		t.Errorf("totals = %+v, want Accepted=0 Ignored=1", tot)
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
	tot := h.SnapshotTotals()
	if tot.Accepted != 1 {
		t.Errorf("accepted = %d, want 1 (self PUT counts)", tot.Accepted)
	}
	if tot.Self != 0 {
		t.Errorf("self counter = %d, want 0 (PUT is not folded)", tot.Self)
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
	tot := h.SnapshotTotals()
	if tot.Accepted != 0 || tot.Rejected != 1 {
		t.Errorf("totals = %+v, want Accepted=0 Rejected=1", tot)
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
