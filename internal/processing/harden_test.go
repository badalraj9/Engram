package processing

import (
	"fmt"
	"testing"

	"engram/internal/rpc"
)

// TestHarden_UnknownTierCandidate proves the fail-closed rank: a
// candidate at an unrecognized tier can never be written over —
// subordinate-or-not, the fold routes to BOTH, never to an
// unauthorized revision.
func TestHarden_UnknownTierCandidate(t *testing.T) {
	cands := []rpc.Record{grec("weird", "pump", "runs", "yes", 1, 5, "ops", "s0", rpc.AclTier("bogus"), rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "no", 2, 6, "ops", "s8", rpc.ACLSystem, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert || out.Status != rpc.StatusBoth || out.TargetID != "weird" || !out.FlagTargetBoth {
		t.Fatalf("system vs unknown-tier must flag BOTH, got %+v", out)
	}
}

// TestHarden_ShortSubjectTrigram pins the short-string rule: subjects
// under TrigramN runes compare as one whole-string gram instead of
// vanishing from the fuzzy band.
func TestHarden_ShortSubjectTrigram(t *testing.T) {
	g := Trigrams("ab")
	if len(g) != 1 {
		t.Fatalf("short subject must yield one gram, got %v", g)
	}
	if _, ok := g["ab"]; !ok {
		t.Fatalf("short-subject gram must be the whole string, got %v", g)
	}
	if got := TrigramJaccard("ab", "ab"); got != 1 {
		t.Fatalf("identical short subjects must score 1, got %v", got)
	}
	if got := TrigramJaccard("ab", "cd"); got != 0 {
		t.Fatalf("disjoint short subjects must score 0, got %v", got)
	}
}

// TestHarden_WeakestLinkEmpty pins the vacuous bound: no parents, no
// confidence — never a guess from nothing.
func TestHarden_WeakestLinkEmpty(t *testing.T) {
	if got := WeakestLink(); got != 0 {
		t.Fatalf("empty weakest-link must be 0, got %v", got)
	}
}

// TestHarden_FuzzyCapBreach proves the fuzzy lane fails closed too:
// an unwritable paraphrase-union is valid live content under no exact
// key → INSERT, never a dropped write.
func TestHarden_FuzzyCapBreach(t *testing.T) {
	full := make(map[string]string, rpc.MaxPayloadKeys)
	for i := 0; i < rpc.MaxPayloadKeys; i++ {
		full[fmt.Sprintf("k%d", i)] = "v"
	}
	cands := []rpc.Record{grec("f", "Acme Corp", "employs", "yes", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, full)}
	out := mustDecide(t, gin("Acme", "employs", "yes", 1, 5, "b", "s2", rpc.ACLUser, map[string]string{"new": "1"}), cands, nil)
	if out.Verdict != rpc.VerdictInsert || out.Status != rpc.StatusActive {
		t.Fatalf("cap-breaching fuzzy union must INSERT, got %+v", out)
	}
}
