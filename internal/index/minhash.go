package index

import (
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
func (x *Index) Fuzzy(subject, predicate string) []rpc.Record {
	inPred := processing.Normalize(predicate)
	if inPred == "" {
		return nil
	}
	inSub := processing.Normalize(subject)
	// Candidate generation: union of trigram postings for the
	// incoming subject's grams. Short subjects (< TrigramN runes)
	// yield one whole-string gram (processing.Trigrams) — rarely
	// clearing the floor, erring toward INSERT over false MERGE.
	var cand = make(Set)
	for g := range processing.Trigrams(subject) {
		cand.Union(x.trigrams[g])
	}
	var out []rpc.Record
	for _, id := range cand.Sorted() {
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
