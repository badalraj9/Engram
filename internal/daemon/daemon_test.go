package daemon

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/rpc"
	"engram/internal/telemetry"
	"engram/proto/engrampb"
)

type harness struct {
	d    *Daemon
	c    engrampb.EngramClient
	ctx  context.Context
	sock string
}

func openHarness(t *testing.T) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir() + "/data"
	cfg.RateLimitPerSec = 100000
	d, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock := t.TempDir() + "/engram.sock"
	go func() { _ = d.Serve(sock) }()
	waitSock(t, sock)
	conn, err := rpc.DialUDS(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); d.Stop(); d.Close() })
	return &harness{d: d, c: engrampb.NewEngramClient(conn), ctx: context.Background(), sock: sock}
}

func emitIn(sub, pred, obj, src, ses string, acl engrampb.AclTier) *engrampb.EmitRequest {
	// Always-true window (DESIGN §1 VALID-NOW open end): the daemon
	// stamps wall-now at recall, so toy-domain windows would filter
	// out — validity is query's tested property, not the wire's.
	return &engrampb.EmitRequest{Record: &engrampb.InputRecord{
		Subject: sub, Predicate: pred, Object: obj,
		ValidFrom: 0, ValidTo: 1 << 62, Source: src, Session: ses,
		Acl: acl, BaseConfidence: 0.5,
	}}
}

// TestDaemon_SevenOps walks all 7 operations over a real UDS: emit,
// recall round-trip, attest, correct, as_of, consolidate.
func TestDaemon_SevenOps(t *testing.T) {
	h := openHarness(t)
	er, err := h.c.Emit(h.ctx, emitIn("pump", "runs", "yes", "s", "s1", engrampb.AclTier_USER))
	if err != nil || er.GetVerdict() != engrampb.Verdict_INSERT || er.GetId() == "" {
		t.Fatalf("emit: %+v err=%v", er, err)
	}
	rr, err := h.c.Recall(h.ctx, &engrampb.RecallRequest{Terms: []string{"pump"}, Budget: 5, AclContext: "user"})
	if err != nil || rr.GetAbstain() || len(rr.GetRecords()) == 0 {
		t.Fatalf("recall: %+v err=%v", rr, err)
	}
	ar, err := h.c.Attest(h.ctx, &engrampb.AttestRequest{Id: er.GetId(), Confirm: true, Source: "s", Session: "s2"})
	if err != nil || ar.GetNewConfidence() <= 0 {
		t.Fatalf("attest: %+v err=%v", ar, err)
	}
	cr, err := h.c.Correct(h.ctx, &engrampb.CorrectRequest{
		Hint: "pump runs yes",
		Correction: &engrampb.InputRecord{Subject: "pump", Predicate: "runs", Object: "no",
			ValidFrom: 0, ValidTo: 1 << 62, Source: "s", Session: "s3", Acl: engrampb.AclTier_USER, BaseConfidence: 0.5},
	})
	if err != nil || cr.GetId() == "" {
		t.Fatalf("correct: %+v err=%v", cr, err)
	}
	if _, err := h.c.AsOf(h.ctx, &engrampb.AsOfRequest{Terms: []string{"pump", "runs"}, T: 1, Axis: engrampb.Axis_VALID, Budget: 5}); err != nil {
		t.Fatalf("as_of: err=%v", err)
	}
	co, err := h.c.Consolidate(h.ctx, &engrampb.ConsolidateRequest{Tick: 0})
	if err != nil || co.GetTick() != 0 {
		t.Fatalf("consolidate: %+v err=%v", co, err)
	}
	if h.d.Metrics().Get(telemetry.OpEmit) == 0 || h.d.Metrics().Get(telemetry.OpRecall) == 0 {
		t.Fatal("operation counters must increment")
	}
}

// TestDaemon_ResolvePair pins the flagged-pair path over the wire:
// subordinate conflict stores BOTH + flagged_id, resolve keep-a
// settles it — the proto addition proved load-bearing end to end.
func TestDaemon_ResolvePair(t *testing.T) {
	h := openHarness(t)
	if _, err := h.c.Emit(h.ctx, emitIn("valve", "holds", "a", "ops", "s0", engrampb.AclTier_SYSTEM)); err != nil {
		t.Fatal(err)
	}
	sub, err := h.c.Emit(h.ctx, emitIn("valve", "holds", "b", "s", "s1", engrampb.AclTier_USER))
	if err != nil {
		t.Fatal(err)
	}
	if sub.GetFlaggedId() == "" {
		t.Fatal("tier-blocked emit must return flagged_id over RPC")
	}
	res, err := h.c.Resolve(h.ctx, &engrampb.ResolveRequest{IdA: sub.GetId(), IdB: sub.GetFlaggedId(), Verdict: engrampb.ResolveVerdict_KEEP_A})
	if err != nil || res.GetId() == "" {
		t.Fatalf("resolve: %+v err=%v", res, err)
	}
}

// TestDaemon_StagedPromote pins FR14 over the wire: low quarantines,
// stays invisible, promotes on independent confirm, then reads.
func TestDaemon_StagedPromote(t *testing.T) {
	h := openHarness(t)
	st, err := h.c.Emit(h.ctx, emitIn("rumor", "says", "x", "low-x", "s9", engrampb.AclTier_LOW))
	if err != nil || st.GetVerdict() != engrampb.Verdict_STAGED {
		t.Fatalf("low must STAGE: %+v err=%v", st, err)
	}
	rr, err := h.c.Recall(h.ctx, &engrampb.RecallRequest{Terms: []string{"rumor"}, Budget: 5, AclContext: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rr.GetRecords() {
		if r.GetId() == st.GetId() {
			t.Fatal("staged must stay invisible to recall")
		}
	}
	at, err := h.c.Attest(h.ctx, &engrampb.AttestRequest{Id: st.GetId(), Confirm: true, Source: "s", Session: "s2"})
	if err != nil || !at.GetPromoted() {
		t.Fatalf("independent confirm must promote: %+v err=%v", at, err)
	}
}

// TestDaemon_TickOrdering pins the durable guard over the wire:
// re-running a tick fails, tick+1 proceeds.
func TestDaemon_TickOrdering(t *testing.T) {
	h := openHarness(t)
	if _, err := h.c.Consolidate(h.ctx, &engrampb.ConsolidateRequest{Tick: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.Consolidate(h.ctx, &engrampb.ConsolidateRequest{Tick: 0}); err == nil {
		t.Fatal("re-running tick 0 must fail")
	}
	if _, err := h.c.Consolidate(h.ctx, &engrampb.ConsolidateRequest{Tick: 1}); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
}

// TestDaemon_FailClosed pins every reject path over the wire: invalid
// input, unknown IDs, unknown tiers, non-positive budgets.
func TestDaemon_FailClosed(t *testing.T) {
	h := openHarness(t)
	if _, err := h.c.Emit(h.ctx, emitIn("", "runs", "yes", "s", "s1", engrampb.AclTier_USER)); err == nil {
		t.Fatal("empty subject must fail")
	}
	if _, err := h.c.Recall(h.ctx, &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 5, AclContext: "nobody"}); err == nil {
		t.Fatal("unknown caller must fail")
	}
	if _, err := h.c.Recall(h.ctx, &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 0, AclContext: "user"}); err == nil {
		t.Fatal("zero budget must fail")
	}
	if _, err := h.c.Attest(h.ctx, &engrampb.AttestRequest{Id: "missing", Confirm: true, Source: "s", Session: "s1"}); err == nil {
		t.Fatal("unknown attest id must fail")
	}
	if _, err := h.c.Resolve(h.ctx, &engrampb.ResolveRequest{IdA: "a", IdB: "b", Verdict: engrampb.ResolveVerdict_KEEP_A}); err == nil {
		t.Fatal("unknown resolve ids must fail")
	}
	if _, err := h.c.AsOf(h.ctx, &engrampb.AsOfRequest{Terms: nil, T: 1, Axis: engrampb.Axis_VALID}); err == nil {
		t.Fatal("empty as_of terms must fail")
	}
	if _, err := h.c.Correct(h.ctx, &engrampb.CorrectRequest{Hint: "   "}); err == nil {
		t.Fatal("blank hint must fail")
	}
}

// TestDaemon_Abstain pins FR7 over the wire: no candidate clears the
// floor → explicit abstain, never a forced guess.
func TestDaemon_Abstain(t *testing.T) {
	h := openHarness(t)
	rr, err := h.c.Recall(h.ctx, &engrampb.RecallRequest{Terms: []string{"nothing-here-xyz"}, Budget: 5, AclContext: "user"})
	if err != nil || !rr.GetAbstain() {
		t.Fatalf("empty query must abstain: %+v err=%v", rr, err)
	}
}

// TestDaemon_ConcurrentEmits pins the single-mutex contract: 4
// writers × 10 emits serialize with every ack durable.
func TestDaemon_ConcurrentEmits(t *testing.T) {
	h := openHarness(t)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_, err := h.c.Emit(h.ctx, emitIn("conc", "p", "v", "src", "sess", engrampb.AclTier_USER))
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent emit: %v", err)
	}
}

// TestDaemon_SocketPerms pins the filesystem-permission ACL model:
// the socket file is owner-only. Unix-only: Windows Go maps Chmod to
// the read-only bit, so permission bits are not enforceable there —
// the daemon still calls Chmod (harmless, correct on unix).
func TestDaemon_SocketPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("socket permission bits are not enforceable on Windows")
	}
	h := openHarness(t)
	fi, err := os.Stat(h.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("socket mode = %o, want 600", fi.Mode().Perm())
	}
}

// TestDaemon_RestartRebuild pins boot ≡ rebuild: close, reopen the
// same dir, previously acked data still serves.
func TestDaemon_RestartRebuild(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir + "/data"
	cfg.RateLimitPerSec = 100000
	d1, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock1 := t.TempDir() + "/a.sock"
	go func() { _ = d1.Serve(sock1) }()
	waitSock(t, sock1)
	conn1, err := rpc.DialUDS(sock1)
	if err != nil {
		t.Fatal(err)
	}
	c1 := engrampb.NewEngramClient(conn1)
	if _, err := c1.Emit(context.Background(), emitIn("kept", "is", "yes", "s", "s1", engrampb.AclTier_USER)); err != nil {
		t.Fatal(err)
	}
	conn1.Close()
	d1.Stop()
	d1.Close()
	d2, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock2 := t.TempDir() + "/b.sock"
	go func() { _ = d2.Serve(sock2) }()
	waitSock(t, sock2)
	conn2, err := rpc.DialUDS(sock2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { conn2.Close(); d2.Stop(); d2.Close() }()
	rr, err := engrampb.NewEngramClient(conn2).Recall(context.Background(),
		&engrampb.RecallRequest{Terms: []string{"kept"}, Budget: 5, AclContext: "user"})
	if err != nil || len(rr.GetRecords()) == 0 {
		t.Fatalf("restart must re-serve: %+v err=%v", rr, err)
	}
}

func waitSock(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
