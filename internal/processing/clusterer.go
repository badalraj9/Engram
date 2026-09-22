package processing

import (
	"sort"

	"engram/internal/rpc"
)

// Cluster groups records by session-cluster BEFORE any combination
// (FR9 ordering: witnesses counted per cluster, not per raw record).
// v1 session-cluster ≡ one (Source, Session) composite pair — the
// session string alone is not identity (cross-source collisions would
// merge witnesses, violating FR14 independence through the back door).
// Output is deterministic: clusters ordered by (Source, Session, ID),
// members in ID order. The input slice is never mutated.
func Cluster(records []rpc.Record) [][]rpc.Record {
	if len(records) == 0 {
		return nil
	}
	idx := make([]int, len(records))
	for i := range idx {
		idx[i] = i
	}
	less := func(a, b rpc.Record) bool {
		if a.Provenance.Source != b.Provenance.Source {
			return a.Provenance.Source < b.Provenance.Source
		}
		if a.Provenance.Session != b.Provenance.Session {
			return a.Provenance.Session < b.Provenance.Session
		}
		return a.ID < b.ID
	}
	sort.Slice(idx, func(i, j int) bool { return less(records[idx[i]], records[idx[j]]) })
	var out [][]rpc.Record
	for _, i := range idx {
		r := records[i]
		if n := len(out); n > 0 {
			last := out[n-1][0].Provenance
			if last == r.Provenance {
				out[n-1] = append(out[n-1], r)
				continue
			}
		}
		out = append(out, []rpc.Record{r})
	}
	return out
}

// Independent reports whether a and b are independent witnesses (FR14):
// they differ in source or session. Promotion (via RESTRENGTHEN or
// attest-confirm) requires independence from the original write's
// session-cluster — self-attestation never promotes.
func Independent(a, b rpc.Provenance) bool {
	return a.Source != b.Source || a.Session != b.Session
}
