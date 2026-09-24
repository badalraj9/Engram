package index

import "sort"

// Set is the v1 postings set (TECH §2.6 slot: postings-list set ops
// for chained FR8 lookups and visited-tracking). Stdlib-only sorted
// discipline over record IDs — per-key candidate sets are a handful
// of versions, so a bitmap dependency is unjustified weight; the
// method shapes (Add/Intersect/Union/Clone/Sorted) are the seam a
// future bitmap backend would implement. Zero value is ready.
type Set map[string]struct{}

// Add inserts id. The set must be non-nil (make(Set) or Clone) —
// maps cannot self-allocate through a value receiver, so Add on nil
// panics by Go semantics; invert.go allocates every posting it
// touches.
func (s Set) Add(id string) {
	s[id] = struct{}{}
}

// Contains reports membership. Nil-safe.
func (s Set) Contains(id string) bool {
	_, ok := s[id]
	return ok
}

// Clone copies the set. Nil-safe (nil in, nil out).
func (s Set) Clone() Set {
	if s == nil {
		return nil
	}
	out := make(Set, len(s))
	for id := range s {
		out[id] = struct{}{}
	}
	return out
}

// Intersect keeps only members of other, in place. Nil-safe: empty
// intersection, never a panic.
func (s Set) Intersect(other Set) {
	for id := range s {
		if _, ok := other[id]; !ok {
			delete(s, id)
		}
	}
}

// Union adds every member of other, in place. Nil other is a no-op.
// Receiver must be non-nil (same note as Add).
func (s Set) Union(other Set) {
	for id := range other {
		s[id] = struct{}{}
	}
}

// Sorted returns members in sorted order (deterministic output for
// chained lookups and tests). Nil-safe.
func (s Set) Sorted() []string {
	out := make([]string, 0, len(s))
	for id := range s {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
