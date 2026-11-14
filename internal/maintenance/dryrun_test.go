package maintenance

// R0 dry-run proof (BUILD-PLAN R0 item 2 gates): the preview answers
// "what would the next tick collect" with zero writes, and a real
// tick after a preview collects exactly what the preview named.
//
// Fixture discipline (P4 finding #8): systematic names mutually clear
// the fuzzy MERGE floor and collapse into one record — subjects here
// are pseudorandom hex, dissimilar by construction.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"engram/internal/index"
	"engram/internal/intake"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// hexSub derives a pseudorandom-hex subject from i — distinct keys
// that never mutually clear the fuzzy floor.
func hexSub(i int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("engram-dryrun-%d", i)))
	return "cal" + hex.EncodeToString(h[:6])
}

// seedE1Candidates plants one expired-ACTIVE, one window-open
// SCHEDULED (both E1 candidates) and a live control, indexed like the
// write paths index them. Returns the expired id and the scheduled
// successor id.
func seedE1Candidates(t *testing.T, e *intake.Emitter, s *truth.Store, idx *index.Index) (string, string) {
	t.Helper()
	emit := func(in rpc.InputRecord) string {
		t.Helper()
		res, err := e.Emit(in)
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		return res.ID
	}
	expiredID := emit(minputW(hexSub(1), "state", "old", "s", "s1", tickWall-7200e9, tickWall-60e9))
	emit(minput(hexSub(2), "state", "live", "s", "s1", rpc.ACLUser)) // control
	// The activation candidate: window OPEN at tickWall (from <= now
	// < to) — opened and unexpired, exactly the F1 shape.
	schedID := emit(minputW(hexSub(3), "date", "done", "s", "s1", tickWall-7200e9, tickWall+7200e9))
	// Restatus mints the SCHEDULED successor under a new id — index
	// it (same discipline as the write paths) so the sweep genuinely
	// sees it.
	schedRec, err := s.Restatus(schedID, rpc.StatusScheduled)
	if err != nil {
		t.Fatalf("restatus: %v", err)
	}
	staged, err := s.ListStaged()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	idx.Add(schedRec, staged)
	return expiredID, schedRec.ID
}

// TestPreview_NoWrites pins the R0 gate: previewing twice leaves the
// store untouched (durable tick mark never moves, classifications
// stable), and the real tick afterwards collects exactly what the
// preview named — the preview consumed nothing.
func TestPreview_NoWrites(t *testing.T) {
	e, s, idx := mopen(t)
	expiredID, schedID := seedE1Candidates(t, e, s, idx)

	before, err := s.LastTick()
	if err != nil {
		t.Fatalf("LastTick: %v", err)
	}
	p1, err := Preview(s, idx, tickWall)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	p2, err := Preview(s, idx, tickWall)
	if err != nil {
		t.Fatalf("preview 2: %v", err)
	}
	if len(p1.ExpiredIDs) != 1 || p1.ExpiredIDs[0] != expiredID {
		t.Fatalf("expired = %v, want [%s]", p1.ExpiredIDs, expiredID)
	}
	if len(p1.ActivatedIDs) != 1 || p1.ActivatedIDs[0] != schedID {
		t.Fatalf("activated = %v, want [%s]", p1.ActivatedIDs, schedID)
	}
	if p1.Calibrated {
		t.Fatal("sub-sample corpus must leave E2 disarmed (MinCalSamples)")
	}
	if len(p1.RetiredIDs) != 0 {
		t.Fatalf("retired = %v, want none on a sub-sample corpus", p1.RetiredIDs)
	}
	if p1.AbstainThreshold != DefaultAbstain {
		t.Fatalf("abstain = %v, want the uncalibrated default", p1.AbstainThreshold)
	}
	if !equalStrings(p1.ExpiredIDs, p2.ExpiredIDs) || !equalStrings(p1.ActivatedIDs, p2.ActivatedIDs) {
		t.Fatal("two previews must classify identically (deterministic)")
	}
	after, err := s.LastTick()
	if err != nil {
		t.Fatalf("LastTick after: %v", err)
	}
	if after != before {
		t.Fatalf("preview moved the durable tick mark: %d -> %d", before, after)
	}

	// The real tick after the preview collects exactly what the
	// preview named — same numbers, one classification source.
	res, err := Consolidate(s, idx, 0, tickWall)
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if res.Expired != len(p1.ExpiredIDs) || res.Activated != len(p1.ActivatedIDs) {
		t.Fatalf("tick collected expired=%d activated=%d, preview named %d/%d",
			res.Expired, res.Activated, len(p1.ExpiredIDs), len(p1.ActivatedIDs))
	}
	got, err := s.Get(expiredID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != rpc.StatusTombstoned {
		t.Fatalf("the tick must close the previewed version, got %v", got.Status)
	}
}

// TestPreview_CalibratedParity pins the E2 leg's arm/disarm at the
// Preview level: a corpus that clears MinCalSamples reports
// Calibrated (E2 armed) and — with all touches fresh — names zero
// retirements, and the real tick agrees (one classifier, zero
// drift). Retirement selection itself is proven drift-free against
// synthetic seq arithmetic in TestPruneVictims_Parity, the house
// pattern for economics tests (real 100k-write aging would cost
// minutes of fsyncs for zero extra signal).
func TestPreview_CalibratedParity(t *testing.T) {
	e, s, idx := mopen(t)
	for i := 0; i < 32; i++ {
		if _, err := e.Emit(minput(hexSub(100+i), "state", "v", "s", "s1", rpc.ACLUser)); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	p1, err := Preview(s, idx, tickWall)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !p1.Calibrated {
		t.Fatalf("32 distinct facts must clear MinCalSamples=%d", MinCalSamples)
	}
	if len(p1.RetiredIDs) != 0 {
		t.Fatalf("fresh touches survive the horizon, retired = %v", p1.RetiredIDs)
	}
	res, err := Consolidate(s, idx, 0, tickWall)
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if res.Retired != len(p1.RetiredIDs) {
		t.Fatalf("tick retired %d, preview named %d", res.Retired, len(p1.RetiredIDs))
	}
	// Post-tick preview names nothing new (idempotence shape, P2).
	p2, err := Preview(s, idx, tickWall)
	if err != nil {
		t.Fatalf("preview 2: %v", err)
	}
	if len(p2.ExpiredIDs) != 0 || len(p2.ActivatedIDs) != 0 || len(p2.RetiredIDs) != 0 {
		t.Fatalf("post-tick preview must be clean, got expired=%v activated=%v retired=%v",
			p2.ExpiredIDs, p2.ActivatedIDs, p2.RetiredIDs)
	}
}

// TestPruneVictims_Parity pins the E2 classifier against the real
// pass with synthetic seq arithmetic (house horizon-guard pattern):
// on the SAME store state, pruneVictims names exactly the victims
// pruneAt closes, ID-for-ID — the preview's retirement numbers
// cannot drift from the tick's.
func TestPruneVictims_Parity(t *testing.T) {
	e, s, idx := mopen(t)
	for i := 0; i < 4; i++ {
		if _, err := e.Emit(minput(hexSub(200+i), "state", "v", "s", "s1", rpc.ACLUser)); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	tail, err := s.TailOffset()
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	// Sub-floor values (aged synthetic now, hits=1) + past-horizon
	// touches → retirement candidates; synthetic now is the house
	// pattern for economics tests.
	now := int64(tail) + RetireHorizon + 1
	named, err := pruneVictims(s, liveIndex(s, idx), 0.5, now, RetireHorizon)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if len(named) != 4 {
		t.Fatalf("classifier named %v, want all 4 aged candidates", named)
	}
	// The real pass over the SAME untouched state must close exactly
	// the named set (classify never mutates, so the re-classification
	// inside pruneAt sees identical input).
	closed, err := pruneAt(s, idx, 0.5, now, RetireHorizon)
	if err != nil {
		t.Fatalf("pruneAt: %v", err)
	}
	if !equalStrings(named, closed) {
		t.Fatalf("classifier %v and pass %v must agree ID-for-ID", named, closed)
	}
	for _, id := range closed {
		got, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != rpc.StatusTombstoned {
			t.Fatalf("victim %s must be closed by the pass", id)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
