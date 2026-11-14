package query

import (
	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// AsOf serves bi-temporal historical queries (NFR8): records under
// one (subject, predicate) whose axis interval contains t. No status
// filter by construction — QueryExact carries none (store.go), so
// historical results are unaffected by current-belief status (M6:
// a retraction tomorrow must not rewrite what was true yesterday).
// ACL still pre-filters by caller (FR13 governs every working
// computation, historical or not).
func AsOf(store *truth.Store, subject, predicate string, t int64, axis rpc.Axis, caller rpc.AclTier) ([]rpc.Record, error) {
	if processing.Rank(caller) < 0 {
		return nil, rpc.E(rpc.CodeInvalidRecord, "as_of requires a known caller tier")
	}
	recs, err := store.QueryExact(subject, predicate, t, axis)
	if err != nil {
		return nil, err
	}
	out := recs[:0:0]
	for _, r := range recs {
		if visibleTo(r, caller) {
			out = append(out, r)
		}
	}
	return out, nil
}
