// M7 write-cost flatness + M8b snapshot-consistent reads (SRS §7).
//
// M7: p99 Append(+Sync, the production ack shape — intake syncs per
// emit) stays flat at 1K/10K/100K/1M records, including through value-log
// GC (the compaction clause). Scope: the INSERT append path with distinct
// keys (supersede adds Gets — a separate curve, not this gate).
// The 1M leg runs unless -short (about fifteen seconds here);
// `go test -short` covers 1K/10K/100K.
//
// M8b: concurrent appends racing scans — every Scan must observe a
// commit-ordered prefix (contiguous offsets from `from`, no gaps, no
// reordering). That is Badger's MVCC snapshot isolation doing the job
// DESIGN §6 credits it with (correction-vs-consolidation reads free).
package truth

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"engram/internal/rpc"
)

func m7Input(i int) rpc.InputRecord {
	return rpc.InputRecord{
		Subject:   fmt.Sprintf("m7-subject-%08d", i),
		Predicate: "employs", Object: "acme",
		ValidTime:  rpc.Interval{From: 1, To: 2},
		Provenance: rpc.Provenance{Source: "m7", Session: "s1"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.5,
	}
}

// p99 over durations (sorted, nearest-rank).
func p99(ds []time.Duration) time.Duration {
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	if len(cp) == 0 {
		return 0
	}
	k := (len(cp)*99 + 99) / 100
	if k < 1 {
		k = 1
	}
	return cp[k-1]
}

// TestM7_WriteCostFlat appends through the scale ladder with per-write
// Sync (production shape) and GC mid-ladder, asserting p99 flatness:
// p99 at 100K/1M within 4x of p99 at 1K, and an absolute ceiling that
// catches pathological (not merely slow) storage.
func TestM7_WriteCostFlat(t *testing.T) {
	s := openTest(t)
	legs := []int{1000, 10000, 100000}
	if !testing.Short() {
		legs = append(legs, 1000000)
	}
	var base time.Duration
	written := 0
	for _, total := range legs {
		var lat []time.Duration
		for written < total {
			start := time.Now()
			if _, err := s.Append(m7Input(written)); err != nil {
				t.Fatal(err)
			}
			if err := s.Sync(); err != nil {
				t.Fatal(err)
			}
			lat = append(lat, time.Since(start))
			written++
		}
		// Compaction clause: GC between legs (tick-owned in production).
		if err := s.RunValueLogGC(); err != nil {
			t.Fatal(err)
		}
		p := p99(lat)
		t.Logf("M7: total=%d leg=%d p99=%v", total, len(lat), p)
		if base == 0 {
			base = p
			continue
		}
		if p > 4*base {
			t.Fatalf("M7: p99 %v at %d exceeds 4x base %v at 1K", p, total, base)
		}
		if p > 50*time.Millisecond {
			t.Fatalf("M7: absolute p99 %v at %d pathological", p, total)
		}
	}
}

// TestM8b_SnapshotConsistentReads races one writer (500 appends)
// against four scanners: every scan from 0 must be a contiguous
// commit-ordered prefix — gaps or reorderings fail the gate.
func TestM8b_SnapshotConsistentReads(t *testing.T) {
	s := openTest(t)
	const writes = 500
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if _, err := s.Append(m7Input(1000000 + i)); err != nil {
				errCh <- err
				return
			}
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				var offs []uint64
				if err := s.Scan(0, func(e Event) error {
					offs = append(offs, e.Offset)
					return nil
				}); err != nil {
					errCh <- err
					return
				}
				for j := 1; j < len(offs); j++ {
					if offs[j] != offs[j-1]+1 {
						errCh <- fmt.Errorf("M8b: scan not a contiguous prefix: ...%d,%d...", offs[j-1], offs[j])
						return
					}
				}
				if len(offs) > 0 && offs[0] != 0 {
					errCh <- fmt.Errorf("M8b: scan prefix must start at 0, got %d", offs[0])
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	tail, err := s.TailOffset()
	if err != nil {
		t.Fatal(err)
	}
	if tail != writes {
		t.Fatalf("M8b: tail %d, want %d", tail, writes)
	}
}
