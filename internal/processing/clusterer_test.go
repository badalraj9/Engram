package processing

import (
	"testing"

	"engram/internal/rpc"
)

func clusRecord(id, src, ses string) rpc.Record {
	return rpc.Record{ID: id, Subject: "s", Predicate: "p", Object: "o",
		Provenance: rpc.Provenance{Source: src, Session: ses}}
}

// TestClusterer_FR9_Counts proves witnesses are counted per
// session-cluster, not per raw record: 5 records in 2 sessions yield
// exactly 2 witnesses, so same-session restatement cannot inflate
// confidence through raw count.
func TestClusterer_FR9_Counts(t *testing.T) {
	in := []rpc.Record{
		clusRecord("r1", "alice", "s1"),
		clusRecord("r2", "alice", "s1"),
		clusRecord("r3", "alice", "s1"),
		clusRecord("r4", "bob", "s9"),
		clusRecord("r5", "bob", "s9"),
	}
	got := Cluster(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 session-clusters, got %d", len(got))
	}
	if len(got[0]) != 3 || len(got[1]) != 2 {
		t.Fatalf("expected cluster sizes 3+2, got %d+%d", len(got[0]), len(got[1]))
	}
	// Composite identity: same session string across sources stays split.
	split := []rpc.Record{
		clusRecord("a", "alice", "s1"),
		clusRecord("b", "bob", "s1"),
	}
	if got := Cluster(split); len(got) != 2 {
		t.Fatalf("cross-source session collision must not merge, got %d clusters", len(got))
	}
	// Deterministic under input permutation.
	rev := []rpc.Record{in[4], in[3], in[2], in[1], in[0]}
	again := Cluster(rev)
	if len(again) != 2 || len(again[0]) != 3 || len(again[1]) != 2 {
		t.Fatalf("clustering must be order-independent, got %v", again)
	}
	if len(in) != 5 {
		t.Fatal("Cluster mutated its input")
	}
	if Cluster(nil) != nil {
		t.Fatal("empty input must yield nil")
	}
}

// TestClusterer_FR14_Independence pins the promotion-independence rule:
// same source+session is not independent (self-attestation never
// promotes); differing in either axis is.
func TestClusterer_FR14_Independence(t *testing.T) {
	base := rpc.Provenance{Source: "alice", Session: "s1"}
	cases := []struct {
		other rpc.Provenance
		want  bool
	}{
		{base, false},
		{rpc.Provenance{Source: "alice", Session: "s2"}, true},
		{rpc.Provenance{Source: "bob", Session: "s1"}, true},
		{rpc.Provenance{Source: "bob", Session: "s2"}, true},
	}
	for _, c := range cases {
		if got := Independent(base, c.other); got != c.want {
			t.Fatalf("Independent(%v, %v) = %v, want %v", base, c.other, got, c.want)
		}
	}
}
