package intake

import (
	"testing"
	"time"

	"engram/internal/index"
	"engram/internal/rpc"
	"engram/internal/truth"
)

func ein(sub, pred, obj string, src, ses string, acl rpc.AclTier) rpc.InputRecord {
	return rpc.InputRecord{Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 1, To: 2},
		Provenance: rpc.Provenance{Source: src, Session: ses},
		ACL:        acl, BaseConfidence: 0.5, Payload: map[string]string{}}
}

func newEmitter(t *testing.T, perSec int) (*Emitter, *truth.Store, *index.Index) {
	t.Helper()
	s, err := truth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	idx := &index.Index{}
	return NewEmitter(s, idx, perSec), s, idx
}

// TestEmit_InsertAck pins the basic write: INSERT verdict, durable
// ack (readable post-Sync), indexed for recall.
func TestEmit_InsertAck(t *testing.T) {
	e, s, idx := newEmitter(t, 100)
	res, err := e.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if res.Verdict != rpc.VerdictInsert || res.ID == "" {
		t.Fatalf("want INSERT with ID, got %+v", res)
	}
	if _, err := s.Get(res.ID); err != nil {
		t.Fatalf("acked write must be durable, got %v", err)
	}
	if got := idx.Exact("pump", "runs"); len(got) != 1 {
		t.Fatalf("committed version must index, got %d", len(got))
	}
}

// TestEmit_SkipRestage pins ack-without-duplicate and quarantine:
// same-session reassertion SKIPPs; low-tier STAGEs invisibly.
func TestEmit_SkipRestage(t *testing.T) {
	e, s, idx := newEmitter(t, 100)
	first, err := e.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	res, err := e.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.Verdict != rpc.VerdictSkip || res.ID != "" || res.TargetID != first.ID {
		t.Fatalf("same-session retry must SKIP against first, got %+v", res)
	}
	staged, err := e.Emit(ein("valve", "runs", "yes", "low-src", "s9", rpc.ACLLow))
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if staged.Verdict != rpc.VerdictStaged {
		t.Fatalf("low tier must STAGE, got %+v", staged)
	}
	if got := idx.Exact("valve", "runs"); len(got) != 0 {
		t.Fatal("staged record must never index")
	}
	if ok, _ := s.IsStaged(staged.ID); !ok {
		t.Fatal("staged record must hold a marker")
	}
	// Staged content is invisible to the gate: a user write under
	// the same key INSERTs fresh, never merges with quarantine.
	fresh, err := e.Emit(ein("valve", "runs", "yes", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if fresh.Verdict != rpc.VerdictInsert {
		t.Fatalf("staged must not contaminate the fold, got %+v", fresh)
	}
}

// TestEmit_RestrengthenPromotes pins FR14's second-signal leg: a
// cross-session corroboration bumps strength AND peels an
// independent staged version into the index.
func TestEmit_RestrengthenPromotes(t *testing.T) {
	e, s, idx := newEmitter(t, 100)
	if _, err := e.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("live: %v", err)
	}
	staged, err := e.Emit(ein("pump", "runs", "yes", "low-src", "s9", rpc.ACLLow))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	res, err := e.Emit(ein("pump", "runs", "yes", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("corroborate: %v", err)
	}
	if res.Verdict != rpc.VerdictRestrengthen {
		t.Fatalf("cross-session corroboration must RESTRENGTHEN, got %+v", res)
	}
	if ok, _ := s.IsStaged(staged.ID); ok {
		t.Fatal("independent corroboration must promote the staged version")
	}
	if got := idx.Exact("pump", "runs"); len(got) != 2 {
		t.Fatalf("promoted version must index, got %d", len(got))
	}
	// Self-corroboration never promotes: staged (s,s2) shares its
	// composite with the corroborator (s,s2), so the peel skips it
	// even though RESTRENGTHEN fires against the (s,s1) head.
	e2, s2, _ := newEmitter(t, 100)
	if _, err := e2.Emit(ein("pump", "runs", "yes", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("live: %v", err)
	}
	selfStaged, err := e2.Emit(ein("pump", "runs", "yes", "s", "s2", rpc.ACLLow))
	if err != nil {
		t.Fatalf("self-stage: %v", err)
	}
	res2, err := e2.Emit(ein("pump", "runs", "yes", "s", "s2", rpc.ACLUser))
	if err != nil {
		t.Fatalf("self-corroborate: %v", err)
	}
	if res2.Verdict != rpc.VerdictRestrengthen {
		t.Fatalf("must RESTRENGTHEN against the head, got %+v", res2)
	}
	if ok, _ := s2.IsStaged(selfStaged.ID); !ok {
		t.Fatal("same-composite corroboration must never promote")
	}
}

// TestEmit_RateLimited pins FR15 pre-gate: floods rejected, nothing written.
func TestEmit_RateLimited(t *testing.T) {
	e, _, _ := newEmitter(t, 2)
	if _, err := e.Emit(ein("a", "p", "o", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("1: %v", err)
	}
	if _, err := e.Emit(ein("b", "p", "o", "s", "s1", rpc.ACLUser)); err != nil {
		t.Fatalf("2: %v", err)
	}
	_, err := e.Emit(ein("c", "p", "o", "s", "s1", rpc.ACLUser))
	if rerr, ok := err.(*rpc.Error); !ok || rerr.Code != rpc.CodeRateLimited {
		t.Fatalf("flood must return RATE_LIMITED, got %v", err)
	}
}

// TestEmit_InvalidFailClosed pins validation before tokens: bad
// input errors INVALID, never rate-limited, never written.
func TestEmit_InvalidFailClosed(t *testing.T) {
	e, _, idx := newEmitter(t, 100)
	bad := ein("  ", "p", "o", "s", "s1", rpc.ACLUser)
	if _, err := e.Emit(bad); err == nil {
		t.Fatal("blank subject must fail closed")
	}
	if idx.Len() != 0 {
		t.Fatal("rejected emit must leave no trace")
	}
}

// TestLimiter_Deterministic pins token-bucket math with injected time.
func TestLimiter_Deterministic(t *testing.T) {
	l := NewLimiter(2)
	now := time.Now()
	if !l.AllowAt("k", now) || !l.AllowAt("k", now) {
		t.Fatal("burst of 2 must pass")
	}
	if l.AllowAt("k", now) {
		t.Fatal("third immediate emit must block")
	}
	if !l.AllowAt("k", now.Add(1100*time.Millisecond)) {
		t.Fatal("refill after a second must pass")
	}
	if l.AllowAt("other", now) == false {
		t.Fatal("buckets are per-key")
	}
}
