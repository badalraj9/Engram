// Cleanup: decode a bundle against the codebook (TECH §2.7 bridge).
//
// v1 shape: linear scan over the single bucket, best-similarity wins.
// Sub-ms at the M-gate sizes (M5); the LSH partitioner arrives only if
// a bench proves the scan load-bearing (stdlib-first frozen rule).
// Thresholding (abstention — M4) lives with the caller: Cleanup reports
// the best name and its similarity, never a verdict.
package dynamics

import "sort"

// Cleanup decodes bundle against book: returns the best-matching name
// and its similarity. Ties break alphabetically (deterministic —
// measured decode accuracy must not depend on map order). ok is false
// for an empty book.
//
// Precondition: book atoms and the query bundle are unit-magnitude
// entries (NewRandom/NewFromAngles/Bundle emit exactly that — pinned by
// TestUnit_ByConstruction), so the unit fast path is exact, not
// approximate.
func Cleanup(bundle Phasor, book *Codebook) (best string, sim float64, ok bool) {
	names := book.Names()
	sort.Strings(names)
	for _, n := range names {
		a, _ := book.Get(n)
		s := SimilarityUnit(bundle, a)
		if !ok || s > sim {
			best, sim, ok = n, s, true
		}
	}
	return best, sim, ok
}

// QueryTop scores every member (ranked decode — the M1 harness asserts
// top-1, the curve is available for the report without extra passes).
func QueryTop(bundle Phasor, book *Codebook) ([]string, []float64) {
	type scored struct {
		n string
		s float64
	}
	var ss []scored
	for _, n := range book.Names() {
		a, _ := book.Get(n)
		ss = append(ss, scored{n, SimilarityUnit(bundle, a)})
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].s != ss[j].s {
			return ss[i].s > ss[j].s
		}
		return ss[i].n < ss[j].n
	})
	ns := make([]string, len(ss))
	sims := make([]float64, len(ss))
	for i, e := range ss {
		ns[i], sims[i] = e.n, e.s
	}
	return ns, sims
}
