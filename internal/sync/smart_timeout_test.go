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
func TestSmartTimeoutConstants(t *testing.T) {
	if DefaultSyncTimeout != 5*time.Minute {
		t.Errorf("DefaultSyncTimeout = %v, want 5m", DefaultSyncTimeout)
	}
	if LongSyncTimeout != 30*time.Minute {
		t.Errorf("LongSyncTimeout = %v, want 30m", LongSyncTimeout)
	}
	if LargeManifestThreshold != 1<<30 {
		t.Errorf("LargeManifestThreshold = %d, want 1 GiB (1<<30)", LargeManifestThreshold)
	}
	// Order matters: long must be strictly larger than default, else
	// the dispatch in pullTag is meaningless.
	if !(DefaultSyncTimeout < LongSyncTimeout) {
		t.Errorf("DefaultSyncTimeout (%v) must be < LongSyncTimeout (%v)",
			DefaultSyncTimeout, LongSyncTimeout)
	}
}
