// Package index implements Engram v2's derived find layer (P3 —
// rebuildable index).
//
// Boundary (BUILD-PLAN P3, locked): derived state only. Everything
// here rebuilds from the Truth Store log alone via TailOffset+Scan
// (DESIGN §2) — the log is law, this package is a cache with a
// demonstrated rebuild. Imports truth (replay source), processing
// (Normalize/Key/liveness — single source, never reimplemented), rpc,
// and stdlib only. No clocks, no goroutines, no I/O of its own: the
// host owns time, the store owns durability.
//
// Compatibility (fail-review if broken): the index applies P2's
// Normalize on every key it builds or looks up (P3 conformance — P1
// secondaries store raw strings, keys.go:64-80, while the gate
// compares normalized, matcher.go:36-38). Lookups return FULL
// live-candidate sets per key, never top-1 (P2→P3 gate blocker #2:
// the gate routes what it is shown). Staged records are excluded at
// build (Option A, DESIGN §2 contract); derived/TOMBSTONED filtered
// per IsLiveCandidate parity — BOTH stays matchable.
package index

import (
	"sort"
	"strings"

	"engram/internal/processing"
	"engram/internal/rpc"
)

// Index is the in-memory derived find structure (TECH §2.6, v1
// shape: maps rebuilt from the log; Badger-backed secondaries stay
// P1's). Exact maps normalized (subject,predicate) keys — built with
// processing.Key, the same function the gate matches with — to every
// live version under that key. Terms maps a normalized token to the
// record IDs containing it (subject/predicate/object tokenized on
// whitespace after Normalize). Trigrams maps a subject trigram to
// record IDs for MERGE-band candidate generation (minhash.go).
//
// Zero value is ready. Not safe for concurrent use: the host swaps
// whole indexes atomically after rebuild (DESIGN §2), and single-writer
// serializes live applies.
type Index struct {
	exact    map[string][]rpc.Record
	terms    map[string]Set
	trigrams map[string]Set
	byID     map[string]rpc.Record
}

// tokenize splits a normalized string on whitespace. Normalize
// already collapsed runs, so Fields never yields empties.
func tokenize(norm string) []string {
	return strings.Fields(norm)
}

// indexTerms records every token of the normalized subject,
// predicate, and object against the record ID.
func (x *Index) indexTerms(rec rpc.Record) {
	seen := make(map[string]struct{})
	for _, field := range []string{rec.Subject, rec.Predicate, rec.Object} {
		for _, tok := range tokenize(processing.Normalize(field)) {
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			if x.terms == nil {
				x.terms = make(map[string]Set)
			}
			s := x.terms[tok]
			if s == nil {
				s = make(Set)
			}
			s.Add(rec.ID)
			x.terms[tok] = s
		}
	}
}

// indexTrigrams records the normalized subject's trigram set against
// the record ID (candidate generation only — the Jaccard decision
// stays in processing, minhash.go filters through it).
func (x *Index) indexTrigrams(rec rpc.Record) {
	for g := range processing.Trigrams(rec.Subject) {
		if x.trigrams == nil {
			x.trigrams = make(map[string]Set)
		}
		s := x.trigrams[g]
		if s == nil {
			s = make(Set)
		}
		s.Add(rec.ID)
		x.trigrams[g] = s
	}
}

// Add inserts one record version, applying the liveness filter by
// construction (processing.IsLiveCandidate — the same predicate P2's
// Match enforces, so index membership and gate candidacy cannot
// diverge). Staged arrives as the caller's snapshot set (P2 pattern;
// the durable M/quarantine prefix-scan wires in with P4's single-txn
// writer — this package proves exclusion given the set). Versions are
// immutable and IDs unique per commit, so re-adding an indexed ID is
// always a replay duplicate, never new content: it is skipped,
// making Apply idempotent under double-delivery (H2). Returns false
// when the record is excluded OR already present.
func (x *Index) Add(rec rpc.Record, staged map[string]bool) bool {
	if !processing.IsLiveCandidate(rec, staged) {
		return false
	}
	if x.byID == nil {
		x.byID = make(map[string]rpc.Record)
	}
	if _, dup := x.byID[rec.ID]; dup {
		return false
	}
	key := processing.Key(rec.Subject, rec.Predicate)
	if x.exact == nil {
		x.exact = make(map[string][]rpc.Record)
	}
	x.exact[key] = append(x.exact[key], rec)
	x.byID[rec.ID] = rec
	x.indexTerms(rec)
	x.indexTrigrams(rec)
	return true
}

// Retire drops one record version from the find structure — the
// close half of supersede/restatus on the live-write path (the
// committed new version is Added separately). Without it the
// serving index accumulates stale ACTIVE copies: recall stays
// correct (store-verify rejects them) but the index grows with
// history instead of live state (NFR4) and every recall drags dead
// candidates through point Gets. Found by P4 concurrent-torture
// (serving 141 vs rebuild 1); added here, not in intake, because
// postings removal owns the tokenization this file built.
// Idempotent: absent IDs are a no-op (replay-safe).
func (x *Index) Retire(id string) {
	rec, ok := x.byID[id]
	if !ok {
		return
	}
	delete(x.byID, id)
	key := processing.Key(rec.Subject, rec.Predicate)
	kept := make([]rpc.Record, 0, len(x.exact[key]))
	for _, r := range x.exact[key] {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		delete(x.exact, key)
	} else {
		x.exact[key] = kept
	}
	for _, field := range []string{rec.Subject, rec.Predicate, rec.Object} {
		for _, tok := range tokenize(processing.Normalize(field)) {
			if s, ok := x.terms[tok]; ok {
				delete(s, id)
				if len(s) == 0 {
					delete(x.terms, tok)
				}
			}
		}
	}
	for g := range processing.Trigrams(rec.Subject) {
		if s, ok := x.trigrams[g]; ok {
			delete(s, id)
			if len(s) == 0 {
				delete(x.trigrams, g)
			}
		}
	}
}

// Exact returns the FULL live-candidate set under one normalized
// (subject,predicate) key — every version, never top-1. Empty (nil)
// when no live candidate exists. Records are copies from Add time.
func (x *Index) Exact(subject, predicate string) []rpc.Record {
	return x.exact[processing.Key(subject, predicate)]
}

// ByID returns the indexed version of id, or false. Staged,
// derived, and TOMBSTONED versions are absent by construction (never
// added); BOTH-status versions are present (conflict detection must
// see flagged pairs).
func (x *Index) ByID(id string) (rpc.Record, bool) {
	rec, ok := x.byID[id]
	return rec, ok
}

// Terms returns the IDs containing every given token (AND semantics
// via intersection — the FR8 chaining primitive at the token level).
// Tokens are normalized before lookup, so query case/whitespace can
// never diverge from build. Nil when any token is absent.
func (x *Index) Terms(tokens ...string) Set {
	var out Set
	for i, tok := range tokens {
		norm := processing.Normalize(tok)
		if norm == "" || strings.Contains(norm, " ") {
			return nil
		}
		s := x.terms[norm]
		if len(s) == 0 {
			return nil
		}
		if i == 0 {
			out = s.Clone()
			continue
		}
		out.Intersect(s)
		if len(out) == 0 {
			return nil
		}
	}
	return out
}

// IDs returns the indexed record IDs in sorted order (deterministic
// for tests and rebuild-comparison).
func (x *Index) IDs() []string {
	out := make([]string, 0, len(x.byID))
	for id := range x.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Len reports the count of indexed (live) record versions.
func (x *Index) Len() int { return len(x.byID) }
