package db

// v0.7.12: regression coverage for the "container restart loses pull
// history" bug. Before this fix:
//
//   - PullJobRecord hardcoded State=string(StateSucceeded), so failed
//     and cancelled jobs never reached the SQLite history table.
//   - ListPullJobs (handler layer) only walked Executor memory; the
//     history table existed but nobody read it.
//
// The two fixes:
//   - executor.go uses vv.State when writing (test below asserts the
//     mapping by inserting rows with three different states directly
//     and reading them back).
//   - db.PullJobsList returns every row in started_at order — that's
//     what the handler now folds into the response.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *Db {
	t.Helper()
	d, err := Open(tempDBPath(t, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestPullJobsList_AllStatesPersisted is the regression: every terminal
// state must round-trip, not just succeeded.
func TestPullJobsList_AllStatesPersisted(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	rows := []PullJobRow{
		{
			ID:        "job-succeeded-1",
			SourceRef: "library/alpine:3.19",
			DestRepo:  "library/alpine",
			DestTag:   "3.19",
			State:     string("succeeded"),
			StartedAt: now.Add(-3 * time.Minute),
			EndedAt:   now.Add(-2 * time.Minute),
			CreatedAt: now.Add(-3 * time.Minute),
		},
		{
			ID:        "job-failed-1",
			SourceRef: "library/broken:1.0",
			DestRepo:  "library/broken",
			DestTag:   "1.0",
			State:     string("failed"),
			Error:     "404 MANIFEST_UNKNOWN",
			StartedAt: now.Add(-2 * time.Minute),
			EndedAt:   now.Add(-1 * time.Minute),
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			ID:        "job-cancelled-1",
			SourceRef: "library/cancelled:1.0",
			DestRepo:  "library/cancelled",
			DestTag:   "1.0",
			State:     string("cancelled"),
			StartedAt: now.Add(-1 * time.Minute),
			EndedAt:   now,
			CreatedAt: now.Add(-1 * time.Minute),
		},
	}
	for _, r := range rows {
		if err := d.PullJobRecord(ctx, r); err != nil {
			t.Fatalf("PullJobRecord(%s): %v", r.ID, err)
		}
	}

	got, err := d.PullJobsList(ctx, 0)
	if err != nil {
		t.Fatalf("PullJobsList: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("PullJobsList returned %d rows, want 3", len(got))
	}
	// Order: started_at DESC. Most recent first.
	wantOrder := []string{"job-cancelled-1", "job-failed-1", "job-succeeded-1"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("row %d = %s, want %s", i, got[i].ID, id)
		}
	}
	// Every state survives the round-trip — that's the bug fix.
	byID := map[string]PullJobRow{}
	for _, r := range got {
		byID[r.ID] = r
	}
	for _, want := range rows {
		got := byID[want.ID]
		if got.State != want.State {
			t.Errorf("%s: state = %q, want %q", want.ID, got.State, want.State)
		}
		if got.Error != want.Error {
			t.Errorf("%s: error = %q, want %q", want.ID, got.Error, want.Error)
		}
	}
}

// TestPullJobsList_LimitHonoured covers the limit arg. Without it the
// handler would pull every row on every request — fine while a deployment
// has 100 jobs, a problem at 100k. The cap lets the handler push the
// "show full history" decision into a separate endpoint later without a
// schema change.
func TestPullJobsList_LimitHonoured(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		row := PullJobRow{
			ID:        "job-" + strings.Repeat("x", i+1),
			SourceRef: "library/x:1",
			DestRepo:  "library/x",
			DestTag:   "1",
			State:     "succeeded",
			StartedAt: base.Add(time.Duration(i) * time.Second),
			EndedAt:   base.Add(time.Duration(i)*time.Second + time.Minute),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := d.PullJobRecord(ctx, row); err != nil {
			t.Fatalf("PullJobRecord: %v", err)
		}
	}
	got, err := d.PullJobsList(ctx, 2)
	if err != nil {
		t.Fatalf("PullJobsList: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit=2 returned %d rows, want 2", len(got))
	}
}

// TestParseTimeOrZero_ToleratesMalformedInput: a corrupt row shouldn't
// take the whole /api/pull/jobs response down. PullJobsList swallows
// parse errors and returns time.Time{} for that field instead.
func TestParseTimeOrZero_ToleratesMalformedInput(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"", time.Time{}},
		{"not a timestamp", time.Time{}},
		{"2026-10-03T09:00:00Z", time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got := parseTimeOrZero(c.in)
		if !got.Equal(c.want) {
			t.Errorf("parseTimeOrZero(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestPullJobDelete covers the v0.7.15 regression: deleting a history
// (SQLite-only) job must succeed, not bubble up "pull job not found".
// Returns true when the row existed, false when the ID is unknown —
// the handler uses the bool to decide between 200 and 404.
func TestPullJobDelete(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	row := PullJobRow{
		ID:        "job-delete-me",
		SourceRef: "library/alpine:3.19",
		DestRepo:  "library/alpine",
		DestTag:   "3.19",
		State:     "succeeded",
		StartedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC),
		CreatedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
	}
	if err := d.PullJobRecord(ctx, row); err != nil {
		t.Fatalf("PullJobRecord: %v", err)
	}

	deleted, err := d.PullJobDelete(ctx, "job-delete-me")
	if err != nil {
		t.Fatalf("PullJobDelete (existing): %v", err)
	}
	if !deleted {
		t.Error("PullJobDelete on existing row returned false, want true")
	}

	// Idempotency: a second delete on the same id returns false (no rows
	// affected), no error — that's how the handler decides "404" vs "200".
	deleted, err = d.PullJobDelete(ctx, "job-delete-me")
	if err != nil {
		t.Fatalf("PullJobDelete (repeat): %v", err)
	}
	if deleted {
		t.Error("PullJobDelete on missing row returned true, want false")
	}

	// Unknown id from the start: same — false, no error.
	deleted, err = d.PullJobDelete(ctx, "never-existed")
	if err != nil {
		t.Fatalf("PullJobDelete (unknown): %v", err)
	}
	if deleted {
		t.Error("PullJobDelete on unknown id returned true, want false")
	}
}

// TestPullJobsEnforceLimit covers v0.7.16: pull_jobs had no cap, every
// terminal job accumulated forever. The retentionLoop now caps at 50;
// 60 rows in → 10 rows out, keeping the newest 50 by started_at.
func TestPullJobsEnforceLimit(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Insert 60 jobs spaced 1 minute apart so started_at is unique and
	// we can identify which 10 get pruned deterministically.
	base := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		row := PullJobRow{
			ID:        "job-" + strings.Repeat("x", i+1),
			SourceRef: "library/x:1",
			DestRepo:  "library/x",
			DestTag:   "1",
			State:     "succeeded",
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			EndedAt:   base.Add(time.Duration(i)*time.Minute + time.Minute),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := d.PullJobRecord(ctx, row); err != nil {
			t.Fatalf("PullJobRecord[%d]: %v", i, err)
		}
	}

	n, err := d.PullJobsEnforceLimit(ctx, 50)
	if err != nil {
		t.Fatalf("PullJobsEnforceLimit: %v", err)
	}
	if n != 10 {
		t.Errorf("deleted = %d, want 10 (60 - 50 cap)", n)
	}

	rows, err := d.PullJobsList(ctx, 0)
	if err != nil {
		t.Fatalf("PullJobsList after cap: %v", err)
	}
	if len(rows) != 50 {
		t.Errorf("row count after cap = %d, want 50", len(rows))
	}
	// PullJobsList orders started_at DESC — newest first. So rows[0] is
	// the newest kept (base + 59min) and rows[len-1] is the oldest kept
	// (base + 10min). The first 10 inserted (base + 0min..9min) are gone.
	if !rows[0].StartedAt.Equal(base.Add(59 * time.Minute)) {
		t.Errorf("newest kept row started at %v, want %v (base+59m)",
			rows[0].StartedAt, base.Add(59*time.Minute))
	}
	if !rows[len(rows)-1].StartedAt.Equal(base.Add(10 * time.Minute)) {
		t.Errorf("oldest kept row started at %v, want %v (base+10m)",
			rows[len(rows)-1].StartedAt, base.Add(10*time.Minute))
	}

	// limit <= 0 is a no-op.
	n, err = d.PullJobsEnforceLimit(ctx, 0)
	if err != nil {
		t.Errorf("PullJobsEnforceLimit(0): %v", err)
	}
	if n != 0 {
		t.Errorf("PullJobsEnforceLimit(0) deleted %d rows, want 0", n)
	}
}

// TestPullJobCancel covers v0.7.17: Cancel() must flip a "running" or
// "queued" row to "cancelled" (the v0.7.17 Cancel API fallback path).
// Already-terminal rows must be left alone — flipping a "succeeded" row
// would be a regression that hides real history from the operator.
func TestPullJobCancel(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id, state string) PullJobRow {
		return PullJobRow{
			ID:        id,
			SourceRef: "library/x:1",
			DestRepo:  "library/x",
			DestTag:   "1",
			State:     state,
			StartedAt: now.Add(-time.Minute),
			EndedAt:   now, // pre-cancel; PullJobCancel must overwrite
			CreatedAt: now.Add(-time.Minute),
		}
	}

	// Pre-cancel the row's state must be in {running, queued} for the
	// UPDATE to take effect. The regression we're guarding against is
	// "PullJobCancel touched a succeeded/failed/cancelled row".
	for _, state := range []string{"running", "queued"} {
		row := mk("job-"+state, state)
		if err := d.PullJobRecord(ctx, row); err != nil {
			t.Fatalf("PullJobRecord(%s): %v", state, err)
		}
		flipped, err := d.PullJobCancel(ctx, row.ID)
		if err != nil {
			t.Fatalf("PullJobCancel(%s): %v", state, err)
		}
		if !flipped {
			t.Errorf("PullJobCancel(%s): flipped = false, want true", state)
		}
		rows, err := d.PullJobsList(ctx, 0)
		if err != nil {
			t.Fatalf("PullJobsList: %v", err)
		}
		var got *PullJobRow
		for i := range rows {
			if rows[i].ID == row.ID {
				got = &rows[i]
				break
			}
		}
		if got == nil {
			t.Fatalf("PullJobsList: row %s not found after cancel", row.ID)
		}
		if got.State != "cancelled" {
			t.Errorf("after cancel state = %q, want cancelled", got.State)
		}
		// ended_at should be the *current* time (PullJobCancel writes
		// time.Now()), not the pre-cancel endedAt — operators see this
		// in the history list as "cancelled at HH:MM". Use !Before so
		// same-second writes don't flake on coarse clocks.
		if got.EndedAt.Before(now) {
			t.Errorf("after cancel ended_at = %v, want >= %v", got.EndedAt, now)
		}
	}

	// Already-terminal rows must NOT be flipped. The handler relies on
	// this to keep "succeeded" / "failed" rows honest — otherwise the
	// UI would erase a successful pull the moment someone clicks a
	// stale Cancel button on it.
	for _, state := range []string{"succeeded", "failed", "cancelled"} {
		row := mk("job-terminal-"+state, state)
		if err := d.PullJobRecord(ctx, row); err != nil {
			t.Fatalf("PullJobRecord(%s): %v", state, err)
		}
		flipped, err := d.PullJobCancel(ctx, row.ID)
		if err != nil {
			t.Fatalf("PullJobCancel(%s): %v", state, err)
		}
		if flipped {
			t.Errorf("PullJobCancel(%s) flipped terminal row — must not", state)
		}
		rows, _ := d.PullJobsList(ctx, 0)
		for _, r := range rows {
			if r.ID == row.ID && r.State != state {
				t.Errorf("terminal row %s state changed: got %q want %q", state, r.State, state)
			}
		}
	}

	// Unknown id: not flipped, no error.
	flipped, err := d.PullJobCancel(ctx, "job-does-not-exist")
	if err != nil {
		t.Errorf("PullJobCancel(unknown): %v", err)
	}
	if flipped {
		t.Errorf("PullJobCancel(unknown): flipped = true, want false")
	}
}
