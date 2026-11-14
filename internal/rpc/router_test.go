package rpc_test

import (
	"testing"

	"engram/internal/intake"
	"engram/internal/rpc"
)

// TestRouter_MirrorsDispatch pins the router's truth table against
// intake.Dispatch case-by-case: same inputs must yield the same
// target on both sides. Either table drifting breaks loudly here,
// never silently in production.
func TestRouter_MirrorsDispatch(t *testing.T) {
	cases := []struct {
		prior, content bool
		rpcWant        rpc.RouteOp
		intakeWant     intake.Op
	}{
		{true, true, rpc.RouteCorrect, intake.OpCorrect},
		{true, false, rpc.RouteAttest, intake.OpAttest},
		{false, true, rpc.RouteEmit, intake.OpEmit},
		{false, false, rpc.RouteEmit, intake.OpEmit},
	}
	for _, c := range cases {
		if got := rpc.Route(c.prior, c.content); got != c.rpcWant {
			t.Fatalf("Route(%v,%v) = %v, want %v", c.prior, c.content, got, c.rpcWant)
		}
		if got := intake.Dispatch(c.prior, c.content); got != c.intakeWant {
			t.Fatalf("Dispatch(%v,%v) = %v, want %v", c.prior, c.content, got, c.intakeWant)
		}
	}
}
