// Durable tick high-water mark (P5 — DESIGN §6 amendment 2026-09-28).
//
// The host owns the tick counter; Engram only remembers the high-water
// mark so a daemon restart cannot re-accept an old tick. Layout:
// `M/tick` → 8-byte big-endian int64 under the existing Meta prefix
// (no new prefix, no schema change — same additive pattern as
// M/quarantine/*). Written in the tick's Sync boundary and acked only
// after Sync; any consolidate(tick) with tick <= last is rejected.
//
// Additive concrete methods on *Store only — the TruthStore interface
// is P0-closed and stays untouched. Single-writer: every method holds
// mu like the rest of store.go.
package truth

import (
	"encoding/binary"

	badger "github.com/dgraph-io/badger/v4"

	"engram/internal/rpc"
)

// corruptTick reports a malformed M/tick value fail-closed: a
// high-water mark that cannot be decoded is corruption, never a
// default. The operator repair path (repair.go) decides.
func corruptTick() error {
	return rpc.E(rpc.CodeCorruptIndex, "truth: malformed M/tick high-water mark")
}

// invalidTick reports a non-monotonic or negative tick commit: caller
// bug, never silent state.
func invalidTick() error {
	return rpc.E(rpc.CodeInvalidRecord, "truth: tick must exceed the last accepted tick")
}

// tickKey is the M/tick high-water mark key.
func tickKey() []byte {
	return append([]byte{prefixMeta}, "tick"...)
}

// LastTick returns the highest accepted consolidate tick, or -1 when
// no tick has ever been accepted (fresh store accepts tick >= 0).
func (s *Store) LastTick() (int64, error) {
	var raw []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(tickKey())
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			raw = append([]byte(nil), val...)
			return nil
		})
	})
	if err != nil {
		return -1, err
	}
	if len(raw) == 0 {
		return -1, nil
	}
	if len(raw) != 8 {
		return -1, corruptTick()
	}
	return int64(binary.BigEndian.Uint64(raw)), nil
}

// CommitTick records tick as the new high-water mark. The write joins
// the tick's Sync boundary — the caller Syncs before acking, so a
// crash before Sync loses both the effects and the mark together
// (never mark-without-effects). Non-monotonic commits are rejected
// without writing: tick <= last is a caller bug, never silent state.
func (s *Store) CommitTick(tick int64) error {
	if tick < 0 {
		return invalidTick()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, err := s.lastTickLocked()
	if err != nil {
		return err
	}
	if tick <= last {
		return invalidTick()
	}
	val := binary.BigEndian.AppendUint64(nil, uint64(tick))
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(tickKey(), val)
	})
}

// lastTickLocked reads the mark under the caller's mu hold.
func (s *Store) lastTickLocked() (int64, error) {
	var raw []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(tickKey())
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			raw = append([]byte(nil), val...)
			return nil
		})
	})
	if err != nil {
		return -1, err
	}
	if len(raw) == 0 {
		return -1, nil
	}
	if len(raw) != 8 {
		return -1, corruptTick()
	}
	return int64(binary.BigEndian.Uint64(raw)), nil
}
