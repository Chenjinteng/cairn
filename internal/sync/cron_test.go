package sync

import (
	"testing"
	"time"
)

func mustParseCron(t *testing.T, expr string) *cronExpr {
	t.Helper()
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q): %v", expr, err)
	}
	return c
}

func TestParseCron_Valid(t *testing.T) {
	cases := []string{
		"*/15 * * * *",
		"0 0 * * *",
		"0 0 * * 1-5",
		"0 0 1 * *",
		"0 0 1 1 *",
		"30 4 1-7 * 1",
		"0 0,12 * * *",
		"5 0 * 8 *",   // single literal in every position
		"0 0 * * 7",   // 7 should normalize to Sunday
		"0 0 * * 0,6", // Sunday or Saturday
	}
	for _, c := range cases {
		if _, err := ParseCron(c); err != nil {
			t.Errorf("ParseCron(%q) failed: %v", c, err)
		}
	}
}

func TestParseCron_Invalid(t *testing.T) {
	cases := []string{
		"",                  // empty
		"* * * *",           // 4 fields
		"* * * * * *",       // 6 fields
		"60 * * * *",        // minute out of range
		"* 24 * * *",        // hour out of range
		"* * 32 * *",        // dom out of range
		"* * * 13 *",        // month out of range
		"* * * * 8",         // dow out of range (0-6 + 7→0)
		"* * 0 * *",         // dom=0 invalid (1-31)
		"*/0 * * * *",       // step 0
		"abc * * * *",       // garbage
		"1-5-9 * * * *",     // malformed range
	}
	for _, c := range cases {
		if _, err := ParseCron(c); err == nil {
			t.Errorf("ParseCron(%q) should have failed", c)
		}
	}
}

func TestCronField_StepAndRange(t *testing.T) {
	// minute */15 → {0, 15, 30, 45}
	f := mustParseCron(t, "*/15 * * * *").minute
	for _, want := range []int{0, 15, 30, 45} {
		if !f.match(want) {
			t.Errorf("*/15 minute should match %d", want)
		}
	}
	for _, not := range []int{1, 14, 16, 29, 31, 44, 46, 59} {
		if f.match(not) {
			t.Errorf("*/15 minute should NOT match %d", not)
		}
	}
}

func TestCronField_ListAndLiteral(t *testing.T) {
	f := mustParseCron(t, "1,15,30 * * * *").minute
	for _, want := range []int{1, 15, 30} {
		if !f.match(want) {
			t.Errorf("1,15,30 minute should match %d", want)
		}
	}
	if f.match(0) {
		t.Error("1,15,30 should not match 0")
	}
}

func TestNextAfter_StarEvery15Minutes(t *testing.T) {
	loc := time.UTC
	// 10:29 → next 0/15/30/45 is 10:30
	now := time.Date(2026, 9, 30, 10, 29, 0, 0, loc)
	got, err := NextAfter("*/15 * * * *", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 30, 10, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextAfter(*/15 from 10:29) = %v, want %v", got, want)
	}

	// 10:31 → 10:30 已过,next is 10:45
	now2 := time.Date(2026, 9, 30, 10, 31, 0, 0, loc)
	got2, err := NextAfter("*/15 * * * *", "UTC", now2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := time.Date(2026, 9, 30, 10, 45, 0, 0, loc)
	if !got2.Equal(want2) {
		t.Errorf("NextAfter(*/15 from 10:31) = %v, want %v", got2, want2)
	}
}

func TestNextAfter_MidnightDaily(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	got, err := NextAfter("0 0 * * *", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NextAfter(0 0 * * * from noon) = %v, want %v", got, want)
	}
}

func TestNextAfter_WeekdaysOnly(t *testing.T) {
	// 2026-09-30 is a Wednesday. "0 0 * * 1-5" means midnight Mon-Fri.
	// Next midnight after noon Wednesday = next Thursday midnight.
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	got, err := NextAfter("0 0 * * 1-5", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) // Thursday
	if got.Weekday() != time.Thursday {
		t.Errorf("NextAfter weekday-only: got weekday=%v, want Thursday", got.Weekday())
	}
	if !got.Equal(want) {
		t.Errorf("NextAfter weekday-only = %v, want %v", got, want)
	}
}

func TestNextAfter_DOM_OR_DOW_Vixie(t *testing.T) {
	// 0 0 1 * 1 — midnight on day-1 OR Monday. Should fire on both.
	// Pick a Friday (2026-10-02) — day 1 of month is Monday so we
	// expect the immediate Monday (2026-10-05? no, day 1 is Thursday
	// in October 2026, let me just check both fire correctly).
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) // Sunday
	got, err := NextAfter("0 0 1 * 1", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	// Sunday Oct 4 — next Monday midnight is Oct 5 00:00 UTC.
	want := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NextAfter(0 0 1 * 1 from Sun) = %v, want %v (Monday midnight)", got, want)
	}

	// From after that Monday (Tuesday Oct 6 noon), next = day-1 of Nov.
	now2 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	got2, err := NextAfter("0 0 1 * 1", "UTC", now2)
	if err != nil {
		t.Fatal(err)
	}
	// From Tue Oct 6 noon, next midnight is Wed Oct 7 — but Wed is dow=3,
	// not Monday. Next Monday midnight is Oct 12. (OR-logic: dom=1 OR dow=Mon.)
	want2 := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Errorf("NextAfter(0 0 1 * 1 from Tue Oct 6) = %v, want %v (Mon Oct 12)", got2, want2)
	}
}

func TestNextAfter_Timezone(t *testing.T) {
	// Schedule "30 4 * * *" in Asia/Shanghai (UTC+8) means 04:30 SGT.
	// now = Sep 30 00:00 UTC. Sep 30 04:30 SGT = Sep 29 20:30 UTC is
	// in the past, so next fire is Oct 1 04:30 SGT = Sep 30 20:30 UTC.
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	got, err := NextAfter("30 4 * * *", "Asia/Shanghai", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 30, 20, 30, 0, 0, time.UTC) // == Oct 1 04:30 SGT
	if !got.Equal(want) {
		t.Errorf("NextAfter tz = %v, want %v (Oct 1 04:30 SGT = Sep 30 20:30 UTC)", got, want)
	}

	// From a time BEFORE today's 04:30 SGT, expect today's.
	now2 := time.Date(2026, 9, 29, 18, 0, 0, 0, time.UTC) // Sep 30 02:00 SGT
	got2, err := NextAfter("30 4 * * *", "Asia/Shanghai", now2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := time.Date(2026, 9, 29, 20, 30, 0, 0, time.UTC) // == Sep 30 04:30 SGT
	if !got2.Equal(want2) {
		t.Errorf("NextAfter tz from Sep 30 02:00 SGT = %v, want %v", got2, want2)
	}
}

func TestSchedule_Validate(t *testing.T) {
	s := &Schedule{
		CronExpr: "0 0 * * *",
		Timezone: "",
		Enabled:  true,
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := s.Validate(now); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.NextRunAt.IsZero() {
		t.Error("NextRunAt not populated")
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if !s.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %v, want %v", s.NextRunAt, want)
	}
}

func TestSchedule_Validate_Errors(t *testing.T) {
	now := time.Now().UTC()
	if err := (&Schedule{CronExpr: ""}).Validate(now); err == nil {
		t.Error("empty cron should error")
	}
	if err := (&Schedule{CronExpr: "bad cron"}).Validate(now); err == nil {
		t.Error("malformed cron should error")
	}
	if err := (&Schedule{CronExpr: "0 0 * * *", Timezone: "Not/A/Zone"}).Validate(now); err == nil {
		t.Error("bad timezone should error")
	}
}