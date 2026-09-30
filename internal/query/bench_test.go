package query

import (
	"fmt"
	"testing"

	"engram/internal/index"
	"engram/internal/intake"
	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// benchSetup fills a store with n records (direct appends — setup
// speed, not system speed) and returns a serving index rebuilt from
// the log exactly as the live path would hold it. Subjects are
// pseudorandom hex (deterministic LCG): systematic names like
// "subject-N" all mutually clear the fuzzy floor (one-char
// differences, Jaccard ≈0.75) and turn the fuzzy lane into a table
// scan — correct but unmeasurable. The bench measures the honest
// path; the adversarial shape is documented, not timed.
func benchSetup(b *testing.B, n int) (*truth.Store, *index.Index) {
	b.Helper()
	s, err := truth.Open(b.TempDir())
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { s.Close() })
	x := uint64(0x9E3779B97F4A7C15)
	for i := 0; i < n; i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		sub := fmt.Sprintf("%016x", x)
		if i == 0 {
			sub = "querysubject"
		}
		_, err := s.Append(rpc.InputRecord{
			Subject:        sub,
			Predicate:      "pred",
			Object:         fmt.Sprintf("obj-%d", i),
			ValidTime:      rpc.Interval{From: 0, To: 1 << 62},
			Provenance:     rpc.Provenance{Source: "s", Session: "s1"},
			ACL:            rpc.ACLUser,
			BaseConfidence: 0.5,
			Payload:        map[string]string{}})
		if err != nil {
			b.Fatalf("append %d: %v", i, err)
		}
	}
	staged, err := s.ListStaged()
	if err != nil {
		b.Fatalf("staged: %v", err)
	}
	idx, err := index.Rebuild(s, staged)
	if err != nil {
		b.Fatalf("rebuild: %v", err)
	}
	return s, idx
}

func benchSpec() Spec {
	return Spec{Subject: "querysubject", Predicate: "pred", Caller: rpc.ACLUser,
		Budget: 10, Threshold: 0, Beta: processing.RankBeta,
		TrustLow: 0.25, TrustUser: 1.0, TrustSys: 1.5}
}

// BenchmarkRecall_10K is the fast local check of recall shape (~1K
// setup + pool): runs in seconds, guards the 100K number.
func BenchmarkRecall_10K(b *testing.B) {
	s, idx := benchSetup(b, 10000)
	spec := benchSpec()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Recall(idx, s, spec); err != nil {
			b.Fatalf("recall: %v", err)
		}
	}
}

// BenchmarkRecall_1K measures end-to-end recall (scan + verify +
// rank) at small scale.
func BenchmarkRecall_1K(b *testing.B) {
	s, idx := benchSetup(b, 1000)
	spec := benchSpec()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Recall(idx, s, spec); err != nil {
			b.Fatalf("recall: %v", err)
		}
	}
}

// BenchmarkRecall_100K measures the same at H7 scale — the number
// that decides whether P6 needs the feedback-by-ID secondary.
func BenchmarkRecall_100K(b *testing.B) {
	s, idx := benchSetup(b, 100000)
	spec := benchSpec()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Recall(idx, s, spec); err != nil {
			b.Fatalf("recall: %v", err)
		}
	}
}

// BenchmarkEmit_Throughput measures the acked write path (gate +
// commit + Sync + index) per record — the H7 counterpart for P4.
func BenchmarkEmit_Throughput(b *testing.B) {
	s, err := truth.Open(b.TempDir())
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { s.Close() })
	e := intake.NewEmitter(s, &index.Index{}, 1000000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Emit(rpc.InputRecord{
			Subject:   fmt.Sprintf("bench-%d", i),
			Predicate: "pred", Object: "o",
			ValidTime:  rpc.Interval{From: 0, To: 1 << 62},
			Provenance: rpc.Provenance{Source: "s", Session: "s1"},
			ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}})
		if err != nil {
			b.Fatalf("emit %d: %v", i, err)
		}
	}
}
