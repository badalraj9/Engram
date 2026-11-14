package maintenance

// P2 tick-GC proof (DESIGN §6 ratified contract): E1 validity sweep,
// E2 horizon + calibrated gates, per-cause counts, idempotence. The
// wall clock is injected (tickWall and explicit neighbors), the
// sequence clock is exact arithmetic — no wall flakiness anywhere.

import (
	"testing"
	"time"

	"engram/internal/query"
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
	expired, _, err := expireAt(s, idx, tickWall)
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
	if _, _, err := expireAt(s, idx, tickWall); err != nil {
		t.Fatalf("first: %v", err)
	}
	again, _, err := expireAt(s, idx, tickWall)
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

// TestExpire_ScheduledActivates pins the F1 activation transition
// (DESIGN §1 VALID-NOW, amended 2026-11-13): a SCHEDULED version
// whose window opened by tick-time flips ACTIVE with its window
// preserved, the closed SCHEDULED version retained for history, and
// recall surfacing it at open-now. Fails on the old code, where
// nothing ever flipped the status and the fact stayed hidden forever.
// Windows ride the real wall clock (write-side maybeSchedule labels
// against time.Now) — future is future on any clock.
func TestExpire_ScheduledActivates(t *testing.T) {
	e, s, idx := mopen(t)
	now := time.Now().UnixNano()
	if _, err := e.Emit(minputW("launch", "date", "midnov", "s", "s1",
		now+3600e9, now+7200e9)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var sched string
	for _, id := range idx.IDs() {
		rec, err := s.Get(id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rec.Status == rpc.StatusScheduled {
			sched = id
		}
	}
	if sched == "" {
		t.Fatal("future-dated emit must commit a SCHEDULED successor, indexed")
	}
	expired, activated, err := expireAt(s, idx, now+5400e9)
	if err != nil {
		t.Fatalf("expireAt: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("nothing may expire here, got %v", expired)
	}
	if len(activated) != 1 {
		t.Fatalf("activated = %+v, want exactly the opened window", activated)
	}
	if activated[0].OldID != sched {
		t.Fatalf("activated wrong row: %s != %s", activated[0].OldID, sched)
	}
	rec, err := s.Get(activated[0].Rec.ID)
	if err != nil {
		t.Fatalf("get activated: %v", err)
	}
	if rec.Status != rpc.StatusActive {
		t.Fatalf("opened window must flip ACTIVE, got %v", rec.Status)
	}
	if rec.ValidTime.From != now+3600e9 || rec.ValidTime.To != now+7200e9 {
		t.Fatalf("valid-time must be preserved, got %+v", rec.ValidTime)
	}
	closed, err := s.Get(sched)
	if err != nil {
		t.Fatalf("get closed: %v", err)
	}
	if closed.Status != rpc.StatusTombstoned || closed.Txn.To == rpc.OpenEnd {
		t.Fatalf("SCHEDULED version must close for history, got %+v", closed)
	}
}

// TestExpire_ScheduledBoundary pins both sides of the activation
// boundary: a window still closed (valid_from > wallNow) stays
// SCHEDULED untouched; a window that opened AND closed between ticks
// tombstones without ever activating (expiry outranks activation).
func TestExpire_ScheduledBoundary(t *testing.T) {
	e, s, idx := mopen(t)
	now := time.Now().UnixNano()
	if _, err := e.Emit(minputW("later", "date", "far", "s", "s1",
		now+9600e9, now+12800e9)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := e.Emit(minputW("past", "date", "missed", "s", "s1",
		now+3600e9, now+6400e9)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	expired, activated, err := expireAt(s, idx, now+8000e9)
	if err != nil {
		t.Fatalf("expireAt: %v", err)
	}
	if len(activated) != 0 {
		t.Fatalf("no window may activate here, got %+v", activated)
	}
	if len(expired) != 1 {
		t.Fatalf("expired = %v, want exactly the missed window", expired)
	}
	closed, err := s.Get(expired[0])
	if err != nil {
		t.Fatalf("get expired: %v", err)
	}
	if closed.Subject != "past" || closed.Status != rpc.StatusTombstoned {
		t.Fatalf("missed window must tombstone, got %+v", closed)
	}
	for _, id := range idx.IDs() {
		rec, err := s.Get(id)
		if err != nil {
			continue
		}
		if rec.Subject == "later" && rec.Status != rpc.StatusScheduled {
			t.Fatalf("still-future window must stay SCHEDULED, got %v", rec.Status)
		}
		if rec.Subject == "past" && rec.Status == rpc.StatusActive {
			t.Fatalf("missed window must never activate, got %+v", rec)
		}
	}
}

// TestTick_ScheduledActivates pins the transition through the full
// serialized tick and its count: Activated=1 lands in the Result,
// the successor indexes, and the next tick flips nothing again
// (idempotent).
func TestTick_ScheduledActivates(t *testing.T) {
	e, s, idx := mopen(t)
	now := time.Now().UnixNano()
	if _, err := e.Emit(minputW("launch", "date", "midnov", "s", "s1",
		now+3600e9, now+7200e9)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	res, err := Consolidate(s, idx, 0, now+5400e9)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Activated != 1 || res.Expired != 0 {
		t.Fatalf("want Activated=1 Expired=0, got %+v", res)
	}
	active := 0
	for _, id := range idx.IDs() {
		rec, err := s.Get(id)
		if err != nil {
			continue
		}
		if rec.Subject == "launch" && rec.Status == rpc.StatusActive {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("exactly one ACTIVE successor expected, got %d", active)
	}
	// The read path agrees: at open-now the fact recalls, ACTIVE —
	// on the old code it abstained forever (nothing flipped the
	// status, recall hard-filters SCHEDULED).
	rres, err := query.Recall(idx, s, query.Spec{
		Subject: "launch", Predicate: "date", Caller: rpc.ACLUser,
		Budget: 8, Beta: 0.5,
		TrustLow: 0.25, TrustUser: 1.0, TrustSys: 1.5,
		Now: now + 5400e9,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if rres.Abstained || len(rres.Records) == 0 {
		t.Fatalf("activated fact must recall at open-now, abstained=%v", rres.Abstained)
	}
	for _, r := range rres.Records {
		if r.Status == rpc.StatusScheduled {
			t.Fatalf("SCHEDULED must not surface post-activation: %+v", r)
		}
		if r.Object == "midnov" && r.Status != rpc.StatusActive {
			t.Fatalf("activated record must recall ACTIVE, got %v", r.Status)
		}
	}
	again, err := Consolidate(s, idx, 1, now+5400e9)
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if again.Activated != 0 || again.Expired != 0 {
		t.Fatalf("second tick must transition nothing, got %+v", again)
	}
}
