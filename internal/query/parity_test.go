package query

import (
	"fmt"
	"testing"

	"engram/internal/index"
	"engram/internal/processing"
	"engram/internal/rpc"
)

func qrec(id, sub, pred, obj string, status rpc.Status, typ rpc.RecordType) rpc.Record {
	return rpc.Record{ID: id, Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 1, To: 10},
		Txn:        rpc.Interval{From: 1, To: rpc.OpenEnd},
		Provenance: rpc.Provenance{Source: "s", Session: "s1"},
		ACL:        rpc.ACLUser, Status: status, Type: typ, BaseConfidence: 0.5,
		Payload: map[string]string{}}
}

// TestRecall_GateParity pins the P4 conformance leg (mirrors P3's
// index/gate parity): everything the gate matcher sees for an input
// must be present in recall's candidate pool — recall may add via
// token-union (superset), but it must never starve the fold by
// dropping a gate candidate. Staged/derived/tombstoned stay out of
// both by construction.
func TestRecall_GateParity(t *testing.T) {
	cands := []rpc.Record{
		qrec("live-a", "Acme", "employs", "bob", rpc.StatusActive, rpc.RecordEpisodic),
		qrec("live-b", "Acme", "employs", "alice", rpc.StatusActive, rpc.RecordEpisodic),
		qrec("para", "Acme Corp", "employs", "bob", rpc.StatusActive, rpc.RecordEpisodic),
		qrec("flagged", "Acme", "employs", "zed", rpc.StatusBoth, rpc.RecordEpisodic),
		qrec("staged", "Acme", "employs", "mallory", rpc.StatusActive, rpc.RecordEpisodic),
		qrec("derived", "Acme", "employs", "bob", rpc.StatusActive, rpc.RecordSummary),
		qrec("dead", "Acme", "employs", "bob", rpc.StatusTombstoned, rpc.RecordEpisodic),
		qrec("other-pred", "Acme", "owns", "valve", rpc.StatusActive, rpc.RecordEpisodic),
	}
	staged := map[string]bool{"staged": true}
	var idx index.Index
	for _, r := range cands {
		idx.Add(r, staged)
	}
	in := rpc.InputRecord{Subject: "Acme", Predicate: "employs", Object: "bob",
		ValidTime:  rpc.Interval{From: 1, To: 2},
		Provenance: rpc.Provenance{Source: "s", Session: "s2"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.5, Payload: map[string]string{}}
	exact, fuzzy := processing.Match(cands, staged, in)
	if len(exact) == 0 {
		t.Fatal("fixture must yield exact candidates")
	}
	pool := poolCandidates(&idx, qspec("Acme", "employs", rpc.ACLUser))
	have := make(map[string]bool)
	for _, r := range pool {
		have[r.ID] = true
	}
	for _, r := range append(append([]*rpc.Record{}, exact...), fuzzy...) {
		if !have[r.ID] {
			t.Fatalf("gate candidate %s missing from recall pool", r.ID)
		}
	}
	for _, absent := range []string{"staged", "derived", "dead"} {
		if have[absent] {
			t.Fatalf("%s must be absent from both gate and pool", absent)
		}
	}
}

// TestRecall_PoolSelective locks AND-semantics for the token lane:
// 1000 records sharing one predicate, one selective query — the
// pool must stay near the exact set, never near the table. The
// per-token-union shape stalled the 100K bench (ubiquitous token
// materialized everything); this test fails that shape back.
func TestRecall_PoolSelective(t *testing.T) {
	var idx index.Index
	for i := 0; i < 999; i++ {
		idx.Add(qrec(fmt.Sprintf("filler-%04d", i), "filler", "pred", "o",
			rpc.StatusActive, rpc.RecordEpisodic), nil)
	}
	// Trigram-disjoint target: shares no paraphrase band with the
	// fillers, so the pool must be exactly the AND lane + exact lane.
	idx.Add(qrec("target", "quorum zed", "pred", "o", rpc.StatusActive, rpc.RecordEpisodic), nil)
	pool := poolCandidates(&idx, qspec("quorum zed", "pred", rpc.ACLUser))
	if len(pool) > 5 {
		t.Fatalf("selective query must pool near the key, got %d", len(pool))
	}
	found := false
	for _, r := range pool {
		if r.ID == "target" {
			found = true
		}
	}
	if !found {
		t.Fatal("selectivity must never drop the exact hit")
	}
}
