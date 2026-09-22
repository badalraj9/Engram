package processing

import (
	"math"
	"sort"

	"engram/internal/rpc"
)

// AttestGain is the fixed BKT step for one attest verdict (DESIGN §5).
// v1 fixed provisional — "BKT-style" ships parameterless in the docs,
// and FR16's calibrated list does not include it. Confirm moves c
// toward 1 by the open interval; disconfirm moves it toward 0 by the
// same proportion. Recalibration path: dogfood (UC5) → M-gates/FR16 →
// TECH §2.10 config.
const AttestGain = 0.2

// Clamp01 bounds confidence to [0,1], fail-closed: NaN (unreachable for
// validated inputs, guarded anyway) maps to 0 — never a guess, since
// the abstention gate (P4) treats 0 as below-threshold.
func Clamp01(c float64) float64 {
	if math.IsNaN(c) {
		return 0
	}
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// NoisyOR combines independent witness confidences (FR9): 1 − Π(1−p).
// Callers pass one value per session-cluster (Cluster's output order),
// never per raw record — session-clustering enforced first. Empty
// input is 0: no witnesses, no confidence.
func NoisyOR(ps ...float64) float64 {
	combined := 0.0
	for _, p := range ps {
		combined = 1 - (1-combined)*(1-Clamp01(p))
	}
	return combined
}

// WeakestLink combines derived/consolidated confidences (FR9): min.
// A chain is only as strong as its weakest parent. Empty input is 0.
func WeakestLink(ps ...float64) float64 {
	if len(ps) == 0 {
		return 0
	}
	m := Clamp01(ps[0])
	for _, p := range ps[1:] {
		if q := Clamp01(p); q < m {
			m = q
		}
	}
	return m
}

// AttestUpdate folds one attest verdict BKT-style (DESIGN §5, TECH
// §2.1): confirm applies the gain plus a strength jump downstream
// (P4 records FeedbackStrength alongside); disconfirm mirrors the
// update without touching strength. Binary verdict space only — no
// caller-supplied weights (confidence-poisoning surface).
func AttestUpdate(c float64, confirm bool) float64 {
	c = Clamp01(c)
	if confirm {
		return Clamp01(c + (1-c)*AttestGain)
	}
	return Clamp01(c * (1 - AttestGain))
}

// ApplyFeedback replays FEEDBACK events against a base confidence
// (FR17: replayed lazily at read, never a field overwrite). Events
// fold in tick order (sorted copy; input order irrelevant), so replay
// is deterministic. Attest kinds route through AttestUpdate; correct
// resets to base — the corrected content's confidence is recomputed
// from new provenance at execution, so the old lineage's accumulated
// adjustments do not carry over. Strength events are
// confidence-neutral here: strength/retrievability is P4's R_m input,
// not a confidence term (corroboration-with-content counts through the
// witness set at feed time, never through this fold).
func ApplyFeedback(base float64, evs []rpc.FeedbackEvent) float64 {
	ordered := append([]rpc.FeedbackEvent(nil), evs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Tick < ordered[j].Tick })
	c := Clamp01(base)
	for _, ev := range ordered {
		switch ev.Kind {
		case rpc.FeedbackAttestConfirm:
			c = AttestUpdate(c, true)
		case rpc.FeedbackAttestDisconfirm:
			c = AttestUpdate(c, false)
		case rpc.FeedbackCorrect:
			c = Clamp01(base)
		case rpc.FeedbackStrength:
			// Confidence-neutral by design; strength flows to P4.
		}
	}
	return c
}

// Score composes read-time confidence (DESIGN §1 resolution): the base
// plus independent witnesses via noisy-OR, then FEEDBACK replay.
// witnessConfs carries one confidence per session-cluster (the
// cluster's max base) across the full version lineage, verdicts
// ignored — counting only live records would make witness membership
// depend on gate outcomes (forbidden). Derived records score through
// WeakestLink over parents instead; this function never sees them.
func Score(base float64, witnessConfs []float64, evs []rpc.FeedbackEvent) float64 {
	return ApplyFeedback(NoisyOR(append([]float64{base}, witnessConfs...)...), evs)
}
