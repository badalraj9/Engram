// Quarantine set (P4 — DESIGN §2 contract, Option A).
//
// Staged records live in the truth log as ordinary versions (ACTIVE);
// quarantine is a separate derived set, never a record field. Marker
// layout: M/quarantine/<record-id> → single 0x01 byte under the
// existing Meta prefix (no new prefix, no schema change; the staged
// record itself already carries provenance + txn-time).
//
// Additive concrete methods on *Store only — the TruthStore
// interface is P0-closed and stays untouched. Single-writer: every
// method holds mu like the rest of store.go.
package truth

import (
	"encoding/binary"
	"strings"

	badger "github.com/dgraph-io/badger/v4"

	"engram/internal/rpc"
)

// quarantineMarker is the sole marker value: presence is the signal,
// the byte carries nothing.
const quarantineMarker = 0x01

// quarantineKey builds M/quarantine/<id>. The trailing slash in the
// namespace keeps the set prefix-scannable and disjoint from
// M/schema by construction (different second component).
func quarantineKey(id string) []byte {
	k := make([]byte, 0, 16+len(id))
	k = append(k, prefixMeta)
	k = append(k, "quarantine/"...)
	return append(k, id...)
}

// quarantinePrefix is the ListStaged scan root.
func quarantinePrefix() []byte {
	return append([]byte{prefixMeta}, "quarantine/"...)
}

// StageAppend commits a staged record version plus its quarantine
// marker in ONE Badger txn (atomic both-or-neither — a record
// without its marker would read as believed, so the window must not
// exist). The record is an ordinary EPISODIC/ACTIVE version; only
// the marker quarantines it. Caller holds no lock; this takes mu.
func (s *Store) StageAppend(in rpc.InputRecord) (rpc.Record, error) {
	if err := in.Validate(); err != nil {
		return rpc.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := stagedRecord(in, s.next)
	id, err := newID()
	if err != nil {
		return rpc.Record{}, err
	}
	rec.ID = id
	if err := rec.Validate(); err != nil {
		return rpc.Record{}, err
	}
	val, err := encodeRecord(rec)
	if err != nil {
		return rpc.Record{}, err
	}
	seq := s.next
	sp := secondarySPKey(rec.Subject, rec.Predicate, seq)
	op := secondaryOPKey(rec.Object, rec.Predicate, seq)
	seqVal := binary.BigEndian.AppendUint64(nil, seq)
	if err := s.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(recordKey(seq), val); err != nil {
			return err
		}
		if err := txn.Set(sp, seqVal); err != nil {
			return err
		}
		if err := txn.Set(op, seqVal); err != nil {
			return err
		}
		return txn.Set(quarantineKey(rec.ID), []byte{quarantineMarker})
	}); err != nil {
		return rpc.Record{}, err
	}
	s.next++
	s.idmap[rec.ID] = seq
	return rec, nil
}

// stagedRecord shapes the version StageAppend commits: same shape as
// Append (EPISODIC/ACTIVE, server-assigned ID + txn-time).
func stagedRecord(in rpc.InputRecord, seq uint64) rpc.Record {
	return rpc.Record{
		Subject:        in.Subject,
		Predicate:      in.Predicate,
		Object:         in.Object,
		ValidTime:      in.ValidTime,
		Txn:            rpc.Interval{From: int64(seq), To: rpc.OpenEnd},
		Provenance:     in.Provenance,
		ACL:            in.ACL,
		Status:         rpc.StatusActive,
		Type:           rpc.RecordEpisodic,
		BaseConfidence: in.BaseConfidence,
		CallerTSMeta:   in.CallerTSMeta,
		Payload:        in.Payload,
	}
}

// Promote peels the quarantine marker; the record stays and re-runs
// the gate as fresh (FR14). Idempotent: promoting a non-staged ID
// is a no-op, never an error (double-promotion is replay, not
// corruption).
func (s *Store) Promote(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(txn *badger.Txn) error {
		if err := txn.Delete(quarantineKey(id)); err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		return nil
	})
}

// IsStaged reports marker presence. Absence (never staged or
// promoted) reads false — believed by default, matching the index
// and gate which treat a missing set entry as live.
func (s *Store) IsStaged(id string) (bool, error) {
	var found bool
	err := s.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(quarantineKey(id))
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return found, err
}

// ListStaged returns the full quarantine set (the snapshot P3's
// rebuild and P4's recall filter consume). Empty set, never nil:
// callers range or index it without a nil check.
func (s *Store) ListStaged() (map[string]bool, error) {
	out := make(map[string]bool)
	prefix := quarantinePrefix()
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			id := strings.TrimPrefix(string(it.Item().Key()), string(prefix))
			if id != "" {
				out[id] = true
			}
		}
		return nil
	})
	return out, err
}
