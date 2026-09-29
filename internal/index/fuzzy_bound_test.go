// Fuzzy bound completeness + selectivity (P6 exit: the count-prefilter
// never drops an above-floor pair, and disjoint corpora stay narrow).
package index

import (
	"fmt"
	"testing"

	"engram/internal/processing"
	"engram/internal/rpc"
)

// TestFuzzy_BoundComplete asserts index/gate parity over a mixed corpus:
// every gate-fuzzy hit must generate from the index (the bound is
// necessary, never sufficient — no false negatives by arithmetic).
func TestFuzzy_BoundComplete(t *testing.T) {
	var x Index
	var cands []rpc.Record
	subs := []string{
		"Acme", "Acme Corp", "Acme Corporation", "Globex", "Globex Inc",
		"Raj", "Rahul", "apple", "application", "pump reading alpha",
		"pump reading beta", "mothers birthday", "fathers birthday",
	}
	for i, s := range subs {
		r := irec(fmt.Sprintf("c%d", i), s, "employs", "yes")
		cands = append(cands, r)
		x.Add(r, nil)
	}
	// Deterministic filler: hex-like tokens (benchSetup discipline —
	// systematic names would mutually clear the floor and fake a scan).
	for i := 0; i < 200; i++ {
		s := fmt.Sprintf("zx%04xqv", i*2654435761%1048576)
		r := irec(fmt.Sprintf("f%d", i), s, "employs", "yes")
		cands = append(cands, r)
		x.Add(r, nil)
	}
	for _, q := range []string{"Acme", "Globex Inc", "Raj", "apple", "pump reading alpha", "mothers birthday"} {
		_, gateFuzzy := processing.Match(cands, nil, iin(q, "employs", "yes"))
		want := idSetP(gateFuzzy)
		got := idSet(x.Fuzzy(q, "employs"))
		if len(got) != len(want) {
			t.Fatalf("query %q: index %v vs gate %v", q, got, want)
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("query %q: gate hit %s missing from index (bound dropped a true positive)", q, id)
			}
		}
	}
}

// TestFuzzy_SelectiveStaysSmall pins the P4 stall's fix: disjoint
// subjects must not materialize the table through the fuzzy lane.
func TestFuzzy_SelectiveStaysSmall(t *testing.T) {
	var x Index
	for i := 0; i < 5000; i++ {
		s := fmt.Sprintf("zx%04xqv", i*2654435761%1048576)
		x.Add(irec(fmt.Sprintf("f%d", i), s, "pred", "obj"), nil)
	}
	got := x.Fuzzy("quorum zed", "pred")
	if len(got) > 5 {
		t.Fatalf("selective fuzzy must stay narrow, got %d", len(got))
	}
}

// TestMinOverlap_BoundHolds pins the arithmetic: the bound never exceeds
// any true-positive's shared count (fuzz-adjacent property over the
// anchor pairs, deterministic).
func TestMinOverlap_BoundHolds(t *testing.T) {
	pairs := [][2]string{
		{"Acme", "Acme Corp"},
		{"Acme", "Acme Corporation"},
		{"pump reading alpha", "pump reading beta"},
	}
	for _, p := range pairs {
		ga := processing.Trigrams(p[0])
		gb := processing.Trigrams(p[1])
		inter := 0
		for g := range ga {
			if _, ok := gb[g]; ok {
				inter++
			}
		}
		if j := processing.TrigramJaccard(p[0], p[1]); j < processing.FuzzyMergeFloor {
			continue // not a true positive — bound owes it nothing
		}
		if inter < minTrigramOverlap(len(ga)) {
			t.Fatalf("pair %q/%q: shared %d below bound %d — bound would drop a true positive",
				p[0], p[1], inter, minTrigramOverlap(len(ga)))
		}
	}
}
