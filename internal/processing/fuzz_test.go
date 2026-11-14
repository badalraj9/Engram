package processing

import (
	"math"
	"testing"
)

// FuzzKeyFraming (H6) hunts framing collisions: distinct normalized
// components must never share a key. Seed corpus carries the locked
// (a,bc)/(ab,c) pair plus case/whitespace folds.
func FuzzKeyFraming(f *testing.F) {
	f.Add("a", "bc", "ab", "c")
	f.Add("Acme", "employs", "  acme ", "EMPLOYS")
	f.Add("", "p", " ", "p")
	f.Fuzz(func(t *testing.T, s1, p1, s2, p2 string) {
		sameKey := Key(s1, p1) == Key(s2, p2)
		sameParts := Normalize(s1) == Normalize(s2) && Normalize(p1) == Normalize(p2)
		if sameKey != sameParts {
			t.Fatalf("framing mismatch: key=%v parts=%v for (%q,%q) vs (%q,%q)",
				sameKey, sameParts, s1, p1, s2, p2)
		}
	})
}

// FuzzJaccardInvariants (H6) pins the similarity contract over
// arbitrary strings: bounded, symmetric, reflexive.
func FuzzJaccardInvariants(f *testing.F) {
	f.Add("Acme", "Acme Corp")
	f.Add("Raj", "Rahul")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, a, b string) {
		ab, ba := TrigramJaccard(a, b), TrigramJaccard(b, a)
		if ab != ba {
			t.Fatalf("asymmetric: (%q,%q)=%v vs %v", a, b, ab, ba)
		}
		if ab < 0 || ab > 1 {
			t.Fatalf("out of bounds: (%q,%q)=%v", a, b, ab)
		}
		if self := TrigramJaccard(a, a); self != 1 {
			t.Fatalf("reflexivity: (%q,%q)=%v, want 1", a, a, self)
		}
	})
}

// FuzzRankScoreInvariants (P4 hardening) pins the score contract
// over arbitrary inputs: never NaN/Inf, never negative, monotone
// non-decreasing in hits. Guards inside RankScore (clamps,
// fallbacks) are what make this hold for hostile floats.
func FuzzRankScoreInvariants(f *testing.F) {
	f.Add(2, 1.0, 1.0, 3, 0.5)
	f.Add(0, 1.0, 1.0, 0, 0.5)
	f.Add(5, 0.0, 1.5, 100, 2.0)
	f.Fuzz(func(t *testing.T, hits int, r, trust float64, corr int, beta float64) {
		s := RankScore(hits, r, trust, corr, beta)
		if math.IsNaN(s) || math.IsInf(s, 0) {
			t.Fatalf("non-finite score: RankScore(%d,%v,%v,%d,%v)=%v", hits, r, trust, corr, beta, s)
		}
		if s < 0 {
			t.Fatalf("negative score: RankScore(%d,%v,%v,%d,%v)=%v", hits, r, trust, corr, beta, s)
		}
		if hits >= 0 && hits < 1<<30 {
			if up := RankScore(hits+1, r, trust, corr, beta); up < s {
				t.Fatalf("hits-monotonicity: %v then %v", s, up)
			}
		}
	})
}
