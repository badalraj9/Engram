package processing

import (
	"math"
	"sort"

	"engram/internal/rpc"
)

// RankBeta is the retrievability exponent in FR6's score curve
// (DESIGN §3: beta ~ 0.5 to start, fixed, no learning in v1).
// Recalibration path: dogfood (UC5) → M-gates/FR16 → TECH §2.10 config.
const RankBeta = 0.5

// RecencyHalfLife is the seq-step half-life for retrievability decay:
// R halves per this many commit-sequence steps of age. v1-fixed
// provisional — the docs specify lazy exponential decay, not the
// constant. Recalibration path as RankBeta.
const RecencyHalfLife = 10000.0

// TrustWeight maps a tier to its FR6 w_trust multiplier. Defaults
// mirror config.Default (TrustLow/User/System); callers inject
// config-loaded values into RankScore directly — this helper is the
// tested default, not a second source. Unknown tiers weigh 0: they
// can never outrank known content (fail closed, same rule as Rank).
func TrustWeight(t rpc.AclTier) float64 {
	switch t {
	case rpc.ACLLow:
		return 0.25
	case rpc.ACLUser:
		return 1.0
	case rpc.ACLSystem:
		return 1.5
	default:
		return 0
	}
}

// Retrievability computes R_m(t): exponential recency decay over
// commit-sequence age, lifted by use. Each strength event divides the
// effective age — a touched record reads fresher, which is DESIGN
// §3's free warm-context win stated as math. Pure, deterministic;
// future ages clamp to 0 (fresh, never negative).
func Retrievability(nowSeq, txnFrom int64, strengths int) float64 {
	if strengths < 0 {
		strengths = 0
	}
	age := nowSeq - txnFrom
	if age < 0 {
		age = 0
	}
	eff := float64(age) / float64(1+strengths)
	return Clamp01(math.Pow(0.5, eff/RecencyHalfLife))
}

// RankScore computes FR6's curve exactly as specified (SRS FR6,
// DESIGN §3): hits × R^beta × w_trust × (1 + log(1+corr)).
// Guards fail closed: no hits → 0 (irrelevant, never ranked);
// negative corr → 0; NaN trust → 0 (never a guess); NaN/negative
// beta falls back to RankBeta (a corrupt exponent must not zero or
// invert the curve). R is clamped by construction.
func RankScore(hits int, r, trust float64, corr int, beta float64) float64 {
	if hits <= 0 {
		return 0
	}
	if corr < 0 {
		corr = 0
	}
	if math.IsNaN(beta) || beta < 0 {
		beta = RankBeta
	}
	if math.IsNaN(trust) || math.IsInf(trust, 0) || trust < 0 {
		trust = 0
	}
	return float64(hits) * math.Pow(Clamp01(r), beta) * trust * (1 + math.Log(1+float64(corr)))
}

// Scored pairs a candidate with its retrieval inputs and output.
// Hits is the index-hit count; Score is RankScore's value.
type Scored struct {
	Record rpc.Record
	Hits   int
	Score  float64
}

// DedupBySubject enforces FR6's per-subject rule: never return two
// records about the same entity in one result set. Keeps the max
// score per normalized subject (ties break by smallest ID),
// output ordered by score desc, ID tiebreak. Input never mutated.
func DedupBySubject(in []Scored) []Scored {
	best := make(map[string]Scored)
	for _, s := range in {
		k := Normalize(s.Record.Subject)
		cur, ok := best[k]
		if !ok || s.Score > cur.Score || (s.Score == cur.Score && s.Record.ID < cur.Record.ID) {
			best[k] = s
		}
	}
	out := make([]Scored, 0, len(best))
	for _, s := range best {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Record.ID < out[j].Record.ID
	})
	return out
}
