package intake

import (
	"fmt"
	"sync"
	"testing"

	"engram/internal/index"
	"engram/internal/rpc"
)

// TestTorture_ConcurrentEmit hammers one Emitter from N goroutines
// with a mixed verdict workload (inserts, conflicting updates,
// staged writes, same-session retries) and proves the single-writer
// invariant: every acked ID reads back, and the serving index is
// bit-identical to a fresh rebuild — no lost update, no double
// apply, no quarantine leak.
func TestTorture_ConcurrentEmit(t *testing.T) {
	for seed := 0; seed < 3; seed++ {
		e, s, _ := newEmitter(t, 1000000)
		const workers, per = 4, 25
		var wg sync.WaitGroup
		type ack struct {
			res EmitResult
			obj string
		}
		acks := make(chan ack, workers*per)
		errs := make(chan error, workers*per)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				ses := fmt.Sprintf("seed%d-w%d", seed, w)
				for i := 0; i < per; i++ {
					// Shared-key conflicts: UPDATE chains under contention.
					obj := fmt.Sprintf("v%d", i%5)
					res, err := e.Emit(rpc.InputRecord{
						Subject: "shared", Predicate: "p", Object: obj,
						ValidTime:  rpc.Interval{From: 1, To: 2},
						Provenance: rpc.Provenance{Source: "s", Session: ses},
						ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}})
					if err != nil {
						errs <- err
						return
					}
					acks <- ack{res, obj}
					// Same-session retry: SKIP, never a duplicate.
					if _, err := e.Emit(rpc.InputRecord{
						Subject: "shared", Predicate: "p", Object: obj,
						ValidTime:  rpc.Interval{From: 1, To: 2},
						Provenance: rpc.Provenance{Source: "s", Session: ses},
						ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}}); err != nil {
						errs <- err
						return
					}
					// Quarantine lane under contention.
					if _, err := e.Emit(rpc.InputRecord{
						Subject: fmt.Sprintf("staged-%d-%d", w, i), Predicate: "p", Object: "o",
						ValidTime:  rpc.Interval{From: 1, To: 2},
						Provenance: rpc.Provenance{Source: "low", Session: ses},
						ACL:        rpc.ACLLow, BaseConfidence: 0.5, Payload: map[string]string{}}); err != nil {
						errs <- err
						return
					}
				}
			}(w)
		}
		wg.Wait()
		close(errs)
		close(acks)
		for err := range errs {
			t.Fatalf("seed %d: worker: %v", seed, err)
		}
		nAck := 0
		for a := range acks {
			nAck++
			if a.res.Verdict == rpc.VerdictSkip {
				continue
			}
			if a.res.ID == "" {
				t.Fatalf("seed %d: acked verdict without ID: %+v", seed, a.res)
			}
			if _, err := s.Get(a.res.ID); err != nil {
				t.Fatalf("seed %d: acked ID unreadable: %v", seed, err)
			}
		}
		if nAck != workers*per {
			t.Fatalf("seed %d: want %d acks, got %d", seed, workers*per, nAck)
		}
		// Serving index must equal a fresh rebuild exactly.
		staged, err := s.ListStaged()
		if err != nil {
			t.Fatalf("seed %d: staged: %v", seed, err)
		}
		if len(staged) != workers*per {
			t.Fatalf("seed %d: want %d staged, got %d", seed, workers*per, len(staged))
		}
		fresh, err := index.Rebuild(s, staged)
		if err != nil {
			t.Fatalf("seed %d: rebuild: %v", seed, err)
		}
		want, got := fresh.IDs(), e.index.IDs()
		if len(want) != len(got) {
			t.Fatalf("seed %d: serving %d vs rebuild %d", seed, len(got), len(want))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("seed %d: serving diverges at %d", seed, i)
			}
		}
	}
}
