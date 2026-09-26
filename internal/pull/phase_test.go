package pull

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The UI binds to these exact JSON keys (web/src/types.ts PullPhase);
// a rename here silently empties the expanded row.
func TestPhaseJSONShape(t *testing.T) {
	total := int64(2048)
	p := Phase{Name: "blob:3", Digest: "sha256:aa", Status: PhaseSuccess, Bytes: 2048, TotalBytes: &total}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"name", "digest", "status", "bytes", "totalBytes", "message"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("phase JSON missing key %q: %s", k, raw)
		}
	}
	if m["totalBytes"].(float64) != 2048 {
		t.Fatalf("totalBytes = %v", m["totalBytes"])
	}

	// A nil total must serialize as null, not 0 — the UI renders a
	// "done / total" fraction only when totalBytes != null.
	p2 := Phase{Name: "manifest", Status: PhaseRunning}
	raw2, err := json.Marshal(p2)
	if err != nil {
		t.Fatalf("marshal p2: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(raw2, &m2); err != nil {
		t.Fatalf("unmarshal p2: %v", err)
	}
	if m2["totalBytes"] != nil {
		t.Fatalf("nil TotalBytes must marshal to null, got %v", m2["totalBytes"])
	}
}

// Views handed out before a mutation must keep their old snapshot
// (copy-on-write), otherwise a polling handler could read a torn phase.
func TestPhaseCopyOnWrite(t *testing.T) {
	j := &Job{view: JobView{ID: "j1", State: StateRunning}}
	appendPhase(j, Phase{Name: "manifest", Status: PhaseRunning})
	snap := j.View()

	appendPhase(j, Phase{Name: "blob:0", Status: PhaseRunning})
	updatePhase(j, 0, func(p *Phase) { p.Status = PhaseSuccess })

	if len(snap.Phases) != 1 || snap.Phases[0].Status != PhaseRunning {
		t.Fatalf("old snapshot mutated: %+v", snap.Phases)
	}
	cur := j.View()
	if len(cur.Phases) != 2 || cur.Phases[0].Status != PhaseSuccess {
		t.Fatalf("current view wrong: %+v", cur.Phases)
	}
	// An out-of-range update is a no-op, not a panic.
	updatePhase(j, 99, func(p *Phase) { p.Status = PhaseFailed })
	updatePhase(j, -1, func(p *Phase) { p.Status = PhaseFailed })
}

func TestSubmitSeedsManifestPhase(t *testing.T) {
	e := NewExecutor(2, func(context.Context, *Job) error { return nil })
	v := e.Submit(NewJob{SourceRef: "alpine:3.19"})
	if len(v.Phases) != 1 || v.Phases[0].Name != "manifest" || v.Phases[0].Status != PhasePending {
		t.Fatalf("queued job should start with a pending manifest phase: %+v", v.Phases)
	}
}

// A failed job must not leave a phase spinning as 'running' — the expanded
// row would show progress on a terminal job.
func TestExecuteOneMarksStragglerPhasesFailed(t *testing.T) {
	e := NewExecutor(2, func(ctx context.Context, j *Job) error {
		updatePhase(j, 0, func(p *Phase) { p.Status = PhaseRunning })
		appendPhase(j, Phase{Name: "blob:0", Status: PhaseRunning})
		return errors.New("boom")
	})
	v := e.Submit(NewJob{SourceRef: "alpine:3.19"})
	e.executeOne(context.Background(), e.jobs[v.ID])

	got := e.Get(v.ID)
	if got.State != StateFailed {
		t.Fatalf("state = %s", got.State)
	}
	for _, p := range got.Phases {
		if p.Status == PhaseRunning || p.Status == PhasePending {
			t.Fatalf("straggler phase %+v survived a failed job", p)
		}
	}
	if got.Phases[1].Message != "Execution failed" {
		t.Fatalf("straggler message = %q", got.Phases[1].Message)
	}
}

// Cancel mid-flight: the job ends cancelled and the in-flight phase is
// marked failed with a cancellation message (the UI has no cancelled
// phase status; the job-level pill already says cancelled).
func TestExecuteOneCancelledMarksPhases(t *testing.T) {
	started := make(chan struct{})
	e := NewExecutor(2, func(ctx context.Context, j *Job) error {
		updatePhase(j, 0, func(p *Phase) { p.Status = PhaseRunning })
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	v := e.Submit(NewJob{SourceRef: "alpine:3.19"})
	done := make(chan struct{})
	go func() {
		e.executeOne(context.Background(), e.jobs[v.ID])
		close(done)
	}()
	<-started
	if err := e.Cancel(v.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("executeOne did not return after cancel")
	}
	got := e.Get(v.ID)
	if got.State != StateCancelled {
		t.Fatalf("state = %s", got.State)
	}
	if got.Phases[0].Status != PhaseFailed || got.Phases[0].Message != "Task cancelled" {
		t.Fatalf("cancelled straggler = %+v", got.Phases[0])
	}
}

// TestExecuteOneRecoversFromPanic pins the second half of the v0.5.16 fix.
// The orchestrator is injected code; a nil dereference inside it used to
// propagate out of the worker goroutine and kill the whole process, taking
// the HTTP server and every in-memory job with it — the user saw the job
// they had just submitted vanish, and the history was empty. The panic is
// now contained per job: the row reports failed, and the queue keeps going.
func TestExecuteOneRecoversFromPanic(t *testing.T) {
	e := NewExecutor(2, func(ctx context.Context, j *Job) error {
		updatePhase(j, 0, func(p *Phase) { p.Status = PhaseRunning })
		panic("simulated nil dereference")
	})
	v := e.Submit(NewJob{SourceRef: "alpine:3.19"})
	e.executeOne(context.Background(), e.jobs[v.ID])

	got := e.Get(v.ID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
	if !strings.Contains(got.Error, "simulated nil dereference") {
		t.Fatalf("error should name the panic, got %q", got.Error)
	}
	if got.Phases[0].Status == PhaseRunning || got.Phases[0].Status == PhasePending {
		t.Fatalf("straggler phase survived: %+v", got.Phases[0])
	}
	// The executor must still be usable after a panic.
	if again := e.Submit(NewJob{SourceRef: "busybox:1.36"}); again.ID == "" {
		t.Fatal("executor stopped accepting jobs after a panic")
	}
}
