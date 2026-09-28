package telemetry

import (
	"testing"
)

func TestTelemetry_NilSafe(t *testing.T) {
	var r *Registry
	r.Inc(OpEmit)
	r.Add(OpRecall, 3)
	r.Observe(OpRecall+LatencySuffix, 0.01)
	if r.Get(OpEmit) != 0 {
		t.Fatal("nil registry must be silent")
	}
}

func TestTelemetry_Counts(t *testing.T) {
	r := New()
	r.Inc(OpEmit)
	r.Inc(OpEmit)
	r.Add(VerdictPrefix+"INSERT", 2)
	r.Observe(OpRecall+LatencySuffix, 0.5)
	if r.Get(OpEmit) != 2 {
		t.Fatalf("emit = %d, want 2", r.Get(OpEmit))
	}
	if r.Get(VerdictPrefix+"INSERT") != 2 {
		t.Fatal("verdict count missing")
	}
	if r.Get(OpRecall+LatencySuffix+"_count") != 1 {
		t.Fatal("latency count missing")
	}
	if Logger() == nil {
		t.Fatal("logger must be non-nil")
	}
}
