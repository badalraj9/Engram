// Validity sweep (P2 E1 — DESIGN §6 ratified contract): tombstone
// EPISODIC versions whose window has closed (valid_to <= tick-now).
// The tick is the closer — transaction-time records the forgetting,
// so as_of answers "who closed it" truthfully. Append-only
// preserved: close-interval entries, retained for history (M6).
//
// Class scope per R0 (criteria decide, status selects class):
// ACTIVE-or-SCHEDULED only. BOTH excluded — resolve owns the pair,
// the sweep never collapses an open contradiction. STAGED absent
// from the serving index by construction. TOMBSTONED skipped.
// SUMMARY/SEMANTIC excluded — derived lifetimes follow their
// episodes (same invariant as pruneAt).
package maintenance

import (
	"sort"

	"engram/internal/index"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// expireAt closes expired EPISODIC versions against an explicit
// wall-clock (daemon-stamped, same authority as recall now —
// injectable here so the boundary is deterministic in tests).
// Returns closed old-version IDs, sorted — the caller retires +
// Syncs. Idempotent: closed versions are skipped, never re-closed.
func expireAt(store *truth.Store, idx *index.Index, wallNow int64) ([]string, error) {
	var expired []string
	for _, r := range liveIndex(store, idx) {
		if r.Type != rpc.RecordEpisodic {
			continue
		}
		if r.Status != rpc.StatusActive && r.Status != rpc.StatusScheduled {
			continue
		}
		if r.ValidTime.To > wallNow {
			continue
		}
		if _, err := store.Restatus(r.ID, rpc.StatusTombstoned); err != nil {
			return expired, err
		}
		expired = append(expired, r.ID)
	}
	sort.Strings(expired)
	return expired, nil
}
