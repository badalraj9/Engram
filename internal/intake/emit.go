// Package intake executes the write path (P4 — emit/correct/attest).
//
// Split of duties (locked): processing decides (pure Outcome over
// in-memory candidates), intake executes against the store and keeps
// the serving index fresh. Intake imports processing + truth + index
// + rpc + stdlib; nothing existing imports intake, so no cycle.
//
// Single-writer: Emitter serializes emits (mu) — the serving index
// is not concurrent-safe (index/invert.go) and the store relies on
// explicit serialization. Acknowledgment implies durability: every
// mutating path Syncs before returning (store.go:Sync); NOTHING is
// confirmed before its covering sync returns.
package intake

import (
	"sync"
	"time"

	"engram/internal/index"
	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// Limiter is a pre-gate token bucket per (source, session) (FR15):
// flooding is rejected before matching, so a flood can never
// contaminate gate decisions — it only costs a map lookup. Burst
// equals rate (one second of tokens). Buckets are keyed by composite
// and unbounded: sized by deployment session cardinality (dogfood
// numbers decide if lazy eviction is ever needed — not this phase).
// Time is injected (AllowAt) so tests are deterministic; production
// passes time.Now via Allow.
type Limiter struct {
	mu      sync.Mutex
	perSec  int
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter builds a limiter allowing perSec emits per key per second.
func NewLimiter(perSec int) *Limiter {
	if perSec <= 0 {
		perSec = 1
	}
	return &Limiter{perSec: perSec, buckets: make(map[string]*bucket)}
}

// AllowAt reports whether key may emit at now, consuming one token.
func (l *Limiter) AllowAt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.perSec), last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * float64(l.perSec)
		if b.tokens > float64(l.perSec) {
			b.tokens = float64(l.perSec)
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Allow is the production entry: wall-clock time (rate limiting is
// wall-clock by nature — never transaction-time).
func (l *Limiter) Allow(key string) bool { return l.AllowAt(key, time.Now()) }

// Emitter executes Outcome against the store. Zero value invalid;
// construct with NewEmitter. Store is concrete *truth.Store (not the
// P0 iface) because staging/promotion are additive concrete methods
// by contract — the iface stays closed.
type Emitter struct {
	mu      sync.Mutex
	store   *truth.Store
	index   *index.Index
	limiter *Limiter
}

// NewEmitter wires store + serving index + rate limit.
func NewEmitter(store *truth.Store, idx *index.Index, perSec int) *Emitter {
	return &Emitter{store: store, index: idx, limiter: NewLimiter(perSec)}
}

// EmitResult is the acknowledged write outcome: the gate verdict
// plus the IDs it produced. ID is the newly committed version
// (empty on SKIP); TargetID names the matched candidate, if any;
// FlaggedID carries the target's flagged-BOTH version on the
// tier-blocked path (Restatus mints a new ID — without it the
// pair's other half would be unaddressable).
type EmitResult struct {
	Verdict   rpc.Verdict
	ID        string
	TargetID  string
	FlaggedID string
}

// rateKey scopes FR15 per source/session composite (FR9 identity:
// session string alone is not identity across sources).
func rateKey(in rpc.InputRecord) string {
	return in.Provenance.Source + "\x00" + in.Provenance.Session
}

// Emit validates → rate-limits (pre-gate) → decides → executes →
// syncs → acks. Invalid input fails closed without consuming a
// token. Rate-limited emits return CodeRateLimited and write
// nothing. SKIP writes nothing and returns the matched target.
func (e *Emitter) Emit(in rpc.InputRecord) (EmitResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := in.Validate(); err != nil {
		return EmitResult{}, err
	}
	if !e.limiter.Allow(rateKey(in)) {
		return EmitResult{}, rpc.E(rpc.CodeRateLimited, "emit rate limit exceeded for source/session")
	}
	staged, err := e.store.ListStaged()
	if err != nil {
		return EmitResult{}, err
	}
	cands, err := e.liveCandidates(in)
	if err != nil {
		return EmitResult{}, err
	}
	out, err := processing.Decide(in, cands, staged)
	if err != nil {
		return EmitResult{}, err
	}
	res, err := e.execute(in, out)
	if err != nil {
		return EmitResult{}, err
	}
	// Durability before acknowledgment: the covering sync returns
	// before any verdict leaves this function (frozen rule). Index
	// mutation follows — the index is derived, never the ack basis.
	if err := e.store.Sync(); err != nil {
		return EmitResult{}, err
	}
	e.applyIndex(in, out, res)
	return res, nil
}

// liveCandidates gathers index Exact + Fuzzy sets and verifies each
// against the store's latest version: the serving index holds copies
// from Add time, so a superseded/restatus version reads stale here.
// The store is authoritative — tombstoned or revised copies are
// dropped (or refreshed) before the gate ever sees them. No P3 edit
// needed; point Gets over a handful of candidates.
func (e *Emitter) liveCandidates(in rpc.InputRecord) ([]rpc.Record, error) {
	seen := make(map[string]bool)
	var pool []rpc.Record
	for _, r := range e.index.Exact(in.Subject, in.Predicate) {
		if !seen[r.ID] {
			seen[r.ID] = true
			pool = append(pool, r)
		}
	}
	for _, r := range e.index.Fuzzy(in.Subject, in.Predicate) {
		if !seen[r.ID] {
			seen[r.ID] = true
			pool = append(pool, r)
		}
	}
	out := make([]rpc.Record, 0, len(pool))
	for _, r := range pool {
		fresh, err := e.store.Get(r.ID)
		if err != nil {
			continue // tombstoned-gone or otherwise unreadable: absent, never partial
		}
		if fresh.Type.Derived() || fresh.Status == rpc.StatusTombstoned {
			continue
		}
		out = append(out, fresh)
	}
	return out, nil
}

// execute maps Outcome to store primitives (P2-verified mapping):
// INSERT→Append, UPDATE/MERGE→Supersede, RESTRENGTHEN→AppendFeedback,
// STAGED→StageAppend, SKIP→nothing, tier-blocked Both→Append +
// Restatus×2. Returns the committed IDs for index application.
// maybeSchedule labels a newly committed version SCHEDULED when its
// valid-time starts in the future (DESIGN §1 VALID-NOW, P1 Phase B):
// a future-dated event is hiding, not gone. Append-only preserved —
// Restatus closes + appends, returning the new version (new id) for
// index application. STAGED versions skip this (quarantine already
// hides them); BOTH-flagged versions skip it (conflict visibility
// wins — a future-dated conflict must still surface for resolve).
func (e *Emitter) maybeSchedule(rec rpc.Record) (rpc.Record, error) {
	if rec.ValidTime.From <= time.Now().UnixNano() {
		return rec, nil
	}
	return e.store.Restatus(rec.ID, rpc.StatusScheduled)
}

func (e *Emitter) execute(in rpc.InputRecord, out processing.Outcome) (EmitResult, error) {
	tick, err := e.store.TailOffset()
	if err != nil {
		return EmitResult{}, err
	}
	switch out.Verdict {
	case rpc.VerdictSkip:
		return EmitResult{Verdict: rpc.VerdictSkip, TargetID: out.TargetID}, nil
	case rpc.VerdictStaged:
		rec, err := e.store.StageAppend(in)
		if err != nil {
			return EmitResult{}, err
		}
		return EmitResult{Verdict: rpc.VerdictStaged, ID: rec.ID}, nil
	case rpc.VerdictRestrengthen:
		if err := e.store.AppendFeedback(rpc.FeedbackEvent{
			RecordID: out.TargetID, Kind: rpc.FeedbackStrength,
			Source: in.Provenance.Source, Session: in.Provenance.Session,
			Tick: int64(tick),
		}); err != nil {
			return EmitResult{}, err
		}
		if err := e.promoteCorroborated(in); err != nil {
			return EmitResult{}, err
		}
		return EmitResult{Verdict: rpc.VerdictRestrengthen, ID: out.TargetID, TargetID: out.TargetID}, nil
	case rpc.VerdictMerge:
		merged := mergeInput(in, out)
		rec, err := e.store.Supersede(out.TargetID, merged)
		if err != nil {
			return EmitResult{}, err
		}
		if rec, err = e.maybeSchedule(rec); err != nil {
			return EmitResult{}, err
		}
		e.index.Retire(out.TargetID)
		if err := e.promoteCorroborated(in); err != nil {
			return EmitResult{}, err
		}
		return EmitResult{Verdict: rpc.VerdictMerge, ID: rec.ID, TargetID: out.TargetID}, nil
	case rpc.VerdictUpdate:
		rec, err := e.store.Supersede(out.TargetID, in)
		if err != nil {
			return EmitResult{}, err
		}
		if rec, err = e.maybeSchedule(rec); err != nil {
			return EmitResult{}, err
		}
		e.index.Retire(out.TargetID)
		if err := e.promoteCorroborated(in); err != nil {
			return EmitResult{}, err
		}
		return EmitResult{Verdict: rpc.VerdictUpdate, ID: rec.ID, TargetID: out.TargetID}, nil
	case rpc.VerdictInsert:
		rec, err := e.store.Append(in)
		if err != nil {
			return EmitResult{}, err
		}
		if out.Status != rpc.StatusBoth {
			if rec, err = e.maybeSchedule(rec); err != nil {
				return EmitResult{}, err
			}
		}
		res := EmitResult{Verdict: rpc.VerdictInsert, ID: rec.ID, TargetID: out.TargetID}
		if out.Status != rpc.StatusBoth {
			// Plain insert is still a corroborating signal for
			// same-key staged versions (FR14 second-signal leg —
			// corroboration-with-content counts, DESIGN §4.8).
			// Flagged-BOTH inserts establish nothing (conflict,
			// not corroboration) and skip the scan.
			if err := e.promoteCorroborated(in); err != nil {
				return EmitResult{}, err
			}
			return res, nil
		}
		// Tier-blocked or flagged-pair conflict: the incoming claim
		// joins (or opens) a Both-state pair (FR12) — stored BOTH,
		// target flagged unless already flagged.
		flagged, err := e.store.Restatus(rec.ID, rpc.StatusBoth)
		if err != nil {
			return EmitResult{}, err
		}
		res.ID = flagged.ID
		if out.FlagTargetBoth {
			flaggedT, err := e.store.Restatus(out.TargetID, rpc.StatusBoth)
			if err != nil {
				return EmitResult{}, err
			}
			res.FlaggedID = flaggedT.ID
		}
		if out.TargetID != "" {
			e.index.Retire(out.TargetID)
		}
		return res, nil
	default:
		return EmitResult{}, rpc.E(rpc.CodeCorruptIndex, "intake: unknown verdict")
	}
}

// mergeInput authors the merged version from the incoming event:
// incoming SPO + monotonic union payload + widened valid-time when
// the fold found widening. Retained content's witness stays on the
// superseded version (receipts in version history — DESIGN §4).
func mergeInput(in rpc.InputRecord, out processing.Outcome) rpc.InputRecord {
	merged := in
	if out.MergedPayload != nil {
		merged.Payload = out.MergedPayload
	}
	if out.ValidWidened {
		merged.ValidTime = out.MergedValid
	}
	return merged
}

// promoteCorroborated implements FR14's second-signal promotion leg:
// staged versions under the incoming key with an INDEPENDENT
// (source, session) corroborator are peeled — self-corroboration
// never promotes. Runs on every content-establishing verdict
// (INSERT-active, MERGE, UPDATE, RESTRENGTHEN): corroboration with
// content counts exactly like bare corroboration (DESIGN §4.8).
// Flagged-BOTH inserts and STAGED/SKIP never scan (conflict and
// first-signal establish nothing). Staged sets are tiny; Get-per-ID
// is cheap.
func (e *Emitter) promoteCorroborated(in rpc.InputRecord) error {
	staged, err := e.store.ListStaged()
	if err != nil {
		return err
	}
	if len(staged) == 0 {
		return nil
	}
	want := processing.Key(in.Subject, in.Predicate)
	for id := range staged {
		rec, err := e.store.Get(id)
		if err != nil {
			continue
		}
		if processing.Key(rec.Subject, rec.Predicate) != want {
			continue
		}
		if !processing.Independent(rec.Provenance, in.Provenance) {
			continue
		}
		if err := e.store.Promote(id); err != nil {
			return err
		}
		e.index.Add(rec, nil)
	}
	return nil
}

// applyIndex folds committed versions into the serving index (DESIGN
// §2 live-write path: past-the-boundary writes apply directly, in
// log order). Both halves of a Both flag enter (incoming + flagged
// target — the pair must be jointly findable). Feedback-only
// outcomes touch no find structure (FR17: replayed lazily at read).
// Tombstoned revisions are filtered by Add's liveness check; BOTH
// versions stay matchable.
func (e *Emitter) applyIndex(in rpc.InputRecord, out processing.Outcome, res EmitResult) {
	staged, err := e.store.ListStaged()
	if err != nil {
		return
	}
	for _, id := range []string{res.ID, res.FlaggedID} {
		if id == "" {
			continue
		}
		rec, err := e.store.Get(id)
		if err != nil {
			continue
		}
		e.index.Add(rec, staged)
	}
}
