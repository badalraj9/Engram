package intake

import (
	"testing"

	"engram/internal/rpc"
)

// TestEmit_MergeUpdate pins content-writing verdicts end to end:
// payload-add MERGEs (monotonic union, indexed), fresh conflicts
// UPDATE (old tombstoned, new live).
func TestEmit_MergeUpdate(t *testing.T) {
	e, s, idx := newEmitter(t, 1000)
	base, err := e.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	withAttr := ein("pump", "runs", "yes", "s", "s2", rpc.ACLUser)
	withAttr.Payload = map[string]string{"model": "x1"}
	merged, err := e.Emit(withAttr)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Verdict != rpc.VerdictMerge {
		t.Fatalf("payload-add must MERGE, got %+v", merged)
	}
	got, err := s.Get(merged.ID)
	if err != nil || got.Payload["model"] != "x1" {
		t.Fatalf("merged version must carry the union, got %+v err=%v", got, err)
	}
	if indexed := idx.Exact("pump", "runs"); len(indexed) == 0 {
		t.Fatal("merged version must index")
	}
	old, err := s.Get(base.ID)
	if err != nil || old.Status != rpc.StatusTombstoned {
		t.Fatalf("superseded base must tombstone, got %+v err=%v", old, err)
	}
	// Fresh conflict at equal tier: UPDATE, never a flagged pair.
	updated, err := e.Emit(ein("pump", "runs", "no", "s", "s3", rpc.ACLUser))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Verdict != rpc.VerdictUpdate {
		t.Fatalf("fresh conflict must UPDATE, got %+v", updated)
	}
	closed, err := s.Get(merged.ID)
	if err != nil || closed.Status != rpc.StatusTombstoned {
		t.Fatalf("updated-over version must tombstone, got %+v err=%v", closed, err)
	}
}

// TestEmit_UpdatePromotesStaged pins FR14's content leg: an UPDATE
// that corroborates a staged same-key version promotes it —
// corroboration-with-content counts (DESIGN §4.8), not just bare
// RESTRENGTHEN.
func TestEmit_UpdatePromotesStaged(t *testing.T) {
	e, s, idx := newEmitter(t, 1000)
	if _, err := e.Emit(ein("pump", "runs", "no", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("live: %v", err)
	}
	staged, err := e.Emit(ein("pump", "runs", "yes", "low-x", "s9", rpc.ACLLow))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	updated, err := e.Emit(ein("pump", "runs", "yes", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Verdict != rpc.VerdictUpdate {
		t.Fatalf("must UPDATE the live head, got %+v", updated)
	}
	if ok, _ := s.IsStaged(staged.ID); ok {
		t.Fatal("corroborating UPDATE must promote the staged version")
	}
	found := false
	for _, r := range idx.Exact("pump", "runs") {
		if r.ID == staged.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("promoted version must index")
	}
}

// TestEmit_TierBlockedBoth pins the no-eligible-claimant path: a
// subordinate conflicting write stores BOTH and flags the target —
// authority overrides recency, nothing is overwritten.
func TestEmit_TierBlockedBoth(t *testing.T) {
	e, s, _ := newEmitter(t, 1000)
	sys := ein("pump", "runs", "yes", "ops", "s0", rpc.ACLSystem)
	if _, err := e.Emit(sys); err != nil {
		t.Fatalf("sys: %v", err)
	}
	sub, err := e.Emit(ein("pump", "runs", "no", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if sub.Verdict != rpc.VerdictInsert || sub.ID == "" {
		t.Fatalf("tier-blocked conflict must store as INSERT, got %+v", sub)
	}
	incoming, err := s.Get(sub.ID)
	if err != nil || incoming.Status != rpc.StatusBoth {
		t.Fatalf("incoming must be BOTH, got %+v err=%v", incoming, err)
	}
	if sub.FlaggedID == "" {
		t.Fatal("tier-blocked path must return the flagged target version")
	}
	target, err := s.Get(sub.FlaggedID)
	if err != nil || target.Status != rpc.StatusBoth {
		t.Fatalf("target must flag BOTH, got %+v err=%v", target, err)
	}
	if incoming.Object != "no" || target.Object != "yes" {
		t.Fatal("flagging must not rewrite either claim's content")
	}
}

// TestCorrect_OutsideWindow pins labile expiry: a correction
// landing LabileWindow+ sequences after the target's birth appends
// fresh + tombstones (no lineage carry) instead of superseding.
func TestCorrect_OutsideWindow(t *testing.T) {
	e, s, _ := newEmitter(t, 1000)
	first, err := e.Emit(ein("db", "runs", "Badger", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	// Age the log past the labile window with direct appends (no
	// index churn needed — the target is already indexed).
	for i := 0; i < LabileWindow+1; i++ {
		if _, err := s.Append(ein("fill", "p", "o", "s", "s1", rpc.ACLUser)); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	rec, v, err := e.Correct(CorrectHint{Subject: "db", Predicate: "runs", Object: "Badger"},
		ein("db", "runs", "Pebble", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if v != rpc.VerdictInsert {
		t.Fatalf("out-of-window correction must append fresh, got %v", v)
	}
	old, err := s.Get(first.ID)
	if err != nil || old.Status != rpc.StatusTombstoned || old.Object != "Badger" {
		t.Fatalf("old claim must tombstone intact, got %+v err=%v", old, err)
	}
	if rec.Object != "Pebble" {
		t.Fatalf("rewrite must carry new content, got %+v", rec)
	}
}

// TestResolve_Exec pins resolve execution: keep-a restatus both
// sides; merge authors the union over the loser and tombstones the
// survivor.
func TestResolve_Exec(t *testing.T) {
	e, s, _ := newEmitter(t, 1000)
	sys := ein("pump", "runs", "yes", "ops", "s0", rpc.ACLSystem)
	sysRes, err := e.Emit(sys)
	if err != nil {
		t.Fatalf("sys: %v", err)
	}
	sub, err := e.Emit(ein("pump", "runs", "no", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	prov := rpc.Provenance{Source: "ops", Session: "s9"}
	newID, err := e.Resolve(sysRes.ID, sub.ID, rpc.ResolveKeepA, prov)
	if err != nil {
		t.Fatalf("keep-a: %v", err)
	}
	survivor, err := s.Get(newID)
	if err != nil || survivor.Status != rpc.StatusActive || survivor.Object != "yes" {
		t.Fatalf("keep-a must survive ACTIVE, got %+v err=%v", survivor, err)
	}
	// Fresh pair for the merge leg.
	sys2, err := e.Emit(ein("valve", "holds", "a", "ops", "s0", rpc.ACLSystem))
	if err != nil {
		t.Fatalf("sys2: %v", err)
	}
	sub2, err := e.Emit(ein("valve", "holds", "b", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("sub2: %v", err)
	}
	mergedID, err := e.Resolve(sys2.ID, sub2.ID, rpc.ResolveMerge, prov)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	merged, err := s.Get(mergedID)
	if err != nil || merged.Status != rpc.StatusActive {
		t.Fatalf("merged must be live, got %+v err=%v", merged, err)
	}
	if _, err := e.Resolve("missing", sub2.ID, rpc.ResolveKeepA, prov); err == nil {
		t.Fatal("unknown IDs must fail closed")
	}
	if _, err := e.Resolve(sys2.ID, sub2.ID, rpc.ResolveVerdict("split"), prov); err == nil {
		t.Fatal("unknown verdicts must fail closed")
	}
}
