package index

import (
	"fmt"
	"sync"
	"testing"

	"engram/internal/rpc"
	"engram/internal/truth"
)

// TestHarden_ConcurrentDrain (H3) tortures the snapshot boundary:
// writers append THROUGHOUT repeated rebuilds, and the final rebuild
// holds every version exactly once — no double-apply, no silent
// drop, across seeds.
func TestHarden_ConcurrentDrain(t *testing.T) {
	for seed := 0; seed < 3; seed++ {
		s, err := truth.Open(t.TempDir())
		if err != nil {
			t.Fatalf("seed %d: Open: %v", seed, err)
		}
		const writers, per = 4, 25
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < per; i++ {
					_, err := s.Append(rpc.InputRecord{
						Subject:   fmt.Sprintf("seed%d-w%d-r%d", seed, w, i),
						Predicate: "p", Object: "o",
						ValidTime:  rpc.Interval{From: 1, To: 2},
						Provenance: rpc.Provenance{Source: "s", Session: fmt.Sprintf("s%d", w)},
						ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}})
					if err != nil {
						errs <- err
						return
					}
				}
			}(w)
		}
		// Rebuild repeatedly mid-write: snapshots, never errors.
		for i := 0; i < 5; i++ {
			if _, err := Rebuild(s, nil); err != nil {
				t.Fatalf("seed %d: mid-write rebuild %d: %v", seed, i, err)
			}
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("seed %d: writer: %v", seed, err)
		}
		x, err := Rebuild(s, nil)
		if err != nil {
			t.Fatalf("seed %d: final rebuild: %v", seed, err)
		}
		if x.Len() != writers*per {
			t.Fatalf("seed %d: want %d exactly once, got %d", seed, writers*per, x.Len())
		}
		for w := 0; w < writers; w++ {
			for i := 0; i < per; i++ {
				got := x.Exact(fmt.Sprintf("seed%d-w%d-r%d", seed, w, i), "p")
				if len(got) != 1 {
					t.Fatalf("seed %d: w%d-r%d present %d times, want 1", seed, w, i, len(got))
				}
			}
		}
		s.Close()
	}
}

// TestHarden_RebuildDeterministic (H4) proves same log → same index:
// two rebuilds agree bit-for-bit (IDs + per-key sets), and in-memory
// Add order never changes membership.
func TestHarden_RebuildDeterministic(t *testing.T) {
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	a := mustAppend(t, s, "pump", "runs", "yes")
	mustAppend(t, s, "pump", "runs", "no")
	if _, err := s.Restatus(a.ID, rpc.StatusBoth); err != nil {
		t.Fatalf("Restatus: %v", err)
	}
	if err := s.AppendFeedback(rpc.FeedbackEvent{RecordID: a.ID, Kind: rpc.FeedbackStrength, Source: "s", Session: "s2", Tick: 1}); err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	x1, err := Rebuild(s, nil)
	if err != nil {
		t.Fatalf("rebuild 1: %v", err)
	}
	x2, err := Rebuild(s, nil)
	if err != nil {
		t.Fatalf("rebuild 2: %v", err)
	}
	ids1, ids2 := x1.IDs(), x2.IDs()
	if len(ids1) != len(ids2) {
		t.Fatalf("rebuilds disagree on size: %d vs %d", len(ids1), len(ids2))
	}
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Fatalf("rebuilds disagree: %v vs %v", ids1, ids2)
		}
	}
	// Permuted Add order: same membership, whatever the slice order.
	var fwd, rev Index
	recs := []rpc.Record{irec("1", "s", "p", "a"), irec("2", "s", "p", "b"), irec("3", "s", "p", "c")}
	for _, r := range recs {
		fwd.Add(r, nil)
	}
	for i := len(recs) - 1; i >= 0; i-- {
		rev.Add(recs[i], nil)
	}
	f, r := idSet(fwd.Exact("s", "p")), idSet(rev.Exact("s", "p"))
	if len(f) != 3 || len(r) != 3 {
		t.Fatalf("permuted adds must hold all 3: %v vs %v", f, r)
	}
	for id := range f {
		if !r[id] {
			t.Fatalf("permuted adds diverge: %v vs %v", f, r)
		}
	}
}
