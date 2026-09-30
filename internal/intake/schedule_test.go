package intake

// Write-side SCHEDULED labeling (DESIGN §1 VALID-NOW, P1 Phase B):
// a version whose valid-time starts in the future commits SCHEDULED
// (hiding, not gone). Windows use wall-clock-relative offsets —
// direction-deterministic, never flaky: +1hr is future on any clock,
// [1,2) is past on any clock.

import (
	"testing"
	"time"

	"engram/internal/rpc"
)

func futureFrom() int64 { return time.Now().UnixNano() + 3600e9 }

// TestEmit_FutureScheduled pins labeling: a future-dated insert lands
// SCHEDULED, addressable and indexed (findable for as_of, invisible
// to recall by the Phase-A loop).
func TestEmit_FutureScheduled(t *testing.T) {
	e, s, idx := newEmitter(t, 100)
	in := ein("launch", "date", "june", "s", "s1", rpc.ACLUser)
	in.ValidTime = rpc.Interval{From: futureFrom(), To: futureFrom() + 3600e9}
	res, err := e.Emit(in)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if res.Verdict != rpc.VerdictInsert {
		t.Fatalf("future insert is still an INSERT, got %+v", res)
	}
	rec, err := s.Get(res.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != rpc.StatusScheduled {
		t.Fatalf("future-dated version must be SCHEDULED, got %v", rec.Status)
	}
	if got := idx.Exact("launch", "date"); len(got) != 1 || got[0].ID != res.ID {
		t.Fatalf("scheduled version must index under its new id, got %+v", got)
	}
}

// TestEmit_PastStaysActive pins the scheduler's negative leg: an
// already-open window commits ACTIVE — the label fires on futurity,
// never by default.
func TestEmit_PastStaysActive(t *testing.T) {
	e, s, _ := newEmitter(t, 100)
	res, err := e.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rec, err := s.Get(res.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != rpc.StatusActive {
		t.Fatalf("open-window version must stay ACTIVE, got %v", rec.Status)
	}
}

// TestCorrect_FutureScheduled pins the correct leg: a future-dated
// rewrite schedules through the shared choke point (correctAck),
// not just the emit legs.
func TestCorrect_FutureScheduled(t *testing.T) {
	e, _, _ := newEmitter(t, 100)
	if _, err := e.Emit(ein("lamp", "state", "off", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("base: %v", err)
	}
	fix := ein("lamp", "state", "on", "s", "s2", rpc.ACLUser)
	fix.ValidTime = rpc.Interval{From: futureFrom(), To: futureFrom() + 3600e9}
	rec, v, err := e.Correct(CorrectHint{Subject: "lamp", Predicate: "state", Object: "off"}, fix)
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if v != rpc.VerdictUpdate {
		t.Fatalf("labile rewrite must UPDATE, got %v", v)
	}
	if rec.Status != rpc.StatusScheduled {
		t.Fatalf("future-dated correction must be SCHEDULED, got %v", rec.Status)
	}
}
