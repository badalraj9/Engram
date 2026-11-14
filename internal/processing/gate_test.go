package processing

import (
	"errors"
	"fmt"
	"testing"

	"engram/internal/rpc"
)

func grec(id, sub, pred, obj string, vf, vt int64, src, ses string, acl rpc.AclTier, status rpc.Status, txnFrom int64, payload map[string]string) rpc.Record {
	if payload == nil {
		payload = map[string]string{}
	}
	return rpc.Record{ID: id, Subject: sub, Predicate: pred, Object: obj,
		ValidTime:      rpc.Interval{From: vf, To: vt},
		Txn:            rpc.Interval{From: txnFrom, To: rpc.OpenEnd},
		Provenance:     rpc.Provenance{Source: src, Session: ses},
		ACL:            acl,
		Status:         status,
		Type:           rpc.RecordEpisodic,
		BaseConfidence: 0.8,
		Payload:        payload}
}

func gin(sub, pred, obj string, vf, vt int64, src, ses string, acl rpc.AclTier, payload map[string]string) rpc.InputRecord {
	if payload == nil {
		payload = map[string]string{}
	}
	return rpc.InputRecord{Subject: sub, Predicate: pred, Object: obj,
		ValidTime:      rpc.Interval{From: vf, To: vt},
		Provenance:     rpc.Provenance{Source: src, Session: ses},
		ACL:            acl,
		BaseConfidence: 0.8,
		Payload:        payload}
}

func mustDecide(t *testing.T, in rpc.InputRecord, cands []rpc.Record, staged map[string]bool) Outcome {
	t.Helper()
	out, err := Decide(in, cands, staged)
	if err != nil {
		t.Fatalf("Decide unexpectedly failed: %v", err)
	}
	return out
}

// TestGate_FR4_InsertNoCandidate: no live candidate → INSERT/ACTIVE,
// no target.
func TestGate_FR4_InsertNoCandidate(t *testing.T) {
	out := mustDecide(t, gin(" pump", "runs", "yes", 1, 2, "a", "s1", rpc.ACLUser, nil), nil, nil)
	if out.Verdict != rpc.VerdictInsert || out.Status != rpc.StatusActive || out.TargetID != "" {
		t.Fatalf("want clean INSERT, got %+v", out)
	}
}

// TestGate_FR4_MergePayloadUnion: same object + missing keys → MERGE
// with the union; conflicting keys keep the existing value (additive
// safety, not a truth judgment).
func TestGate_FR4_MergePayloadUnion(t *testing.T) {
	cands := []rpc.Record{grec("m", "pump", "runs", "yes", 1, 2, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0,
		map[string]string{"a": "1", "b": "old"})}
	out := mustDecide(t, gin("pump", "runs", "yes", 1, 2, "b", "s2", rpc.ACLUser,
		map[string]string{"b": "new", "c": "3"}), cands, nil)
	if out.Verdict != rpc.VerdictMerge || out.TargetID != "m" {
		t.Fatalf("want MERGE vs m, got %+v", out)
	}
	if out.MergedPayload["b"] != "old" || out.MergedPayload["c"] != "3" || out.MergedPayload["a"] != "1" {
		t.Fatalf("existing must win, missing added: %v", out.MergedPayload)
	}
	if out.ValidWidened {
		t.Fatal("same interval must not widen")
	}
}

// TestGate_FR4_UpdateDiffObject: different object, overlapping time →
// UPDATE against the latest conflicting claim.
func TestGate_FR4_UpdateDiffObject(t *testing.T) {
	cands := []rpc.Record{
		grec("old", "pump", "runs", "no", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil),
		grec("older", "pump", "runs", "maybe", 1, 5, "a", "s0", rpc.ACLUser, rpc.StatusActive, 1, nil),
	}
	out := mustDecide(t, gin("pump", "runs", "yes", 2, 6, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictUpdate || out.TargetID != "older" {
		t.Fatalf("want UPDATE vs latest conflict (older), got %+v", out)
	}
}

// TestGate_FR4_RestrengthenCrossSession: identical SPO, overlapping
// time, no delta, different session → genuine corroboration.
func TestGate_FR4_RestrengthenCrossSession(t *testing.T) {
	cands := []rpc.Record{grec("r", "pump", "runs", "yes", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "yes", 1, 5, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictRestrengthen || out.TargetID != "r" {
		t.Fatalf("want RESTRENGTHEN vs r, got %+v", out)
	}
}

// TestGate_FR4_StagedLow: low-tier writes quarantine before matching —
// even with zero candidates, and a staged candidate stays invisible.
func TestGate_FR4_StagedLow(t *testing.T) {
	out := mustDecide(t, gin("pump", "runs", "yes", 1, 2, "x", "s9", rpc.ACLLow, nil), nil, nil)
	if out.Verdict != rpc.VerdictStaged {
		t.Fatalf("want STAGED, got %+v", out)
	}
	cands := []rpc.Record{grec("st", "pump", "runs", "yes", 1, 2, "x", "s9", rpc.ACLLow, rpc.StatusActive, 0, nil)}
	out = mustDecide(t, gin("pump", "runs", "yes", 1, 2, "a", "s1", rpc.ACLUser, nil), cands, map[string]bool{"st": true})
	if out.Verdict != rpc.VerdictInsert {
		t.Fatalf("staged-only candidate must read as no candidate, got %+v", out)
	}
}

// TestGate_SessionSplitSkip: same (Source, Session), same SPO,
// overlapping, no delta → true no-op (retry-safe: no bump possible).
func TestGate_SessionSplitSkip(t *testing.T) {
	cands := []rpc.Record{grec("r", "pump", "runs", "yes", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "yes", 2, 4, "a", "s1", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictSkip || out.TargetID != "r" {
		t.Fatalf("want SKIP vs r, got %+v", out)
	}
}

// TestGate_DisjointTimeAppend: identical SPO, disjoint valid-time →
// new temporal fact, both records live (INSERT, no target).
func TestGate_DisjointTimeAppend(t *testing.T) {
	cands := []rpc.Record{grec("mon", "pump", "runs", "yes", 1, 2, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "yes", 5, 6, "a", "s1", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert || out.TargetID != "" {
		t.Fatalf("want disjoint INSERT, got %+v", out)
	}
	// Adjacent half-open intervals are disjoint, not overlapping.
	out = mustDecide(t, gin("pump", "runs", "yes", 2, 5, "a", "s1", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert {
		t.Fatalf("adjacent intervals must be disjoint INSERT, got %+v", out)
	}
}

// TestGate_WidenMerge: [Mon,Tue) → [Mon,Wed) carries new content (the
// Wednesday stretch) → MERGE with the union interval, authored by the
// incoming event.
func TestGate_WidenMerge(t *testing.T) {
	cands := []rpc.Record{grec("w", "pump", "runs", "yes", 10, 20, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "yes", 10, 30, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictMerge || !out.ValidWidened {
		t.Fatalf("want widening MERGE, got %+v", out)
	}
	if out.MergedValid != (rpc.Interval{From: 10, To: 30}) {
		t.Fatalf("want union [10,30), got %v", out.MergedValid)
	}
}

// TestGate_NarrowingAck: incoming ⊆ existing adds no content → the
// delta test rejects shrink by construction (retraction is correct()'s
// job, never the gate's).
func TestGate_NarrowingAck(t *testing.T) {
	cands := []rpc.Record{grec("w", "pump", "runs", "yes", 10, 30, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "yes", 12, 15, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictRestrengthen {
		t.Fatalf("narrowing must ack, got %+v", out)
	}
}

// TestGate_TierAuthority: content-writing needs incoming ≥ candidate.
// Below → BOTH (flagged for resolve); above → normal UPDATE.
func TestGate_TierAuthority(t *testing.T) {
	cands := []rpc.Record{grec("sys", "pump", "runs", "yes", 1, 5, "ops", "s0", rpc.ACLSystem, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "no", 2, 6, "u", "s7", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert || out.Status != rpc.StatusBoth || out.TargetID != "sys" || !out.FlagTargetBoth {
		t.Fatalf("subordinate conflict must flag BOTH, got %+v", out)
	}
	out = mustDecide(t, gin("pump", "runs", "no", 2, 6, "ops", "s8", rpc.ACLSystem, nil), cands, nil)
	if out.Verdict != rpc.VerdictUpdate || out.TargetID != "sys" {
		t.Fatalf("authority-sufficient conflict must UPDATE, got %+v", out)
	}
	// MERGE is content-writing too: subordinate payload-add → BOTH.
	out = mustDecide(t, gin("pump", "runs", "yes", 1, 5, "u", "s7", rpc.ACLUser, map[string]string{"x": "1"}), cands, nil)
	if out.Status != rpc.StatusBoth || !out.FlagTargetBoth {
		t.Fatalf("subordinate merge must flag BOTH, got %+v", out)
	}
}

// TestGate_ConflictFirst: one event corroborating A while contradicting
// B must route by max severity — B's contradiction is processed, never
// waved through by the ack branch.
func TestGate_ConflictFirst(t *testing.T) {
	cands := []rpc.Record{
		grec("a", "pump", "runs", "yes", 1, 3, "x", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil),
		grec("b", "pump", "runs", "no", 2, 6, "y", "s2", rpc.ACLUser, rpc.StatusActive, 1, nil),
	}
	out := mustDecide(t, gin("pump", "runs", "yes", 1, 4, "z", "s3", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictUpdate || out.TargetID != "b" {
		t.Fatalf("conflict must dominate corroboration, got %+v", out)
	}
}

// TestGate_FuzzyBand: paraphrase same-object merges; paraphrase with a
// different object proves nothing — INSERT, never UPDATE on fuzzy.
func TestGate_FuzzyBand(t *testing.T) {
	cands := []rpc.Record{grec("f", "Acme Corp", "employs", "yes", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, nil)}
	out := mustDecide(t, gin("Acme", "employs", "yes", 1, 5, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictMerge || out.TargetID != "f" {
		t.Fatalf("paraphrase same-object must MERGE, got %+v", out)
	}
	out = mustDecide(t, gin("Acme", "employs", "no", 1, 5, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert {
		t.Fatalf("fuzzy different-object must INSERT, got %+v", out)
	}
	// Exact dominates fuzzy: disjoint exact append wins over fuzzy merge.
	cands = append(cands, grec("e", "Acme", "employs", "yes", 10, 12, "c", "s3", rpc.ACLUser, rpc.StatusActive, 1, nil))
	out = mustDecide(t, gin("Acme", "employs", "yes", 20, 22, "d", "s4", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert || out.TargetID != "" {
		t.Fatalf("exact disjoint must dominate fuzzy, got %+v", out)
	}
}

// TestGate_FlaggedPairs: BOTH candidates join conflicts without
// stacking flags, and never serve as revision targets.
func TestGate_FlaggedPairs(t *testing.T) {
	cands := []rpc.Record{grec("fl", "pump", "runs", "yes", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusBoth, 0, nil)}
	out := mustDecide(t, gin("pump", "runs", "no", 2, 6, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictInsert || out.Status != rpc.StatusBoth || out.FlagTargetBoth {
		t.Fatalf("conflict with flagged pair must join without stacking, got %+v", out)
	}
	out = mustDecide(t, gin("pump", "runs", "yes", 1, 5, "b", "s2", rpc.ACLUser, nil), cands, nil)
	if out.Verdict != rpc.VerdictRestrengthen || out.TargetID != "fl" {
		t.Fatalf("same-SPO vs flagged pair must ack, got %+v", out)
	}
	// Same-SPO + delta vs a BOTH record must not MERGE (revision would
	// tombstone the flag before resolve): falls to INSERT.
	out = mustDecide(t, gin("pump", "runs", "yes", 1, 5, "b", "s2", rpc.ACLUser, map[string]string{"n": "1"}), cands, nil)
	if out.Verdict == rpc.VerdictMerge || out.Verdict == rpc.VerdictUpdate {
		t.Fatalf("flagged pair must never be a revision target, got %+v", out)
	}
}

// TestGate_UnionCapBreach: an unwritable union is not a delta — the
// gate acks against the live claim instead of emitting a verdict P1
// would reject at write time.
func TestGate_UnionCapBreach(t *testing.T) {
	full := make(map[string]string, rpc.MaxPayloadKeys)
	for i := 0; i < rpc.MaxPayloadKeys; i++ {
		full[fmt.Sprintf("k%d", i)] = "v"
	}
	cands := []rpc.Record{grec("c", "pump", "runs", "yes", 1, 5, "a", "s1", rpc.ACLUser, rpc.StatusActive, 0, full)}
	out := mustDecide(t, gin("pump", "runs", "yes", 1, 5, "a", "s1", rpc.ACLUser, map[string]string{"new": "1"}), cands, nil)
	if out.Verdict != rpc.VerdictSkip {
		t.Fatalf("cap-breaching union must ack, got %+v", out)
	}
}

// TestGate_InvalidInput: validation fails closed with a coded error
// before any matching runs.
func TestGate_InvalidInput(t *testing.T) {
	_, err := Decide(gin("", "runs", "yes", 1, 2, "a", "s1", rpc.ACLUser, nil), nil, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeInvalidRecord {
		t.Fatalf("want coded INVALID_RECORD, got %v", err)
	}
}

// BenchmarkDecide_Mixed200 bounds the gate's linear pass for the
// performance report.
func BenchmarkDecide_Mixed200(b *testing.B) {
	var cands []rpc.Record
	for i := 0; i < 200; i++ {
		cands = append(cands, grec(fmt.Sprintf("r%d", i), fmt.Sprintf("subject-%d", i), "pred", "obj", 1, 100, "s", "s1", rpc.ACLUser, rpc.StatusActive, int64(i), nil))
	}
	in := gin("subject-199", "pred", "obj", 1, 100, "s", "s2", rpc.ACLUser, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Decide(in, cands, nil); err != nil {
			b.Fatal(err)
		}
	}
}
