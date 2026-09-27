package processing

import (
	"sort"

	"engram/internal/rpc"
)

// Both-state resolution planning (P4 — FR12).
//
// A Both-state persists until explicitly resolved via resolve —
// never silently collapsed (M6). This file plans the transition;
// intake executes it with the store primitives (same decide-pure /
// execute-intake split as the gate). No store access here, no clock.

// StatusTransition closes one record version toward another status
// via Restatus (same one-entry atomicity as Supersede).
type StatusTransition struct {
	ID string
	To rpc.Status
}

// ResolvePlan is the full execution for one resolve call. Merged is
// set on merge only: the replacement content, executed as
// Supersede(loser) so the merged version inherits lineage.
type ResolvePlan struct {
	Transitions []StatusTransition
	Merged      *rpc.InputRecord
}

// PlanResolve maps a Both-state pair + human verdict to transitions.
// keep-a/keep-b: survivor returns ACTIVE, loser tombstones. merge:
// the latest version's claim survives as content (recency), payload
// unions monotonically (survivor wins conflicts — additive safety,
// gate.go's rule), valid-time widens; executed as Supersede over the
// loser with the survivor tombstoned after. Unknown verdicts fail
// closed. Provenance is the resolver's authorship for the merged
// version (a merge is new authorship, not inherited witness).
func PlanResolve(a, b rpc.Record, verdict rpc.ResolveVerdict, prov rpc.Provenance) (ResolvePlan, error) {
	if !verdict.WellFormed() {
		return ResolvePlan{}, rpc.E(rpc.CodeInvalidRecord, "unknown resolve verdict")
	}
	if err := validProvenance(prov); err != nil {
		return ResolvePlan{}, err
	}
	switch verdict {
	case rpc.ResolveKeepA:
		return ResolvePlan{Transitions: []StatusTransition{{a.ID, rpc.StatusActive}, {b.ID, rpc.StatusTombstoned}}}, nil
	case rpc.ResolveKeepB:
		return ResolvePlan{Transitions: []StatusTransition{{b.ID, rpc.StatusActive}, {a.ID, rpc.StatusTombstoned}}}, nil
	}
	survivor, loser := a, b
	if b.Txn.From > a.Txn.From || (b.Txn.From == a.Txn.From && b.ID < a.ID) {
		survivor, loser = b, a
	}
	union, ok := unionPayload(loser.Payload, survivor.Payload)
	if !ok {
		return ResolvePlan{}, rpc.E(rpc.CodeInvalidRecord, "resolve-merge union breaches payload caps; keep-a/keep-b instead")
	}
	from, to := survivor.ValidTime.From, survivor.ValidTime.To
	if loser.ValidTime.From < from {
		from = loser.ValidTime.From
	}
	if loser.ValidTime.To > to {
		to = loser.ValidTime.To
	}
	merged := &rpc.InputRecord{
		Subject:        survivor.Subject,
		Predicate:      survivor.Predicate,
		Object:         survivor.Object,
		ValidTime:      rpc.Interval{From: from, To: to},
		Provenance:     prov,
		ACL:            survivor.ACL,
		BaseConfidence: survivor.BaseConfidence,
		Payload:        union,
	}
	return ResolvePlan{
		Transitions: []StatusTransition{{loser.ID, rpc.StatusTombstoned}, {survivor.ID, rpc.StatusTombstoned}},
		Merged:      merged,
	}, nil
}

// validProvenance rejects blank authorship without importing more.
func validProvenance(p rpc.Provenance) error {
	if !p.WellFormed() {
		return rpc.E(rpc.CodeInvalidRecord, "resolve requires resolver provenance")
	}
	return nil
}

// sortedIDs orders transition IDs for deterministic tests.
func sortedIDs(ts []StatusTransition) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	sort.Strings(out)
	return out
}
