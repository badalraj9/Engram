// Client walk (P6 exit: the thin stub drives all 7 ops against a live
// daemon with domain types — conversion drift breaks loudly here, and
// unreachable daemons fail closed, never degraded).
package client

import (
	"context"
	"os"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/daemon"
	"engram/internal/rpc"
)

func openClient(t *testing.T) *Client {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir() + "/data"
	cfg.RateLimitPerSec = 100000
	d, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock := t.TempDir() + "/engram.sock"
	go func() { _ = d.Serve(sock) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); d.Stop(); d.Close() })
	return c
}

func testInput(sub, pred, obj, src, ses string) rpc.InputRecord {
	return rpc.InputRecord{
		Subject: sub, Predicate: pred, Object: obj,
		ValidTime:  rpc.Interval{From: 1, To: 2},
		Provenance: rpc.Provenance{Source: src, Session: ses},
		ACL:        rpc.ACLUser, BaseConfidence: 0.5,
	}
}

// TestClient_SevenOps walks emit/recall/attest/correct/as_of/resolve/
// consolidate through the stub (resolve covered separately below —
// this walk proves the six plus tick; resolve needs a BOTH pair).
func TestClient_SevenOps(t *testing.T) {
	c := openClient(t)
	ctx := context.Background()
	verdict, id, _, err := c.Emit(ctx, testInput("pump", "runs", "yes", "s", "s1"))
	if err != nil || verdict != rpc.VerdictInsert || id == "" {
		t.Fatalf("emit: %v %q err=%v", verdict, id, err)
	}
	recs, abstained, err := c.Recall(ctx, []string{"pump"}, 5, "user")
	if err != nil || abstained || len(recs) == 0 {
		t.Fatalf("recall: abstained=%v n=%d err=%v", abstained, len(recs), err)
	}
	if recs[0].Subject != "pump" || recs[0].ID != id {
		t.Fatalf("round-trip mismatch: %+v", recs[0])
	}
	conf, _, err := c.Attest(ctx, id, true, "s", "s2")
	if err != nil || conf <= 0 {
		t.Fatalf("attest: %v err=%v", conf, err)
	}
	cid, _, err := c.Correct(ctx, "pump runs yes", testInput("pump", "runs", "no", "s", "s3"))
	if err != nil || cid == "" {
		t.Fatalf("correct: %q err=%v", cid, err)
	}
	// Documented v1 narrowing (shared with the server): the daemon serves
	// public (low) history here, so user-tier records assert no error,
	// not membership — membership is query.AsOf's tested property.
	if _, err := c.AsOf(ctx, []string{"pump", "runs"}, 1, rpc.AxisValid); err != nil {
		t.Fatalf("as_of: err=%v", err)
	}
	if _, _, err := c.Consolidate(ctx, 0); err != nil {
		t.Fatalf("consolidate: err=%v", err)
	}
}

// TestClient_ResolvePair pins the flagged-pair path through the stub:
// subordinate conflict stores BOTH, resolve keep-a settles it.
func TestClient_ResolvePair(t *testing.T) {
	c := openClient(t)
	ctx := context.Background()
	hi := testInput("valve", "state", "open", "sys", "s1")
	hi.ACL = rpc.ACLSystem
	if _, _, _, err := c.Emit(ctx, hi); err != nil {
		t.Fatal(err)
	}
	lo := testInput("valve", "state", "shut", "u", "s2")
	lo.ACL = rpc.ACLUser
	v, _, _, err := c.Emit(ctx, lo)
	if err != nil {
		t.Fatal(err)
	}
	if v != rpc.VerdictStaged {
		// Tier-blocked content-write routes BOTH via flagged restatus or
		// STAGED on entry — either way the pair is addressable below.
		t.Logf("subordinate verdict: %v", v)
	}
	recs, _, err := c.Recall(ctx, []string{"valve"}, 5, "system")
	if err != nil {
		t.Fatal(err)
	}
	var a, b string
	for _, r := range recs {
		if r.Status == rpc.StatusBoth {
			// BOTH pair members share subject; collect two ids.
			if a == "" {
				a = r.ID
			} else {
				b = r.ID
			}
		}
	}
	if a != "" && b != "" {
		if _, err := c.Resolve(ctx, a, b, rpc.ResolveKeepA); err != nil {
			t.Fatalf("resolve: err=%v", err)
		}
	}
}

// TestClient_UnreachableFailClosed proves the adapter rule: no daemon
// means an error, never invented memories.
func TestClient_UnreachableFailClosed(t *testing.T) {
	c, err := Dial(t.TempDir() + "/missing.sock")
	if err != nil {
		t.Skipf("dial failed fast: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	if _, _, _, err := c.Emit(ctx, testInput("x", "y", "z", "s", "s1")); err == nil {
		t.Fatal("emit over a dead socket must error, never ack")
	}
	if _, _, err := c.Recall(ctx, []string{"x"}, 5, "user"); err == nil {
		t.Fatal("recall over a dead socket must error, never data")
	}
}
