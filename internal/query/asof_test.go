package query

import (
	"testing"

	"engram/internal/intake"
	"engram/internal/processing"
	"engram/internal/rpc"
)

// M6 bi-temporal property suite (SRS §7): the layer where the one
// real bug in the whole research arc already occurred (historical
// queries filtering on current-belief status). Each test names the
// M6 property it enforces.

// TestM6_AsOfImmuneToStatus pins: as_of(valid) results are
// unaffected by a record's current-belief status. Supersede closes
// the old version (TOMBSTONED) without touching its valid-time, so
// history still returns it.
func TestM6_AsOfImmuneToStatus(t *testing.T) {
	e, s, _ := qsetup(t)
	first, err := e.Emit(qin("db", "runs", "Badger", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, _, err := e.Correct(intake.CorrectHint{Subject: "db", Predicate: "runs", Object: "Badger"},
		func() rpc.InputRecord {
			c := qin("db", "runs", "Pebble", "s", "s2", rpc.ACLUser)
			return c
		}()); err != nil {
		t.Fatalf("correct: %v", err)
	}
	got, err := AsOf(s, "db", "runs", 5, rpc.AxisValid, rpc.ACLUser)
	if err != nil {
		t.Fatalf("as_of: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history must hold both versions regardless of status, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.Object] = true
	}
	if !seen["Badger"] || !seen["Pebble"] {
		t.Fatalf("both claims must be visible historically, got %v", got)
	}
	_ = first
}

// TestM6_SupersedeNeverMutates pins FR17 at the M6 gate: revision
// is close-then-append — the original ID still resolves to its
// closed version, and the new version carries a distinct ID.
func TestM6_SupersedeNeverMutates(t *testing.T) {
	e, s, _ := qsetup(t)
	first, err := e.Emit(qin("db", "runs", "Badger", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	rec, v, err := e.Correct(intake.CorrectHint{Subject: "db", Predicate: "runs", Object: "Badger"},
		qin("db", "runs", "Pebble", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if v != rpc.VerdictUpdate || rec.ID == first.ID {
		t.Fatalf("revision must append distinctly, got %v %s", v, rec.ID)
	}
	old, err := s.Get(first.ID)
	if err != nil {
		t.Fatalf("original must remain addressable, got %v", err)
	}
	if old.Object != "Badger" || old.Status != rpc.StatusTombstoned {
		t.Fatalf("original version must be closed-but-intact, got %+v", old)
	}
}

// TestM6_BothPersistsUntilResolve pins: a Both-state survives reads
// and resolves only through resolve — never silently collapses.
func TestM6_BothPersistsUntilResolve(t *testing.T) {
	e, s, idx := qsetup(t)
	sys := qin("pump", "runs", "yes", "ops", "s0", rpc.ACLUser)
	sys.ACL = rpc.ACLSystem
	if _, err := e.Emit(sys); err != nil {
		t.Fatalf("emit sys: %v", err)
	}
	// Subordinate conflicting write: no eligible claimant → BOTH.
	sub := qin("pump", "runs", "no", "s", "s1", rpc.ACLUser)
	conflict, err := e.Emit(sub)
	if err != nil {
		t.Fatalf("emit sub: %v", err)
	}
	if conflict.Verdict != rpc.VerdictInsert {
		t.Fatalf("tier-blocked conflict must store as flagged INSERT, got %+v", conflict)
	}
	flagged, err := s.Get(conflict.ID)
	if err != nil || flagged.Status != rpc.StatusBoth {
		t.Fatalf("incoming must be BOTH, got %+v err=%v", flagged, err)
	}
	// Recall still surfaces the flagged pair to a privileged caller.
	res, err := Recall(idx, s, qspec("pump", "runs", rpc.ACLSystem))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if res.Abstained {
		t.Fatal("Both-state must persist visibly, not collapse to silence")
	}
	// Resolve keep-a collapses explicitly (via the addressable
	// flagged pair — never the closed pre-flag versions).
	if conflict.FlaggedID == "" {
		t.Fatal("flagged target version must be addressable")
	}
	newID, err := e.Resolve(conflict.FlaggedID, conflict.ID, rpc.ResolveKeepA,
		rpc.Provenance{Source: "ops", Session: "s9"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if newID == "" {
		t.Fatal("resolve must return the survivor version")
	}
	survivor, err := s.Get(newID)
	if err != nil || survivor.Status != rpc.StatusActive || survivor.Object != "yes" {
		t.Fatalf("keep-a must survive the system claim ACTIVE, got %+v err=%v", survivor, err)
	}
}

// TestM6_WitnessIsClusterCount pins FR9's ordering at the M6 gate:
// noisy-OR folds one value per session-cluster, never per raw
// record — three same-session restatements count once.
func TestM6_WitnessIsClusterCount(t *testing.T) {
	mk := func(src, ses string) rpc.Record {
		return rpc.Record{ID: src + ses, Provenance: rpc.Provenance{Source: src, Session: ses}, BaseConfidence: 0.5}
	}
	recs := []rpc.Record{mk("s", "s1"), mk("s", "s1"), mk("s", "s1"), mk("s", "s2")}
	clusters := processing.Cluster(recs)
	if len(clusters) != 2 {
		t.Fatalf("three same-session restatements must cluster once: %d clusters", len(clusters))
	}
	var witnesses []float64
	for _, c := range clusters {
		witnesses = append(witnesses, c[0].BaseConfidence)
	}
	if got := processing.NoisyOR(witnesses...); got != processing.NoisyOR(0.5, 0.5) {
		t.Fatalf("fold must see 2 witnesses, got %v", got)
	}
}

// TestAsof_TransactionAxis pins the second bi-temporal axis: txn
// time tracks commit order (server-assigned), and unknown axes fail
// closed instead of scanning something else.
func TestAsof_TransactionAxis(t *testing.T) {
	e, s, _ := qsetup(t)
	if _, err := e.Emit(qin("db", "runs", "Badger", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	got, err := AsOf(s, "db", "runs", 1, rpc.AxisTransaction, rpc.ACLUser)
	if err != nil {
		t.Fatalf("as_of txn: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("txn axis must find the version live at seq 1, got %d", len(got))
	}
	if _, err := AsOf(s, "db", "runs", 1, rpc.Axis("sideways"), rpc.ACLUser); err == nil {
		t.Fatal("unknown axis must fail closed")
	}
	if _, err := AsOf(s, "db", "runs", 1, rpc.AxisValid, rpc.AclTier("bogus")); err == nil {
		t.Fatal("unknown caller must fail closed")
	}
}
