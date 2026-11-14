package query

// VALID-NOW read-path proof (DESIGN §1, P1 Phase A): recall serves
// only records whose window contains the daemon-stamped now, under
// [t-, t+), and never SCHEDULED — with now an explicit injected
// parameter, so every edge below is deterministic. The external
// (a)/(b) pair lives with the probe; the (c) boundary pair lives
// here, where the clock is injectable.

import (
	"testing"

	"engram/internal/rpc"
)

// qnow is a fixed wall-like unix-nano instant (deterministic — never
// time.Now, so these tests cannot flake on clock races).
const qnow = 1700000000000000000

func qinW(sub, pred, obj, src, ses string, from, to int64) rpc.InputRecord {
	in := qin(sub, pred, obj, src, ses, rpc.ACLUser)
	in.ValidTime = rpc.Interval{From: from, To: to}
	return in
}

func qspecAt(sub, pred string, caller rpc.AclTier, now int64) Spec {
	spec := qspec(sub, pred, caller)
	spec.Threshold = 0
	spec.Now = now
	return spec
}

// TestRecall_ValidityFilter pins the P1 read rule: expired and
// not-yet-true windows fall out, in-window records return — and every
// returned record satisfies the DESIGN §1 guarantee.
func TestRecall_ValidityFilter(t *testing.T) {
	e, s, idx := qsetup(t)
	emit := func(in rpc.InputRecord) {
		t.Helper()
		if _, err := e.Emit(in); err != nil {
			t.Fatalf("emit %s|%s|%s: %v", in.Subject, in.Predicate, in.Object, err)
		}
	}
	emit(qinW("was", "state", "expired", "s", "s1", qnow-7200e9, qnow-60e9))
	emit(qinW("will", "state", "future", "s", "s1", qnow+3600e9, qnow+7200e9))
	emit(qinW("is", "state", "live", "s", "s1", qnow-60e9, qnow+3600e9))

	res, err := Recall(idx, s, qspecAt("was", "state", rpc.ACLUser, qnow))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !res.Abstained {
		t.Fatalf("expired window must filter out, got %+v", res.Records)
	}
	res, err = Recall(idx, s, qspecAt("will", "state", rpc.ACLUser, qnow))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !res.Abstained {
		t.Fatalf("future window must filter out, got %+v", res.Records)
	}
	res, err = Recall(idx, s, qspecAt("is", "state", rpc.ACLUser, qnow))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 {
		t.Fatalf("in-window record must return, got %+v", res)
	}
	r := res.Records[0]
	if r.Status != rpc.StatusActive && r.Status != rpc.StatusBoth {
		t.Fatalf("guarantee: status in {ACTIVE,BOTH}, got %v", r.Status)
	}
	if r.ValidTime.From > qnow || qnow >= r.ValidTime.To {
		t.Fatalf("guarantee: from <= now < to, got %+v", r.ValidTime)
	}
}

// TestRecall_Boundary pins ruling (3) at both edges under one injected
// clock: valid_to == now excludes (< is exclusive), valid_from == now
// includes. External callers cannot pin equality against a
// server-stamped clock — that is why this pair lives here.
func TestRecall_Boundary(t *testing.T) {
	e, s, idx := qsetup(t)
	if _, err := e.Emit(qinW("edge", "ends", "at-now", "s", "s1", qnow-10e9, qnow)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := e.Emit(qinW("edge", "starts", "at-now", "s", "s1", qnow, qnow+10e9)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	res, err := Recall(idx, s, qspecAt("edge", "ends", rpc.ACLUser, qnow))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !res.Abstained {
		t.Fatalf("valid_to == now must exclude, got %+v", res.Records)
	}
	res, err = Recall(idx, s, qspecAt("edge", "starts", rpc.ACLUser, qnow))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 || res.Records[0].Object != "at-now" {
		t.Fatalf("valid_from == now must include, got %+v", res)
	}
}

// TestRecall_DisjointTime pins DESIGN §4 branch (c) under a now
// filter: disjoint same-SPO versions coexist in the store, and recall
// serves whichever version contains now — time-relative truth.
func TestRecall_DisjointTime(t *testing.T) {
	e, s, idx := qsetup(t)
	if _, err := e.Emit(qinW("season", "fruit", "plum", "s", "s1", 0, 10)); err != nil {
		t.Fatalf("emit v1: %v", err)
	}
	if _, err := e.Emit(qinW("season", "fruit", "fig", "s", "s2", 20, 30)); err != nil {
		t.Fatalf("emit v2: %v", err)
	}
	res, err := Recall(idx, s, qspecAt("season", "fruit", rpc.ACLUser, 5))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 || res.Records[0].Object != "plum" {
		t.Fatalf("now=5 must serve v1, got %+v", res)
	}
	res, err = Recall(idx, s, qspecAt("season", "fruit", rpc.ACLUser, 25))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 || res.Records[0].Object != "fig" {
		t.Fatalf("now=25 must serve v2, got %+v", res)
	}
}

// TestRecall_ScheduledExcluded pins ruling (4): a SCHEDULED record is
// hiding, not gone — excluded from recall even in-window, while an
// ACTIVE control in the same recall returns (proves the pool was live
// and the filter selective, not a vacuous empty).
func TestRecall_ScheduledExcluded(t *testing.T) {
	e, s, idx := qsetup(t)
	if _, err := e.Emit(qin("sched", "runs", "ok", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit control: %v", err)
	}
	target, err := e.Emit(qin("sched", "holds", "later", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit target: %v", err)
	}
	// Restatus mints a new version id (same as the BOTH-flag path) —
	// index the new version so the pool genuinely contains it.
	schedRec, err := s.Restatus(target.ID, rpc.StatusScheduled)
	if err != nil {
		t.Fatalf("restatus: %v", err)
	}
	staged, err := s.ListStaged()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	idx.Add(schedRec, staged)

	res, err := Recall(idx, s, qspecAt("sched", "holds", rpc.ACLUser, 5))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !res.Abstained {
		t.Fatalf("SCHEDULED must hide from recall, got %+v", res.Records)
	}
	res, err = Recall(idx, s, qspecAt("sched", "runs", rpc.ACLUser, 5))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 || res.Records[0].Object != "ok" {
		t.Fatalf("ACTIVE control must return, got %+v", res)
	}
}

// TestRecall_NowRejects pins fail-closed now: a negative instant is a
// caller bug, never a filter.
func TestRecall_NowRejects(t *testing.T) {
	_, s, idx := qsetup(t)
	bad := qspec("a", "b", rpc.ACLUser)
	bad.Now = -1
	if _, err := Recall(idx, s, bad); err == nil {
		t.Fatal("negative now must fail closed")
	}
}
