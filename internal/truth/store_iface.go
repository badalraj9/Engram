// Package truth declares the Truth Store contract (P0, closed pre-P1).
//
// The store is the append-only authoritative log (SRS FR17). Every
// derived structure (index, MinHash, bitsets, ID→seq lookup) rebuilds
// from this log alone (DESIGN §2). P1 provides the Badger-backed
// implementation; later phases program against this interface
// (escape-hatch rule).
//
// Through-line (locked): one writer, one clock, one unit of atomicity,
// one authority for acknowledgment.
//   - The store owns a monotonic commit sequence. That sequence IS the
//     transaction-time axis: birth seq → txn_from, close seq → txn_to,
//     open end otherwise. No separate txn field can drift because none
//     exists — seq and txn are born together, in the same function.
//   - Record IDs (UUIDv4, opaque) live in the value, never in the key
//     path. The ID→seq lookup is derived state: never logged, never
//     required for replay, rebuilt in the deterministic index pass.
//   - Primary key = prefix + commit sequence (uint64 big-endian), so
//     every version is a distinct key and compaction can never merge
//     multi-version history — by construction, not by config flag.
//   - Acknowledgment implies durability: sync boundaries are explicit
//     group commits, and nothing is confirmed before the sync covering
//     it returns.
package truth

import "engram/internal/rpc"

// EventKind distinguishes log entries.
type EventKind string

// Log entry kinds.
const (
	EventRecord   EventKind = "record"   // new record version (INSERT/MERGE/UPDATE append)
	EventClose    EventKind = "close"    // interval close (supersede/correction/tombstone)
	EventFeedback EventKind = "feedback" // confidence/strength update (FR17)
)

// Event is one entry in the append-only log, in commit order.
// Exactly one of Record / Feedback is populated, per Kind.
type Event struct {
	Offset   uint64
	Kind     EventKind
	Record   rpc.Record
	Feedback rpc.FeedbackEvent
}

// TruthStore is the authoritative record log.
//
// Half-supersede is unrepresentable by construction: multi-action
// revision goes through Supersede only. CloseInterval-style two-step
// writes exist, if at all, as unexported implementation building
// blocks — never as a public path.
type TruthStore interface {
	// Append commits a new record version (INSERT path), assigning its
	// ID and transaction-time from the store's own sequence.
	Append(in rpc.InputRecord) (rpc.Record, error)

	// Supersede atomically closes oldID's transaction interval and
	// appends the new version as ONE log entry: no partial state can
	// exist, because there is no such thing as half of one entry.
	// Serves state-change supersede; the new version inherits the old
	// record's Type and DerivedFrom lineage. Correction and Both-state
	// resolution reuse the same shape (see Restatus for status-only
	// transitions).
	Supersede(oldID string, in rpc.InputRecord) (rpc.Record, error)

	// AppendDerived commits a server-synthesized record (consolidation
	// lane, FR10): typ must be SUMMARY or SEMANTIC with non-empty
	// derivedFrom. Bypasses the write gate — no STAGED, status ACTIVE.
	AppendDerived(in rpc.InputRecord, typ rpc.RecordType, derivedFrom []string) (rpc.Record, error)

	// Restatus atomically closes id and re-appends identical content
	// under the given status (BOTH flagging, SCHEDULED assignment,
	// resolution bookkeeping) — same one-entry atomicity as Supersede.
	Restatus(id string, status rpc.Status) (rpc.Record, error)

	// Get returns the latest committed version of id.
	Get(id string) (rpc.Record, error)

	// AppendFeedback commits a confidence/strength update event.
	AppendFeedback(ev rpc.FeedbackEvent) error

	// TailOffset returns the current log tail. A rebuild records this
	// first and replays only up to it; live writes past the boundary
	// apply directly to the serving index (DESIGN §2).
	TailOffset() (uint64, error)

	// Scan replays events in [from, tail) order, calling fn per event.
	// Replay validates each entry's checksum and truncates loudly at
	// the first invalid or incomplete entry.
	Scan(from uint64, fn func(Event) error) error

	// Close flushes and releases resources.
	Close() error
}
