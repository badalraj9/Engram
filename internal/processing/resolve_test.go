package processing

import (
	"testing"

	"engram/internal/rpc"
)

func rrec(id, obj string, txnFrom int64) rpc.Record {
	return grec(id, "pump", "runs", obj, 1, 5, "s", "s1", rpc.ACLUser, rpc.StatusBoth, txnFrom, map[string]string{"k": "v"})
}

func rprov() rpc.Provenance { return rpc.Provenance{Source: "op", Session: "s9"} }

// TestResolve_FR12_Keep pins survivor/loser transitions.
func TestResolve_FR12_Keep(t *testing.T) {
	a, b := rrec("a", "yes", 3), rrec("b", "no", 7)
	plan, err := PlanResolve(a, b, rpc.ResolveKeepA, rprov())
	if err != nil {
		t.Fatalf("keep-a: %v", err)
	}
	if plan.Merged != nil {
		t.Fatal("keep must not author content")
	}
	if len(plan.Transitions) != 2 || plan.Transitions[0] != (StatusTransition{"a", rpc.StatusActive}) ||
		plan.Transitions[1] != (StatusTransition{"b", rpc.StatusTombstoned}) {
		t.Fatalf("keep-a transitions wrong: %+v", plan.Transitions)
	}
	plan, err = PlanResolve(a, b, rpc.ResolveKeepB, rprov())
	if err != nil || plan.Transitions[0].ID != "b" || plan.Transitions[0].To != rpc.StatusActive {
		t.Fatalf("keep-b must survive b, got %+v err=%v", plan.Transitions, err)
	}
}

// TestResolve_FR12_Merge pins latest-wins content + monotonic union.
func TestResolve_FR12_Merge(t *testing.T) {
	a := grec("a", "pump", "runs", "yes", 1, 4, "s", "s1", rpc.ACLUser, rpc.StatusBoth, 3, map[string]string{"old": "1"})
	b := grec("b", "pump", "runs", "no", 2, 6, "s", "s2", rpc.ACLUser, rpc.StatusBoth, 7, map[string]string{"new": "2"})
	plan, err := PlanResolve(a, b, rpc.ResolveMerge, rprov())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if plan.Merged == nil {
		t.Fatal("merge must author content")
	}
	m := plan.Merged
	if m.Object != "no" || m.Provenance != rprov() {
		t.Fatalf("merge keeps latest claim + resolver authorship, got %+v", m)
	}
	if m.Payload["old"] != "1" || m.Payload["new"] != "2" {
		t.Fatalf("merge must union payload, got %v", m.Payload)
	}
	if m.ValidTime.From != 1 || m.ValidTime.To != 6 {
		t.Fatalf("merge must widen valid-time, got %v", m.ValidTime)
	}
	if got := sortedIDs(plan.Transitions); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("both originals close, got %v", got)
	}
}

// TestResolve_FR12_Reject pins fail-closed verdicts and authorship.
func TestResolve_FR12_Reject(t *testing.T) {
	a, b := rrec("a", "yes", 3), rrec("b", "no", 7)
	if _, err := PlanResolve(a, b, rpc.ResolveVerdict("split"), rprov()); err == nil {
		t.Fatal("unknown verdict must fail closed")
	}
	if _, err := PlanResolve(a, b, rpc.ResolveKeepA, rpc.Provenance{}); err == nil {
		t.Fatal("blank resolver provenance must fail closed")
	}
}
