package processing

import (
	"math"
	"testing"

	"engram/internal/rpc"
)

// TestAbstain_FR7_Gate pins the threshold boundary both sides.
func TestAbstain_FR7_Gate(t *testing.T) {
	in := []Scored{
		{Record: grec("a", "s", "p", "o", 1, 2, "s", "s1", rpc.ACLUser, rpc.StatusActive, 1, nil), Score: 0.8},
		{Record: grec("b", "s", "p", "o", 1, 2, "s", "s1", rpc.ACLUser, rpc.StatusActive, 2, nil), Score: 0.3},
	}
	kept, ab := ApplyThreshold(in, 0.5)
	if ab || len(kept) != 1 || kept[0].Record.ID != "a" {
		t.Fatalf("want [a] returned, got %v abstained=%v", kept, ab)
	}
	// Exact-boundary clears: "clears the threshold" is >=.
	kept, ab = ApplyThreshold(in, 0.3)
	if ab || len(kept) != 2 {
		t.Fatalf("boundary score must clear, got %v abstained=%v", kept, ab)
	}
	if _, ab := ApplyThreshold(in, 0.9); !ab {
		t.Fatal("all-below must abstain, never best-effort noise")
	}
}

// TestAbstain_FR7_FailClosed pins empty and NaN inputs.
func TestAbstain_FR7_FailClosed(t *testing.T) {
	if _, ab := ApplyThreshold(nil, 0.5); !ab {
		t.Fatal("empty input must abstain")
	}
	in := []Scored{{Score: 0.99}}
	if _, ab := ApplyThreshold(in, math.NaN()); !ab {
		t.Fatal("NaN threshold must abstain everything")
	}
}
