package intake

import (
	"testing"

	"engram/internal/index"
	"engram/internal/rpc"
	"engram/internal/truth"
)

// TestFailClosed_DeadStore pins total-store-failure behavior: every
// mutating path errors, nothing is acked, the serving index is
// unmutated. No fault-injection seam — a closed store is the honest
// total-failure harness (Badger rejects every txn on it).
func TestFailClosed_DeadStore(t *testing.T) {
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	idx := &index.Index{}
	e := NewEmitter(s, idx, 1000)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	in := ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser)
	if _, err := e.Emit(in); err == nil {
		t.Fatal("emit on a dead store must error, never ack")
	}
	if _, _, err := e.Correct(CorrectHint{Subject: "pump", Predicate: "runs", Object: "yes"}, in); err == nil {
		t.Fatal("correct on a dead store must error")
	}
	if _, _, err := e.Attest("any", true, "s", "s1"); err == nil {
		t.Fatal("attest on a dead store must error")
	}
	if _, err := e.Resolve("a", "b", rpc.ResolveKeepA, rpc.Provenance{Source: "s", Session: "s1"}); err == nil {
		t.Fatal("resolve on a dead store must error")
	}
	if idx.Len() != 0 {
		t.Fatal("failed writes must leave the serving index untouched")
	}
}
