package index

import (
	"errors"
	"testing"

	"engram/internal/processing"
	"engram/internal/rpc"
	"engram/internal/truth"
)

func mustAppend(t *testing.T, s *truth.Store, sub, pred, obj string) rpc.Record {
	t.Helper()
	rec, err := s.Append(rpc.InputRecord{Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 1, To: 5},
		Provenance: rpc.Provenance{Source: "s", Session: "s1"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.8, Payload: map[string]string{}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return rec
}

// TestRebuild_SnapshotBoundary proves replay is exactly-once: every
// live version present once, superseded (TOMBSTONED) versions absent,
// and post-rebuild writes apply directly without duplication.
func TestRebuild_SnapshotBoundary(t *testing.T) {
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	a := mustAppend(t, s, "pump", "runs", "yes")
	b := mustAppend(t, s, "pump", "runs", "no")
	mustAppend(t, s, "acme", "employs", "bob")
	// Supersede a: old TOMBSTONED version must not index, new must.
	a2, err := s.Supersede(a.ID, rpc.InputRecord{Subject: "pump", Predicate: "runs", Object: "yes",
		ValidTime:  rpc.Interval{From: 1, To: 9},
		Provenance: rpc.Provenance{Source: "s", Session: "s2"},
		ACL:        rpc.ACLUser, BaseConfidence: 0.8, Payload: map[string]string{}})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	x, err := Rebuild(s, nil)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idSet(x.Exact("pump", "runs"))
	if len(got) != 2 || !got[b.ID] || !got[a2.ID] {
		t.Fatalf("want {new-a b} exactly once, got %v", got)
	}
	if _, ok := x.ByID(a.ID); ok {
		t.Fatalf("superseded version %s must not index", a.ID)
	}
	if x.Len() != 3 {
		t.Fatalf("want 3 live versions indexed, got %d (%v)", x.Len(), x.IDs())
	}
	// Live write past the boundary applies directly, exactly once.
	tail, _ := s.TailOffset()
	c := mustAppend(t, s, "valve", "runs", "no")
	if err := s.Scan(tail, func(ev truth.Event) error { return x.Apply(ev, nil) }); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, ok := x.ByID(c.ID); !ok {
		t.Fatal("post-boundary write must apply to the serving index")
	}
	if n := len(x.Exact("valve", "runs")); n != 1 {
		t.Fatalf("post-boundary write must appear exactly once, got %d", n)
	}
}

// TestRebuild_StagedExcluded proves Option A at the build boundary:
// quarantined IDs never enter the index, so recall can never serve
// them — a missing entry leaks nothing.
func TestRebuild_StagedExcluded(t *testing.T) {
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	gossip := mustAppend(t, s, "acme", "ceo", "fired")
	real := mustAppend(t, s, "acme", "ceo", "stays")
	x, err := Rebuild(s, map[string]bool{gossip.ID: true})
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, ok := x.ByID(gossip.ID); ok {
		t.Fatal("quarantined record must not index")
	}
	if _, ok := x.ByID(real.ID); !ok {
		t.Fatal("live record must index")
	}
	if got := x.Exact("acme", "ceo"); len(got) != 1 || got[0].ID != real.ID {
		t.Fatalf("want only the live claim, got %v", got)
	}
}

// TestRebuild_Conformance replays gate-shaped fixtures end to end —
// store → rebuild → index lookup vs P2 Match over the same content —
// and requires identical candidacy. This is the P3 conformance exit.
func TestRebuild_Conformance(t *testing.T) {
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	mustAppend(t, s, "Acme Corp", "employs", "yes")
	mustAppend(t, s, "pump", "runs", "no")
	x, err := Rebuild(s, nil)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// Collect stored content for the gate-side comparison.
	var cands []rpc.Record
	if err := s.Scan(0, func(ev truth.Event) error {
		if ev.Kind == truth.EventRecord && ev.Record.Status == rpc.StatusActive {
			cands = append(cands, ev.Record)
		}
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, q := range []struct{ sub, pred, obj string }{
		{"Acme", "employs", "yes"},
		{"pump", "runs", "yes"},
	} {
		in := iin(q.sub, q.pred, q.obj)
		mExact, mFuzzy := processing.Match(cands, nil, in)
		xExact := x.Exact(q.sub, q.pred)
		if len(mExact) != len(xExact) {
			t.Fatalf("(%q,%q): gate exact %d vs index %d", q.sub, q.pred, len(mExact), len(xExact))
		}
		for _, r := range mExact {
			if !idSet(xExact)[r.ID] {
				t.Fatalf("(%q,%q): gate sees %s, index does not", q.sub, q.pred, r.ID)
			}
		}
		xFuzzy := x.Fuzzy(q.sub, q.pred)
		if len(mFuzzy) != len(xFuzzy) {
			t.Fatalf("(%q,%q): gate fuzzy %d vs index %d", q.sub, q.pred, len(mFuzzy), len(xFuzzy))
		}
	}
}

// TestApply_Idempotent (H2) proves double-delivery collapses: the
// same version applied twice indexes exactly once — versions are
// immutable, so a repeated ID is always a replay duplicate.
func TestApply_Idempotent(t *testing.T) {
	var x Index
	rec := irec("dup", "pump", "runs", "yes")
	ev := truth.Event{Offset: 7, Kind: truth.EventRecord, Record: rec}
	if err := x.Apply(ev, nil); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := x.Apply(ev, nil); err != nil {
		t.Fatalf("second apply must not error, got %v", err)
	}
	if got := x.Exact("pump", "runs"); len(got) != 1 {
		t.Fatalf("want exactly one posting after double-apply, got %d", len(got))
	}
	if x.Len() != 1 {
		t.Fatalf("want Len 1 after double-apply, got %d", x.Len())
	}
	// Feedback replays are no-ops at this layer, never errors.
	fb := truth.Event{Offset: 8, Kind: truth.EventFeedback,
		Feedback: rpc.FeedbackEvent{RecordID: "dup", Kind: rpc.FeedbackStrength, Source: "s", Session: "s2", Tick: 1}}
	if err := x.Apply(fb, nil); err != nil {
		t.Fatalf("feedback apply: %v", err)
	}
	if x.Len() != 1 {
		t.Fatalf("feedback must not index, Len %d", x.Len())
	}
}

// TestApply_RejectsUnknownKind proves the fail-closed branch: a
// postings entry must never point at an uninterpretable event.
func TestApply_RejectsUnknownKind(t *testing.T) {
	var x Index
	err := x.Apply(truth.Event{Offset: 9, Kind: truth.EventKind("bogus")}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeCorruptIndex {
		t.Fatalf("want coded CORRUPT_INDEX, got %v", err)
	}
	if x.Len() != 0 {
		t.Fatal("rejected event must leave no trace")
	}
}
