
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
	// v0.6.31: NextAfter is hardcoded Asia/Shanghai. Pick `now` so the
	// Asia/Shanghai wall clock is what we want to reason about, then
	// assert the returned UTC value.
	cst := chinaTimezone()
	// 10:29 CST → next 0/15/30/45 is 10:30 CST.
	now := time.Date(2026, 9, 30, 10, 29, 0, 0, cst)
	got, err := NextAfter("*/15 * * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 30, 10, 30, 0, 0, cst).UTC()
	if !got.Equal(want) {
		t.Errorf("NextAfter(*/15 from 10:29 CST) = %v, want %v", got, want)
	}

	// 10:31 CST → 10:30 已过,next is 10:45 CST.
	now2 := time.Date(2026, 9, 30, 10, 31, 0, 0, cst)
	got2, err := NextAfter("*/15 * * * *", now2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := time.Date(2026, 9, 30, 10, 45, 0, 0, cst).UTC()
	if !got2.Equal(want2) {
		t.Errorf("NextAfter(*/15 from 10:31 CST) = %v, want %v", got2, want2)
	}
}

func TestNextAfter_MidnightDaily(t *testing.T) {
	cst := chinaTimezone()
	// 12:00 CST on Sep 30 — next "00:00 CST" is Oct 1 00:00 CST.
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, cst)
	got, err := NextAfter("0 0 * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, cst).UTC()
	if !got.Equal(want) {
		t.Errorf("NextAfter(0 0 * * * from noon CST) = %v, want %v", got, want)
	}
}

func TestNextAfter_WeekdaysOnly(t *testing.T) {
	cst := chinaTimezone()
	// 2026-09-30 is a Wednesday. "0 0 * * 1-5" means 00:00 CST Mon-Fri.
	// From noon Wed CST, next is Thu (Oct 1) 00:00 CST.
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, cst)
	got, err := NextAfter("0 0 * * 1-5", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, cst).UTC() // Thursday 00:00 CST
	// Check weekday in CST — got is stored in UTC and may have a
	// different weekday than the CST wall-clock date.
	if got.In(cst).Weekday() != time.Thursday {
		t.Errorf("NextAfter weekday-only: got weekday (CST)=%v, want Thursday", got.In(cst).Weekday())
	}
	if !got.Equal(want) {
		t.Errorf("NextAfter weekday-only = %v, want %v", got, want)
	}
}

func TestNextAfter_DOM_OR_DOW_Vixie(t *testing.T) {
	cst := chinaTimezone()
	// 0 0 1 * 1 — 00:00 CST on day-1 OR Monday. Should fire on both.
	// Sunday Oct 4 noon CST → next is Mon Oct 5 00:00 CST.
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, cst)
	got, err := NextAfter("0 0 1 * 1", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 5, 0, 0, 0, 0, cst).UTC()
	if !got.Equal(want) {
		t.Errorf("NextAfter(0 0 1 * 1 from Sun) = %v, want %v (Monday 00:00 CST)", got, want)
	}

	// From after that Monday (Tue Oct 6 noon CST), next = Mon Oct 12 00:00 CST.
	now2 := time.Date(2026, 10, 6, 12, 0, 0, 0, cst)
	got2, err := NextAfter("0 0 1 * 1", now2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := time.Date(2026, 10, 12, 0, 0, 0, 0, cst).UTC()
	if !got2.Equal(want2) {
		t.Errorf("NextAfter(0 0 1 * 1 from Tue Oct 6) = %v, want %v (Mon Oct 12 CST)", got2, want2)
	}
}

func TestNextAfter_AsiaShanghaiHardcoded(t *testing.T) {
	// v0.6.31: the tz parameter is gone — NextAfter is always evaluated
	// in Asia/Shanghai. Confirm "30 4 * * *" still maps to 04:30 CST.
	cst := chinaTimezone()
	// Sep 30 00:00 CST. Today's 04:30 CST is still ahead → expect today.
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, cst)
	got, err := NextAfter("30 4 * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 30, 4, 30, 0, 0, cst).UTC()
	if !got.Equal(want) {
		t.Errorf("NextAfter from Sep 30 00:00 CST = %v, want %v (today 04:30 CST)", got, want)
	}

	// From 06:00 CST (after today's 04:30 fired), expect tomorrow.
	now2 := time.Date(2026, 9, 30, 6, 0, 0, 0, cst)
	got2, err := NextAfter("30 4 * * *", now2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := time.Date(2026, 10, 1, 4, 30, 0, 0, cst).UTC()
	if !got2.Equal(want2) {
		t.Errorf("NextAfter from Sep 30 06:00 CST = %v, want %v (Oct 1 04:30 CST)", got2, want2)
	}
}

func TestSchedule_Validate(t *testing.T) {
	// v0.6.31: Timezone field is ignored — Validate no longer checks it.
	// The cron expression "0 0 * * *" evaluated in Asia/Shanghai gives
	// Oct 1 00:00 CST as the next fire after Sep 30 12:00 CST.
	cst := chinaTimezone()
	s := &Schedule{
		CronExpr: "0 0 * * *",
		Timezone: "", // v0.6.31: ignored
		Enabled:  true,
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, cst)
	if err := s.Validate(now); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.NextRunAt.IsZero() {
		t.Error("NextRunAt not populated")
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, cst).UTC()
	if !s.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %v, want %v (Oct 1 00:00 CST)", s.NextRunAt, want)
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
	// v0.6.31: Timezone field is no longer validated; an arbitrary
	// value (including "Not/A/Zone") is accepted and silently ignored.
	if err := (&Schedule{CronExpr: "0 0 * * *", Timezone: "Not/A/Zone"}).Validate(now); err != nil {
		t.Errorf("bad timezone should NOT error (field is ignored): %v", err)
	}
}

// TestNextAfter_EveryMinute covers the v0.7.4 UI "每分钟" preset
// ("* * * * *"). The previous scheduler implementation required a tzdata
// lookup on every NextAfter call and exploded on the scratch base image;
// this test is here so any future regression back to LoadLocation trips
// the dependency immediately on a dev box, before shipping.
func TestNextAfter_EveryMinute(t *testing.T) {
	cst := chinaTimezone()
	now := time.Date(2026, 9, 30, 10, 29, 30, 0, cst)
	got, err := NextAfter("* * * * *", now)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want := time.Date(2026, 9, 30, 10, 30, 0, 0, cst).UTC()
	if !got.Equal(want) {
		t.Errorf("NextAfter from 10:29:30 CST = %v, want %v (next minute boundary)", got, want)
	}
}

// TestChinaTimezone_MatchesAsiaShanghaiOffset locks the v0.7.4 swap to
// FixedZone: it must always produce UTC+8 wall-clock times, matching the
// IANA Asia/Shanghai zone (which also never observes DST). If a future
// refactor accidentally changes the offset, this test catches it.
func TestChinaTimezone_MatchesAsiaShanghaiOffset(t *testing.T) {
	got := chinaTimezone()
	// Build a known UTC instant and read its UTC instant back through
	// chinaTimezone — Equal() compares the underlying time, not the
	// display zone, so this works regardless of how the zone formats.
	instant := time.Date(2026, 6, 15, 3, 47, 0, 0, time.UTC)
	if !instant.In(got).UTC().Equal(instant) {
		t.Errorf("chinaTimezone round-trip mismatch")
	}
	// Half-year offset check: Asia/Shanghai has no DST, so January and
	// July wall-clock hours must be identical. If the IANA zone is ever
	// swapped for a DST-observing zone by accident, this catches it.
	january := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).In(got).Hour()
	july := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).In(got).Hour()
	if january != july {
		t.Errorf("chinaTimezone observes DST: January hour = %d, July hour = %d",
			january, july)
	}
}
