// Read-path parity for the B1 rule (dogfood 2026-09-29): recall's
// confidence fold discounts self-confirms exactly like intake's.
package query

import (
	"testing"

	"engram/internal/rpc"
)

func drec() rpc.Record {
	return rpc.Record{ID: "r", Subject: "s", Predicate: "p", Object: "o",
		Provenance:     rpc.Provenance{Source: "s", Session: "s1"},
		BaseConfidence: 0.5, ACL: rpc.ACLUser}
}

func dev(kind rpc.FeedbackKind, src, ses string) rpc.FeedbackEvent {
	return rpc.FeedbackEvent{RecordID: "r", Kind: kind, Source: src, Session: ses}
}

// TestConfidenceOf_SelfConfirmDiscounted: three solo self-confirms must
// score base (the probe's 0.84→0.90 walk is now flat); one foreign
// confirm must lift.
func TestConfidenceOf_SelfConfirmDiscounted(t *testing.T) {
	rec := drec()
	self := []rpc.FeedbackEvent{
		dev(rpc.FeedbackAttestConfirm, "s", "s1"),
		dev(rpc.FeedbackAttestConfirm, "s", "s1"),
		dev(rpc.FeedbackAttestConfirm, "s", "s1"),
	}
	if conf, _ := confidenceOf(rec, self); conf != 0.5 {
		t.Fatalf("solo self-confirms must score base, got %v", conf)
	}
	foreign := append(append([]rpc.FeedbackEvent(nil), self...),
		dev(rpc.FeedbackAttestConfirm, "o", "s2"))
	if conf, _ := confidenceOf(rec, foreign); conf <= 0.5 {
		t.Fatalf("foreign confirm must lift, got %v", conf)
	}
}

// TestCountStrength_IndependentOnly: self-authored strengths (pre-fix
// logs hold them) must not feed R_m; cross-session ones must.
func TestCountStrength_IndependentOnly(t *testing.T) {
	self := rpc.Provenance{Source: "s", Session: "s1"}
	evs := []rpc.FeedbackEvent{
		dev(rpc.FeedbackStrength, "s", "s1"),
		dev(rpc.FeedbackStrength, "o", "s2"),
	}
	if n := countStrength(self, evs); n != 1 {
		t.Fatalf("want 1 independent strength, got %d", n)
	}
}
