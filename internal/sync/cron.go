package sync

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronField is a parsed representation of one cron field (e.g. "*/15" or
// "1,15,30"). minute / hour / dom / month / dow each have their own.
//
// Field evaluation is by membership: a time's component matches if it
// falls inside the set. No special OR/AND semantics — that lives in
// NextAfter, which treats the whole expression as a conjunction of
// fields except for the (dom vs dow) Vixie-cron quirk (see NextAfter).
type cronField struct {
	values map[int]bool // 0..N-1 with N = field range (e.g. 60 for minute)
}

// cronExpr is the parsed 5-field schedule.
type cronExpr struct {
	minute  cronField
	hour    cronField
	dom     cronField // 1..31
	month   cronField // 1..12
	dow     cronField // 0..6 (Sunday=0; also accept 7 → 0)
	domStar bool      // dom == "*" — when true, dow is the only day constraint
	dowStar bool      // dow == "*" — when true, dom is the only day constraint
}

// ParseCron parses a 5-field cron expression with the vanilla operators
// (* , - /). Returns an error usable for Validate (wrapped by the
// caller). Whitespace tolerated; month / dow names are NOT supported —
// "JAN" or "MON" intentionally rejected for simplicity. Empty string
// is an error.
func ParseCron(expr string) (*cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}
	// (low, high) bounds per field, inclusive
	bounds := []struct {
		lo, hi int
	}{
		{0, 59},  // minute
		{0, 23},  // hour
		{1, 31},  // day of month
		{1, 12},  // month
		{0, 6},   // day of week (Sunday = 0; 7 maps to 0)
	}
	parsed := &cronExpr{}
	specs := []*cronField{&parsed.minute, &parsed.hour, &parsed.dom, &parsed.month, &parsed.dow}
	stars := []*bool{new(bool), new(bool), &parsed.domStar, new(bool), &parsed.dowStar}

	// Specs/stars have a fixed size — fill star trackers first.
	if fields[2] == "*" {
		*stars[2] = true
	}
	if fields[4] == "*" {
		*stars[4] = true
	}

	for i, raw := range fields {
		cf, err := parseCronField(raw, bounds[i].lo, bounds[i].hi)
		if err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i+1, raw, err)
		}
		*specs[i] = cf
	}
	return parsed, nil
}

// parseCronField parses one field like "*/15", "1,15,30", "1-30/3", or "5".
// The "*" form fills values with every value in [lo, hi] inclusive.
func parseCronField(raw string, lo, hi int) (cronField, error) {
	out := cronField{values: map[int]bool{}}
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		if err := parseCronPart(p, lo, hi, out.values); err != nil {
			return out, err
		}
	}
	if len(out.values) == 0 {
		return out, fmt.Errorf("empty field")
	}
	return out, nil
}

func parseCronPart(p string, lo, hi int, into map[int]bool) error {
	step := 1
	rangePart := p
	if i := strings.Index(p, "/"); i >= 0 {
		rangePart = p[:i]
		var err error
		step, err = strconv.Atoi(p[i+1:])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step %q", p[i+1:])
		}
	}
	// day-of-week accepts 7 as a Sunday synonym (Vixie cron quirk).
	// disambiguate by the bound: only the dow field has hi=6 (not 59
	// like minute), so "lo=0 AND hi=6" uniquely identifies dow.
	isDow := lo == 0 && hi == 6
	normalize := func(v int) int {
		if isDow && v == 7 {
			return 0
		}
		return v
	}
	start, end := lo, hi
	if rangePart != "*" {
		if i := strings.Index(rangePart, "-"); i >= 0 {
			a, errA := strconv.Atoi(rangePart[:i])
			b, errB := strconv.Atoi(rangePart[i+1:])
			if errA != nil || errB != nil {
				return fmt.Errorf("invalid range %q", rangePart)
			}
			start, end = normalize(a), normalize(b)
		} else {
			n, err := strconv.Atoi(rangePart)
			if err != nil {
				return fmt.Errorf("invalid literal %q", rangePart)
			}
			// "N" alone means just N (NOT a range up to hi).
			// "N/s" (literal/step, no dash) means N..hi step s.
			start = normalize(n)
			if strings.Contains(p, "/") {
				end = hi
			} else {
				end = start
			}
		}
	}
	if start < lo || end > hi || start > end {
		return fmt.Errorf("range %d-%d out of bounds [%d,%d]", start, end, lo, hi)
	}
	for v := start; v <= end; v += step {
		into[v] = true
	}
	return nil
}

// match reports whether a single integer is in the field's set.
func (f cronField) match(v int) bool { return f.values[v] }

// NextAfter returns the next time >= now (exclusive) when the cron
// expression fires. The expression is evaluated in Asia/Shanghai (the
// cairn deployment's home zone); the returned time is in UTC so the
// scheduler's DB-clock compare stays consistent.
//
// v0.6.31: timezone parameter removed — cairn is China-deployed and
// users wanted a single predictable wall-clock semantics. Removing the
// parameter also kills the "what does 0 0 * * * fire at" ambiguity
// (was UTC before; would silently shift 8h if the container's TZ env
// was anything else). The DB column `sync_schedules.timezone` is kept
// for backward compat but is forced to "Asia/Shanghai" on every write;
// see ScheduleValidate docs.
//
// Implements Vixie cron semantics for dom vs dow: if both fields are
// restricted (i.e. neither is "*"), a match fires when EITHER field
// matches (OR). When one is "*", only the restricted field applies.
// This matches Kubernetes / Cron-UI behaviour, and what users mean
// when they write "0 0 * * 1-5" (midnight on weekdays) — they don't
// expect to also constrain dom.
//
// next-after returns an error only for catastrophic conditions
// (impossible to satisfy in 4 years of minutes — basically never).
// In practice ParseCron already rejects many bad expressions; this is
// a defensive floor.
func NextAfter(cronExprStr string, now time.Time) (time.Time, error) {
	expr, err := ParseCron(cronExprStr)
	if err != nil {
		return time.Time{}, err
	}
	// v0.7.4: switch from time.LoadLocation("Asia/Shanghai") to a fixed
	// UTC+8 offset. The IANA lookup walks /usr/share/zoneinfo, which the
	// scratch base image doesn't ship — every cron save failed with
	// "load Asia/Shanghai: unknown time zone" until this fix.
	// Asia/Shanghai has no DST so FixedZone is exact-equivalent here,
	// and it works anywhere Go runs (no tzdata dependency).
	loc := chinaTimezone()
	// Anchor in target timezone. Iterate minute-by-minute; this is fast
	// enough for human-scale cadences (rarely more than a few thousand
	// minutes to next fire) and the alternative (computing fields
	// directly) reinvents Vixie cron with bugs.
	t := now.In(loc).Add(time.Minute)
	t = t.Truncate(time.Minute)
	deadline := now.Add(4 * 365 * 24 * time.Hour) // hard cap: 4 years
	for {
		if t.After(deadline) {
			return time.Time{}, fmt.Errorf("no fire time within 4 years")
		}
		if expr.matches(t) {
			return t.UTC(), nil
		}
		t = t.Add(time.Minute)
	}
}

// chinaTimezone returns a *time.Location fixed at UTC+8 (Asia/Shanghai's
// offset; the zone has no DST so this is exact-equivalent to the IANA
// zone without depending on the host's tzdata). Pulled out so tests can
// reference the same constant — they used to call time.LoadLocation
// directly and so didn't catch the scratch-image bug this replaces.
func chinaTimezone() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}

// matches evaluates all 5 fields against t. CRITICAL: t.Hour() / t.Minute()
// / etc. return the *local* timezone's wall-clock components — if t is
// stored in UTC but the schedule targets Asia/Shanghai, t.Hour() reads
// UTC hour, not SGT hour. Caller MUST pass t already in the schedule's
// target timezone (NextAfter does `now.In(loc)` first).
func (e *cronExpr) matches(t time.Time) bool {
	if !e.minute.match(t.Minute()) {
		return false
	}
	if !e.hour.match(t.Hour()) {
		return false
	}
	if !e.month.match(int(t.Month())) {
		return false
	}
	// dom vs dow OR-logic (Vixie cron)
	domOK := e.dom.match(t.Day())
	dowOK := e.dow.match(weekdaySundayZero(t.Weekday()))
	switch {
	case e.domStar && e.dowStar:
		return true // both "*" — already passed month/hour/minute gates
	case e.domStar:
		return dowOK
	case e.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// weekdaySundayZero maps time.Weekday so Sunday = 0 and 7 both
// resolve to 0 (Vixie cron permits both for Sunday).
func weekdaySundayZero(w time.Weekday) int {
	n := int(w)
	if n == 0 {
		return 0 // Sunday
	}
	// time.Sunday == 0, time.Monday == 1, ..., time.Saturday == 6
	return n
}