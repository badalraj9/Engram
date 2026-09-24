// Store is the Badger-backed TruthStore implementation (P1).
//
// Single-writer: every mutating path holds mu, and Badger conflict
// detection is off because serialization is explicit, not optimistic.
// Reads use Badger snapshots, so concurrent readers never block and a
// consolidation snapshot simply doesn't see mid-tick corrections
// (DESIGN §6) — that property arrives free with the engine.
package truth

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"sort"
	"sync"

	badger "github.com/dgraph-io/badger/v4"

	"engram/internal/rpc"
)

// crcTable backs entry checksums (CRC32-Castagnoli, validated at our
// boundary — never inherited trust from the engine's framing).
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Store is a Badger-backed TruthStore. The zero value is invalid;
// construct with Open.
type Store struct {
	db    *badger.DB
	mu    sync.Mutex
	next  uint64
	idmap map[string]uint64
	// truncated/truncatedAt record a corrupt tail found at open.
	// Scan still reports it loudly; TruncateFrom cleans it.
	truncated   bool
	truncatedAt uint64
}

// Open creates or opens the store at path, enforcing the schema marker
// (fail-closed on newer versions) and recovering the sequence tail and
// ID index from disk.
func Open(path string) (*Store, error) {
	db, err := badger.Open(OpenOptions(path))
	if err != nil {
		return nil, fmt.Errorf("truth: open: %w", err)
	}
	s := &Store{db: db, idmap: make(map[string]uint64)}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.recover(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// initSchema writes the version marker on first create and rejects
// newer-than-known markers fail-closed.
func (s *Store) initSchema() error {
	var raw []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(schemaKey())
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			raw = append([]byte(nil), val...)
			return nil
		})
	})
	switch {
	case err == badger.ErrKeyNotFound:
		if err := s.db.Update(func(txn *badger.Txn) error {
			return txn.Set(schemaKey(), []byte{CodecVersion})
		}); err != nil {
			return fmt.Errorf("truth: schema init: %w", err)
		}
		return s.db.Sync()
	case err != nil:
		return fmt.Errorf("truth: schema read: %w", err)
	}
	if len(raw) != 1 || raw[0] != CodecVersion {
		got := byte(0)
		if len(raw) == 1 {
			got = raw[0]
		}
		return rpc.E(rpc.CodeCorruptIndex,
			fmt.Sprintf("unsupported codec version %d (known %d)", got, CodecVersion))
	}
	return nil
}

// recover rebuilds the sequence tail (max over R/F prefixes — one
// reverse-seek each, trivial) and the ID→seq derived index.
//
// Corruption split (locked — replay-aborts vs index-skips):
//   - Scan (replay) aborts at the first bad entry: rebuild is always a
//     prefix, log-is-law holds.
//   - This index build skips bad values (best-effort over intact
//     entries): the idmap is derived state, never the log.
//
// Never-writable-past-corruption (locked):
//   - Pure-tail corruption (nothing decodable after) is amputated
//     automatically, loudly — the store never exists writable with a
//     broken scan below new writes.
//   - Mid-log corruption with intact successors refuses open
//     (fail-closed): amputating would silently delete the good suffix.
//     Operator repair (P5 emergency op) decides.
//
// Get on a corrupt record returns NOT_FOUND — absence, never partial data.
func (s *Store) recover() error {
	var max uint64
	var found bool
	for _, prefix := range [][]byte{{prefixRecord}, {prefixFeedback}} {
		seq, ok, err := s.latestSeq(prefix)
		if err != nil {
			return err
		}
		if ok && (!found || seq > max) {
			max, found = seq, true
		}
	}
	if found {
		s.next = max + 1
	}
	var firstBad uint64
	hasBad := false
	var maxGood uint64
	goodFound := false
	note := func(seq uint64, ok bool) {
		if ok {
			if !goodFound || seq > maxGood {
				maxGood, goodFound = seq, true
			}
			return
		}
		if !hasBad || seq < firstBad {
			firstBad, hasBad = seq, true
		}
	}
	// R scan: decode every value (open-time integrity check), indexing
	// intact entries, noting corruption.
	if err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte{prefixRecord}
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			seq, serr := parseSeqFromKey(item.Key())
			if serr != nil {
				note(0, false)
				continue
			}
			if err := item.Value(func(val []byte) error {
				rec, err := decodeRecord(val)
				if err != nil {
					note(seq, false)
					return nil
				}
				note(seq, true)
				s.idmap[rec.ID] = seq
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// F scan: decodability check only.
	if err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte{prefixFeedback}
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			seq, serr := parseSeqFromKey(item.Key())
			if serr != nil {
				note(0, false)
				continue
			}
			if err := item.Value(func(val []byte) error {
				if _, err := decodeFeedback(val); err != nil {
					note(seq, false)
					return nil
				}
				note(seq, true)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if !hasBad {
		return nil
	}
	if goodFound && maxGood > firstBad {
		return rpc.E(rpc.CodeCorruptIndex,
			fmt.Sprintf("mid-log corruption at seq %d with intact successors; refusing open (operator repair required)", firstBad))
	}
	fmt.Fprintf(os.Stderr, "truth: corrupt tail at seq %d; auto-truncating\n", firstBad)
	if _, err := s.truncateFrom(firstBad); err != nil {
		return err
	}
	s.truncated, s.truncatedAt = true, firstBad
	return nil
}

// mustSeq extracts the trailing sequence; keys come from our own
// iterator, so structural failure here is unreachable — and still
// reported rather than panicked on.
func mustSeq(key []byte) uint64 {
	seq, err := parseSeqFromKey(key)
	if err != nil {
		return 0
	}
	return seq
}

// latestSeq reverse-seeks one prefix for its highest sequence.
func (s *Store) latestSeq(prefix []byte) (uint64, bool, error) {
	var seq uint64
	var found bool
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		it := txn.NewIterator(opts)
		defer it.Close()
		// Upper bound: prefix followed by 0xFF bytes sorts past every
		// key under this prefix.
		hi := append(append([]byte(nil), prefix...), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
		it.Seek(hi)
		if !it.Valid() {
			return nil
		}
		key := it.Item().Key()
		if len(key) == 0 || key[0] != prefix[0] {
			return nil
		}
		v, err := parseSeqFromKey(key)
		if err != nil {
			return err
		}
		seq, found = v, true
		return nil
	})
	return seq, found, err
}

// Append implements TruthStore. The record is stored EPISODIC/ACTIVE;
// ID and txn-time come from the store's own sequence. The write is
// buffered — durability lands at the next Sync (group commit); nothing
// is acknowledged before its covering sync returns (P5 enforces this
// structurally by awaiting Sync pre-ack).
func (s *Store) Append(in rpc.InputRecord) (rpc.Record, error) {
	if err := in.Validate(); err != nil {
		return rpc.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := rpc.Record{
		Subject:        in.Subject,
		Predicate:      in.Predicate,
		Object:         in.Object,
		ValidTime:      in.ValidTime,
		Txn:            rpc.Interval{From: int64(s.next), To: rpc.OpenEnd},
		Provenance:     in.Provenance,
		ACL:            in.ACL,
		Status:         rpc.StatusActive,
		Type:           rpc.RecordEpisodic,
		BaseConfidence: in.BaseConfidence,
		CallerTSMeta:   in.CallerTSMeta,
		Payload:        in.Payload,
	}
	id, err := newID()
	if err != nil {
		return rpc.Record{}, err
	}
	rec.ID = id
	if err := rec.Validate(); err != nil {
		return rpc.Record{}, err
	}
	if err := s.commitNew(rec); err != nil {
		return rpc.Record{}, err
	}
	return rec, nil
}

// AppendDerived implements TruthStore (consolidation lane).
func (s *Store) AppendDerived(in rpc.InputRecord, typ rpc.RecordType, derivedFrom []string) (rpc.Record, error) {
	if err := in.Validate(); err != nil {
		return rpc.Record{}, err
	}
	if !typ.Derived() {
		return rpc.Record{}, rpc.E(rpc.CodeInvalidRecord, "derived lane requires SUMMARY or SEMANTIC")
	}
	if len(derivedFrom) == 0 {
		return rpc.Record{}, rpc.E(rpc.CodeInvalidRecord, "derived records require provenance parents")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := rpc.Record{
		Subject:        in.Subject,
		Predicate:      in.Predicate,
		Object:         in.Object,
		ValidTime:      in.ValidTime,
		Txn:            rpc.Interval{From: int64(s.next), To: rpc.OpenEnd},
		Provenance:     in.Provenance,
		DerivedFrom:    append([]string(nil), derivedFrom...),
		ACL:            in.ACL,
		Status:         rpc.StatusActive,
		Type:           typ,
		BaseConfidence: in.BaseConfidence,
		CallerTSMeta:   in.CallerTSMeta,
		Payload:        in.Payload,
	}
	id, err := newID()
	if err != nil {
		return rpc.Record{}, err
	}
	rec.ID = id
	if err := rec.Validate(); err != nil {
		return rpc.Record{}, err
	}
	if err := s.commitNew(rec); err != nil {
		return rpc.Record{}, err
	}
	return rec, nil
}

// commitNew writes one record version: primary key, both versioned
// secondaries, ID index. Caller holds mu.
func (s *Store) commitNew(rec rpc.Record) error {
	seq := s.next
	val, err := encodeRecord(rec)
	if err != nil {
		return err
	}
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
		return txn.Set(op, seqVal)
	}); err != nil {
		return fmt.Errorf("truth: append: %w", err)
	}
	s.next++
	s.idmap[rec.ID] = seq
	return nil
}

// Supersede implements TruthStore as ONE Badger transaction: the old
// version closes (txn_to = seq, TOMBSTONED) and the new version opens
// (txn_from = seq) under the same seq. The new version inherits Type
// and DerivedFrom lineage. Old secondaries remain — versioned history
// is never deleted by revision.
func (s *Store) Supersede(oldID string, in rpc.InputRecord) (rpc.Record, error) {
	if err := in.Validate(); err != nil {
		return rpc.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldSeq, ok := s.idmap[oldID]
	if !ok {
		return rpc.Record{}, rpc.E(rpc.CodeNotFound, "unknown record "+oldID)
	}
	old, err := s.getAt(oldSeq)
	if err != nil {
		return rpc.Record{}, err
	}
	seq := s.next
	id, err := newID()
	if err != nil {
		return rpc.Record{}, err
	}
	closed := old
	closed.Txn.To = int64(seq)
	closed.Status = rpc.StatusTombstoned
	rec := rpc.Record{
		ID:             id,
		Subject:        in.Subject,
		Predicate:      in.Predicate,
		Object:         in.Object,
		ValidTime:      in.ValidTime,
		Txn:            rpc.Interval{From: int64(seq), To: rpc.OpenEnd},
		Provenance:     in.Provenance,
		DerivedFrom:    old.DerivedFrom,
		ACL:            in.ACL,
		Status:         rpc.StatusActive,
		Type:           old.Type,
		BaseConfidence: in.BaseConfidence,
		CallerTSMeta:   in.CallerTSMeta,
		Payload:        in.Payload,
	}
	if err := rec.Validate(); err != nil {
		return rpc.Record{}, err
	}
	closedVal, err := encodeRecord(closed)
	if err != nil {
		return rpc.Record{}, err
	}
	newVal, err := encodeRecord(rec)
	if err != nil {
		return rpc.Record{}, err
	}
	sp := secondarySPKey(rec.Subject, rec.Predicate, seq)
	op := secondaryOPKey(rec.Object, rec.Predicate, seq)
	seqVal := binary.BigEndian.AppendUint64(nil, seq)
	if err := s.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(recordKey(oldSeq), closedVal); err != nil {
			return err
		}
		if err := txn.Set(recordKey(seq), newVal); err != nil {
			return err
		}
		if err := txn.Set(sp, seqVal); err != nil {
			return err
		}
		return txn.Set(op, seqVal)
	}); err != nil {
		return rpc.Record{}, fmt.Errorf("truth: supersede: %w", err)
	}
	s.next++
	s.idmap[id] = seq
	return rec, nil
}

// Restatus implements TruthStore: identical content, new status, one
// atomic entry. Used for BOTH flagging, SCHEDULED assignment, and
// resolution bookkeeping.
func (s *Store) Restatus(id string, status rpc.Status) (rpc.Record, error) {
	if !status.WellFormed() {
		return rpc.Record{}, rpc.E(rpc.CodeInvalidRecord, "unknown status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldSeq, ok := s.idmap[id]
	if !ok {
		return rpc.Record{}, rpc.E(rpc.CodeNotFound, "unknown record "+id)
	}
	old, err := s.getAt(oldSeq)
	if err != nil {
		return rpc.Record{}, err
	}
	seq := s.next
	nid, err := newID()
	if err != nil {
		return rpc.Record{}, err
	}
	closed := old
	closed.Txn.To = int64(seq)
	closed.Status = rpc.StatusTombstoned
	rec := old
	rec.ID = nid
	rec.Txn = rpc.Interval{From: int64(seq), To: rpc.OpenEnd}
	rec.Status = status
	if err := rec.Validate(); err != nil {
		return rpc.Record{}, err
	}
	closedVal, err := encodeRecord(closed)
	if err != nil {
		return rpc.Record{}, err
	}
	newVal, err := encodeRecord(rec)
	if err != nil {
		return rpc.Record{}, err
	}
	// Same (subject,predicate): versioned secondaries gain one more key.
	sp := secondarySPKey(rec.Subject, rec.Predicate, seq)
	op := secondaryOPKey(rec.Object, rec.Predicate, seq)
	seqVal := binary.BigEndian.AppendUint64(nil, seq)
	if err := s.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(recordKey(oldSeq), closedVal); err != nil {
			return err
		}
		if err := txn.Set(recordKey(seq), newVal); err != nil {
			return err
		}
		if err := txn.Set(sp, seqVal); err != nil {
			return err
		}
		return txn.Set(op, seqVal)
	}); err != nil {
		return rpc.Record{}, fmt.Errorf("truth: restatus: %w", err)
	}
	s.next++
	s.idmap[nid] = seq
	return rec, nil
}

// Get implements TruthStore: latest committed version of id.
func (s *Store) Get(id string) (rpc.Record, error) {
	s.mu.Lock()
	seq, ok := s.idmap[id]
	s.mu.Unlock()
	if !ok {
		return rpc.Record{}, rpc.E(rpc.CodeNotFound, "unknown record "+id)
	}
	return s.getAt(seq)
}

// getAt reads one primary key. Callers hold mu where mutation order
// matters; reads take a snapshot either way.
func (s *Store) getAt(seq uint64) (rpc.Record, error) {
	var rec rpc.Record
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(seq))
		if err == badger.ErrKeyNotFound {
			return rpc.E(rpc.CodeNotFound, "record version missing")
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			r, err := decodeRecord(val)
			if err != nil {
				return err
			}
			rec = r
			return nil
		})
	})
	return rec, err
}

// AppendFeedback implements TruthStore: confidence/strength events only,
// never content. No gate, no secondaries — replayed lazily at read.
func (s *Store) AppendFeedback(ev rpc.FeedbackEvent) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	val, err := encodeFeedback(ev)
	if err != nil {
		return err
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(feedbackKey(s.next), val)
	}); err != nil {
		return fmt.Errorf("truth: feedback: %w", err)
	}
	s.next++
	return nil
}

// QueryExact returns records under one (subject,predicate) whose axis
// interval contains t — with NO status filter, per M6: historical
// results are unaffected by current-belief status. Seq-ordered.
// P1 scope: exact-key only; term search is P3.
func (s *Store) QueryExact(sub, pred string, t int64, axis rpc.Axis) ([]rpc.Record, error) {
	if !axis.WellFormed() {
		return nil, rpc.E(rpc.CodeInvalidRecord, "unknown axis")
	}
	prefix := secondarySPPrefix(sub, pred)
	var out []rpc.Record
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			seq, err := parseSeqFromKey(it.Item().Key())
			if err != nil {
				return err
			}
			rec, err := s.getAt(seq)
			if err != nil {
				return err
			}
			var iv rpc.Interval
			if axis == rpc.AxisValid {
				iv = rec.ValidTime
			} else {
				iv = rec.Txn
			}
			if iv.Contains(t) {
				out = append(out, rec)
			}
		}
		return nil
	})
	return out, err
}

// Scan implements TruthStore: commit-ordered replay over [from, tail)
// by merging the R and F iterators on sequence. The first invalid or
// incomplete entry aborts with its offset — loud truncation, and the
// caller decides the boundary (see TruncateFrom).
func (s *Store) Scan(from uint64, fn func(Event) error) error {
	txn := s.db.NewTransaction(false)
	defer txn.Discard()
	ri := txn.NewIterator(prefixIterator([]byte{prefixRecord}))
	defer ri.Close()
	fi := txn.NewIterator(prefixIterator([]byte{prefixFeedback}))
	defer fi.Close()
	seekFrom := func(it *badger.Iterator, prefix []byte) {
		lo := append(append([]byte(nil), prefix...), make([]byte, 8)...)
		binary.BigEndian.PutUint64(lo[len(prefix):], from)
		it.Seek(lo)
	}
	seekFrom(ri, []byte{prefixRecord})
	seekFrom(fi, []byte{prefixFeedback})
	next := func(it *badger.Iterator, prefix []byte) (uint64, []byte, bool, error) {
		if !it.ValidForPrefix(prefix) {
			return 0, nil, false, nil
		}
		item := it.Item()
		seq, err := parseSeqFromKey(item.Key())
		if err != nil {
			return 0, nil, false, err
		}
		var val []byte
		if err := item.Value(func(v []byte) error {
			val = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return 0, nil, false, err
		}
		return seq, val, true, nil
	}
	for {
		rseq, rval, rok, err := next(ri, []byte{prefixRecord})
		if err != nil {
			return err
		}
		fseq, fval, fok, err := next(fi, []byte{prefixFeedback})
		if err != nil {
			return err
		}
		if !rok && !fok {
			return nil
		}
		if fok && (!rok || fseq < rseq) {
			ev, err := decodeFeedback(fval)
			if err != nil {
				return fmt.Errorf("truth: scan: feedback at %d: %w", fseq, err)
			}
			if err := fn(Event{Offset: fseq, Kind: EventFeedback, Feedback: ev}); err != nil {
				return err
			}
			fi.Next()
			continue
		}
		rec, err := decodeRecord(rval)
		if err != nil {
			return fmt.Errorf("truth: scan: record at %d: %w", rseq, err)
		}
		if err := fn(Event{Offset: rseq, Kind: EventRecord, Record: rec}); err != nil {
			return err
		}
		ri.Next()
	}
}

// prefixIterator builds forward iterator options for one prefix.
func prefixIterator(prefix []byte) badger.IteratorOptions {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	return opts
}

// TailOffset implements TruthStore: the next unassigned sequence.
// Callers Sync first when the boundary must cover buffered writes.
func (s *Store) TailOffset() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next, nil
}

// Sync flushes the WAL: the group-commit boundary. P5 awaits Sync
// before acknowledging any verdict it covers (ack implies durable).
func (s *Store) Sync() error {
	return s.db.Sync()
}

// Truncated reports a corrupt tail auto-amputated at open (see recover).
// Scan-reachable history below it is intact by construction.
func (s *Store) Truncated() (bool, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.truncated, s.truncatedAt
}

// truncateFrom deletes every key at sequence >= seq across all versioned
// prefixes and rebuilds the in-memory index. Unexported by design:
// only recovery (auto-amputation) and the future P5 operator path may
// amputate history — never the gate, recall, or consolidation paths.
func (s *Store) truncateFrom(seq uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var doomed [][]byte
	for _, prefix := range [][]byte{
		{prefixRecord}, {prefixFeedback}, {prefixSubPred}, {prefixObjPred},
	} {
		if err := s.db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = prefix
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
				kseq, err := parseSeqFromKey(it.Item().Key())
				if err != nil {
					return err
				}
				if kseq >= seq {
					doomed = append(doomed, append([]byte(nil), it.Item().Key()...))
				}
			}
			return nil
		}); err != nil {
			return 0, err
		}
	}
	wb := s.db.NewWriteBatch()
	defer wb.Cancel()
	for _, k := range doomed {
		if err := wb.Delete(k); err != nil {
			return 0, err
		}
	}
	if err := wb.Flush(); err != nil {
		return 0, err
	}
	s.next = seq
	s.idmap = make(map[string]uint64)
	if err := s.rebuildIDMap(); err != nil {
		return 0, err
	}
	return uint64(len(doomed)), nil
}

// rebuildIDMap rescans R without touching next (caller holds mu).
func (s *Store) rebuildIDMap() error {
	return s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte{prefixRecord}
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				rec, err := decodeRecord(val)
				if err != nil {
					return err
				}
				s.idmap[rec.ID] = mustSeq(item.Key())
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Close implements TruthStore.
func (s *Store) Close() error {
	return s.db.Close()
}

// newID mints an opaque 128-bit hex identity (crypto/rand, stdlib —
// no uuid dependency for one call site).
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("truth: id: %w", err)
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out), nil
}

// ---- value codec: version byte + CRC32C + deterministic body ----

// enum byte mappings (fail-closed: unknown bytes rejected on decode).
const (
	aclLowByte    = 1
	aclUserByte   = 2
	aclSystemByte = 3

	statusActiveByte     = 1
	statusScheduledByte  = 2
	statusBothByte       = 3
	statusTombstonedByte = 4

	typeEpisodicByte = 1
	typeSummaryByte  = 2
	typeSemanticByte = 3

	fbConfirmByte    = 1
	fbDisconfirmByte = 2
	fbCorrectByte    = 3
	fbStrengthByte   = 4
)

func encodeACL(a rpc.AclTier) byte {
	switch a {
	case rpc.ACLLow:
		return aclLowByte
	case rpc.ACLUser:
		return aclUserByte
	default:
		return aclSystemByte
	}
}

func decodeACL(b byte) (rpc.AclTier, error) {
	switch b {
	case aclLowByte:
		return rpc.ACLLow, nil
	case aclUserByte:
		return rpc.ACLUser, nil
	case aclSystemByte:
		return rpc.ACLSystem, nil
	}
	return "", rpc.E(rpc.CodeInvalidRecord, "unknown acl byte")
}

func encodeStatus(st rpc.Status) byte {
	switch st {
	case rpc.StatusActive:
		return statusActiveByte
	case rpc.StatusScheduled:
		return statusScheduledByte
	case rpc.StatusBoth:
		return statusBothByte
	default:
		return statusTombstonedByte
	}
}

func decodeStatus(b byte) (rpc.Status, error) {
	switch b {
	case statusActiveByte:
		return rpc.StatusActive, nil
	case statusScheduledByte:
		return rpc.StatusScheduled, nil
	case statusBothByte:
		return rpc.StatusBoth, nil
	case statusTombstonedByte:
		return rpc.StatusTombstoned, nil
	}
	return "", rpc.E(rpc.CodeInvalidRecord, "unknown status byte")
}

func encodeType(t rpc.RecordType) byte {
	switch t {
	case rpc.RecordEpisodic:
		return typeEpisodicByte
	case rpc.RecordSummary:
		return typeSummaryByte
	default:
		return typeSemanticByte
	}
}

func decodeType(b byte) (rpc.RecordType, error) {
	switch b {
	case typeEpisodicByte:
		return rpc.RecordEpisodic, nil
	case typeSummaryByte:
		return rpc.RecordSummary, nil
	case typeSemanticByte:
		return rpc.RecordSemantic, nil
	}
	return "", rpc.E(rpc.CodeInvalidRecord, "unknown record-type byte")
}

func encodeFeedbackKind(k rpc.FeedbackKind) byte {
	switch k {
	case rpc.FeedbackAttestConfirm:
		return fbConfirmByte
	case rpc.FeedbackAttestDisconfirm:
		return fbDisconfirmByte
	case rpc.FeedbackCorrect:
		return fbCorrectByte
	default:
		return fbStrengthByte
	}
}

func decodeFeedbackKind(b byte) (rpc.FeedbackKind, error) {
	switch b {
	case fbConfirmByte:
		return rpc.FeedbackAttestConfirm, nil
	case fbDisconfirmByte:
		return rpc.FeedbackAttestDisconfirm, nil
	case fbCorrectByte:
		return rpc.FeedbackCorrect, nil
	case fbStrengthByte:
		return rpc.FeedbackStrength, nil
	}
	return "", rpc.E(rpc.CodeInvalidRecord, "unknown feedback-kind byte")
}

// frame splits version + checksum from body, rejecting short values and
// newer-than-known versions fail-closed.
func frame(body []byte) (ver byte, rest []byte, err error) {
	if len(body) < 5 {
		return 0, nil, rpc.E(rpc.CodeCorruptIndex, "short value")
	}
	if body[0] > CodecVersion {
		return 0, nil, rpc.E(rpc.CodeInvalidRecord,
			fmt.Sprintf("unsupported codec version %d", body[0]))
	}
	if crc32.Checksum(body[5:], crcTable) != binary.BigEndian.Uint32(body[1:5]) {
		return 0, nil, rpc.E(rpc.CodeCorruptIndex, "checksum mismatch")
	}
	return body[0], body[5:], nil
}

// seal prepends version + checksum to a body.
func seal(body []byte) []byte {
	out := make([]byte, 0, len(body)+5)
	out = append(out, CodecVersion)
	sum := crc32.Checksum(body, crcTable)
	out = binary.BigEndian.AppendUint32(out, sum)
	return append(out, body...)
}

// cursor is a bounds-checked body reader.
type cursor struct {
	b   []byte
	err error
}

func (c *cursor) uvarint() uint64 {
	if c.err != nil {
		return 0
	}
	v, n := binary.Uvarint(c.b)
	if n <= 0 {
		c.err = rpc.E(rpc.CodeCorruptIndex, "bad varint")
		return 0
	}
	c.b = c.b[n:]
	return v
}

func (c *cursor) bytes() []byte {
	n := c.uvarint()
	if c.err != nil {
		return nil
	}
	if uint64(len(c.b)) < n {
		c.b = nil
		c.err = rpc.E(rpc.CodeCorruptIndex, "short body")
		return nil
	}
	v := c.b[:n]
	c.b = c.b[n:]
	return v
}

func (c *cursor) str() string { return string(c.bytes()) }

func (c *cursor) i64() int64 {
	if c.err != nil {
		return 0
	}
	if len(c.b) < 8 {
		c.b = nil
		c.err = rpc.E(rpc.CodeCorruptIndex, "short body")
		return 0
	}
	v := int64(binary.BigEndian.Uint64(c.b))
	c.b = c.b[8:]
	return v
}

func (c *cursor) u8() byte {
	if c.err != nil {
		return 0
	}
	if len(c.b) < 1 {
		c.err = rpc.E(rpc.CodeCorruptIndex, "short body")
		return 0
	}
	v := c.b[0]
	c.b = c.b[1:]
	return v
}

// count reads a repetition count, failing closed when the count
// exceeds the remaining bytes: every element consumes at least one
// byte (its length prefix), so a larger count is corrupt by
// construction. Without this bound a corrupt value drives a
// multi-billion-iteration loop (slice OOM, not an error) — found by
// FuzzDecodeRecord (H6), fixed here rather than at any call site.
func (c *cursor) count() uint64 {
	n := c.uvarint()
	if c.err != nil {
		return 0
	}
	if n > uint64(len(c.b)) {
		c.err = rpc.E(rpc.CodeCorruptIndex, "impossible repetition count")
		return 0
	}
	return n
}

func (c *cursor) f64() float64 {
	return math.Float64frombits(uint64(c.i64()))
}

func (c *cursor) end() error {
	if c.err != nil {
		return c.err
	}
	if len(c.b) != 0 {
		return rpc.E(rpc.CodeCorruptIndex, "trailing bytes")
	}
	return nil
}

func putStr(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

func putI64(dst []byte, v int64) []byte {
	return binary.BigEndian.AppendUint64(dst, uint64(v))
}

// encodeRecord serializes a stored record deterministically.
func encodeRecord(r rpc.Record) ([]byte, error) {
	body := make([]byte, 0, 256)
	body = putStr(body, r.ID)
	body = putStr(body, r.Subject)
	body = putStr(body, r.Predicate)
	body = putStr(body, r.Object)
	body = putI64(body, r.ValidTime.From)
	body = putI64(body, r.ValidTime.To)
	body = putI64(body, r.Txn.From)
	body = putI64(body, r.Txn.To)
	body = putStr(body, r.Provenance.Source)
	body = putStr(body, r.Provenance.Session)
	body = binary.AppendUvarint(body, uint64(len(r.DerivedFrom)))
	for _, id := range r.DerivedFrom {
		body = putStr(body, id)
	}
	body = append(body, encodeACL(r.ACL), encodeStatus(r.Status), encodeType(r.Type))
	body = putI64(body, int64(math.Float64bits(r.BaseConfidence)))
	body = putI64(body, r.CallerTSMeta)
	names := make([]string, 0, len(r.Payload))
	for k := range r.Payload {
		names = append(names, k)
	}
	sortStrings(names)
	body = binary.AppendUvarint(body, uint64(len(names)))
	for _, k := range names {
		body = putStr(body, k)
		body = putStr(body, r.Payload[k])
	}
	return seal(body), nil
}

// decodeRecord parses and structurally validates a stored record.
func decodeRecord(val []byte) (rpc.Record, error) {
	var r rpc.Record
	_, body, err := frame(val)
	if err != nil {
		return r, err
	}
	c := &cursor{b: body}
	r.ID = c.str()
	r.Subject = c.str()
	r.Predicate = c.str()
	r.Object = c.str()
	r.ValidTime.From = c.i64()
	r.ValidTime.To = c.i64()
	r.Txn.From = c.i64()
	r.Txn.To = c.i64()
	r.Provenance.Source = c.str()
	r.Provenance.Session = c.str()
	for i, n := 0, c.count(); uint64(i) < n; i++ {
		r.DerivedFrom = append(r.DerivedFrom, c.str())
	}
	acl, err := decodeACL(c.u8())
	if err != nil {
		return r, err
	}
	r.ACL = acl
	st, err := decodeStatus(c.u8())
	if err != nil {
		return r, err
	}
	r.Status = st
	typ, err := decodeType(c.u8())
	if err != nil {
		return r, err
	}
	r.Type = typ
	r.BaseConfidence = c.f64()
	r.CallerTSMeta = c.i64()
	r.Payload = make(map[string]string)
	for i, n := 0, c.count(); uint64(i) < n; i++ {
		k := c.str()
		v := c.str()
		if c.err != nil {
			break
		}
		r.Payload[k] = v
	}
	if err := c.end(); err != nil {
		return r, err
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	return r, nil
}

// encodeFeedback serializes a feedback event deterministically.
func encodeFeedback(ev rpc.FeedbackEvent) ([]byte, error) {
	body := make([]byte, 0, 64)
	body = putStr(body, ev.RecordID)
	body = append(body, encodeFeedbackKind(ev.Kind))
	body = putStr(body, ev.Source)
	body = putStr(body, ev.Session)
	body = putI64(body, ev.Tick)
	return seal(body), nil
}

// decodeFeedback parses and validates a feedback event.
func decodeFeedback(val []byte) (rpc.FeedbackEvent, error) {
	var ev rpc.FeedbackEvent
	_, body, err := frame(val)
	if err != nil {
		return ev, err
	}
	c := &cursor{b: body}
	ev.RecordID = c.str()
	kind, err := decodeFeedbackKind(c.u8())
	if err != nil {
		return ev, err
	}
	ev.Kind = kind
	ev.Source = c.str()
	ev.Session = c.str()
	ev.Tick = c.i64()
	if err := c.end(); err != nil {
		return ev, err
	}
	if err := ev.Validate(); err != nil {
		return ev, err
	}
	return ev, nil
}

// sortStrings orders map keys for deterministic encoding.
func sortStrings(s []string) { sort.Strings(s) }
