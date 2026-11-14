package processing

import (
	"encoding/binary"
	"strings"

	"engram/internal/rpc"
)

// TrigramN is the n-gram width for the P2 fuzzy rule. v1 fixed; any
// reasonable n works — the floor carries the real selectivity.
// Recalibration path: dogfood (UC5) → M-gates/FR16 → TECH §2.10 config.
const TrigramN = 3

// FuzzyMergeFloor is the minimum subject similarity for the MERGE band
// (DESIGN §4 match semantics: paraphrase catching, e.g. "Acme" vs
// "Acme Corp"). v1 fixed provisional — the docs are structurally
// silent (FR16's calibrated list is closed and does not include it).
// The behavioral anchors derive a window, not a free pick:
// "Acme"/"Acme Corp" scores 2/7 ≈ 0.286 and must merge;
// "apple"/"application" scores exactly 0.2 and must stay distinct;
// "Raj"/"Rahul" scores 0. So floor ∈ (0.2, 0.286], and 0.25 sits
// mid-window. The anchors, pinned as computed values in test, are the
// spec; this line is the setting. Short subjects (< TrigramN runes)
// compare as whole strings and rarely clear the floor — that errs
// toward INSERT (duplicates, which legitimately coexist) over false
// MERGE (lost distinctions), the safe direction under exact-first.
// Recalibration changes this line, never the logic: dogfood (UC5) →
// M-gates/FR16 → TECH §2.10 config.
const FuzzyMergeFloor = 0.25

// Normalize canonicalizes a subject/predicate/object for comparison:
// lowercase, trimmed, internal whitespace collapsed. One normalization
// shared by the gate — P3 must apply it when building and looking up
// index keys, or gate-match and index-lookup diverge (P3 conformance).
func Normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Key builds the normalized exact-match key for (subject, predicate)
// (FR4 match semantics). Length-prefixed framing keeps ("a","bc")
// distinct from ("ab","c") — same discipline as P1's secondaries.
func Key(subject, predicate string) string {
	var k []byte
	k = frameKeyComponent(k, Normalize(subject))
	k = frameKeyComponent(k, Normalize(predicate))
	return string(k)
}

func frameKeyComponent(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// Trigrams returns the n-gram set of the normalized string. Strings
// shorter than n yield one gram (the whole string) so short subjects
// still participate instead of vanishing from the fuzzy band.
func Trigrams(s string) map[string]struct{} {
	r := []rune(Normalize(s))
	set := make(map[string]struct{})
	if len(r) < TrigramN {
		set[string(r)] = struct{}{}
		return set
	}
	for i := 0; i+TrigramN <= len(r); i++ {
		set[string(r[i:i+TrigramN])] = struct{}{}
	}
	return set
}

// TrigramJaccard is the Jaccard similarity over trigram sets: 1 for
// identical strings, 0 for disjoint. Both-empty (unreachable for
// validated records) yields 1.
func TrigramJaccard(a, b string) float64 {
	sa, sb := Trigrams(a), Trigrams(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	var inter int
	for g := range sa {
		if _, ok := sb[g]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

// IsLiveCandidate reports whether r may compete in the gate.
// Excluded, per locked rules: STAGED (caller-supplied quarantine set —
// DESIGN §4 rule 2, enforced at this boundary so a poisoned staged
// record can never contaminate a gate decision); TOMBSTONED (closed,
// history only); SUMMARY/SEMANTIC (derived records are inert in both
// directions — never built from derivations, never evidence here).
// BOTH-status records ARE matchable (conflict detection must see
// flagged pairs); the gate fold, not this filter, bars them as UPDATE
// targets (flagged pairs don't stack).
func IsLiveCandidate(r rpc.Record, staged map[string]bool) bool {
	if staged[r.ID] {
		return false
	}
	if r.Type.Derived() {
		return false
	}
	return r.Status != rpc.StatusTombstoned
}

// Match finds live candidates for an incoming record over in-memory
// fixtures (BUILD-PLAN P2: matcher is pure, no P3 import). Exact holds
// normalized (subject, predicate) hits; Fuzzy holds same-predicate
// paraphrases at or above FuzzyMergeFloor — the predicate stays exact
// (v1 exact-first default; a paraphrased subject proves nothing about
// a reworded predicate). Pointers alias the caller's slice for
// read-only use.
func Match(cands []rpc.Record, staged map[string]bool, in rpc.InputRecord) (exact, fuzzy []*rpc.Record) {
	key := Key(in.Subject, in.Predicate)
	inPred := Normalize(in.Predicate)
	inSub := Normalize(in.Subject)
	for i := range cands {
		r := &cands[i]
		if !IsLiveCandidate(*r, staged) {
			continue
		}
		if Key(r.Subject, r.Predicate) == key {
			exact = append(exact, r)
			continue
		}
		if Normalize(r.Predicate) == inPred && TrigramJaccard(r.Subject, inSub) >= FuzzyMergeFloor {
			fuzzy = append(fuzzy, r)
		}
	}
	return exact, fuzzy
}
