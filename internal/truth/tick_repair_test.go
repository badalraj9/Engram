package truth

import (
	"testing"

	"engram/internal/rpc"
)

func openTickTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTick_FreshMinusOne(t *testing.T) {
	s := openTickTest(t)
	last, err := s.LastTick()
	if err != nil {
		t.Fatal(err)
	}
	if last != -1 {
		t.Fatalf("fresh LastTick = %d, want -1", last)
	}
}

func TestTick_MonotonicCommitAndReject(t *testing.T) {
	s := openTickTest(t)
	if err := s.CommitTick(0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitTick(0); err == nil {
		t.Fatal("re-commit of same tick must fail")
	}
	if err := s.CommitTick(1); err != nil {
		t.Fatal(err)
	}
	last, err := s.LastTick()
	if err != nil {
		t.Fatal(err)
	}
	if last != 1 {
		t.Fatalf("LastTick = %d, want 1", last)
	}
	if err := s.CommitTick(-1); err == nil {
		t.Fatal("negative tick must fail")
	}
}

func TestTick_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitTick(7); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	last, err := s2.LastTick()
	if err != nil {
		t.Fatal(err)
	}
	if last != 7 {
		t.Fatalf("reopened LastTick = %d, want 7", last)
	}
	if err := s2.CommitTick(7); err == nil {
		t.Fatal("old tick must be rejected after reopen")
	}
}

func TestRepair_GatedNoMutation(t *testing.T) {
	s := openTickTest(t)
	rec, err := s.Append(rpc.InputRecord{
		Subject: "a", Predicate: "p", Object: "o",
		ValidTime:  rpc.Interval{From: 0, To: rpc.OpenEnd},
		Provenance: rpc.Provenance{Source: "s", Session: "ss"},
		ACL:        rpc.ACLUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepairTruncateFrom(0, "", false); err == nil {
		t.Fatal("unauthorized repair must fail")
	}
	if _, err := s.RepairTruncateFrom(0, "test", false); err == nil {
		t.Fatal("operator=false must fail")
	}
	if _, err := s.RepairTruncateFrom(999999, "beyond", true); err == nil {
		t.Fatal("seq beyond tail must fail")
	}
	if _, err := s.Get(rec.ID); err != nil {
		t.Fatalf("gated repair mutated the log: %v", err)
	}
}
