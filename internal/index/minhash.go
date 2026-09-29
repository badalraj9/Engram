package index

import (
	"math"
	"sort"

	"engram/internal/processing"
	"engram/internal/rpc"
)

// Fuzzy returns MERGE-band candidates for an incoming record: live
// indexed versions sharing the normalized predicate whose subject
// trigram-Jaccard clears processing.FuzzyMergeFloor (FR4 match
// semantics — paraphrase catching, e.g. "Acme" vs "Acme Corp").
// Generation (trigram-overlap prefilter over x.trigrams) narrows;
// the DECISION reuses processing.TrigramJaccard, so the floor and
// the anchors stay P2-owned — this function cannot drift from the
// gate's band by construction. Exact-key hits are excluded (the gate
// routes exact first; fuzzy serves paraphrase only). Different-object
// versions are included here and rejected by the gate (unproven
// identity → INSERT, never UPDATE on fuzzy): generation overincludes,
// the verdict layer decides. Deterministic ID order.
//
// P6 worst-case strategy (P4 open risk — fuzzy lane is O(trigram-union)
// under near-duplicate corpora, and no top-N cap is possible because
// full sets are gate-locked): count-prefilter with a PROVEN lower
// bound. Jaccard >= F implies inter >= F*(|A|+|B|)/(1+F); with |B| >= 1
// the per-query minimum inter >= ceil(F*(|A|+1)/(1+F)) is necessary —
// any candidate below it cannot clear the floor, so skipping Jaccard
// (and the byID touch) is complete by arithmetic, never by sampling.
// Truly-near-duplicate corpora still pool large, correctly: those
// candidates genuinely clear the band and the gate must see them.
func (x *Index) Fuzzy(subject, predicate string) []rpc.Record {
	inPred := processing.Normalize(predicate)
	if inPred == "" {
		return nil
	}
	inSub := processing.Normalize(subject)
	inGrams := processing.Trigrams(subject)
	// Candidate generation: count shared trigrams per ID over the
	// incoming subject's grams. Short subjects (< TrigramN runes)
	// yield one whole-string gram (processing.Trigrams) — rarely
	// clearing the floor, erring toward INSERT over false MERGE.
	counts := make(map[string]int)
	for g := range inGrams {
		for id := range x.trigrams[g] {
			counts[id]++
		}
	}
	minOverlap := minTrigramOverlap(len(inGrams))
	var out []rpc.Record
	for _, id := range sortedKeys(counts) {
		if counts[id] < minOverlap {
			continue // arithmetically cannot clear the floor
		}
		rec, ok := x.byID[id]
		if !ok {
			continue
		}
		if processing.Normalize(rec.Predicate) != inPred {
			continue
		}
		if processing.Key(rec.Subject, rec.Predicate) == processing.Key(subject, predicate) {
			continue // exact lane owns these
		}
		if processing.TrigramJaccard(rec.Subject, inSub) < processing.FuzzyMergeFloor {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// minTrigramOverlap is the P6 necessary-condition bound: Jaccard >= F
// with union = |A|+|B|-inter gives inter >= F*(|A|+|B|)/(1+F). |B| >= 1
// always (Trigrams never yields empty), so inter >= F*(|A|+1)/(1+F) is
// necessary for any candidate. At F=0.25 this reduces to (|A|+1)/5, but
// the computation reads the floor — recalibration moves the bound, not
// the logic. Returns 1 minimum (short-subject single-gram case).
func minTrigramOverlap(lenA int) int {
	if lenA <= 1 {
		return 1
	}
	f := processing.FuzzyMergeFloor
	n := int(math.Ceil(f * float64(lenA+1) / (1 + f)))
	if n < 1 {
		return 1
	}
	return n
}

// sortedKeys orders count-map IDs deterministically (the Set.Sorted
// discipline, for the map shape the counter needs).
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Chained resolves a two-hop lookup without a graph (FR8): exact-set
// under (subject,predicate), then for each hit, exact-set under
// (hit.Object,predicate2). Returns hop-2 records, deduplicated by ID
// in sorted-ID order. Revisits collapse by construction (byID keying).
func (x *Index) Chained(subject, predicate, predicate2 string) []rpc.Record {
	var out []rpc.Record
	seen := make(Set)
	for _, hop1 := range x.Exact(subject, predicate) {
		for _, hop2 := range x.Exact(hop1.Object, predicate2) {
			if seen.Contains(hop2.ID) {
				continue
			}
			seen.Add(hop2.ID)
			out = append(out, hop2)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
