// Feedback-by-ID secondary proof (P6 exit: serving ≡ rebuild, FR17).
package truth

import (
	"path/filepath"
	"testing"

	"engram/internal/rpc"
)

// TestFeedbackFor_MatchesScan pins the secondary against the log: the
// Scan oracle and FeedbackFor must agree per record, in commit order.
func TestFeedbackFor_MatchesScan(t *testing.T) {
	s := openTest(t)
	r1, err := s.Append(testInput("a", "p", "1"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Append(testInput("b", "q", "2"))
	if err != nil {
		t.Fatal(err)
	}
	evs := []rpc.FeedbackEvent{
		{RecordID: r1.ID, Kind: rpc.FeedbackAttestConfirm, Source: "u", Session: "s2"},
		{RecordID: r1.ID, Kind: rpc.FeedbackStrength, Source: "u", Session: "s3"},
		{RecordID: r2.ID, Kind: rpc.FeedbackAttestDisconfirm, Source: "v", Session: "s4"},
	}
	for _, ev := range evs {
		if err := s.AppendFeedback(ev); err != nil {
			t.Fatal(err)
		}
	}
	// Scan oracle.
	oracle := make(map[string][]rpc.FeedbackEvent)
	if err := s.Scan(0, func(e Event) error {
		if e.Kind != EventFeedback {
			return nil
		}
		oracle[e.Feedback.RecordID] = append(oracle[e.Feedback.RecordID], e.Feedback)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{r1.ID, r2.ID} {
		got := s.FeedbackFor(id)
		want := oracle[id]
		if len(got) != len(want) {
			t.Fatalf("id %s: secondary %d events, log %d", id, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("id %s event %d mismatch: %+v vs %+v", id, i, got[i], want[i])
			}
		}
	}
	if len(s.FeedbackFor("missing")) != 0 {
		t.Fatal("unknown id must yield nil")
	}
}

// TestFeedbackFor_ServingEqualsRebuild reopens the store: boot ≡
// rebuild must reproduce the secondary bit-identically (DESIGN §2).
func TestFeedbackFor_ServingEqualsRebuild(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Clean(dir))
	if err != nil {
		t.Fatal(err)
	}
	r1, err := s.Append(testInput("a", "p", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendFeedback(rpc.FeedbackEvent{RecordID: r1.ID, Kind: rpc.FeedbackAttestConfirm, Source: "u", Session: "s2"}); err != nil {
		t.Fatal(err)
	}
	before := s.FeedbackFor(r1.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(filepath.Clean(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	after := s2.FeedbackFor(r1.ID)
	if len(before) != 1 || len(after) != 1 || before[0] != after[0] {
		t.Fatalf("rebuild mismatch: %+v vs %+v", before, after)
	}
}

// TestFeedbackFor_TruncateRebuilds pins amputation hygiene: feedback at
// or past the truncate seq vanishes from the secondary with the log.
func TestFeedbackFor_TruncateRebuilds(t *testing.T) {
	s := openTest(t)
	r1, err := s.Append(testInput("a", "p", "1"))
	if err != nil {
		t.Fatal(err)
	}
	tail, err := s.TailOffset()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendFeedback(rpc.FeedbackEvent{RecordID: r1.ID, Kind: rpc.FeedbackStrength, Source: "u", Session: "s2"}); err != nil {
		t.Fatal(err)
	}
	if len(s.FeedbackFor(r1.ID)) != 1 {
		t.Fatal("expected one feedback event")
	}
	if _, err := s.RepairTruncateFrom(tail, "test", true); err != nil {
		t.Fatal(err)
	}
	if len(s.FeedbackFor(r1.ID)) != 0 {
		t.Fatal("truncated feedback must vanish from the secondary")
	}
}
