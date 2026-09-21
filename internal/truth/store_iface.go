// Package truth declares the Truth Store contract (P0).
//
// The store is the append-only authoritative log (SRS FR17). Every
// derived structure (index, MinHash, bitsets) rebuilds from this log
// alone (DESIGN §2). P1 provides the Badger-backed implementation;
// later phases program against this interface (escape-hatch rule).
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
type TruthStore interface {
	// Append commits a new record version, assigning its ID and
	// transaction-time from the server clock value txn.
	Append(in rpc.InputRecord, txn int64) (rpc.Record, error)

	// CloseInterval closes a record's transaction interval at txn
	// (supersede/correction/tombstone). The record is retained for
	// historical/as_of queries, never deleted.
	CloseInterval(id string, txn int64) error

	// Get returns the latest committed version of id.
	Get(id string) (rpc.Record, error)

	// AppendFeedback commits a confidence/strength update event.
	AppendFeedback(ev rpc.FeedbackEvent) error

	// TailOffset returns the current log tail. A rebuild records this
	// first and replays only up to it; live writes past the boundary
	// apply directly to the serving index (DESIGN §2).
	TailOffset() (uint64, error)

	// Scan replays events in [from, tail) order, calling fn per event.
	Scan(from uint64, fn func(Event) error) error

	// Close flushes and releases resources.
	Close() error
}
