package intake

import (
	"fmt"

	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// Correction and attestation (P4 — FR5/FR18, DESIGN §5).
//
// Dispatch key is payload shape (TECH §2.1), never intent
// classification: replacement content targeting a prior claim →
// correct; pure verdict on a prior claim → attest; new content →
// emit. Ambiguous short verdict-like utterances bias attest over
// emit — a missed attestation costs one skipped confidence bump; a
// verdict wrongly emitted manufactures a garbage record.

// LabileWindow is the seq-step window for labile rewrite: a
// correction landing within this many commit sequences of the
// target's birth supersedes in place (lineage inherited); later
// corrections append fresh + tombstone (no lineage carry — the old
// claim's witness history must not launder into the rewrite).
// v1-fixed provisional; recalibration path as RankBeta. Expiry is
// lazy on next touch (DESIGN §4): no timer reads this const.
const LabileWindow = 100000

// Op is the router's dispatch target.
type Op uint8

// Dispatch targets.
const (
	OpEmit Op = iota
	OpCorrect
	OpAttest
)

// Dispatch routes by payload shape: targetsPriorClaim + replacement
// content → correct; prior claim + pure verdict → attest; otherwise
// emit (new propositional content). Ambiguous verdict-like shorts
// arrive here as targetsPriorClaim without content → attest.
func Dispatch(targetsPriorClaim, hasReplacementContent bool) Op {
	switch {
	case targetsPriorClaim && hasReplacementContent:
		return OpCorrect
	case targetsPriorClaim:
		return OpAttest
	default:
		return OpEmit
	}
}

// CorrectHint locates the offending record: normalized SP key plus
// the claimed object for floor-scored selection.
type CorrectHint struct {
	Subject   string
	Predicate string
	Object    string
}

// Correct rewrites wrong content (FR5). Locate via similarity floor
// over the hint — never always-argmax: floor failure falls back to
// INSERT as new content instead of silently rewriting an unrelated
// record. Confidence recomputes from new provenance: a FeedbackCorrect
// event resets the old lineage's adjustments (confidence.go), so the
// rewrite never inherits the error's corroboration.
func (e *Emitter) Correct(hint CorrectHint, correction rpc.InputRecord) (rpc.Record, rpc.Verdict, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := correction.Validate(); err != nil {
		return rpc.Record{}, "", err
	}
	target, ok, err := e.locate(hint)
	if err != nil {
		return rpc.Record{}, "", err
	}
	if !ok {
		return e.correctInsert(correction)
	}
	tail, err := e.store.TailOffset()
	if err != nil {
		return rpc.Record{}, "", err
	}
	fb := rpc.FeedbackEvent{RecordID: target.ID, Kind: rpc.FeedbackCorrect,
		Source: correction.Provenance.Source, Session: correction.Provenance.Session,
		Tick: int64(tail)}
	if int64(tail)-target.Txn.From <= LabileWindow {
		rec, err := e.store.Supersede(target.ID, correction)
		if err != nil {
			return rpc.Record{}, "", err
		}
		e.index.Retire(target.ID)
		fb.RecordID = rec.ID
		if err := e.store.AppendFeedback(fb); err != nil {
			return rpc.Record{}, "", err
		}
		return e.correctAck(rec, rpc.VerdictUpdate)
	}
	rec, err := e.store.Append(correction)
	if err != nil {
		return rpc.Record{}, "", err
	}
	if _, err := e.store.Restatus(target.ID, rpc.StatusTombstoned); err != nil {
		return rpc.Record{}, "", err
	}
	e.index.Retire(target.ID)
	fb.RecordID = rec.ID
	if err := e.store.AppendFeedback(fb); err != nil {
		return rpc.Record{}, "", err
	}
	return e.correctAck(rec, rpc.VerdictInsert)
}

// locate scores same-key + paraphrase candidates by object Jaccard
// and requires CorrectFloor (the MERGE band's floor — one band, one
// constant, never two). Best-below-floor is a miss, not a rewrite.
func (e *Emitter) locate(hint CorrectHint) (rpc.Record, bool, error) {
	seen := make(map[string]bool)
	var pool []rpc.Record
	for _, r := range e.index.Exact(hint.Subject, hint.Predicate) {
		if !seen[r.ID] {
			seen[r.ID] = true
			pool = append(pool, r)
		}
	}
	for _, r := range e.index.Fuzzy(hint.Subject, hint.Predicate) {
		if !seen[r.ID] {
			seen[r.ID] = true
			pool = append(pool, r)
		}
	}
	want := processing.Normalize(hint.Object)
	var best rpc.Record
	bestScore := -1.0
	found := false
	for _, r := range pool {
		fresh, err := e.store.Get(r.ID)
		if err != nil {
			continue
		}
		if fresh.Type.Derived() || fresh.Status == rpc.StatusTombstoned {
			continue
		}
		s := processing.TrigramJaccard(fresh.Object, want)
		if s > bestScore {
			bestScore, best, found = s, fresh, true
		}
	}
	if !found || bestScore < processing.FuzzyMergeFloor {
		return rpc.Record{}, false, nil
	}
	return best, true, nil
}

// correctInsert is the floor-miss fallback: the correction is valid
// live content under no located key → INSERT as new (fail open to a
// new claim, never to a wrong rewrite).
func (e *Emitter) correctInsert(correction rpc.InputRecord) (rpc.Record, rpc.Verdict, error) {
	rec, err := e.store.Append(correction)
	if err != nil {
		return rpc.Record{}, "", err
	}
	return e.correctAck(rec, rpc.VerdictInsert)
}

// correctAck syncs, indexes, and returns the committed revision.
func (e *Emitter) correctAck(rec rpc.Record, v rpc.Verdict) (rpc.Record, rpc.Verdict, error) {
	if err := e.store.Sync(); err != nil {
		return rpc.Record{}, "", err
	}
	staged, err := e.store.ListStaged()
	if err != nil {
		return rpc.Record{}, "", err
	}
	e.index.Add(rec, staged)
	return rec, v, nil
}

// Resolve executes a Both-state resolution (FR12): plans the pair
// transition pure, then commits via Restatus/Supersede in one
// serialized intake hold. keep-a/keep-b restatus both sides
// (survivor ACTIVE, loser TOMBSTONED); merge supersedes over the
// loser with the authored union, then tombstones the survivor.
// Returns the survivor's (or merged) new version ID. Unknown IDs
// fail closed — resolve never invents a pair.
func (e *Emitter) Resolve(aID, bID string, verdict rpc.ResolveVerdict, prov rpc.Provenance) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.store.Get(aID)
	if err != nil {
		return "", err
	}
	b, err := e.store.Get(bID)
	if err != nil {
		return "", err
	}
	plan, err := processing.PlanResolve(a, b, verdict, prov)
	if err != nil {
		return "", err
	}
	var newID string
	if plan.Merged != nil {
		rec, err := e.store.Supersede(plan.Transitions[0].ID, *plan.Merged)
		if err != nil {
			return "", err
		}
		newID = rec.ID
		e.index.Retire(plan.Transitions[0].ID)
		e.index.Add(rec, nil)
		if _, err := e.store.Restatus(plan.Transitions[1].ID, rpc.StatusTombstoned); err != nil {
			return "", err
		}
		e.index.Retire(plan.Transitions[1].ID)
	} else {
		for _, tr := range plan.Transitions {
			rec, err := e.store.Restatus(tr.ID, tr.To)
			if err != nil {
				return "", err
			}
			if tr.To == rpc.StatusActive {
				newID = rec.ID
			}
			e.index.Retire(tr.ID)
			e.index.Add(rec, nil)
		}
	}
	if err := e.store.Sync(); err != nil {
		return "", err
	}
	return newID, nil
}

// Attest folds a verdict on a prior claim (FR18, DESIGN §5): id from
// a prior recall's result set, binary verdict, no caller-supplied
// weights. Confirm applies the BKT gain plus a strength jump
// (confirmation is stronger evidence than mere use — two events);
// disconfirm mirrors the BKT update without touching strength.
// Bypasses the gate entirely (TECH §2.2: gate input would corrupt
// verdict counting). Promotion via confirm requires an INDEPENDENT
// attester — self-attestation never promotes (FR14).
func (e *Emitter) Attest(id string, confirm bool, source, session string) (float64, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, err := e.store.Get(id)
	if err != nil {
		return 0, false, err
	}
	attester := rpc.Provenance{Source: source, Session: session}
	if !attester.WellFormed() {
		return 0, false, rpc.E(rpc.CodeInvalidRecord, "attest requires source and session")
	}
	tail, err := e.store.TailOffset()
	if err != nil {
		return 0, false, err
	}
	kind := rpc.FeedbackAttestConfirm
	if !confirm {
		kind = rpc.FeedbackAttestDisconfirm
	}
	evs := []rpc.FeedbackEvent{{RecordID: id, Kind: kind,
		Source: source, Session: session, Tick: int64(tail)}}
	if confirm {
		evs = append(evs, rpc.FeedbackEvent{RecordID: id, Kind: rpc.FeedbackStrength,
			Source: source, Session: session, Tick: int64(tail)})
	}
	for _, ev := range evs {
		if err := e.store.AppendFeedback(ev); err != nil {
			return 0, false, err
		}
	}
	conf, err := e.replayConfidence(rec)
	if err != nil {
		return 0, false, err
	}
	promoted := false
	if confirm {
		staged, err := e.store.IsStaged(id)
		if err != nil {
			return 0, false, err
		}
		if staged && processing.Independent(rec.Provenance, attester) {
			if err := e.store.Promote(id); err != nil {
				return 0, false, err
			}
			set, err := e.store.ListStaged()
			if err != nil {
				return 0, false, err
			}
			e.index.Add(rec, set)
			promoted = true
		}
	}
	if err := e.store.Sync(); err != nil {
		return 0, false, err
	}
	return conf, promoted, nil
}

// replayConfidence recomputes read-time confidence: base + witness
// clusters folded through Score. Witnesses are the record's
// provenance plus every NON-DISCONFIRMING feedback attester, deduped
// by composite (FR9): noisy-OR combines independent CORROBORATING
// sources (confidence.go), so a disconfirming attester contributes
// only its BKT drop via ApplyFeedback — counting it as a witness
// would lift confidence for negative evidence. FR9's
// "verdict-independent" spans gate verdicts (a MERGE tombstone must
// not drop a witness), never attest polarity.
func (e *Emitter) replayConfidence(rec rpc.Record) (float64, error) {
	provs := []rpc.Provenance{rec.Provenance}
	var evs []rpc.FeedbackEvent
	if err := e.store.Scan(0, func(ev truth.Event) error {
		if ev.Kind != truth.EventFeedback || ev.Feedback.RecordID != rec.ID {
			return nil
		}
		evs = append(evs, ev.Feedback)
		if ev.Feedback.Kind != rpc.FeedbackAttestDisconfirm {
			provs = append(provs, rpc.Provenance{Source: ev.Feedback.Source, Session: ev.Feedback.Session})
		}
		return nil
	}); err != nil {
		return 0, err
	}
	synth := make([]rpc.Record, 0, len(provs))
	for i, p := range provs {
		synth = append(synth, rpc.Record{ID: fmt.Sprintf("witness-%d", i), Provenance: p, BaseConfidence: rec.BaseConfidence})
	}
	clusters := processing.Cluster(synth)
	var witnesses []float64
	for i := 1; i < len(clusters); i++ {
		witnesses = append(witnesses, rec.BaseConfidence)
	}
	return processing.Score(rec.BaseConfidence, witnesses, evs), nil
}
