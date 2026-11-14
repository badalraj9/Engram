// Economics eviction (P5 — FR11, second leg of the serialized tick).
//
// Runs AFTER consolidation completes in the same tick (DESIGN §6:
// ordering prevents evicting a record an in-flight consolidation
// cluster is still reading — cheaper than a lock). Eviction is
// economics-based — value < evict threshold — never a fixed-count or
// capacity cutoff.
//
// Append-only compliance: pruning never deletes keys. Victims are
// closed via Restatus(TOMBSTONED) — ordinary close-interval entries,
// retained for as_of history. The caller Syncs once for the whole
// tick and retires victims from the serving index.
//
// Prune invariant (exit-tested): never closes a live EPISODIC record
// that is reachable from a live DerivedFrom chain (a SUMMARY's
// parents), never touches non-EPISODIC records, never touches
// already-closed versions.
package maintenance

import (
	"engram/internal/index"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// Prune tombstones live EPISODIC records scoring below evict that no
// live derived record depends on. Returns victim IDs (committed via
// Restatus, unretired from the index — the caller retires + Syncs).
// Deterministic: victims sort by ID.
//
// Raw mechanism, not tick policy: horizon disabled (0) — the FR11
// fence tests pin economics here, while the tick passes RetireHorizon
// and the Calibrated gate (P2 E2). Callers wanting the contract read
// Consolidate, not this function.
func Prune(store *truth.Store, idx *index.Index, evict float64) ([]string, error) {
	tail, err := store.TailOffset()
	if err != nil {
		return nil, err
	}
	return pruneAt(store, idx, evict, int64(tail), 0)
}

// RetireHorizon is the E2 recency guard (v1-fixed provisional, commit
// sequence steps): a version touched — written or feedback-verdict —
// within the horizon survives regardless of birth. Forgetting must
// mean disuse, never age. Same fitting path as the trio (dogfood →
// M-gates/FR16 → config).
const RetireHorizon int64 = 100000

// pruneAt runs the pass against a frozen now (see calibrateAt: the
// tick threads one clock through the whole pass). horizon <= 0
// disables the recency guard (raw economics for the fence tests);
// the tick passes RetireHorizon.
func pruneAt(store *truth.Store, idx *index.Index, evict float64, now, horizon int64) ([]string, error) {
	live := liveIndex(store, idx)
	reachable, err := reachableFrom(store, live)
	if err != nil {
		return nil, err
	}
	var victims []string
	for _, r := range live {
		if r.Type != rpc.RecordEpisodic {
			continue
		}
		if r.Status != rpc.StatusActive {
			continue
		}
		if reachable[r.ID] {
			continue
		}
		if baselineValue(r, now) >= evict {
			continue
		}
		if horizon > 0 && now-lastTouch(store, r) <= horizon {
			continue
		}
		if _, err := store.Restatus(r.ID, rpc.StatusTombstoned); err != nil {
			return victims, err
		}
		victims = append(victims, r.ID)
	}
	return victims, nil
}

// lastTouch returns the newest commit-sequence step at which the
// version saw activity: its birth or its latest feedback verdict.
// Attest/correct ticks ride the same commit sequence as writes — one
// clock, so recency needs no second source.
func lastTouch(store *truth.Store, r rpc.Record) int64 {
	touch := r.Txn.From
	for _, ev := range store.FeedbackFor(r.ID) {
		if ev.Tick > touch {
			touch = ev.Tick
		}
	}
	return touch
}

// liveIndex resolves the serving index to store-authoritative latest
// versions (same store-verify rule as intake/query: index copies go
// stale on supersede — point Gets over the ID set).
func liveIndex(store *truth.Store, idx *index.Index) []rpc.Record {
	var out []rpc.Record
	for _, id := range idx.IDs() {
		fresh, err := store.Get(id)
		if err != nil {
			continue
		}
		out = append(out, fresh)
	}
	return out
}

// reachableFrom collects every ID named in a live record's
// DerivedFrom chain — the set pruning must never close. Lineage is
// read from the STORE, not the serving index: derived records are
// excluded from term lanes by construction (IsLiveCandidate), so an
// index-only walk would miss SUMMARY parents and prune them —
// found by TestPrune_Invariant.
func reachableFrom(store *truth.Store, live []rpc.Record) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, r := range live {
		if r.Status == rpc.StatusTombstoned {
			continue
		}
		for _, id := range r.DerivedFrom {
			out[id] = true
		}
	}
	seen := make(map[string]bool)
	err := store.Scan(0, func(ev truth.Event) error {
		if ev.Kind != truth.EventRecord || ev.Record.Type == rpc.RecordEpisodic {
			return nil
		}
		if seen[ev.Record.ID] {
			return nil
		}
		seen[ev.Record.ID] = true
		fresh, err := store.Get(ev.Record.ID)
		if err != nil {
			return nil
		}
		if fresh.Status == rpc.StatusTombstoned {
			return nil
		}
		for _, id := range fresh.DerivedFrom {
			out[id] = true
		}
		return nil
	})
	return out, err
}
