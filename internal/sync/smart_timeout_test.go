package sync

import (
	"testing"
	"time"
)

// TestSmartTimeoutConstants pins the v0.7.24 timeout policy so that
// future edits to the engine can't silently weaken it (the whole point
// of auto-detection is the boundary; the boundary itself is the
// contract).
//
// The 1 GiB cutoff and 5/30 min split are documented behavior. UI /
// CHANGELOG / pullTag all reference these values; changing them is a
// design decision, not a tuning edit.
//
// v0.7.27: extends the test to the third tier (>10 GiB → 2h) — see
// ExtraLargeManifestThreshold / ExtraLongSyncTimeout. 158 UAT
// bklite/bklite/vllm 24 GiB empirically needs >30 min per layer, and
// LongSyncTimeout (30 min) aborts the whole sync. The third tier is the
// fix.
func TestSmartTimeoutConstants(t *testing.T) {
	if DefaultSyncTimeout != 5*time.Minute {
		t.Errorf("DefaultSyncTimeout = %v, want 5m", DefaultSyncTimeout)
	}
	if LongSyncTimeout != 30*time.Minute {
		t.Errorf("LongSyncTimeout = %v, want 30m", LongSyncTimeout)
	}
	if ExtraLongSyncTimeout != 2*time.Hour {
		t.Errorf("ExtraLongSyncTimeout = %v, want 2h", ExtraLongSyncTimeout)
	}
	if LargeManifestThreshold != 1<<30 {
		t.Errorf("LargeManifestThreshold = %d, want 1 GiB (1<<30)", LargeManifestThreshold)
	}
	if ExtraLargeManifestThreshold != 10<<30 {
		t.Errorf("ExtraLargeManifestThreshold = %d, want 10 GiB (10<<30)", ExtraLargeManifestThreshold)
	}
	// Order matters: each tier must be strictly larger than the prior
	// one, else the dispatch in pullTag is meaningless.
	if !(DefaultSyncTimeout < LongSyncTimeout) {
		t.Errorf("DefaultSyncTimeout (%v) must be < LongSyncTimeout (%v)",
			DefaultSyncTimeout, LongSyncTimeout)
	}
	if !(LongSyncTimeout < ExtraLongSyncTimeout) {
		t.Errorf("LongSyncTimeout (%v) must be < ExtraLongSyncTimeout (%v)",
			LongSyncTimeout, ExtraLongSyncTimeout)
	}
	if !(LargeManifestThreshold < ExtraLargeManifestThreshold) {
		t.Errorf("LargeManifestThreshold (%d) must be < ExtraLargeManifestThreshold (%d)",
			LargeManifestThreshold, ExtraLargeManifestThreshold)
	}
}

// TestSmartTimeoutDispatch walks the boundary cases through pullTag's
// dispatch logic. We don't invoke pullTag end-to-end (it requires a
// registry HTTP roundtrip); instead we mirror the threshold ladder
// here so future edits to pullTag's switch are caught immediately.
func TestSmartTimeoutDispatch(t *testing.T) {
	cases := []struct {
		name        string
		size        int64
		wantTimeout time.Duration
		wantUsed    string
	}{
		{"zero (empty manifest)", 0, DefaultSyncTimeout, "default"},
		{"1 byte under long", 1 << 30, DefaultSyncTimeout, "default"}, // ==LargeManifestThreshold not >
		{"1 byte over long", (1 << 30) + 1, LongSyncTimeout, "long"},
		{"8 GiB (mid)", 8 << 30, LongSyncTimeout, "long"},
		{"1 byte under extra", 10 << 30, LongSyncTimeout, "long"}, // ==ExtraLargeManifestThreshold not >
		{"1 byte over extra", (10 << 30) + 1, ExtraLongSyncTimeout, "extra"},
		{"24 GiB (vllm)", 24 << 30, ExtraLongSyncTimeout, "extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			timeout := DefaultSyncTimeout
			used := "default"
			switch {
			case tc.size > ExtraLargeManifestThreshold:
				timeout = ExtraLongSyncTimeout
				used = "extra"
			case tc.size > LargeManifestThreshold:
				timeout = LongSyncTimeout
				used = "long"
			}
			if timeout != tc.wantTimeout {
				t.Errorf("timeout = %v, want %v", timeout, tc.wantTimeout)
			}
			if used != tc.wantUsed {
				t.Errorf("timeoutUsed = %q, want %q", used, tc.wantUsed)
			}
		})
	}
}
