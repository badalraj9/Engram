// Package query serves the read path (P4 — recall/as_of).
//
// recall: indexed lookup → ACL pre-filter → rank → abstain. The
// ACL pre-filter runs BEFORE any working computation (FR13): no
// post-hoc filtering of already-combined data — the helper is
// processing.Visible, the enforcement is here. as_of: bi-temporal
// history with no status filter (M6). Neither path calls an LLM or
// a vector DB (frozen NFR2/NFR3): relevance is term hits, ranking is
// FR6 arithmetic.
package query

import (
	"fmt"
	"strings"

	"engram/internal/index"
	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// Spec is the recall query. Weights and thresholds are injected
// (config-owned, TECH §2.10) — this package holds no tuned numbers.
// Budget caps the returned set post-threshold (top-k); Threshold is
// FR16's calibrated value (P5 refreshes it, P4 applies it). Now is
// valid-now in unix-nanoseconds, stamped by the daemon at recall
// execution (DESIGN §1 VALID-NOW) — carried as an explicit parameter
// so tests inject it deterministically; there is no wire field for it.
type Spec struct {
	Subject   string
	Predicate string
	Object    string
	Caller    rpc.AclTier
	Budget    int
	Threshold float64
	Beta      float64
	TrustLow  float64
	TrustUser float64
	TrustSys  float64
	Now       int64
}

// Validate rejects unusable specs fail-closed: unknown callers see
// nothing (never an unfiltered set), non-positive budgets are caller
// bugs. Threshold is unbounded above (FR6 scores are unbounded:
// hits × trust × log-boost routinely exceed 1) with 0 as the floor —
// a negative threshold is a caller bug, never a filter.
func (s Spec) Validate() error {
	if processing.Rank(s.Caller) < 0 {
		return rpc.E(rpc.CodeInvalidRecord, "recall requires a known caller tier")
	}
	if s.Budget <= 0 {
		return rpc.E(rpc.CodeInvalidRecord, "recall budget must be positive")
	}
	if s.Threshold < 0 {
		return rpc.E(rpc.CodeInvalidRecord, "recall threshold must be >= 0")
	}
	if s.Now < 0 {
		return rpc.E(rpc.CodeInvalidRecord, "recall now must be >= 0")
	}
	return nil
}

// Result is the recall outcome: records or ABSTAIN, never a forced
// guess. Abstained is true when nothing clears the threshold (FR7).
// Scores align with Records (the ranker's own curve, shared with
// the abstention gate — DESIGN §3); Confidences carries read-time
// confidence per record (DESIGN §1: base + feedback replay) so
// callers see belief strength alongside rank.
type Result struct {
	Records     []rpc.Record
	Scores      []float64
	Confidences []float64
	Abstained   bool
}

// Recall drives indexed lookup → ACL pre-filter → rank → dedup →
// abstain → budget. Candidates pool Exact + Fuzzy + token-union
// (FR8's chaining primitive at the token level); every candidate is
// verified against the store's latest version (serving-index copies
// go stale on supersede — same rule as intake's gather); staged
// records filter as defense-in-depth beside index exclusion
// (DESIGN §2: three readers). Feedback replays per record from the
// P6 feedback-by-ID secondary (FR17, O(candidates) — never a
// per-recall log scan): strengths feed R_m, attest verdicts feed
// confidence. Guarantee (DESIGN §1 VALID-NOW): every returned record
// has status in {ACTIVE, BOTH}, is not staged, and satisfies
// valid_from <= now < valid_to.
func Recall(idx *index.Index, store *truth.Store, spec Spec) (Result, error) {
	if err := spec.Validate(); err != nil {
		return Result{}, err
	}
	pool := poolCandidates(idx, spec)
	staged, err := store.ListStaged()
	if err != nil {
		return Result{}, err
	}
	now, err := store.TailOffset()
	if err != nil {
		return Result{}, err
	}
	var scored []processing.Scored
	confByID := make(map[string]float64)
	for _, cand := range pool {
		fresh, err := store.Get(cand.ID)
		if err != nil {
			continue
		}
		if !processing.IsLiveCandidate(fresh, staged) {
			continue
		}
		if !visibleTo(fresh, spec.Caller) {
			continue
		}
		// VALID-NOW (DESIGN §1): a future-dated event is not
		// currently-true — SCHEDULED records are hiding, not gone
		// (visible via as_of); expired and not-yet-true windows fall
		// out by the [t-, t+) boundary. Deliberately here, not in
		// IsLiveCandidate: the gate matcher shares that helper and
		// gate semantics for SCHEDULED are unratified.
		if fresh.Status == rpc.StatusScheduled {
			continue
		}
		if fresh.ValidTime.From > spec.Now || spec.Now >= fresh.ValidTime.To {
			continue
		}
		hits := hitCount(idx, spec, fresh.ID)
		if hits == 0 {
			continue
		}
		evs := store.FeedbackFor(fresh.ID)
		r := processing.Retrievability(int64(now), fresh.Txn.From, countStrength(fresh.Provenance, evs))
		conf, corr := confidenceOf(fresh, evs)
		score := processing.RankScore(hits, r, trustOf(fresh.ACL, spec), corr, spec.Beta)
		scored = append(scored, processing.Scored{Record: fresh, Hits: hits, Score: score})
		confByID[fresh.ID] = conf
	}
	// ACL pre-filter already applied per-record above (before any
	// scoring); the helper-level Visible call is unnecessary here —
	// stated so the FR13 audit finds the enforcement point.
	deduped := processing.DedupBySubject(scored)
	kept, abstained := processing.ApplyThreshold(deduped, spec.Threshold)
	if abstained {
		return Result{Abstained: true}, nil
	}
	if len(kept) > spec.Budget {
		kept = kept[:spec.Budget]
	}
	res := Result{}
	for _, s := range kept {
		res.Records = append(res.Records, s.Record)
		res.Scores = append(res.Scores, s.Score)
		res.Confidences = append(res.Confidences, confByID[s.Record.ID])
	}
	return res, nil
}

// poolCandidates unions the three find lanes, deduplicated by ID in
// sorted order (deterministic). Exact serves known keys, Fuzzy the
// MERGE band, Terms the token-level FR8 primitive — queried once
// with AND semantics (not unioned per-token: a ubiquitous token
// like a common predicate would otherwise materialize the whole
// table as candidates — found stalling the 100K bench).
func poolCandidates(idx *index.Index, spec Spec) []rpc.Record {
	ids := make(index.Set)
	for _, r := range idx.Exact(spec.Subject, spec.Predicate) {
		ids.Add(r.ID)
	}
	for _, r := range idx.Fuzzy(spec.Subject, spec.Predicate) {
		ids.Add(r.ID)
	}
	ids.Union(idx.Terms(queryTokens(spec)...))
	var out []rpc.Record
	for _, id := range ids.Sorted() {
		if r, ok := idx.ByID(id); ok {
			out = append(out, r)
		}
	}
	return out
}

// queryTokens normalizes the query into lookup tokens (same
// Normalize the index was built with — query case/whitespace can
// never diverge from build). Multi-word raw fields tokenize
// naturally; empty queries yield no tokens (recall then abstains).
func queryTokens(spec Spec) []string {
	var toks []string
	for _, field := range []string{spec.Subject, spec.Predicate, spec.Object} {
		toks = append(toks, strings.Fields(processing.Normalize(field))...)
	}
	return toks
}

// hitCount is hits(q,m): query tokens present in the record's
// indexed token set. Single-token queries still discriminate via
// the pool (candidates all contain ≥1 pool token by construction —
// the count refines, never redefines, membership).
func hitCount(idx *index.Index, spec Spec, id string) int {
	n := 0
	for _, tok := range queryTokens(spec) {
		if s := idx.Terms(tok); s.Contains(id) {
			n++
		}
	}
	return n
}

// visibleTo is the FR13 pre-filter at record granularity: caller
// sees records at or below its own tier; unknown record tiers never
// enter computation (processing.Visible's rule, applied pre-rank).
func visibleTo(r rpc.Record, caller rpc.AclTier) bool {
	if processing.Rank(r.ACL) < 0 {
		return false
	}
	return processing.Rank(r.ACL) <= processing.Rank(caller)
}

// countStrength counts INDEPENDENT use-confirmations feeding R_m
// (strength is a retrievability input, never a confidence term —
// confidence.go). Self-authored strengths score nothing (the B1 rule:
// author's conviction is already in base) — pre-fix logs may hold
// self-confirm strengths, so the read filters rather than trusting
// the write to have skipped them.
func countStrength(self rpc.Provenance, evs []rpc.FeedbackEvent) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == rpc.FeedbackStrength &&
			processing.Independent(self, rpc.Provenance{Source: ev.Source, Session: ev.Session}) {
			n++
		}
	}
	return n
}

// confidenceOf composes read-time confidence (DESIGN §1): base plus
// witness clusters (provenance + non-disconfirming attesters —
// intake's rule, shared: disconfirm contributes only its BKT drop).
// Self-confirms are discounted in the fold (DiscountSelf — recorded,
// never scoring). corr is supporting-clusters-beyond-self, feeding
// FR6's log boost.
func confidenceOf(rec rpc.Record, evs []rpc.FeedbackEvent) (float64, int) {
	provs := []rpc.Provenance{rec.Provenance}
	for _, ev := range evs {
		if ev.Kind == rpc.FeedbackAttestDisconfirm {
			continue
		}
		provs = append(provs, rpc.Provenance{Source: ev.Source, Session: ev.Session})
	}
	synth := make([]rpc.Record, 0, len(provs))
	for i, p := range provs {
		synth = append(synth, rpc.Record{
			ID:             fmt.Sprintf("witness-%d", i),
			Provenance:     p,
			BaseConfidence: rec.BaseConfidence,
		})
	}
	clusters := processing.Cluster(synth)
	var witnesses []float64
	for i := 1; i < len(clusters); i++ {
		witnesses = append(witnesses, rec.BaseConfidence)
	}
	return processing.Score(rec.BaseConfidence, witnesses, processing.DiscountSelf(rec.Provenance, evs)), len(clusters) - 1
}

// trustOf maps a record tier to the injected weights. Unknown tiers
// weigh 0 — unreachable past visibleTo, guarded anyway.
func trustOf(t rpc.AclTier, spec Spec) float64 {
	switch t {
	case rpc.ACLLow:
		return spec.TrustLow
	case rpc.ACLUser:
		return spec.TrustUser
	case rpc.ACLSystem:
		return spec.TrustSys
	default:
		return 0
	}
}
