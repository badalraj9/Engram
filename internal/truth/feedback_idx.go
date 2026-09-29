// Feedback-by-ID secondary (P6 — bench-required).
//
// The P4 bench proved recall's per-query Scan(0) is O(table): 10ms @10K,
// 111.5ms @100K, linear to ~1.1s @1M. This file is the fix: a derived
// in-memory map from record ID to its FEEDBACK events, rebuilt at boot
// from the log alone and extended on every AppendFeedback under the
// store's existing single-writer mutex.
//
// Derived-state rules (DESIGN §2, same as idmap and M/quarantine):
// never logged, never required for replay, rebuilt by Scan/replay,
// dropped and rebuilt on truncate. The TruthStore interface is
// P0-closed and stays untouched — this is a concrete *Store method,
// the quarantine precedent.
package truth

import (
	badger "github.com/dgraph-io/badger/v4"

	"engram/internal/rpc"
)

// FeedbackFor returns the FEEDBACK events for one record ID, oldest
// first (commit order — the F scan that builds the map is seq-ordered,
// and AppendFeedback appends under mu). Returns nil for unknown IDs.
// The slice is a copy: callers must not mutate derived state.
func (s *Store) FeedbackFor(id string) []rpc.FeedbackEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.fbIdx[id]
	out := make([]rpc.FeedbackEvent, len(evs))
	copy(out, evs)
	return out
}

// rebuildFeedbackIdx rescans the F prefix into fbIdx. Caller holds mu
// (matches rebuildIDMap's contract — called from recover before serve
// and from truncateFrom after amputation).
func (s *Store) rebuildFeedbackIdx() error {
	fresh := make(map[string][]rpc.FeedbackEvent)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte{prefixFeedback}
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				ev, err := decodeFeedback(val)
				if err != nil {
					return err
				}
				fresh[ev.RecordID] = append(fresh[ev.RecordID], ev)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.fbIdx = fresh
	return nil
}
