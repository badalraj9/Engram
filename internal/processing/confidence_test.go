package processing

import (
	"math"
	"testing"

	"engram/internal/rpc"
)

func fb(kind rpc.FeedbackKind, tick int64) rpc.FeedbackEvent {
	return rpc.FeedbackEvent{RecordID: "r", Kind: kind, Source: "s", Session: "s1", Tick: tick}
}

// TestConfidence_FR17_FeedbackReplay proves confidence is computed from
// base + replayed events, never mutated state: replay is deterministic
// under event permutation, and the base is untouched (inputs are
// values; nothing to overwrite — FR17 by construction).
func TestConfidence_FR17_FeedbackReplay(t *testing.T) {
	evs := []rpc.FeedbackEvent{fb(rpc.FeedbackAttestConfirm, 3), fb(rpc.FeedbackAttestConfirm, 1), fb(rpc.FeedbackAttestDisconfirm, 2)}
	a := ApplyFeedback(0.5, evs)
	b := ApplyFeedback(0.5, []rpc.FeedbackEvent{evs[1], evs[2], evs[0]})
	if a != b {
		t.Fatalf("replay must be order-stable, got %v vs %v", a, b)
	}
	if !(a > 0.5 && a < 1) {
		t.Fatalf("two confirms + one disconfirm on 0.5 must land inside (0.5,1), got %v", a)
	}
	// Correct resets to base: old adjustments don't carry over.
	if got := ApplyFeedback(0.9, []rpc.FeedbackEvent{fb(rpc.FeedbackCorrect, 5)}); got != 0.9 {
		t.Fatalf("correct must reset to base 0.9, got %v", got)
	}
	// Strength is confidence-neutral.
	if got := ApplyFeedback(0.6, []rpc.FeedbackEvent{fb(rpc.FeedbackStrength, 1)}); got != 0.6 {
		t.Fatalf("strength must not move confidence, got %v", got)
	}
}

// TestConfidence_FR9_Combination proves the two combination rules pull
// apart: clustered noisy-OR counts witnesses (not records), and
// derived takes the weakest link.
func TestConfidence_FR9_Combination(t *testing.T) {
	if got := NoisyOR(0.5, 0.5); got != 0.75 {
		t.Fatalf("noisy-OR(0.5,0.5) must be 0.75, got %v", got)
	}
	if got := NoisyOR(); got != 0 {
		t.Fatalf("no witnesses must be 0, got %v", got)
	}
	if got := WeakestLink(0.9, 0.4, 0.7); got != 0.4 {
		t.Fatalf("weakest link must be min, got %v", got)
	}
	// Clustered (2 witnesses) vs raw (5 records): different numbers.
	clustered := Score(0.5, []float64{0.5}, nil)
	raw := NoisyOR(0.5, 0.5, 0.5, 0.5, 0.5)
	if clustered == raw {
		t.Fatalf("clustered witnesses must differ from raw count: %v", clustered)
	}
	if clustered != 0.75 {
		t.Fatalf("2-cluster score on 0.5 must be 0.75, got %v", clustered)
	}
}

// TestConfidence_Bounds pins clamping and the attest directions:
// confirm rises, disconfirm falls, both stay in [0,1], NaN fails to 0.
func TestConfidence_Bounds(t *testing.T) {
	if got := AttestUpdate(0.5, true); !(got > 0.5) {
		t.Fatalf("confirm must rise, got %v", got)
	}
	if got := AttestUpdate(0.5, false); !(got < 0.5) {
		t.Fatalf("disconfirm must fall, got %v", got)
	}
	for _, c := range []float64{0, 1} {
		if got := AttestUpdate(c, true); got < 0 || got > 1 {
			t.Fatalf("confirm(%v) out of bounds: %v", c, got)
		}
		if got := AttestUpdate(c, false); got < 0 || got > 1 {
			t.Fatalf("disconfirm(%v) out of bounds: %v", c, got)
		}
	}
	if got := Clamp01(math.NaN()); got != 0 {
		t.Fatalf("NaN must fail to 0, got %v", got)
	}
	if got := Score(2, nil, nil); got != 1 {
		t.Fatalf("out-of-range base must clamp to 1, got %v", got)
	}
}
