package maintenance

import (
	"fmt"
	"testing"

	"engram/internal/index"
	"engram/internal/intake"
	"engram/internal/rpc"
	"engram/internal/truth"
)

func minput(sub, pred, obj, src, ses string, acl rpc.AclTier) rpc.InputRecord {
	// Always-true window (DESIGN §1 VALID-NOW open end): the tick
	// stamps wall-now, so toy-domain windows would expire under test.
	return rpc.InputRecord{Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 0, To: 1 << 62},
		Provenance: rpc.Provenance{Source: src, Session: ses},
		ACL:        acl, BaseConfidence: 0.5, Payload: map[string]string{}}
}

// tickWall is the fixed wall-clock the tick tests stamp (deterministic
// — validity fixtures place explicit windows around it or beside it).
const tickWall = 1700000000000000000

func mopen(t *testing.T) (*intake.Emitter, *truth.Store, *index.Index) {
	t.Helper()
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	idx := &index.Index{}
	return intake.NewEmitter(s, idx, 100000), s, idx
}

// TestTick_MonotonicNoMutation pins the durable guard: re-running a
// tick fails closed with no new effects (no second summary, no extra
// prune), and tick+1 proceeds.
func TestTick_MonotonicNoMutation(t *testing.T) {
	e, s, idx := mopen(t)
	for _, p := range []string{"p1", "p2"} {
		if _, err := e.Emit(minput("saga", p, "o", "s", "sess", rpc.ACLUser)); err != nil {
			t.Fatal(err)
		}
	}
	r1, err := Consolidate(s, idx, 0, tickWall)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Consolidated != 1 {
		t.Fatalf("want 1 summary, got %+v", r1)
	}
	if _, err := Consolidate(s, idx, 0, tickWall); err == nil {
		t.Fatal("re-running tick 0 must fail")
	}
	r2, err := Consolidate(s, idx, 1, tickWall)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Consolidated != 0 {
		t.Fatalf("second tick must not re-summarize, got %+v", r2)
	}
	last, _ := s.LastTick()
	if last != 1 {
		t.Fatalf("LastTick = %d, want 1", last)
	}
}

// TestSweep_FlagsOverlap pins the invariant-audit: two live ACTIVE
// versions under one key with different objects over overlapping time
// (planted past the gate via direct appends) route to BOTH.
func TestSweep_FlagsOverlap(t *testing.T) {
	_, s, idx := mopen(t)
	a, err := s.Append(minput("pump", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Append(minput("pump", "runs", "no", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatal(err)
	}
	staged, _ := s.ListStaged()
	idx.Add(a, staged)
	idx.Add(b, staged)
	res, err := Consolidate(s, idx, 0, tickWall)
	if err != nil {
		t.Fatal(err)
	}
	if res.Swept != 2 {
		t.Fatalf("swept = %d, want 2", res.Swept)
	}
	for _, id := range []string{a.ID, b.ID} {
		// Old versions close; the BOTH successors carry the content.
		got, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == rpc.StatusActive {
			t.Fatalf("violator %s must not stay ACTIVE", id)
		}
	}
}

// TestPrune_Invariant pins FR11's fence: a huge evict threshold
// tombstones the lone low record but never a SUMMARY, never a live
// parent reachable from a SUMMARY chain.
func TestPrune_Invariant(t *testing.T) {
	e, s, idx := mopen(t)
	for _, p := range []string{"p1", "p2"} {
		if _, err := e.Emit(minput("keep", p, "o", "s", "sess", rpc.ACLUser)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Emit(minput("lone", "p", "o", "s", "other", rpc.ACLUser)); err != nil {
		t.Fatal(err)
	}
	if _, err := Consolidate(s, idx, 0, tickWall); err != nil {
		t.Fatal(err)
	}
	victims, err := Prune(s, idx, 999)
	if err != nil {
		t.Fatal(err)
	}
	if len(victims) != 1 {
		t.Fatalf("victims = %v, want exactly the lone record", victims)
	}
	got, err := s.Get(victims[0])
	if err != nil || got.Subject != "lone" {
		t.Fatalf("wrong victim: %+v err=%v", got, err)
	}
	// Parents + summary survive the unbounded threshold.
	for _, id := range idx.IDs() {
		rec, err := s.Get(id)
		if err != nil {
			continue
		}
		if rec.Type == rpc.RecordSummary && rec.Status == rpc.StatusTombstoned {
			t.Fatal("summary must never prune")
		}
		if rec.Subject == "keep" && rec.Status == rpc.StatusTombstoned {
			t.Fatal("reachable parent must never prune")
		}
	}
}

// TestCalibrate_Scale pins the score-scale contract: thresholds finite
// and >= 0 on a populated log, defaults on an empty one.
func TestCalibrate_Scale(t *testing.T) {
	e, s, _ := mopen(t)
	th, err := Calibrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if th != Defaults() {
		t.Fatalf("empty store must yield defaults, got %+v", th)
	}
	for i := 0; i < 8; i++ {
		if _, err := e.Emit(minput(fmt.Sprintf("s%d", i), "p", "o", "s", "sess", rpc.ACLUser)); err != nil {
			t.Fatal(err)
		}
	}
	th, err = Calibrate(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{th.Abstain, th.Evict} {
		if v < 0 || v != v || v > 1e9 {
			t.Fatalf("score-scale threshold out of range: %+v", th)
		}
	}
	if th.Accept != DefaultAccept {
		t.Fatalf("accept = %v, want default %v", th.Accept, DefaultAccept)
	}
}

// TestCalibrate_SmallCorpusDefaults pins the dogfood-2026-09-29 guard:
// below MinCalSamples the pass returns defaults — a handful of
// uniformly-fresh values must never floor queries at their own
// baseline (that blinded a live store after its first tick).
func TestCalibrate_SmallCorpusDefaults(t *testing.T) {
	e, s, _ := mopen(t)
	for i := 0; i < MinCalSamples-1; i++ {
		if _, err := e.Emit(minput(fmt.Sprintf("fresh%d", i), "p", "o", "s", "sess", rpc.ACLUser)); err != nil {
			t.Fatal(err)
		}
	}
	th, err := Calibrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if th != Defaults() {
		t.Fatalf("sub-sample corpus must yield defaults, got %+v", th)
	}
}

// TestConsolidate_RejectPath pins the distortion gate: a 33-member
// cluster with all-distinct objects exceeds SummaryMaxObjects, so
// D_probe ≈ 0.52 > 0.5 accept — no summary ships.
func TestConsolidate_RejectPath(t *testing.T) {
	e, s, idx := mopen(t)
	for i := 0; i < 33; i++ {
		if _, err := e.Emit(minput("wide", fmt.Sprintf("p%d", i), fmt.Sprintf("obj-%d", i), "s", "sess", rpc.ACLUser)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Consolidate(s, idx, 0, tickWall)
	if err != nil {
		t.Fatal(err)
	}
	if res.Consolidated != 0 {
		t.Fatalf("distorted cluster must not consolidate, got %+v", res)
	}
}
