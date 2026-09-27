package processing

import (
	"math"
	"testing"

	"engram/internal/config"
	"engram/internal/rpc"
)

// TestTrustWeight_MirrorsConfig pins the mirror: TrustWeight's
// defaults must equal config.Default's weights exactly. Two sources
// for one tuning is driftable — this test breaks loudly on drift
// instead of silently mis-ranking. (Test-only import; prod code
// takes injected weights via query Spec.)
func TestTrustWeight_MirrorsConfig(t *testing.T) {
	d := config.Default()
	if TrustWeight(rpc.ACLLow) != d.TrustLow ||
		TrustWeight(rpc.ACLUser) != d.TrustUser ||
		TrustWeight(rpc.ACLSystem) != d.TrustSystem {
		t.Fatalf("TrustWeight %v/%v/%v vs config %v/%v/%v",
			TrustWeight(rpc.ACLLow), TrustWeight(rpc.ACLUser), TrustWeight(rpc.ACLSystem),
			d.TrustLow, d.TrustUser, d.TrustSystem)
	}
}

// TestRanker_FR6_Curve pins the specified shape: linear in hits,
// monotonic in R, ordered by trust tier, log-dampened in corr.
func TestRanker_FR6_Curve(t *testing.T) {
	base := RankScore(2, 1.0, 1.0, 0, RankBeta)
	if base != 2.0 {
		t.Fatalf("hits must scale linearly, got %v", base)
	}
	if got := RankScore(2, 0.25, 1.0, 0, RankBeta); got >= base {
		t.Fatalf("lower R must score lower, got %v vs %v", got, base)
	}
	if got := RankScore(2, 1.0, TrustWeight(rpc.ACLSystem), 0, RankBeta); got <= base {
		t.Fatalf("system trust must outscore user, got %v vs %v", got, base)
	}
	c1 := RankScore(2, 1.0, 1.0, 1, RankBeta)
	c2 := RankScore(2, 1.0, 1.0, 2, RankBeta)
	c7 := RankScore(2, 1.0, 1.0, 7, RankBeta)
	if !(c1 > base && c2 > c1 && c7 > c2 && c2-c1 < c1-base) {
		t.Fatalf("corr must boost with diminishing marginal returns: base=%v c1=%v c2=%v c7=%v", base, c1, c2, c7)
	}
	want := 2.0 * 1.0 * 1.0 * (1 + math.Log(8))
	if math.Abs(c7-want) > 1e-9 {
		t.Fatalf("exact formula mismatch: got %v want %v", c7, want)
	}
}

// TestRanker_FR6_Guards proves fail-closed inputs: zero hits,
// unknown tier, NaN trust, corrupt beta.
func TestRanker_FR6_Guards(t *testing.T) {
	if got := RankScore(0, 1.0, 1.0, 5, RankBeta); got != 0 {
		t.Fatalf("zero hits must score 0, got %v", got)
	}
	if TrustWeight(rpc.AclTier("bogus")) != 0 {
		t.Fatal("unknown tier must weigh 0")
	}
	if got := RankScore(2, 1.0, math.NaN(), 0, RankBeta); got != 0 {
		t.Fatalf("NaN trust must score 0, got %v", got)
	}
	if got := RankScore(2, 1.0, 1.0, 0, math.NaN()); got != 2.0 {
		t.Fatalf("NaN beta must fall back to RankBeta, got %v", got)
	}
	if got := RankScore(2, 1.0, 1.0, -3, RankBeta); got != 2.0 {
		t.Fatalf("negative corr must clamp to 0, got %v", got)
	}
}

// TestRetrievability_Monotonic pins decay direction and strength lift.
func TestRetrievability_Monotonic(t *testing.T) {
	if got := Retrievability(100, 100, 0); got != 1.0 {
		t.Fatalf("fresh record must retrieve at 1, got %v", got)
	}
	fresh, old := Retrievability(100, 90, 0), Retrievability(100, 0, 0)
	if !(fresh > old && old > 0) {
		t.Fatalf("decay must be monotonic and positive: %v vs %v", fresh, old)
	}
	if got := Retrievability(100, 0, 3); got <= old {
		t.Fatalf("strength must lift retrievability, got %v vs %v", got, old)
	}
	if got := Retrievability(50, 100, 0); got != 1.0 {
		t.Fatalf("future txn must clamp fresh, got %v", got)
	}
}

// TestDedup_FR6_PerSubject pins one-record-per-entity and order.
func TestDedup_FR6_PerSubject(t *testing.T) {
	in := []Scored{
		{Record: grec("lo", "Pump", "runs", "yes", 1, 2, "s", "s1", rpc.ACLUser, rpc.StatusActive, 1, nil), Hits: 1, Score: 0.5},
		{Record: grec("hi", "pump", "runs", "no", 1, 2, "s", "s2", rpc.ACLUser, rpc.StatusActive, 2, nil), Hits: 2, Score: 0.9},
		{Record: grec("other", "valve", "runs", "yes", 1, 2, "s", "s1", rpc.ACLUser, rpc.StatusActive, 3, nil), Hits: 1, Score: 0.7},
	}
	got := DedupBySubject(in)
	if len(got) != 2 || got[0].Record.ID != "hi" || got[1].Record.ID != "other" {
		t.Fatalf("want [hi other] ordered, got %v", got)
	}
	if len(in) != 3 {
		t.Fatal("input must not be mutated")
	}
}
