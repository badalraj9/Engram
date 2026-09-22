package processing

import (
	"testing"

	"engram/internal/rpc"
)

func matchRecord(id, sub, pred string, status rpc.Status, typ rpc.RecordType) rpc.Record {
	return rpc.Record{ID: id, Subject: sub, Predicate: pred, Object: "o",
		Status: status, Type: typ}
}

func matchInput(sub, pred string) rpc.InputRecord {
	return rpc.InputRecord{
		Subject: sub, Predicate: pred, Object: "o",
		ValidTime:  rpc.Interval{From: 1000, To: 2000},
		Provenance: rpc.Provenance{Source: "s", Session: "s1"},
		ACL:        rpc.ACLUser,
		Payload:    map[string]string{},
	}
}

// TestMatcher_FR4_ExactKey proves normalized exact-key matching on
// (subject, predicate): case/whitespace variants hit, key framing keeps
// ("a","bc") distinct from ("ab","c").
func TestMatcher_FR4_ExactKey(t *testing.T) {
	cands := []rpc.Record{
		matchRecord("e1", "Acme", "employs", rpc.StatusActive, rpc.RecordEpisodic),
	}
	exact, _ := Match(cands, nil, matchInput("  acme ", "EMPLOYS"))
	if len(exact) != 1 || exact[0].ID != "e1" {
		t.Fatalf("normalized exact-key must hit, got %v", exact)
	}
	if Key("a", "bc") == Key("ab", "c") {
		t.Fatal("framing must keep (a,bc) distinct from (ab,c)")
	}
	exact, fuzzy := Match(cands, nil, matchInput("Acme", "owns"))
	if len(exact) != 0 || len(fuzzy) != 0 {
		t.Fatal("different predicate must not match at all")
	}
}

// TestMatcher_FR4_MergeBandAnchors pins the floor's derivation window
// as computed values: the doc's paraphrase pair merges, the distinct
// pairs don't. If the algorithm changes, these fail loudly instead of
// silently shifting the band.
func TestMatcher_FR4_MergeBandAnchors(t *testing.T) {
	if got := TrigramJaccard("Acme", "Acme Corp"); got < FuzzyMergeFloor {
		t.Fatalf("Acme/Acme Corp = %v, must clear floor %v", got, FuzzyMergeFloor)
	}
	pairs := [][2]string{
		{"Raj", "Rahul"},
		{"apple", "application"},
	}
	for _, p := range pairs {
		if got := TrigramJaccard(p[0], p[1]); got >= FuzzyMergeFloor {
			t.Fatalf("%s/%s = %v, must stay below floor %v", p[0], p[1], got, FuzzyMergeFloor)
		}
	}
	if got := TrigramJaccard("Acme", "Acme"); got != 1 {
		t.Fatalf("identical strings must score 1, got %v", got)
	}
	cands := []rpc.Record{
		matchRecord("f1", "Acme Corp", "employs", rpc.StatusActive, rpc.RecordEpisodic),
	}
	exact, fuzzy := Match(cands, nil, matchInput("Acme", "employs"))
	if len(exact) != 0 {
		t.Fatalf("paraphrase must not be exact, got %v", exact)
	}
	if len(fuzzy) != 1 || fuzzy[0].ID != "f1" {
		t.Fatalf("paraphrase must land in fuzzy band, got %v", fuzzy)
	}
}

// TestMatcher_STAGEDInvisible proves a quarantined record never
// contaminates a gate decision: present in the store, absent to the
// matcher — the gate behaves as if it isn't there.
func TestMatcher_STAGEDInvisible(t *testing.T) {
	cands := []rpc.Record{
		matchRecord("st", "Acme", "employs", rpc.StatusActive, rpc.RecordEpisodic),
	}
	staged := map[string]bool{"st": true}
	if exact, fuzzy := Match(cands, staged, matchInput("Acme", "employs")); len(exact)+len(fuzzy) != 0 {
		t.Fatalf("staged candidate must be invisible, got %v %v", exact, fuzzy)
	}
	if exact, _ := Match(cands, nil, matchInput("Acme", "employs")); len(exact) != 1 {
		t.Fatal("unstaged control must hit")
	}
}

// TestMatcher_NonCandidates proves TOMBSTONED (closed history) and
// SUMMARY/SEMANTIC (derived-inert consumption end) never compete,
// while BOTH stays matchable for conflict detection.
func TestMatcher_NonCandidates(t *testing.T) {
	cands := []rpc.Record{
		matchRecord("tomb", "Acme", "employs", rpc.StatusTombstoned, rpc.RecordEpisodic),
		matchRecord("sum", "Acme", "employs", rpc.StatusActive, rpc.RecordSummary),
		matchRecord("sem", "Acme", "employs", rpc.StatusActive, rpc.RecordSemantic),
		matchRecord("both", "Acme", "employs", rpc.StatusBoth, rpc.RecordEpisodic),
	}
	exact, _ := Match(cands, nil, matchInput("Acme", "employs"))
	if len(exact) != 1 || exact[0].ID != "both" {
		t.Fatalf("only the BOTH candidate must match, got %v", exact)
	}
}

// BenchmarkMatch_Mixed200 bounds the matcher's linear pass for the
// performance report.
func BenchmarkMatch_Mixed200(b *testing.B) {
	var cands []rpc.Record
	for i := 0; i < 200; i++ {
		cands = append(cands, matchRecord(
			string(rune('a'+i%26))+string(rune('0'+i%10)),
			"subject", "pred", rpc.StatusActive, rpc.RecordEpisodic))
	}
	in := matchInput("subject", "pred")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Match(cands, nil, in)
	}
}
