// Package dynamics is Engram v2's transient working-memory layer (P6).
//
// Scope (BUILD-PLAN P6: stubbed — FHRR production deferred to v1.1):
// pure phasor primitives only. No recall wiring, no persistence, no
// goroutine, no model, no vector DB (frozen NFR2/NFR3). Production
// recall stays lexical (BUILD-PLAN:3); this package exists so the M1–M5
// measurement gates run against real math, not theory (SRS §7, C2).
// v1.1 plugs a working bundle into recall without touching the core —
// the seam is these three files (BUILD-RANKS D).
//
// Representation (TECH §2.7): Go native complex128 phasors. Bind is
// elementwise complex multiplication (angle addition); bundle is
// normalized superposition; similarity is mean cosine of angle
// differences; dampen scales magnitudes (decay-as-damping). No FFT —
// that was the reason for choosing FHRR over HRR.
package dynamics

import (
	"math"
	"math/cmplx"
	"math/rand"
)

// Phasor is one FHRR atom or bundle: unit-magnitude complex entries
// (bundles may carry magnitude < n after dampening — similarity
// normalizes, so magnitude never leaks into identity).
type Phasor []complex128

// NewRandom draws a unit-magnitude atom from rng (measurement harness
// seeds it; production v1.1 will hash entity strings — M2's path).
func NewRandom(dim int, rng *rand.Rand) Phasor {
	v := make(Phasor, dim)
	for i := range v {
		th := rng.Float64() * 2 * math.Pi
		v[i] = cmplx.Exp(complex(0, th))
	}
	return v
}

// NewFromAngles builds a phasor from explicit phases (deterministic
// construction for real-vocabulary hashing — M2).
func NewFromAngles(phases []float64) Phasor {
	v := make(Phasor, len(phases))
	for i, th := range phases {
		v[i] = cmplx.Exp(complex(0, th))
	}
	return v
}

// Bind is FHRR binding: elementwise multiplication (angle addition mod
// 2π). No allocation inside the hot path beyond the output.
func Bind(a, b Phasor) Phasor {
	out := make(Phasor, len(a))
	for i := range a {
		out[i] = a[i] * b[i]
	}
	return out
}

// Bundle superposes atoms with magnitude normalization: the sum scaled
// to unit-magnitude entries (phase preserved, magnitude reset). Empty
// input yields nil — never a zero vector masquerading as a bundle.
func Bundle(vs []Phasor) Phasor {
	if len(vs) == 0 {
		return nil
	}
	dim := len(vs[0])
	sum := make([]complex128, dim)
	for _, v := range vs {
		for i := 0; i < dim && i < len(v); i++ {
			sum[i] += v[i]
		}
	}
	out := make(Phasor, dim)
	for i, s := range sum {
		if m := cmplx.Abs(s); m > 1e-12 {
			out[i] = s / complex(m, 0)
		} else {
			out[i] = 1
		}
	}
	return out
}

// Similarity is mean cosine of per-dimension angle differences in
// [-1, 1]: 1 for identical, ~0 for unrelated (high-dim concentration).
// Magnitude-normalized — identity never leaks through scaling
// (Dampen alone cannot move a similarity). The per-dim Abs calls make
// this the slow path; unit-entry callers use SimilarityUnit.
func Similarity(a, b Phasor) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	acc := 0.0
	for i := 0; i < n; i++ {
		acc += real(a[i]*cmplx.Conj(b[i])) / (cmplx.Abs(a[i])*cmplx.Abs(b[i]) + 1e-12)
	}
	return acc / float64(n)
}

// SimilarityUnit is the fast path for unit-magnitude entries — exactly
// what NewRandom, NewFromAngles, and Bundle emit by construction (the
// gate pins this with TestUnit_ByConstruction). No Abs, no division:
// one multiply-conjugate per dimension. Cleanup's documented
// precondition; non-unit callers use Similarity.
func SimilarityUnit(a, b Phasor) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	acc := 0.0
	for i := 0; i < n; i++ {
		acc += real(a[i] * cmplx.Conj(b[i]))
	}
	return acc / float64(n)
}

// Dampen scales magnitudes by gamma (decay-as-damping, M3): identity
// (phases) preserved, retrievability reduced. Similarity normalizes
// magnitudes away, so dampening alone cannot move identity — M3 probes
// dampening plus noise, the honest legibility shape.
func Dampen(v Phasor, gamma float64) Phasor {
	out := make(Phasor, len(v))
	for i, e := range v {
		out[i] = e * complex(gamma, 0)
	}
	return out
}

// Noisy damps and perturbs each phase by uniform noise in [-spread,
// spread] (the M3 probe: damped bundles lose members monotonically as
// noise accumulates — legibility, not just scaling).
func Noisy(v Phasor, gamma, spread float64, rng *rand.Rand) Phasor {
	out := make(Phasor, len(v))
	for i, e := range v {
		th, m := cmplx.Phase(e), cmplx.Abs(e)
		th += (rng.Float64()*2 - 1) * spread
		out[i] = cmplx.Rect(m*gamma, th)
	}
	return out
}
