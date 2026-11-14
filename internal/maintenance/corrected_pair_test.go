package maintenance

// F2 pin (tester finding, 2026-11-13): a corrected pair must never
// reach BOTH-state. Both correct paths tombstone the superseded row
// (intake/correct_attest.go:93 labile Supersede, :104 late Restatus)
// and the DESIGN §4.10 sweep skips TOMBSTONED — this test pins that
// chain end-to-end through the tick (FR5: corrections rewrite, they
// do not coexist as contradictions).

import (
	"testing"

	"engram/internal/intake"
	"engram/internal/rpc"
	"engram/internal/truth"
)

func TestTick_CorrectedPairNotFlagged(t *testing.T) {
	e, s, idx := mopen(t)
	orig, err := e.Emit(minput("lang", "choice", "go", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	rec, verdict, err := e.Correct(
		intake.CorrectHint{Subject: "lang", Predicate: "choice", Object: "go"},
		minput("lang", "choice", "rust", "s", "s2", rpc.ACLUser),
	)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if verdict != rpc.VerdictUpdate {
		t.Fatalf("same-tick correction must take the labile supersede path, got %v", verdict)
	}
	// The superseded row is marked at correct time — store-verified.
	old, err := s.Get(orig.ID)
	if err != nil {
		t.Fatalf("get old: %v", err)
	}
	if old.Status != rpc.StatusTombstoned {
		t.Fatalf("superseded row must tombstone on correct, got %v", old.Status)
	}
	newer, err := s.Get(rec.ID)
	if err != nil {
		t.Fatalf("get new: %v", err)
	}
	if newer.Status != rpc.StatusActive {
		t.Fatalf("correction must stay ACTIVE, got %v", newer.Status)
	}
	// The detector must agree: a full tick flags nothing.
	if _, err := Consolidate(s, idx, 0, tickWall); err != nil {
		t.Fatalf("tick: %v", err)
	}
	both := 0
	err = s.Scan(0, func(ev truth.Event) error {
		if ev.Kind == truth.EventRecord && ev.Record.Status == rpc.StatusBoth {
			both++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if both != 0 {
		t.Fatalf("corrected pair must never enter BOTH-state, found %d", both)
	}
}
