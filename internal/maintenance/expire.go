// Validity sweep (P2 E1 — DESIGN §6 ratified contract, amended
// 2026-11-13): two window transitions in one pass, EPISODIC only.
// Expiry tombstones versions whose window has closed (valid_to <=
// tick-now); activation flips SCHEDULED versions whose window has
// opened (valid_from <= tick-now, still unexpired) to ACTIVE — before
// the amendment nothing ever flipped the status back, so a
// future-dated fact stayed invisible to recall forever after its
// window opened. Append-only preserved: Restatus closes + appends
// under a new id, retained for history (M6).
//
// The tick is the closer — transaction-time records the forgetting
// and the waking, so as_of answers "who closed it" and "when did it
// become true" truthfully.
//
// Class scope per R0 (criteria decide, status selects class):
// expiry touches ACTIVE-or-SCHEDULED; activation touches SCHEDULED
// only. Expiry outranks activation inside the pass — a window that
// opened and closed between ticks tombstones without ever
// activating. BOTH excluded — resolve owns the pair, the sweep never
// collapses an open contradiction. STAGED absent from the serving
// index by construction. TOMBSTONED skipped. SUMMARY/SEMANTIC
// excluded — derived lifetimes follow their episodes (same invariant
// as pruneAt).
package maintenance

import (
	"sort"

	"engram/internal/index"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// activation is one SCHEDULED→ACTIVE transition (F1, 2026-11-13):
// the window opened between write-time labeling and the tick. The
// caller swaps the serving index — retire the closed SCHEDULED
// version's id, add the fresh ACTIVE one (Restatus mints a new id).
type activation struct {
	OldID string
	Rec   rpc.Record
}

// classifyWindows is the E1 criteria, read-only (R0 dry-run shares it
// with the real sweep — one implementation, zero drift): given the
// store-verified live records and tick-now, it names the versions
// expiry would close (valid_to <= wallNow) and the SCHEDULED versions
// activation would wake (window opened, still unexpired), both
// sorted. Expiry outranks activation — a window that opened AND
// closed between ticks classifies as expired, never activated.
// Classification never mutates; the callers own the Restatus calls.
func classifyWindows(recs []rpc.Record, wallNow int64) (expired, activated []string) {
	for _, r := range recs {
		if r.Type != rpc.RecordEpisodic {
			continue
		}
		if r.Status != rpc.StatusActive && r.Status != rpc.StatusScheduled {
			continue
		}
		if r.ValidTime.To <= wallNow {
			expired = append(expired, r.ID)
			continue
		}
		if r.Status == rpc.StatusScheduled && r.ValidTime.From <= wallNow {
			activated = append(activated, r.ID)
		}
	}
	sort.Strings(expired)
	sort.Strings(activated)
	return expired, activated
}

// expireAt runs the validity sweep against an explicit wall-clock
// (daemon-stamped, same authority as recall now — injectable here so
// the boundary is deterministic in tests). Classifies first, then
// closes/wakes: the returned closed IDs are retired + Synced by the
// caller, the activations swapped into the serving index. Idempotent:
// closed versions are skipped, never re-closed; an ACTIVE version
// matches no transition.
func expireAt(store *truth.Store, idx *index.Index, wallNow int64) ([]string, []activation, error) {
	expired, activatedIDs := classifyWindows(liveIndex(store, idx), wallNow)
	for _, id := range expired {
		if _, err := store.Restatus(id, rpc.StatusTombstoned); err != nil {
			return nil, nil, err
		}
	}
	var activated []activation
	for _, id := range activatedIDs {
		rec, err := store.Restatus(id, rpc.StatusActive)
		if err != nil {
			return expired, activated, err
		}
		activated = append(activated, activation{OldID: id, Rec: rec})
	}
	return expired, activated, nil
}
