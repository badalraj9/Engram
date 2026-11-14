// Package processing implements Engram v2's write-layer decision logic
// (P2 — verdicts before index).
//
// Boundary (BUILD-PLAN P2, locked): pure Go over in-memory fixtures.
// This package imports engram/internal/rpc plus the standard library
// only — never the truth store, the index layer, or transport. Every
// decision here returns a verdict; execution against the store (Append,
// Supersede, Restatus, AppendFeedback) is P4's intake layer. No clocks,
// no goroutines, no I/O: the host owns time and durability.
package processing

import "engram/internal/rpc"

// Rank orders ACL tiers least to most privileged (P0 locked tiers,
// FR13 pre-filter). The hierarchy is P0-implied — low quarantined per
// FR14, trust weights ascending in config — and defined once here so
// the gate's tier rule (DESIGN §4.5) and recall's pre-filter share one
// ordering, not two. Unknown tiers rank below everything (fail closed:
// Rank is used with >=, so unknown never outranks a known tier).
func Rank(t rpc.AclTier) int {
	switch t {
	case rpc.ACLLow:
		return 0
	case rpc.ACLUser:
		return 1
	case rpc.ACLSystem:
		return 2
	default:
		return -1
	}
}

// Visible returns the records caller may use — the ACL pre-filter,
// applied BEFORE any working/active computation (FR13, locked
// resolution: helper owned here, enforced in query/recall.go at P4).
// Caller sees records at or below its own tier. Unknown caller tier
// sees nothing. The input slice is never mutated.
func Visible(records []rpc.Record, caller rpc.AclTier) []rpc.Record {
	if Rank(caller) < 0 {
		return nil
	}
	var out []rpc.Record
	for _, r := range records {
		rr := Rank(r.ACL)
		if rr < 0 {
			continue // unknown record tier: never enters computation
		}
		if rr <= Rank(caller) {
			out = append(out, r)
		}
	}
	return out
}
