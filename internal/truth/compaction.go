// Compaction and engine options (P1).
//
// The anti-densification guard here is structural, not configurational:
// every version is a distinct key (prefix + global sequence), so there
// is nothing for compaction to merge. These helpers hold the engine
// settings and the value-log GC entry point the P5 tick will call —
// the daemon never schedules either itself.
package truth

import (
	badger "github.com/dgraph-io/badger/v4"
)

// OpenOptions centralizes the Badger configuration. Single-writer is
// enforced by Store.mu; DetectConflicts is off because serialization
// is explicit, not optimistic. SyncWrites stays off — durability lands
// at explicit Sync boundaries (group commit), never per-write.
func OpenOptions(path string) badger.Options {
	opts := badger.DefaultOptions(path)
	opts.SyncWrites = false
	opts.NumVersionsToKeep = 1
	opts.DetectConflicts = false
	return opts
}

// RunValueLogGC reclaims value-log space. Called only from the P5
// consolidate tick — never on a timer, never in the background.
func RunValueLogGC(db *badger.DB) error {
	for i := 0; i < 10; i++ {
		err := db.RunValueLogGC(0.5)
		if err != nil {
			if err == badger.ErrNoRewrite {
				return nil
			}
			return err
		}
	}
	return nil
}
