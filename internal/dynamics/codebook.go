// Codebook: the cleanup memory (TECH §2.7).
//
// v1 shape: an in-memory map from atom hash → phasor. Cleanup scans one
// bucket — in this stub the whole book is the bucket (no LSH yet; the
// P3 MinHash machinery is the named future partitioner, TECH §2.7).
// Lifecycle: entirely in-process, per-query or per-session — never
// persisted (the litmus test: dynamics are recomputable, not stored).
package dynamics

import (
	"hash/fnv"
	"math"
	"math/rand"
)

// Codebook maps names to atoms. Zero value is ready.
type Codebook struct {
	atoms map[string]Phasor
	dim   int
}

// NewCodebook makes a book for dim-wide phasors.
func NewCodebook(dim int) *Codebook {
	return &Codebook{atoms: make(map[string]Phasor), dim: dim}
}

// Add stores a prebuilt atom (random or hashed).
func (c *Codebook) Add(name string, v Phasor) {
	if c.atoms == nil {
		c.atoms = make(map[string]Phasor)
	}
	c.atoms[name] = v
}

// AddRandom draws and stores an atom from rng.
func (c *Codebook) AddRandom(name string, rng *rand.Rand) {
	c.Add(name, NewRandom(c.dim, rng))
}

// AddString hashes a real-vocabulary string to a deterministic atom
// (M2's path: FNV-64 over the string seeds the phase stream, so the
// same entity always draws the same atom — no stored random table).
func (c *Codebook) AddString(name string) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))
	c.Add(name, NewRandom(c.dim, rng))
}

// Get returns the atom, second value false when absent.
func (c *Codebook) Get(name string) (Phasor, bool) {
	v, ok := c.atoms[name]
	return v, ok
}

// Names lists members in map order (callers sort when determinism
// matters — cleanup ties break by first-max, measured in M1).
func (c *Codebook) Names() []string {
	out := make([]string, 0, len(c.atoms))
	for n := range c.atoms {
		out = append(out, n)
	}
	return out
}

// Len reports membership.
func (c *Codebook) Len() int { return len(c.atoms) }

// Dim reports the phasor width.
func (c *Codebook) Dim() int { return c.dim }

// phasesHash is the M2 determinism witness: the same string always
// yields phases within float noise of itself (exact — same seed).
func phasesHash(s string) []float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))
	ph := make([]float64, 8)
	for i := range ph {
		ph[i] = rng.Float64() * 2 * math.Phi
	}
	return ph
}
