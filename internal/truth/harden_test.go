package truth

import (
	"fmt"
	"testing"
	"time"

	"engram/internal/rpc"
)

// TestHarden_KeyFraming pins the length-prefix discipline at unit
// level for BOTH secondary families: ("a","bc") never collides with
// ("ab","c"), and the SP/OP namespaces never collide with each
// other. secondaryOPPrefix has no P1 reader yet (P4's hop-2 lane will
// own it) — this test holds its framing honest meanwhile.
func TestHarden_KeyFraming(t *testing.T) {
	if string(secondarySPPrefix("a", "bc")) == string(secondarySPPrefix("ab", "c")) {
		t.Fatal("SP framing must keep (a,bc) distinct from (ab,c)")
	}
	if string(secondaryOPPrefix("a", "bc")) == string(secondaryOPPrefix("ab", "c")) {
		t.Fatal("OP framing must keep (a,bc) distinct from (ab,c)")
	}
	if string(secondarySPPrefix("x", "y")) == string(secondaryOPPrefix("x", "y")) {
		t.Fatal("SP and OP namespaces must never collide")
	}
	if keyPrefix(secondarySPPrefix("x", "y")) != prefixSubPred {
		t.Fatal("keyPrefix must report the SP namespace")
	}
	if keyPrefix(nil) != 0 {
		t.Fatal("keyPrefix(nil) must be 0, never panic")
	}
}

// TestHarden_ParseSeqRejectsShort proves the structural check fails
// closed instead of slicing out of bounds.
func TestHarden_ParseSeqRejectsShort(t *testing.T) {
	if _, err := parseSeqFromKey([]byte{prefixRecord, 1, 2}); err == nil {
		t.Fatal("short key must fail closed")
	}
	if _, err := parseSeqFromKey(nil); err == nil {
		t.Fatal("nil key must fail closed")
	}
}

// TestHarden_QueryExactRejectsAxis proves an unknown axis never
// executes a scan that means something else.
func TestHarden_QueryExactRejectsAxis(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := s.QueryExact("s", "p", 1, rpc.Axis("sideways")); err == nil {
		t.Fatal("unknown axis must fail closed")
	}
}

// TestHarden_ValueLogGCSmoke proves the P5-owned entry point is at
// least callable against a live store — untested-until-P5 is
// recorded, not assumed safe.
func TestHarden_ValueLogGCSmoke(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := s.Append(rpc.InputRecord{Subject: "s", Predicate: "p", Object: "o",
		ValidTime:  rpc.Interval{From: 1, To: 2},
		Provenance: rpc.Provenance{Source: "s", Session: "s1"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := RunValueLogGC(s.db); err != nil {
		t.Fatalf("GC smoke must not error on a live store, got %v", err)
	}
}

// TestHarden_ScaleSmoke (H7) is the M7 down-payment: 100K-record
// append timings on record (Short-mode skips it; full runs pay the
// seconds once). Not the 1M gate — just numbers so P6 holds no
// surprise. Index-side timings at scale live beside it in P6.
func TestHarden_ScaleSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("scale smoke runs in full mode only")
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	const n = 100000
	start := time.Now()
	for i := 0; i < n; i++ {
		_, err := s.Append(rpc.InputRecord{
			Subject:   fmt.Sprintf("subject-%d", i%10000),
			Predicate: "pred", Object: fmt.Sprintf("obj-%d", i),
			ValidTime:  rpc.Interval{From: 1, To: 2},
			Provenance: rpc.Provenance{Source: "s", Session: "s1"},
			ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	took := time.Since(start)
	t.Logf("append %d records: %v (%.1fk/s)", n, took, float64(n)/took.Seconds()/1000)
	start = time.Now()
	var scanned uint64
	if err := s.Scan(0, func(ev Event) error {
		scanned++
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	stook := time.Since(start)
	t.Logf("scan %d events: %v (%.1fk/s)", scanned, stook, float64(scanned)/stook.Seconds()/1000)
}
