package intake

import (
	"testing"

	"engram/internal/rpc"
)

// TestCorrect_LabileRewrite pins FR5's hot path: located record,
// in-window supersede, lineage-correct confidence reset.
func TestCorrect_LabileRewrite(t *testing.T) {
	e, _, _ := newEmitter(t, 100)
	res, err := e.Emit(ein("db", "runs", "Badger", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	rec, v, err := e.Correct(CorrectHint{Subject: "db", Predicate: "runs", Object: "Badger"},
		ein("db", "runs", "Pebble", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if v != rpc.VerdictUpdate || rec.Object != "Pebble" {
		t.Fatalf("labile correction must supersede, got %v %+v", v, rec)
	}
	if rec.ID == res.ID {
		t.Fatal("supersede must append a new version, never mutate")
	}
}

// TestCorrect_FloorMissInsert pins the anti-argmax rule: a hint no
// candidate resembles becomes a new claim, never a wrong rewrite.
func TestCorrect_FloorMissInsert(t *testing.T) {
	e, s, _ := newEmitter(t, 100)
	orig, err := e.Emit(ein("db", "runs", "Badger", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	rec, v, err := e.Correct(CorrectHint{Subject: "db", Predicate: "runs", Object: "quantum flux capacitor"},
		ein("db", "runs", "Pebble", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if v != rpc.VerdictInsert {
		t.Fatalf("floor miss must INSERT, got %v", v)
	}
	untouched, err := s.Get(orig.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if untouched.Object != "Badger" {
		t.Fatal("floor miss must leave the original untouched")
	}
	_ = rec
}

// TestAttest_ConfirmDisconfirm pins FR18: confirm lifts confidence
// and strength; disconfirm lowers without touching strength;
// self-confirm never promotes.
func TestAttest_ConfirmDisconfirm(t *testing.T) {
	e, s, _ := newEmitter(t, 100)
	staged, err := e.Emit(ein("pump", "runs", "yes", "low-src", "s9", rpc.ACLLow))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Self-attestation: same composite as the write — no promotion, and
	// (closed by dogfood 2026-09-29, B1) no scoring lift either: the
	// confirm is recorded in the log but discounted in both folds, and
	// no strength event ships. [SUPERSEDED 2026-09-29: this block
	// previously asserted self-confirm lifts confidence above base —
	// that assertion was the self-dealing lever (0.84→0.90 solo);
	// author's conviction is already in base.]
	conf1, promoted, err := e.Attest(staged.ID, true, "low-src", "s9")
	if err != nil {
		t.Fatalf("self-attest: %v", err)
	}
	if promoted {
		t.Fatal("self-attestation must never promote")
	}
	if conf1 != 0.5 {
		t.Fatalf("self-confirm must score nothing (base 0.5), got %v", conf1)
	}
	for _, ev := range s.FeedbackFor(staged.ID) {
		if ev.Kind == rpc.FeedbackStrength {
			t.Fatal("self-confirm must ship no strength event")
		}
	}
	// Independent confirm promotes.
	conf2, promoted, err := e.Attest(staged.ID, true, "ops", "s2")
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if !promoted {
		t.Fatal("independent confirm must promote")
	}
	if conf2 <= conf1 {
		t.Fatalf("second witness must lift further, %v vs %v", conf2, conf1)
	}
	if ok, _ := s.IsStaged(staged.ID); ok {
		t.Fatal("promoted marker must peel")
	}
	// Disconfirm lowers, no strength path involved.
	live, err := e.Emit(ein("valve", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	conf3, _, err := e.Attest(live.ID, false, "ops", "s3")
	if err != nil {
		t.Fatalf("disconfirm: %v", err)
	}
	if conf3 >= 0.5 {
		t.Fatalf("disconfirm must lower confidence below base, got %v", conf3)
	}
}

// TestAttest_Reject pins unknown IDs and blank attestors.
func TestAttest_Reject(t *testing.T) {
	e, _, _ := newEmitter(t, 100)
	if _, _, err := e.Attest("missing", true, "s", "s1"); err == nil {
		t.Fatal("unknown ID must fail")
	}
	live, err := e.Emit(ein("v", "p", "o", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, _, err := e.Attest(live.ID, true, "", ""); err == nil {
		t.Fatal("blank attester must fail closed")
	}
}

// TestDispatch_Shape pins the payload-shape router + attest bias.
func TestDispatch_Shape(t *testing.T) {
	if Dispatch(true, true) != OpCorrect {
		t.Fatal("replacement content on a prior claim → correct")
	}
	if Dispatch(true, false) != OpAttest {
		t.Fatal("pure verdict on a prior claim → attest (asymmetric-cost bias)")
	}
	if Dispatch(false, true) != OpEmit || Dispatch(false, false) != OpEmit {
		t.Fatal("new content → emit")
	}
}
