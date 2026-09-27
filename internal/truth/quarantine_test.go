package truth

import (
	"testing"

	"engram/internal/rpc"
)

func qin() rpc.InputRecord {
	return rpc.InputRecord{Subject: "s", Predicate: "p", Object: "o",
		ValidTime:  rpc.Interval{From: 1, To: 2},
		Provenance: rpc.Provenance{Source: "low-src", Session: "s1"},
		ACL:        rpc.ACLLow, BaseConfidence: 0.5, Payload: map[string]string{}}
}

// TestQuarantine_RoundTrip pins stage → listed+staged → promote → clear.
func TestQuarantine_RoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	rec, err := s.StageAppend(qin())
	if err != nil {
		t.Fatalf("StageAppend: %v", err)
	}
	if rec.Status != rpc.StatusActive || rec.Type != rpc.RecordEpisodic {
		t.Fatalf("staged version is ordinary ACTIVE/EPISODIC, got %+v", rec)
	}
	staged, err := s.IsStaged(rec.ID)
	if err != nil || !staged {
		t.Fatalf("IsStaged must be true, got %v err=%v", staged, err)
	}
	set, err := s.ListStaged()
	if err != nil || !set[rec.ID] {
		t.Fatalf("ListStaged must contain the record, got %v err=%v", set, err)
	}
	if err := s.Promote(rec.ID); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if staged, _ := s.IsStaged(rec.ID); staged {
		t.Fatal("promoted record must read believed")
	}
	if set, _ := s.ListStaged(); set[rec.ID] {
		t.Fatal("promoted record must leave the set")
	}
	// The record itself survives promotion: it re-runs the gate fresh.
	if _, err := s.Get(rec.ID); err != nil {
		t.Fatalf("promoted record must remain readable, got %v", err)
	}
}

// TestQuarantine_IdempotentPromote pins double-promotion as replay.
func TestQuarantine_IdempotentPromote(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if err := s.Promote("never-staged"); err != nil {
		t.Fatalf("promoting a never-staged ID must be a no-op, got %v", err)
	}
	rec, err := s.StageAppend(qin())
	if err != nil {
		t.Fatalf("StageAppend: %v", err)
	}
	if err := s.Promote(rec.ID); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if err := s.Promote(rec.ID); err != nil {
		t.Fatalf("second promote must be a no-op, got %v", err)
	}
}

// TestQuarantine_Reject pins fail-closed staging.
func TestQuarantine_Reject(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	bad := qin()
	bad.Subject = "  "
	if _, err := s.StageAppend(bad); err == nil {
		t.Fatal("invalid input must never stage")
	}
	if set, _ := s.ListStaged(); len(set) != 0 {
		t.Fatal("rejected stage must leave no marker")
	}
	if staged, _ := s.IsStaged("nope"); staged {
		t.Fatal("unknown ID must read believed")
	}
}

// TestQuarantine_NamespaceDisjoint pins M/schema vs M/quarantine
// separation: schema init must never appear staged and vice versa.
func TestQuarantine_NamespaceDisjoint(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if set, _ := s.ListStaged(); len(set) != 0 {
		t.Fatalf("fresh store stages nothing, got %v", set)
	}
}
