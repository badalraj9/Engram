// Payload-shape router (P5 — TECH §2.1, DESIGN §5).
//
// Dispatch key is payload shape, never intent classification:
// replacement content targeting a prior claim → correct; pure verdict
// on a prior claim → attest; new propositional content → emit.
// Ambiguous short verdict-like utterances bias toward attest over emit
// (asymmetric cost: a missed attestation costs one skipped confidence
// bump; a verdict wrongly emitted manufactures a garbage record).
//
// Pure function in the leaf rpc package: it intentionally mirrors
// intake.Dispatch's truth table WITHOUT importing intake (intake
// imports rpc — the reverse import would be a cycle). The table test
// pins both sides to the same expectations, so any divergence breaks
// loudly. Resolve is not routed here — it addresses a BOTH pair by
// IDs, never by payload shape.
package rpc

// RouteOp is the router's dispatch target (mirrors intake.Op).
type RouteOp uint8

// Dispatch targets.
const (
	RouteEmit RouteOp = iota
	RouteCorrect
	RouteAttest
)

// Route maps payload shape onto a dispatch target: prior claim +
// replacement content → correct; prior claim + pure verdict → attest;
// otherwise emit (new propositional content). Verdict-like shorts
// arrive as prior-claim without content → attest.
func Route(targetsPriorClaim, hasReplacementContent bool) RouteOp {
	switch {
	case targetsPriorClaim && hasReplacementContent:
		return RouteCorrect
	case targetsPriorClaim:
		return RouteAttest
	default:
		return RouteEmit
	}
}
