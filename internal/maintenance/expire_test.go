package maintenance

// P2 tick-GC proof (DESIGN §6 ratified contract): E1 validity sweep,
// E2 horizon + calibrated gates, per-cause counts, idempotence. The
// wall clock is injected (tickWall and explicit neighbors), the
// sequence clock is exact arithmetic — no wall flakiness anywhere.

import (
	"testing"

	"engram/internal/rpc"
)

func minputW(sub, pred, obj, src, ses string, from, to int64) rpc.InputRecord {
	in := minput(sub, pred, obj, src, ses, rpc.ACLUser)
	in.ValidTime = rpc.Interval{From: from, To: to}
	return in
}

// TestExpire_Sweep pins E1: expired ACTIVE and expired SCHEDULED
// tombstone with the tick as closer; future and always-true rows are
// untouched; the count splits by cause downstream.
func TestExpire_Sweep(t *testing.T) {
	e, s, idx := mopen(t)
	emit := func(in rpc.InputRecord) string {
		t.Helper()
		res, err := e.Emit(in)
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		return res.ID
	}
	pastID := emit(minputW("was", "state", "old", "s", "s1", tickWall-7200e9, tickWall-60e9))
	emit(minputW("will", "state", "new", "s", "s1", tickWall+3600e9, tickWall+7200e9))
	emit(minput("is", "state", "live", "s", "s1", rpc.ACLUser))
	schedIn := minputW("fete", "date", "done", "s", "s1", tickWall-7200e9, tickWall-60e9)
	schedID := emit(schedIn)
	// Restatus mints the SCHEDULED successor under a new id — index
	// it (same discipline as the write paths) so the sweep genuinely
	// sees it; the stale old id resolves tombstoned via store-verify.
	schedRec, err := s.Restatus(schedID, rpc.StatusScheduled)
	if err != nil {
		t.Fatalf("restatus: %v", err)
	}
	staged, err := s.ListStaged()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	idx.Add(schedRec, staged)
	expired, err := expireAt(s, idx, tickWall)
	if err != nil {
		t.Fatalf("expireAt: %v", err)
	}
	if len(expired) != 2 {
		t.Fatalf("expired = %v, want the 2 closed windows", expired)
	}
	for _, id := range []string{pastID, schedRec.ID} {
		got, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != rpc.StatusTombstoned {
			t.Fatalf("expired version %s must tombstone, got %v", id, got.Status)
		}
		if got.Txn.To == rpc.OpenEnd {
			t.Fatalf("the tick must close the interval, got %+v", got.Txn)
		}
	}
}

// TestExpire_Idempotent pins re-run safety: the second pass over the
// same state closes nothing new (closed versions are skipped, never
// re-closed).
func TestExpire_Idempotent(t *testing.T) {
	e, s, idx := mopen(t)
	in := minputW("was", "state", "old", "s", "s1", tickWall-7200e9, tickWall-60e9)
	if _, err := e.Emit(in); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := expireAt(s, idx, tickWall); err != nil {
		t.Fatalf("first: %v", err)
	}
	again, err := expireAt(s, idx, tickWall)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep must close nothing, got %v", again)
	}
}

// TestPrune_HorizonGuard pins the E2 recency rule at exact sequence
// arithmetic: with a tiny horizon, the untouched elder tombstones
// while the feedback-touched peer survives — forgetting means disuse,
// never age. evict is unbounded so only the horizon decides.
func TestPrune_HorizonGuard(t *testing.T) {
	e, s, idx := mopen(t)
	if _, err := e.Emit(minput("elder", "p", "o", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit elder: %v", err)
	}
	peer, err := e.Emit(minput("peer", "p", "o", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit peer: %v", err)
	}
	tail, err := s.TailOffset()
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	// Touch the peer at the current tail (attest-strength tick rides
	// the same commit sequence as writes — one clock).
	if err := s.AppendFeedback(rpc.FeedbackEvent{RecordID: peer.ID,
		Kind: rpc.FeedbackStrength, Source: "s", Session: "s2", Tick: int64(tail)}); err != nil {
		t.Fatalf("feedback: %v", err)
	}
	victims, err := pruneAt(s, idx, 999, int64(tail), 1)
	if err != nil {
		t.Fatalf("pruneAt: %v", err)
	}
	if len(victims) != 1 {
		t.Fatalf("victims = %v, want exactly the untouched elder", victims)
	}
	got, err := s.Get(victims[0])
	if err != nil || got.Subject != "elder" {
		t.Fatalf("wrong victim: %+v err=%v", got, err)
	}
}

// TestTick_CountsSplit pins the contract's observable: one expired
// row splits Expired=1/Retired=0/Pruned=1 on a sub-sample corpus
// (E2 conservatively retires nothing there — the calibrated gate).
func TestTick_CountsSplit(t *testing.T) {
	e, s, idx := mopen(t)
	in := minputW("was", "state", "old", "s", "s1", tickWall-7200e9, tickWall-60e9)
	if _, err := e.Emit(in); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := e.Emit(minput("is", "state", "live", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	res, err := Consolidate(s, idx, 0, tickWall)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Expired != 1 || res.Retired != 0 || res.Pruned != 1 {
		t.Fatalf("want Expired=1 Retired=0 Pruned=1, got %+v", res)
	}
	// The survivor stays ACTIVE and recall-visible in shape.
	for _, id := range idx.IDs() {
		rec, err := s.Get(id)
		if err != nil {
			continue
		}
		if rec.Subject == "is" && rec.Status != rpc.StatusActive {
			t.Fatalf("live row must survive the tick, got %+v", rec)
		}
	}
}

// TestTick_ConsecutiveZeros pins idempotence: the immediate next tick
// with no intervening writes closes nothing new.
func TestTick_ConsecutiveZeros(t *testing.T) {
	e, s, idx := mopen(t)
	in := minputW("was", "state", "old", "s", "s1", tickWall-7200e9, tickWall-60e9)
	if _, err := e.Emit(in); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := Consolidate(s, idx, 0, tickWall); err != nil {
		t.Fatalf("tick 0: %v", err)
	}
	res, err := Consolidate(s, idx, 1, tickWall)
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if res.Expired != 0 || res.Retired != 0 || res.Pruned != 0 {
		t.Fatalf("second tick must close nothing, got %+v", res)
	}
}
