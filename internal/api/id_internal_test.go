package api

import (
	"strings"
	"sync"
	"testing"
)

// hexChars is the alphabet randHex is required to emit.
const hexChars = "0123456789abcdef"

// TestNewIDUniqueUnderConcurrency pins the v0.5.10 fix and its v0.5.11 widening.
//
// The previous implementation built the random suffix from
// time.Now().UnixNano()%16 plus a time.Sleep(time.Microsecond) per character.
// The low bits of the clock repeat under load, so two concurrent creates could
// receive the SAME id — and because both the credential and the proxy store key
// by id with overwrite semantics, the second write silently destroyed the
// first. Measured on registry.example.com: 20 concurrent POST /api/credentials lost 3
// entries (18 remained instead of 21).
//
// A collision here is silent data loss, so the guard is deliberately wide: 400
// ids created as simultaneously as the runtime allows must all be distinct.
//
// v0.5.11: at a 16-bit suffix this guard failed 13 runs out of 20 — the birthday
// collision rate for 400 ids sharing one millisecond is ~1.2. At 32 bits it is
// ~2e-5, so a failure now signals a real regression instead of a coin flip.
func TestNewIDUniqueUnderConcurrency(t *testing.T) {
	const n = 400

	ids := make([]string, n)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // release every goroutine at once to maximise overlap
			ids[i] = newID()
		}(i)
	}
	start.Done()
	done.Wait()

	counts := make(map[string]int, n)
	for _, id := range ids {
		counts[id]++
	}
	if len(counts) == n {
		return
	}

	dups := make([]string, 0, 4)
	for id, c := range counts {
		if c > 1 && len(dups) < 4 {
			dups = append(dups, id)
		}
	}
	t.Fatalf("newID returned only %d unique ids out of %d (collisions would silently overwrite entries); e.g. %v",
		len(counts), n, dups)
}

// TestRandHexShape checks length and alphabet — an id is used verbatim in URL
// paths and JSON keys, so it must stay lowercase hex of the requested width.
func TestRandHexShape(t *testing.T) {
	for _, n := range []int{1, 2, 4, 7, 16} {
		got := randHex(n)
		if len(got) != n {
			t.Fatalf("randHex(%d) = %q: length %d, want %d", n, got, len(got), n)
		}
		for _, c := range got {
			if !strings.ContainsRune(hexChars, c) {
				t.Fatalf("randHex(%d) = %q: %q is not lowercase hex", n, got, c)
			}
		}
	}
}

// TestRandHexDistinctSamples is an entropy guard: randHex must not be a
// function of the clock. 200 draws from a 4-hex-digit space (65536) collide
// ~0.3 times by birthday, so tolerating 5 is still a decisive signal — the
// clock-derived version produced far fewer distinct values.
func TestRandHexDistinctSamples(t *testing.T) {
	const draws = 200
	seen := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		seen[randHex(4)] = struct{}{}
	}
	if len(seen) < draws-5 {
		t.Fatalf("randHex(4) produced only %d distinct values in %d draws; the suffix looks clock-derived, not random",
			len(seen), draws)
	}
}

// TestNewIDShape documents the id format the UI and the API expose.
func TestNewIDShape(t *testing.T) {
	id := newID()
	// 20060102-150405.000 -> "20060102-150405-000" (dots become dashes)
	if len(id) != len("20060102-150405-000")+1+8 {
		t.Fatalf("newID() = %q: length %d, want %d", id, len(id),
			len("20060102-150405-000")+1+8)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 4 {
		t.Fatalf("newID() = %q: want 4 dash-separated parts, got %d", id, len(parts))
	}
	if len(parts[3]) != 8 {
		t.Fatalf("newID() = %q: random suffix %q should be 8 chars", id, parts[3])
	}
	for _, c := range parts[3] {
		if !strings.ContainsRune(hexChars, c) {
			t.Fatalf("newID() = %q: suffix %q is not lowercase hex", id, parts[3])
		}
	}
}
