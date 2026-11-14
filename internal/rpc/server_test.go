package rpc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"engram/internal/config"
	"engram/internal/rpc"
	"engram/proto/engrampb"
)

func testPorts() rpc.Ports {
	return rpc.Ports{
		Emit: func(in rpc.InputRecord) (rpc.Verdict, string, string, error) {
			return rpc.VerdictInsert, "id-1", "", nil
		},
		Correct: func(sub, pred, obj string, cor rpc.InputRecord) (string, rpc.Verdict, error) {
			return "id-2", rpc.VerdictUpdate, nil
		},
		Attest: func(id string, confirm bool, src, ses string) (float64, bool, error) {
			return 0.7, false, nil
		},
		Resolve: func(aID, bID string, v rpc.ResolveVerdict, prov rpc.Provenance) (string, error) {
			return "new-id", nil
		},
		Recall: func(spec rpc.RecallSpec) ([]rpc.Record, bool, error) {
			return nil, true, nil
		},
		AsOf: func(subject, predicate string, t int64, axis rpc.Axis) ([]rpc.Record, error) {
			return nil, nil
		},
		Consolidate: func(tick int64) (int, int, float64, float64, error) {
			return 1, 2, 0.9, 0.1, nil
		},
		Rebuild: func() error { return nil },
	}
}

func testServer(t *testing.T) *rpc.Server {
	t.Helper()
	s, err := rpc.NewServer(config.Default(), testPorts())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func codeOf(t *testing.T, err error) codes.Code {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	return st.Code()
}

// TestServer_EmitAck pins the handler contract: valid input →
// verdict+id; invalid input → InvalidArgument, never an ack.
func TestServer_EmitAck(t *testing.T) {
	s := testServer(t)
	res, err := s.Emit(context.Background(), &engrampb.EmitRequest{Record: &engrampb.InputRecord{
		Subject: "s", Predicate: "p", Object: "o", ValidFrom: 1, ValidTo: 2,
		Source: "src", Session: "ses", Acl: engrampb.AclTier_USER, BaseConfidence: 0.5,
	}})
	if err != nil || res.GetVerdict() != engrampb.Verdict_INSERT || res.GetId() != "id-1" {
		t.Fatalf("emit: %+v err=%v", res, err)
	}
	if _, err := s.Emit(context.Background(), &engrampb.EmitRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("nil record must be InvalidArgument, got %v", err)
	}
}

// TestServer_ThresholdRefresh pins the FR16 wiring: consolidate's
// calibrated abstain becomes the next recall's threshold.
func TestServer_ThresholdRefresh(t *testing.T) {
	var got float64
	ports := testPorts()
	ports.Recall = func(spec rpc.RecallSpec) ([]rpc.Record, bool, error) {
		got = spec.Threshold
		return nil, true, nil
	}
	s, err := rpc.NewServer(config.Default(), ports)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consolidate(context.Background(), &engrampb.ConsolidateRequest{Tick: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recall(context.Background(), &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 1, AclContext: "user"}); err != nil {
		t.Fatal(err)
	}
	if got != 0.9 {
		t.Fatalf("recall threshold = %v, want calibrated 0.9", got)
	}
}

// TestServer_RejectPaths pins every fail-closed mapping at the
// handler level (codes the wire contract promises).
func TestServer_RejectPaths(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	if _, err := s.Recall(ctx, &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 1, AclContext: "nobody"}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("unknown caller: %v", err)
	}
	if _, err := s.Recall(ctx, &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 1, AclContext: "user", LatencyBudgetMs: -1}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("negative latency budget: %v", err)
	}
	if _, err := s.Resolve(ctx, &engrampb.ResolveRequest{IdA: "a", IdB: "b", Verdict: engrampb.ResolveVerdict_RESOLVE_VERDICT_UNSPECIFIED}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("unknown resolve verdict: %v", err)
	}
	if _, err := s.AsOf(ctx, &engrampb.AsOfRequest{Axis: engrampb.Axis_VALID}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("empty as_of terms: %v", err)
	}
	if _, err := s.Attest(ctx, &engrampb.AttestRequest{Id: "  "}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("blank attest id: %v", err)
	}
	if _, err := s.Correct(ctx, &engrampb.CorrectRequest{Hint: "s p o"}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("nil correction must fail: %v", err)
	}
}

// TestServer_RebuildTrigger pins the DESIGN §8 rule: corruption
// rebuilds AND still reports the original error; ordinary errors
// never rebuild.
func TestServer_RebuildTrigger(t *testing.T) {
	var rebuilt int
	ports := testPorts()
	ports.Rebuild = func() error { rebuilt++; return nil }
	ports.Recall = func(spec rpc.RecallSpec) ([]rpc.Record, bool, error) {
		return nil, false, rpc.E(rpc.CodeCorruptIndex, "index bad")
	}
	s, err := rpc.NewServer(config.Default(), ports)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Recall(context.Background(), &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 1, AclContext: "user"})
	if codeOf(t, err) != codes.DataLoss {
		t.Fatalf("corruption must surface DataLoss, got %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("corruption must trigger exactly one rebuild, got %d", rebuilt)
	}
	ports.Recall = func(spec rpc.RecallSpec) ([]rpc.Record, bool, error) {
		return nil, false, rpc.E(rpc.CodeInvalidRecord, "bad")
	}
	// Ports copy into the server at construction — a second server
	// carries the ordinary-error fake (swapping the struct afterward
	// could never take).
	s2, err := rpc.NewServer(config.Default(), ports)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Recall(context.Background(), &engrampb.RecallRequest{Terms: []string{"x"}, Budget: 1, AclContext: "user"}); err == nil {
		t.Fatal("ordinary error must still fail")
	}
	if rebuilt != 1 {
		t.Fatalf("ordinary error must not rebuild, got %d", rebuilt)
	}
}

// TestServer_BadConfig pins startup validation: unusable config
// refuses the daemon, never a half-wired server.
func TestServer_BadConfig(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = ""
	if _, err := rpc.NewServer(cfg, testPorts()); err == nil {
		t.Fatal("empty data dir must fail")
	}
}
