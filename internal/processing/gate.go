package processing

import (
	"sort"

	"engram/internal/rpc"
)

// Outcome is the gate's decision on one incoming record (DESIGN §4
// table). Verdict-only: the gate never writes — P4's intake layer
// executes (INSERT→Append, UPDATE/MERGE→Supersede, RESTRENGTHEN→
// AppendFeedback, STAGED→Append + quarantine set, SKIP→nothing).
//
// The six rpc verdicts are P0-locked, so Both-state rides Status plus
// FlagTargetBoth instead of a seventh verdict: tier-blocked or
// flagged-pair conflicts store the incoming record with Status BOTH
// and restatus the target (unless already flagged). Status is ACTIVE
// on every other outcome; Merged* are set on MERGE only.
type Outcome struct {
	Verdict        rpc.Verdict
	TargetID       string // matched live candidate, "" when none
	Status         rpc.Status
	FlagTargetBoth bool
	MergedPayload  map[string]string // MERGE only: monotonic union
	MergedValid    rpc.Interval      // MERGE only when widening
	ValidWidened   bool
}

// overlaps reports half-open interval overlap. Adjacent intervals
// ([1,2),[2,3)) do NOT overlap — disjoint by construction, so a
// Friday fact never acks away against a Monday fact ending at Friday.
func overlaps(a, b rpc.Interval) bool {
	return a.From < b.To && b.From < a.To
}

// effectiveRank maps unknown tiers above system: a candidate whose
// tier is unrecognized can never be written over (fail closed — the
// fold then routes to BOTH, never to an unauthorized revision).
// Incoming tiers are always known (Validate rejects the rest).
func effectiveRank(t rpc.AclTier) int {
	if r := Rank(t); r >= 0 {
		return r
	}
	return 3
}

// unionPayload merges incoming keys missing from the candidate
// (existing wins on conflict — additive safety, not a truth judgment:
// an additive verdict has no authority to subtract). ok=false when the
// union would breach the P0 payload caps: both sides arrive validated,
// so only the key count can breach, and an unwritable union is not a
// delta — the fold falls back to the ack path (fail closed at decision
// time, not write time).
func unionPayload(in, cand map[string]string) (union map[string]string, ok bool) {
	union = make(map[string]string, len(cand)+len(in))
	for k, v := range cand {
		union[k] = v
	}
	for k, v := range in {
		if _, exists := union[k]; !exists {
			union[k] = v
		}
	}
	if len(union) > rpc.MaxPayloadKeys {
		return nil, false
	}
	return union, true
}

// latest picks the most recent claim (highest txn-from, i.e. current
// belief); ties break by smallest ID. Deterministic, complete-case.
func latest(rs []*rpc.Record) *rpc.Record {
	best := rs[0]
	for _, r := range rs[1:] {
		if r.Txn.From > best.Txn.From || (r.Txn.From == best.Txn.From && r.ID < best.ID) {
			best = r
		}
	}
	return best
}

// bothOutcome stores the incoming event as a flagged claim: it joins
// (or opens, via flag) a Both-state pair awaiting resolve — never
// silently overwritten (FR12). Pairs already flagged don't stack.
func bothOutcome(target *rpc.Record) Outcome {
	return Outcome{
		Verdict:        rpc.VerdictInsert,
		TargetID:       target.ID,
		Status:         rpc.StatusBoth,
		FlagTargetBoth: target.Status != rpc.StatusBoth,
	}
}

// Decide runs the five-way gate plus STAGED over in-memory candidates
// (DESIGN §4 table). Order: validate (fail closed) → entry quarantine
// (low→STAGED, never reaches the fold) → match → max-severity fold
// (conflict > same-SPO > disjoint > INSERT, exact relationships
// dominate fuzzy ones throughout — v1 exact-first default).
func Decide(in rpc.InputRecord, cands []rpc.Record, staged map[string]bool) (Outcome, error) {
	if err := in.Validate(); err != nil {
		return Outcome{}, err
	}
	if in.ACL.Quarantined() {
		return Outcome{Verdict: rpc.VerdictStaged, Status: rpc.StatusActive}, nil
	}
	exact, fuzzy := Match(cands, staged, in)
	if len(exact) == 0 {
		return decideFuzzy(in, fuzzy), nil
	}
	return decideExact(in, exact), nil
}

// decideFuzzy handles paraphrase-only matches: same-object paraphrase
// merges (the entity is identified, attributes union); different
// objects never conflict on paraphrase (unproven identity → INSERT,
// never UPDATE). Cap-breach falls back to INSERT — the content is
// valid and live under no exact key.
func decideFuzzy(in rpc.InputRecord, fuzzy []*rpc.Record) Outcome {
	inObj := Normalize(in.Object)
	for _, r := range fuzzy {
		if Normalize(r.Object) != inObj {
			continue
		}
		union, ok := unionPayload(in.Payload, r.Payload)
		if !ok {
			return Outcome{Verdict: rpc.VerdictInsert, Status: rpc.StatusActive}
		}
		return Outcome{
			Verdict:       rpc.VerdictMerge,
			TargetID:      r.ID,
			Status:        rpc.StatusActive,
			MergedPayload: union,
		}
	}
	return Outcome{Verdict: rpc.VerdictInsert, Status: rpc.StatusActive}
}

// decideExact folds exact-key candidates by max severity. BOTH-status
// candidates are visible to the conflict branch only (flagged pairs
// neither stack nor serve as revision targets — revision would
// tombstone the flag before resolve); every other relationship with a
// flagged candidate is evaluated as if it were absent, except the ack
// branch may still name it.
func decideExact(in rpc.InputRecord, exact []*rpc.Record) Outcome {
	inObj := Normalize(in.Object)
	var conflicts, sames, disjoints, flagged []*rpc.Record
	for _, r := range exact {
		if r.Status == rpc.StatusBoth {
			flagged = append(flagged, r)
			continue
		}
		switch {
		case Normalize(r.Object) != inObj && overlaps(r.ValidTime, in.ValidTime):
			conflicts = append(conflicts, r)
		case Normalize(r.Object) == inObj && overlaps(r.ValidTime, in.ValidTime):
			sames = append(sames, r)
		default:
			disjoints = append(disjoints, r)
		}
	}
	// Conflict dominates corroboration (§4.4a): an unprocessed conflict
	// corrupts belief state; an unprocessed corroboration is just an
	// uncounted witness.
	if len(conflicts) > 0 {
		target := latest(conflicts)
		if effectiveRank(in.ACL) >= effectiveRank(target.ACL) {
			return Outcome{Verdict: rpc.VerdictUpdate, TargetID: target.ID, Status: rpc.StatusActive}
		}
		return bothOutcome(target)
	}
	// Same SPO, overlapping (§4.4b): content delta merges, pure
	// reassertion acknowledges. Delta is measured against the latest
	// live claim — the store knowing strictly more already is an ack,
	// not a merge.
	if len(sames) > 0 {
		ordered := append([]*rpc.Record(nil), sames...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Txn.From > ordered[j].Txn.From })
		for _, cand := range ordered {
			union, ok := unionPayload(in.Payload, cand.Payload)
			widens := in.ValidTime.From < cand.ValidTime.From || in.ValidTime.To > cand.ValidTime.To
			if !ok {
				continue // unwritable union is not a delta
			}
			if len(union) > len(cand.Payload) || widens {
				if effectiveRank(in.ACL) < effectiveRank(cand.ACL) {
					return bothOutcome(cand)
				}
				out := Outcome{
					Verdict:       rpc.VerdictMerge,
					TargetID:      cand.ID,
					Status:        rpc.StatusActive,
					MergedPayload: union,
				}
				if widens {
					from, to := cand.ValidTime.From, cand.ValidTime.To
					if in.ValidTime.From < from {
						from = in.ValidTime.From
					}
					if in.ValidTime.To > to {
						to = in.ValidTime.To
					}
					out.MergedValid = rpc.Interval{From: from, To: to}
					out.ValidWidened = true
				}
				return out
			}
		}
		head := latest(sames)
		if in.Provenance.Source == head.Provenance.Source && in.Provenance.Session == head.Provenance.Session {
			return Outcome{Verdict: rpc.VerdictSkip, TargetID: head.ID, Status: rpc.StatusActive}
		}
		return Outcome{Verdict: rpc.VerdictRestrengthen, TargetID: head.ID, Status: rpc.StatusActive}
	}
	// Same SPO, disjoint valid-time (§4.4c): a new temporal assertion,
	// plain append — both records legitimately live.
	if len(disjoints) > 0 {
		return Outcome{Verdict: rpc.VerdictInsert, Status: rpc.StatusActive}
	}
	// Only flagged candidates remain: new contradictions join the
	// flagged set; anything else acks against the latest flag.
	for _, r := range flagged {
		if Normalize(r.Object) != inObj && overlaps(r.ValidTime, in.ValidTime) {
			return bothOutcome(r)
		}
	}
	if len(flagged) > 0 {
		head := latest(flagged)
		if in.Provenance.Source == head.Provenance.Source && in.Provenance.Session == head.Provenance.Session {
			return Outcome{Verdict: rpc.VerdictSkip, TargetID: head.ID, Status: rpc.StatusActive}
		}
		return Outcome{Verdict: rpc.VerdictRestrengthen, TargetID: head.ID, Status: rpc.StatusActive}
	}
	return Outcome{Verdict: rpc.VerdictInsert, Status: rpc.StatusActive}
}
