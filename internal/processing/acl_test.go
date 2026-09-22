package processing

import (
	"testing"

	"engram/internal/rpc"
)

func aclRecord(id string, tier rpc.AclTier) rpc.Record {
	return rpc.Record{ID: id, Subject: "s", Predicate: "p", Object: "o", ACL: tier}
}

// TestACL_FR13_PreFilter proves filtering happens by tier, at the exact
// boundary on both sides: caller sees at-or-below, never above.
func TestACL_FR13_PreFilter(t *testing.T) {
	all := []rpc.Record{
		aclRecord("low", rpc.ACLLow),
		aclRecord("user", rpc.ACLUser),
		aclRecord("sys", rpc.ACLSystem),
	}
	seen := func(rs []rpc.Record) map[string]bool {
		m := make(map[string]bool)
		for _, r := range rs {
			m[r.ID] = true
		}
		return m
	}
	low := seen(Visible(all, rpc.ACLLow))
	if len(low) != 1 || !low["low"] {
		t.Fatalf("low caller must see only low, saw %v", low)
	}
	user := seen(Visible(all, rpc.ACLUser))
	if len(user) != 2 || !user["low"] || !user["user"] {
		t.Fatalf("user caller must see low+user, saw %v", user)
	}
	sys := seen(Visible(all, rpc.ACLSystem))
	if len(sys) != 3 {
		t.Fatalf("system caller must see all, saw %v", sys)
	}
	// Unknown caller tier: fail closed, sees nothing.
	if got := Visible(all, rpc.AclTier("root")); len(got) != 0 {
		t.Fatalf("unknown tier must see nothing, saw %v", got)
	}
	// Unknown record tier ranks below everything: invisible to low.
	weird := []rpc.Record{aclRecord("x", rpc.AclTier("bogus"))}
	if got := Visible(weird, rpc.ACLLow); len(got) != 0 {
		t.Fatalf("bogus record tier must be invisible, saw %v", got)
	}
	// Input never mutated.
	if len(all) != 3 {
		t.Fatal("Visible mutated its input")
	}
}
