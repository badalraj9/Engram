package index

import (
	"testing"
)

// TestSetOps_Chain proves the FR8 chaining primitives: AND across
// token postings, union accumulation, deterministic output — both
// sides of empty included.
func TestSetOps_Chain(t *testing.T) {
	a := Set{"r1": {}, "r2": {}, "r3": {}}
	b := Set{"r2": {}, "r3": {}, "r4": {}}
	a.Intersect(b)
	if len(a) != 2 || !a.Contains("r2") || !a.Contains("r3") {
		t.Fatalf("want {r2 r3}, got %v", a.Sorted())
	}
	a.Intersect(Set{})
	if len(a) != 0 {
		t.Fatalf("intersect with empty must empty, got %v", a.Sorted())
	}
	var nilSet Set
	nilSet.Intersect(b) // must not panic
	if len(nilSet) != 0 {
		t.Fatalf("nil intersect must stay empty, got %v", nilSet.Sorted())
	}
	u := Set{"r1": {}}
	u.Union(b)
	if len(u) != 4 {
		t.Fatalf("want 4 after union, got %v", u.Sorted())
	}
	u.Union(nil)
	if len(u) != 4 {
		t.Fatalf("nil union must be a no-op, got %v", u.Sorted())
	}
	if got := b.Sorted(); len(got) != 3 || got[0] != "r2" || got[2] != "r4" {
		t.Fatalf("sorted output must be deterministic, got %v", got)
	}
	if c := Set(nil).Clone(); c != nil {
		t.Fatal("nil clone must stay nil")
	}
}

// BenchmarkExact200 bounds the exact path for the performance report.
func BenchmarkExact200(b *testing.B) {
	var x Index
	for i := 0; i < 200; i++ {
		x.Add(irec(string(rune('a'+i%26))+string(rune('0'+i%10)),
			"subject", "pred", "obj"), nil)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x.Exact("subject", "pred")
	}
}
