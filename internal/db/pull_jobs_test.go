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
