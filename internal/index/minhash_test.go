package index

import (
	"testing"

	"engram/internal/processing"
	"engram/internal/rpc"
)

// TestFuzzy_MergeBandAnchors replays P2's behavioral anchors through
// the index: the doc's paraphrase pair generates, the distinct pairs
// don't, and exact-key hits stay in the exact lane (never fuzzy).
func TestFuzzy_MergeBandAnchors(t *testing.T) {
	cands := []rpc.Record{
		irec("f1", "Acme Corp", "employs", "yes"),
		irec("e1", "Acme", "employs", "yes"),
		irec("raj", "Rahul", "employs", "yes"),
		irec("app", "application", "employs", "yes"),
	}
	var x Index
	for _, r := range cands {
		x.Add(r, nil)
	}
	// Paraphrase generates — and agrees with P2's band exactly.
	got := idSet(x.Fuzzy("Acme", "employs"))
	mExact, mFuzzy := processing.Match(cands, nil, iin("Acme", "employs", "yes"))
	want := idSetP(mFuzzy)
	if len(got) != len(want) {
		t.Fatalf("index fuzzy %v vs gate fuzzy %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("index fuzzy %v vs gate fuzzy %v", got, want)
		}
	}
	if !got["f1"] {
		t.Fatalf("Acme/Acme Corp must generate, got %v", got)
	}
	if got["e1"] {
		t.Fatalf("exact-key hit must stay out of fuzzy, got %v", got)
	}
	_ = mExact
	// Distinct pairs never generate — through either path.
	for _, q := range [][2]string{{"Raj", "employs"}, {"apple", "employs"}} {
		if got := x.Fuzzy(q[0], q[1]); len(got) != 0 {
			t.Fatalf("Fuzzy(%q) must be empty, got %v", q[0], got)
		}
		if _, mf := processing.Match(cands, nil, iin(q[0], q[1], "yes")); len(mf) != 0 {
			t.Fatalf("gate fuzzy for %q must be empty, got %v", q[0], mf)
		}
	}
	// Different predicate: no generation even on identical subject.
	if got := x.Fuzzy("Acme Corp", "owns"); len(got) != 0 {
		t.Fatalf("cross-predicate fuzzy must be empty, got %v", got)
	}
}

// BenchmarkFuzzy200 bounds the paraphrase path for the report.
func BenchmarkFuzzy200(b *testing.B) {
	var x Index
	for i := 0; i < 200; i++ {
		x.Add(irec(string(rune('a'+i%26))+string(rune('0'+i%10)),
			"subject", "pred", "obj"), nil)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x.Fuzzy("subject", "pred")
	}
}

// TestChained_FR8 proves multi-hop without a graph: hop-1 exact set
// fans out through hop-2 exact sets, deduplicated, order-stable.
func TestChained_FR8(t *testing.T) {
	var x Index
	for _, r := range []rpc.Record{
		irec("w1", "alice", "works-at", "acme"),
		irec("w2", "bob", "works-at", "acme"),
		irec("hq", "acme", "hq-in", "berlin"),
		irec("other", "globex", "hq-in", "paris"),
	} {
		x.Add(r, nil)
	}
	got := x.Chained("alice", "works-at", "hq-in")
	if len(got) != 1 || got[0].ID != "hq" {
		t.Fatalf("want [hq] via acme, got %v", got)
	}
	if got := x.Chained("nobody", "works-at", "hq-in"); len(got) != 0 {
		t.Fatalf("dead hop-1 must yield nothing, got %v", got)
	}
	if got := x.Chained("alice", "works-at", "nope"); len(got) != 0 {
		t.Fatalf("dead hop-2 must yield nothing, got %v", got)
	}
}
