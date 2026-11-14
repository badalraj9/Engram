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
	"engram/internal/truth"
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

// scheduledWith scans for SCHEDULED versions under one key — the
// promotion tests assert labeling by status, not by id (scheduling
// mints a new version id by construction).
func scheduledWith(t *testing.T, s *truth.Store, sub, pred string) []rpc.Record {
	t.Helper()
	var out []rpc.Record
	err := s.Scan(0, func(ev truth.Event) error {
		if ev.Kind != truth.EventRecord {
			return nil
		}
		r := ev.Record
		if r.Subject == sub && r.Predicate == pred && r.Status == rpc.StatusScheduled {
			fresh, err := s.Get(r.ID)
			if err != nil {
				return nil
			}
			if fresh.Status == rpc.StatusScheduled {
				out = append(out, fresh)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// TestAttest_PromoteFutureScheduled pins N2 on the attest leg (the
// probe's repro): STAGED future → independent confirm → promoted AND
// SCHEDULED. The label must not lag until a later UPDATE.
func TestAttest_PromoteFutureScheduled(t *testing.T) {
	e, s, _ := newEmitter(t, 100)
	in := ein("omen", "says", "rain", "low-x", "s9", rpc.ACLLow)
	in.ValidTime = rpc.Interval{From: futureFrom(), To: futureFrom() + 3600e9}
	st, err := e.Emit(in)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if st.Verdict != rpc.VerdictStaged {
		t.Fatalf("low must STAGE, got %+v", st)
	}
	if _, promoted, err := e.Attest(st.ID, true, "s", "s2"); err != nil || !promoted {
		t.Fatalf("independent confirm must promote: promoted=%v err=%v", promoted, err)
	}
	old, err := s.Get(st.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if old.Status != rpc.StatusTombstoned {
		t.Fatalf("scheduling must close the promoted version, got %v", old.Status)
	}
	got := scheduledWith(t, s, "omen", "says")
	if len(got) != 1 || got[0].Object != "rain" {
		t.Fatalf("exactly one SCHEDULED successor expected, got %+v", got)
	}
}

// TestEmit_PromoteFutureScheduled pins N2 on the corroborated leg:
// a staged future version promoted by independent corroboration
// schedules — same helper, same rule.
func TestEmit_PromoteFutureScheduled(t *testing.T) {
	e, s, _ := newEmitter(t, 100)
	in := ein("rumor", "says", "x", "low-x", "s9", rpc.ACLLow)
	in.ValidTime = rpc.Interval{From: futureFrom(), To: futureFrom() + 3600e9}
	if _, err := e.Emit(in); err != nil {
		t.Fatalf("stage: %v", err)
	}
	corro := ein("rumor", "says", "x", "s", "s2", rpc.ACLUser)
	// Past window (ein default): the corroborator itself stays ACTIVE,
	// so the scan below isolates the promoted successor — exactly one
	// SCHEDULED row.
	res, err := e.Emit(corro)
	if err != nil {
		t.Fatalf("corroborate: %v", err)
	}
	if res.Verdict != rpc.VerdictInsert {
		t.Fatalf("staged-invisible gate must INSERT, got %+v", res)
	}
	if staged, err := s.ListStaged(); err != nil || len(staged) != 0 {
		t.Fatalf("promotion must peel the marker, got %+v err=%v", staged, err)
	}
	got := scheduledWith(t, s, "rumor", "says")
	if len(got) != 1 || got[0].Object != "x" {
		t.Fatalf("exactly one SCHEDULED successor expected, got %+v", got)
	}
}
