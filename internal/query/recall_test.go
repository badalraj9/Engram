package query

import (
	"testing"

	"engram/internal/index"
	"engram/internal/intake"
	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

func qsetup(t *testing.T) (*intake.Emitter, *truth.Store, *index.Index) {
	t.Helper()
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	idx := &index.Index{}
	return intake.NewEmitter(s, idx, 1000), s, idx
}

func qin(sub, pred, obj, src, ses string, acl rpc.AclTier) rpc.InputRecord {
	// Always-true window (DESIGN §1 VALID-NOW open end): ordinary
	// fixtures never exercise the validity filter — validity tests
	// pass explicit windows + explicit now instead. qspec leaves Now
	// at 0, which the always-true window contains.
	return rpc.InputRecord{Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 0, To: 1 << 62},
		Provenance: rpc.Provenance{Source: src, Session: ses},
		ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}}
}

func qspec(sub, pred string, caller rpc.AclTier) Spec {
	return Spec{Subject: sub, Predicate: pred, Caller: caller,
		Budget: 10, Threshold: 0.001, Beta: processing.RankBeta,
		TrustLow: 0.25, TrustUser: 1.0, TrustSys: 1.5}
}

// TestRecall_ReturnsRanked pins the read path end to end: indexed
// lookup finds live records, scores order desc, confidences align.
func TestRecall_ReturnsRanked(t *testing.T) {
	e, s, idx := qsetup(t)
	if _, err := e.Emit(qin("pump", "runs", "yes", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := e.Emit(qin("valve", "holds", "pressure", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	res, err := Recall(idx, s, qspec("pump", "runs", rpc.ACLUser))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 || res.Records[0].Subject != "pump" {
		t.Fatalf("want the pump record, got %+v", res)
	}
	if len(res.Scores) != 1 || len(res.Confidences) != 1 {
		t.Fatal("scores and confidences must align with records")
	}
	if res.Scores[0] <= 0 || res.Confidences[0] <= 0 {
		t.Fatalf("positive score and confidence expected, got %v %v", res.Scores, res.Confidences)
	}
}

// TestRecall_Abstains pins FR7: nothing clearing the threshold
// returns ABSTAIN, never best-effort noise.
func TestRecall_Abstains(t *testing.T) {
	e, s, idx := qsetup(t)
	if _, err := e.Emit(qin("pump", "runs", "yes", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	spec := qspec("pump", "runs", rpc.ACLUser)
	spec.Threshold = 5.0 // above the ~2.0 two-hit user score
	res, err := Recall(idx, s, spec)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !res.Abstained || len(res.Records) != 0 {
		t.Fatalf("must ABSTAIN, got %+v", res)
	}
	// Unknown query (no candidates at all) abstains too.
	res, err = Recall(idx, s, qspec("nobody", "does", rpc.ACLUser))
	if err != nil || !res.Abstained {
		t.Fatalf("empty pool must ABSTAIN, got %+v err=%v", res, err)
	}
}

// TestRecall_ACLPreFilter pins FR13 on the read path: a user caller
// never sees system records, and the ranking never scores them.
func TestRecall_ACLPreFilter(t *testing.T) {
	e, s, idx := qsetup(t)
	sys := qin("pump", "runs", "yes", "ops", "s0", rpc.ACLUser)
	sys.ACL = rpc.ACLSystem
	if _, err := e.Emit(sys); err != nil {
		t.Fatalf("emit sys: %v", err)
	}
	if _, err := e.Emit(qin("pump", "runs", "yes", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit user: %v", err)
	}
	res, err := Recall(idx, s, qspec("pump", "runs", rpc.ACLUser))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, r := range res.Records {
		if r.ACL == rpc.ACLSystem {
			t.Fatal("system record leaked to user caller")
		}
	}
	sysRes, err := Recall(idx, s, qspec("pump", "runs", rpc.ACLSystem))
	if err != nil {
		t.Fatalf("sys recall: %v", err)
	}
	// Per-subject dedup collapses the pair; the system version wins
	// on trust — ACL visibility AND dedup in one assertion.
	if sysRes.Abstained || len(sysRes.Records) != 1 || sysRes.Records[0].ACL != rpc.ACLSystem {
		t.Fatalf("system caller must see the winning system version, got %+v", sysRes)
	}
}

// TestRecall_StagedExcluded pins quarantine on the read path: staged
// records never return, even at a zero threshold.
func TestRecall_StagedExcluded(t *testing.T) {
	e, s, idx := qsetup(t)
	low := qin("pump", "runs", "yes", "low-src", "s9", rpc.ACLUser)
	low.ACL = rpc.ACLLow
	if _, err := e.Emit(low); err != nil {
		t.Fatalf("stage: %v", err)
	}
	spec := qspec("pump", "runs", rpc.ACLUser)
	spec.Threshold = 0
	res, err := Recall(idx, s, spec)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !res.Abstained {
		t.Fatalf("staged-only pool must ABSTAIN, got %+v", res)
	}
}

// TestRecall_DedupBudget pins per-subject top-k and budget caps.
func TestRecall_DedupBudget(t *testing.T) {
	e, s, idx := qsetup(t)
	if _, err := e.Emit(qin("pump", "runs", "yes", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit1: %v", err)
	}
	// Second live version about the same subject via a disjoint key
	// (predicate "runs dry" ≠ "runs" → no gate interaction, both
	// INSERT): both legitimately coexist and both carry the query
	// tokens, so per-subject dedup collapses them.
	// (Pre-filter this was a disjoint-time pair; under VALID-NOW two
	// disjoint windows can never both contain now, so coexistence-at-
	// recall is pinned by disjoint keys and time-relativity by
	// TestRecall_DisjointTime.)
	v2 := qin("pump", "runs dry", "no", "s", "s2", rpc.ACLUser)
	if _, err := e.Emit(v2); err != nil {
		t.Fatalf("emit2: %v", err)
	}
	spec := qspec("pump", "runs", rpc.ACLUser)
	spec.Budget = 1
	res, err := Recall(idx, s, spec)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained || len(res.Records) != 1 {
		t.Fatalf("dedup+budget must yield exactly one, got %+v", res)
	}
}

// TestRecall_Reject pins fail-closed specs.
func TestRecall_Reject(t *testing.T) {
	_, s, idx := qsetup(t)
	if _, err := Recall(idx, s, qspec("a", "b", rpc.AclTier("bogus"))); err == nil {
		t.Fatal("unknown caller must fail closed")
	}
	bad := qspec("a", "b", rpc.ACLUser)
	bad.Budget = 0
	if _, err := Recall(idx, s, bad); err == nil {
		t.Fatal("zero budget must fail closed")
	}
	bad = qspec("a", "b", rpc.ACLUser)
	bad.Threshold = -1
	if _, err := Recall(idx, s, bad); err == nil {
		t.Fatal("negative threshold must fail closed")
	}
}

// TestRecall_DeadStore pins total-store-failure: recall errors,
// never an unfiltered set, never ABSTAIN-as-answer.
func TestRecall_DeadStore(t *testing.T) {
	_, s, idx := qsetup(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Recall(idx, s, qspec("a", "b", rpc.ACLUser)); err == nil {
		t.Fatal("recall on a dead store must error")
	}
	if _, err := AsOf(s, "a", "b", 1, rpc.AxisValid, rpc.ACLUser); err == nil {
		t.Fatal("as_of on a dead store must error")
	}
}
