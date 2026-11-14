// DiscountSelf proof (FR9/FR14 — dogfood B1, 2026-09-29).
package processing

import (
	"testing"

	"engram/internal/rpc"
)

func selfEv(kind rpc.FeedbackKind, src, ses string) rpc.FeedbackEvent {
	return rpc.FeedbackEvent{RecordID: "r", Kind: kind, Source: src, Session: ses}
}

// TestDiscountSelf_Rule pins the closed lever: same-composite confirms
// drop from the fold (order preserved); self-disconforms, foreign
// confirms, and other kinds pass through.
func TestDiscountSelf_Rule(t *testing.T) {
	self := rpc.Provenance{Source: "s", Session: "s1"}
	evs := []rpc.FeedbackEvent{
		selfEv(rpc.FeedbackAttestConfirm, "s", "s1"),    // dropped
		selfEv(rpc.FeedbackAttestDisconfirm, "s", "s1"), // kept
		selfEv(rpc.FeedbackAttestConfirm, "o", "s2"),    // kept
		selfEv(rpc.FeedbackStrength, "s", "s1"),         // kept (write-gated, not here)
		selfEv(rpc.FeedbackAttestConfirm, "s", "s9"),    // kept (session differs)
	}
	got := DiscountSelf(self, evs)
	if len(got) != 4 {
		t.Fatalf("want 4 survivors, got %d", len(got))
	}
	if got[0].Kind != rpc.FeedbackAttestDisconfirm || got[1].Source != "o" {
		t.Fatalf("order/content broken: %+v", got)
	}
	// Fold parity: discounted self-confirm scores exactly base.
	if c := ApplyFeedback(0.5, DiscountSelf(self,
		[]rpc.FeedbackEvent{selfEv(rpc.FeedbackAttestConfirm, "s", "s1")})); c != 0.5 {
		t.Fatalf("solo self-confirm must score base, got %v", c)
	}
	// Input list itself is never mutated (fold reads, log owns).
	if len(evs) != 5 {
		t.Fatalf("DiscountSelf must not mutate input, len %d", len(evs))
	}
}
