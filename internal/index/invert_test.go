package index

import (
	"testing"

	"engram/internal/processing"
	"engram/internal/rpc"
)

func irec(id, sub, pred, obj string) rpc.Record {
	return rpc.Record{ID: id, Subject: sub, Predicate: pred, Object: obj,
		ValidTime: rpc.Interval{From: 1, To: 5},
		Txn:       rpc.Interval{From: 0, To: rpc.OpenEnd},
		Status:    rpc.StatusActive, Type: rpc.RecordEpisodic,
		Provenance: rpc.Provenance{Source: "s", Session: "s1"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.8, Payload: map[string]string{}}
}

func iin(sub, pred, obj string) rpc.InputRecord {
	return rpc.InputRecord{Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 1, To: 5},
		Provenance: rpc.Provenance{Source: "s", Session: "s1"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.8, Payload: map[string]string{}}
}

func idSet(rs []rpc.Record) map[string]bool {
	m := make(map[string]bool)
	for _, r := range rs {
		m[r.ID] = true
	}
	return m
}

func idSetP(rs []*rpc.Record) map[string]bool {
	m := make(map[string]bool)
	for _, r := range rs {
		m[r.ID] = true
	}
	return m
}

// TestIndex_FullSets proves the gate-starvation rule by construction:
// every live version under a key is returned, so the max-severity
// fold always sees conflicts and corroborations alike.
func TestIndex_FullSets(t *testing.T) {
	var x Index
	live := []rpc.Record{
		irec("a", "pump", "runs", "yes"),
		irec("b", "pump", "runs", "no"),
		irec("c", "pump", "runs", "maybe"),
	}
	for _, r := range live {
		if !x.Add(r, nil) {
			t.Fatalf("live record %s refused", r.ID)
		}
	}
	got := idSet(x.Exact("pump", "runs"))
	if len(got) != 3 || !got["a"] || !got["b"] || !got["c"] {
		t.Fatalf("want full set {a b c}, got %v", got)
	}
	// Liveness filter parity: staged, derived, TOMBSTONED excluded;
	// BOTH stays (flagged pairs must be visible to conflict detection).
	stagedRec := irec("st", "pump", "runs", "yes")
	if x.Add(stagedRec, map[string]bool{"st": true}) {
		t.Fatal("staged record must be excluded at build")
	}
	der := irec("der", "pump", "runs", "yes")
	der.Type = rpc.RecordSummary
	if x.Add(der, nil) {
		t.Fatal("derived record must be excluded at build")
	}
	tomb := irec("tomb", "pump", "runs", "yes")
	tomb.Status = rpc.StatusTombstoned
	if x.Add(tomb, nil) {
		t.Fatal("tombstoned record must be excluded at build")
	}
	both := irec("both", "pump", "runs", "no")
	both.Status = rpc.StatusBoth
	if !x.Add(both, nil) {
		t.Fatal("BOTH record must be indexed")
	}
	if got := idSet(x.Exact("pump", "runs")); len(got) != 4 || !got["both"] {
		t.Fatalf("want {a b c both}, got %v", got)
	}
	if _, ok := x.ByID("st"); ok {
		t.Fatal("staged ID must not resolve")
	}
}

// TestIndex_NormalizeConform proves gate/index convergence: the index
// returns exactly what P2's Match sees, over the same fixtures —
// case/whitespace variants hit, framing keeps ("a","bc") off
// ("ab","c").
func TestIndex_NormalizeConform(t *testing.T) {
	cands := []rpc.Record{
		irec("e1", "Acme", "employs", "yes"),
		irec("f1", "Acme Corp", "employs", "yes"),
		irec("ab", "ab", "c", "x"),
	}
	var x Index
	for _, r := range cands {
		x.Add(r, nil)
	}
	for _, q := range []struct{ sub, pred string }{
		{"  acme ", "EMPLOYS"},
		{"ACME", "employs"},
	} {
		in := iin(q.sub, q.pred, "yes")
		mExact, _ := processing.Match(cands, nil, in)
		xExact := x.Exact(q.sub, q.pred)
		if idSetP(mExact)["e1"] != idSet(xExact)["e1"] || len(mExact) != len(xExact) {
			t.Fatalf("index/gate diverge on (%q,%q): match=%v index=%v",
				q.sub, q.pred, idSetP(mExact), idSet(xExact))
		}
	}
	if got := x.Exact("a", "bc"); len(got) != 0 {
		t.Fatalf("framing must keep (a,bc) empty, got %v", got)
	}
	if got := x.Exact("ab", "c"); len(got) != 1 {
		t.Fatalf("want the (ab,c) record, got %v", got)
	}
}

// TestTerms_TokenAND proves the token path P4 recall will rank on:
// AND across tokens, normalized lookup, fail-closed on unindexable
// input (multi-word token, absent token, empty query).
func TestTerms_TokenAND(t *testing.T) {
	var x Index
	for _, r := range []rpc.Record{
		irec("r1", "Acme Corp", "employs", "bob"),
		irec("r2", "Acme Corp", "owns", "valve"),
		irec("r3", "Globex", "employs", "alice"),
	} {
		x.Add(r, nil)
	}
	if got := x.Terms("ACME").Sorted(); len(got) != 2 || got[0] != "r1" || got[1] != "r2" {
		t.Fatalf("want [r1 r2] case-insensitive, got %v", got)
	}
	if got := x.Terms("acme", "employs").Sorted(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("want [r1] for the AND, got %v", got)
	}
	if got := x.Terms("acme", "nobody"); got != nil {
		t.Fatalf("absent token must yield nil, got %v", got.Sorted())
	}
	if got := x.Terms("two words"); got != nil {
		t.Fatal("multi-word token must yield nil, never a guess")
	}
	if got := x.Terms(); got != nil {
		t.Fatal("empty query must yield nil")
	}
	if got := x.Terms("bob").Sorted(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("object tokens must index too, got %v", got)
	}
}

// TestIDs_Deterministic pins sorted-ID output for rebuild comparison.
func TestIDs_Deterministic(t *testing.T) {
	var x Index
	for _, r := range []rpc.Record{irec("b", "s", "p", "o"), irec("a", "s", "p", "o")} {
		x.Add(r, nil)
	}
	if got := x.IDs(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("want [a b] sorted, got %v", got)
	}
}
