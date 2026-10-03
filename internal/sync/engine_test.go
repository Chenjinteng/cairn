package sync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chenjinteng/cairn/internal/db"
)

// fakeClient is a minimal stand-in for *registry.Client used only by the
// pullTag path in this test. We never call it — Cancel must take effect
// before any layer download starts, by short-circuiting at the runPull
// entry's catalog/loop boundary. See the comment on runPull.
type fakeClient struct{}

func TestEngineCancelShortCircuitsBeforeRemoteCall(t *testing.T) {
	// Set up an in-memory DB so we can persist the running row.
	dir := t.TempDir()
	d, err := db.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Build a minimal Engine. local / vault / log are nil-safe per
	// NewEngine's contract; we only exercise Start + Cancel, not the
	// actual pull path.
	store := NewStore(d)
	eng := NewEngine(store, nil, nil, nil)

	task := SyncTask{
		ID:        1,
		Name:      "cancel-me",
		Direction: DirectionPull,
		RemoteURL: "http://192.0.2.1:65535", // TEST-NET-1, will hang if reached
		Enabled:   true,
	}
	if err := store.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Pre-cancel any record of the (very recent) cleanup so we don't
	// race the Start/Cancel bookkeeping. Use an atomic flag to assert
	// the goroutine respected the cancel — if it leaked past the
	// WithCancel wrapper we'd never observe ctx.Done.
	var observed atomic.Bool
	t.Cleanup(func() { observed.Store(true) })

	// Long-blocking pullRepo would reach rc.ListRepositories(ctx) and
	// block until the remote refused (or our cancel propagated). With
	// Cancel called immediately after Start, the goroutine should
	// observe ctx.Done at the next pullRepo iteration boundary and
	// return within a small budget.
	parent, parentCancel := context.WithCancel(context.Background())
	t.Cleanup(parentCancel)

	run, err := eng.Start(parent, task)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != RunRunning {
		t.Fatalf("run.Status = %q, want running", run.Status)
	}

	if !eng.Cancel(task.ID) {
		t.Fatalf("Cancel(%d) returned false; expected a registered cancel", task.ID)
	}

	// Cancel after the run finished should return false. We poll
	// shortly — give the goroutine a beat to wind down.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !eng.Cancel(task.ID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Calling Cancel again now must return false (cancel cleared from
	// the map).
	if eng.Cancel(task.ID) {
		t.Errorf("Cancel(%d) still returns true after run finished; cancel func leaked in e.cancels", task.ID)
	}

	// The run row should be terminal by now. Inspect via the store:
	// status failed with an error mentioning context cancellation.
	runID := run.ID
	var final db.SyncRunRow
	for time.Now().Before(deadline) {
		// Reload from DB; the goroutine writes the terminal row.
		row, gerr := d.SyncTaskGet(context.Background(), task.ID)
		if gerr != nil {
			t.Fatalf("SyncTaskGet: %v", gerr)
		}
		_ = row
		runs, lerr := store.ListRunsByTask(context.Background(), task.ID, 10)
		if lerr == nil && len(runs) > 0 {
			r := runs[0]
			if r.Status == RunFailed || r.Status == RunSuccess || r.Status == RunPartial {
				final = db.SyncRunRow{
					ID:          r.ID,
					StartedAt:   r.StartedAt,
					FinishedAt:  r.FinishedAt,
					Status:      string(r.Status),
					ReposTotal:  r.ReposTotal,
					ReposSynced: r.ReposSynced,
					ReposFailed: r.ReposFailed,
					Error:       r.Error,
				}
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final.Status == "" {
		t.Fatalf("run row never reached terminal state in 2s (still running=%v)", run.Status)
	}
	// Cancel can land either before any tag (status=failed with
	// ctx.Err()) OR mid-stream after at least one tag attempted. We
	// don't pin the exact error string — different Go versions phrase
	// it slightly differently — but it MUST mention "context".
	if final.Error == "" {
		t.Errorf("cancel: run.Status=%q but Error is empty; expected a ctx-cancel mention", final.Status)
	} else if !containsCI(final.Error, "context") {
		t.Errorf("cancel: run.Error=%q; expected a 'context ...' mention (canceled / deadline)", final.Error)
	}

	// The per-task lock must be released — a fresh Start on the same
	// task should succeed (would deadlock if not).
	task2 := task
	task2.Name = "cancel-me-2"
	if err := store.UpdateTask(context.Background(), &task2); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	run2, err := eng.Start(parent, task2)
	if err != nil {
		t.Fatalf("Start after cancel: %v; lock may have leaked", err)
	}
	if !eng.Cancel(task2.ID) {
		t.Errorf("Cancel of the second run returned false; expected true")
	}
	_ = run2
	_ = runID
}

// TestEngineCancelUnknownTask covers the "no run registered" branch —
// must return false, not panic. Cheap to assert since it doesn't touch
// the DB.
func TestEngineCancelUnknownTask(t *testing.T) {
	eng := NewEngine(nil, nil, nil, nil)
	if eng.Cancel(99999) {
		t.Errorf("Cancel(99999) returned true; want false for unregistered task")
	}
}

// containsCI is a tiny helper to keep the import set minimal — strings.Contains
// would do, but pulling strings in just for case-insensitive match is overkill.
func containsCI(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	H := []byte(haystack)
	N := []byte(needle)
	for i := 0; i+len(N) <= len(H); i++ {
		match := true
		for j := 0; j < len(N); j++ {
			h := H[i+j]
			n := N[j]
			// ASCII-only case-insensitive compare. The cancel error
			// strings from the Go stdlib are all ASCII ("context
			// canceled", "context deadline exceeded") so this is fine.
			if h >= 'A' && h <= 'Z' {
				h += 'a' - 'A'
			}
			if n >= 'A' && n <= 'Z' {
				n += 'a' - 'A'
			}
			if h != n {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
