// Daemon server (P5 — TECH §2.1, BUILD-PLAN P5 exit).
//
// gRPC over a Unix domain socket (not TCP — local-only daemon, lower
// latency, filesystem-permission access: the socket file is chmodded
// 0600 at bind). One Server serializes ALL 7 ops under a single mutex
// (BUILD-PLAN 2026-09-28: index/invert.go is not concurrent-safe, so
// reads share the write lock — throughput cost accepted, correctness
// first). The host owns the lifecycle: Serve blocks the caller, Stop
// drains; the server never spawns a ticker or background goroutine
// (frozen FR10).
//
// Layering (no import cycle by construction): this leaf package owns
// transport + wire↔domain conversion + the mutex. Execution arrives
// through Ports — plain function fields over domain types, wired by
// internal/daemon (production) or the test harness. Intake, query,
// and maintenance all import this package, so this file imports none
// of them — the reverse would be a cycle, caught by the build. Wire
// types are the protoc-generated engrampb package (proto/engram.proto
// is the schema authority; see the generation note in wire_gen.go).
//
// Structural ack rule (BUILD-PLAN exit checklist): every mutating
// handler flows validate → execute → Sync → respond with no early
// return carrying an ack. Sync lives inside the core ports; the
// server adds no second ack path — success responses only leave after
// the port's Sync covered them.
//
// Wire narrowings, all documented (v1 proto carries less than the
// domain — P6 may widen the proto, recorded per item):
//   - Recall terms join into the query subject (token lane carries;
//     Exact/Fuzzy lanes serve when terms name keys).
//   - AsOf terms[0]=subject, terms[1]=predicate; the daemon serves
//     public (low) history (most restrictive — unknown callers see
//     public history only, never unfiltered state).
//   - Correct hint splits "subject [predicate [object…]]" on
//     whitespace (documented v1 shape; full hint preferred).
//   - Resolve attributes to a fixed operator composite (resolves are
//     rulings, not corroboration — shared session is correct here).
//   - latency_budget_ms < 0 fails closed; otherwise accepted and
//     recorded (v1 recall is single-pass — no cascade to shed yet).
package rpc

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"engram/internal/config"
	"engram/internal/telemetry"
	"engram/proto/engrampb"
)

// resolveProvenance attributes RPC resolves to a fixed operator
// composite (documented narrowing — see package comment).
var resolveProvenance = Provenance{Source: "operator", Session: "rpc-resolve"}

// RecallSpec is the transport-side recall query: converted from wire,
// consumed by the query port. Threshold/Beta/Trusts arrive from the
// server's config + calibrated state — never from the wire (callers
// do not grade their own evidence).
type RecallSpec struct {
	Subject   string
	Caller    AclTier
	Budget    int
	Threshold float64
	Beta      float64
	TrustLow  float64
	TrustUser float64
	TrustSys  float64
}

// Ports is the execution boundary: every field is implemented by
// internal/daemon against intake/query/maintenance (or by the test
// harness). All functions run under the server's mutex — they must
// not add their own cross-op locking.
type Ports struct {
	Emit        func(in InputRecord) (verdict Verdict, id, flagged string, err error)
	Correct     func(subject, predicate, object string, cor InputRecord) (id string, verdict Verdict, err error)
	Attest      func(id string, confirm bool, src, ses string) (conf float64, promoted bool, err error)
	Resolve     func(aID, bID string, v ResolveVerdict, prov Provenance) (newID string, err error)
	Recall      func(spec RecallSpec) (recs []Record, abstained bool, err error)
	AsOf        func(subject, predicate string, t int64, axis Axis) ([]Record, error)
	Consolidate func(tick int64) (cons, pruned int, abstain, evict float64, err error)
	// Rebuild discards the serving index and rebuilds from the log
	// (DESIGN §8 trigger). Nil means findability is restored (the
	// caller still reports the original corruption failure).
	Rebuild func() error
}

// Server is the P5 daemon: one mutex, one port set, one clock view.
// It embeds the generated unimplemented server for forward-compat:
// a future 8th RPC fails as Unimplemented, never as a panic.
type Server struct {
	engrampb.UnimplementedEngramServer
	mu      sync.Mutex
	ports   Ports
	cfg     config.Config
	metrics *telemetry.Registry
	thresh  float64

	grpcSrv *grpc.Server
	sock    string
}

// NewServer wires a daemon over validated config + ports. The abstain
// threshold seeds from cfg and refreshes on every consolidate tick
// (FR16 wiring).
func NewServer(cfg config.Config, ports Ports) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Server{
		ports:   ports,
		cfg:     cfg,
		metrics: telemetry.New(),
		thresh:  cfg.AbstainThreshold,
	}, nil
}

// Metrics exposes the registry for host-side scraping (no wire
// exposure in P5 — inspect is deferred post-v1).
func (s *Server) Metrics() *telemetry.Registry { return s.metrics }

// Serve binds sock (removing a stale file first), chmods 0600, and
// serves gRPC until Stop or failure. Blocks the caller — the host
// owns the goroutine, never the server.
func (s *Server) Serve(sock string) error {
	_ = os.Remove(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0600); err != nil {
		lis.Close()
		return err
	}
	s.sock = sock
	s.grpcSrv = grpc.NewServer()
	engrampb.RegisterEngramServer(s.grpcSrv, s)
	return s.grpcSrv.Serve(lis)
}

// Stop drains (no new RPCs, in-flight complete) and removes the socket
// file. Unblocks Serve.
func (s *Server) Stop() {
	if s.grpcSrv != nil {
		s.grpcSrv.GracefulStop()
	}
	if s.sock != "" {
		_ = os.Remove(s.sock)
	}
}

// DialUDS is the client-side dialer shared by tests and (P6) stubs:
// gRPC over a Unix socket with no transport credentials (local-only).
func DialUDS(sock string) (*grpc.ClientConn, error) {
	return grpc.NewClient("passthrough:///engram",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}))
}

// --- conversions: wire onto domain, never the reverse ---

func wireACL(w engrampb.AclTier) (AclTier, error) {
	switch w {
	case engrampb.AclTier_LOW:
		return ACLLow, nil
	case engrampb.AclTier_USER:
		return ACLUser, nil
	case engrampb.AclTier_SYSTEM:
		return ACLSystem, nil
	default:
		return "", E(CodeInvalidRecord, "unknown ACL tier")
	}
}

func wireVerdict(v Verdict) engrampb.Verdict {
	switch v {
	case VerdictSkip:
		return engrampb.Verdict_SKIP
	case VerdictInsert:
		return engrampb.Verdict_INSERT
	case VerdictMerge:
		return engrampb.Verdict_MERGE
	case VerdictUpdate:
		return engrampb.Verdict_UPDATE
	case VerdictRestrengthen:
		return engrampb.Verdict_RESTRENGTHEN
	case VerdictStaged:
		return engrampb.Verdict_STAGED
	default:
		return engrampb.Verdict_VERDICT_UNSPECIFIED
	}
}

func wireStatus(st Status) engrampb.Status {
	switch st {
	case StatusActive:
		return engrampb.Status_ACTIVE
	case StatusScheduled:
		return engrampb.Status_SCHEDULED
	case StatusBoth:
		return engrampb.Status_BOTH
	case StatusTombstoned:
		return engrampb.Status_TOMBSTONED
	default:
		return engrampb.Status_STATUS_UNSPECIFIED
	}
}

func wireType(t RecordType) engrampb.RecordType {
	switch t {
	case RecordEpisodic:
		return engrampb.RecordType_EPISODIC
	case RecordSummary:
		return engrampb.RecordType_SUMMARY
	case RecordSemantic:
		return engrampb.RecordType_SEMANTIC
	default:
		return engrampb.RecordType_RECORD_TYPE_UNSPECIFIED
	}
}

func wireResolveVerdict(w engrampb.ResolveVerdict) (ResolveVerdict, error) {
	switch w {
	case engrampb.ResolveVerdict_KEEP_A:
		return ResolveKeepA, nil
	case engrampb.ResolveVerdict_KEEP_B:
		return ResolveKeepB, nil
	case engrampb.ResolveVerdict_MERGE_PAIR:
		return ResolveMerge, nil
	default:
		return "", E(CodeInvalidRecord, "unknown resolve verdict")
	}
}

func wireAxis(w engrampb.Axis) (Axis, error) {
	switch w {
	case engrampb.Axis_VALID:
		return AxisValid, nil
	case engrampb.Axis_TRANSACTION:
		return AxisTransaction, nil
	default:
		return "", E(CodeInvalidRecord, "unknown axis")
	}
}

func wireInput(w *engrampb.InputRecord) (InputRecord, error) {
	if w == nil {
		return InputRecord{}, E(CodeInvalidRecord, "record is required")
	}
	acl, err := wireACL(w.GetAcl())
	if err != nil {
		return InputRecord{}, err
	}
	return InputRecord{
		Subject:        w.GetSubject(),
		Predicate:      w.GetPredicate(),
		Object:         w.GetObject(),
		ValidTime:      Interval{From: w.GetValidFrom(), To: w.GetValidTo()},
		Provenance:     Provenance{Source: w.GetSource(), Session: w.GetSession()},
		ACL:            acl,
		BaseConfidence: w.GetBaseConfidence(),
		CallerTSMeta:   w.GetCallerTsMeta(),
		Payload:        w.GetPayload(),
	}, nil
}

func wireRecord(r Record) *engrampb.StoredRecord {
	return &engrampb.StoredRecord{
		Id: r.ID, Subject: r.Subject, Predicate: r.Predicate, Object: r.Object,
		ValidFrom: r.ValidTime.From, ValidTo: r.ValidTime.To,
		TxnFrom: r.Txn.From, TxnTo: r.Txn.To,
		Source: r.Provenance.Source, Session: r.Provenance.Session,
		Acl: wireTier(r.ACL), Status: wireStatus(r.Status),
		BaseConfidence: r.BaseConfidence, Type: wireType(r.Type),
		DerivedFrom:  append([]string(nil), r.DerivedFrom...),
		CallerTsMeta: r.CallerTSMeta, Payload: r.Payload,
	}
}

func wireTier(t AclTier) engrampb.AclTier {
	switch t {
	case ACLLow:
		return engrampb.AclTier_LOW
	case ACLUser:
		return engrampb.AclTier_USER
	case ACLSystem:
		return engrampb.AclTier_SYSTEM
	default:
		return engrampb.AclTier_ACL_TIER_UNSPECIFIED
	}
}

// parseCaller maps acl_context onto a tier fail-closed: unknown (or
// empty) callers are caller bugs, never unfiltered sets.
func parseCaller(s string) (AclTier, error) {
	switch AclTier(strings.TrimSpace(s)) {
	case ACLLow:
		return ACLLow, nil
	case ACLUser:
		return ACLUser, nil
	case ACLSystem:
		return ACLSystem, nil
	default:
		return "", E(CodeInvalidRecord, "recall requires a known caller tier")
	}
}

// splitHint maps the v1 wire hint onto subject/predicate/object:
// "subject [predicate [object…]]" on whitespace. Subject is required;
// absent predicate/object narrow the locator (documented v1 shape).
func splitHint(hint string) (sub, pred, obj string, err error) {
	fields := strings.Fields(hint)
	if len(fields) == 0 {
		return "", "", "", E(CodeInvalidRecord, "correct hint is required")
	}
	sub = fields[0]
	if len(fields) > 1 {
		pred = fields[1]
	}
	if len(fields) > 2 {
		obj = strings.Join(fields[2:], " ")
	}
	return sub, pred, obj, nil
}

// --- handlers: validate → execute → Sync (inside ports) → respond ---

func (s *Server) Emit(_ context.Context, req *engrampb.EmitRequest) (*engrampb.EmitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Now()
	in, err := wireInput(req.GetRecord())
	if err != nil {
		return nil, ToStatus(err)
	}
	verdict, id, flagged, err := s.ports.Emit(in)
	if err != nil {
		return nil, ToStatus(s.maybeRebuild(err))
	}
	s.metrics.Inc(telemetry.OpEmit)
	s.metrics.Inc(telemetry.VerdictPrefix + string(verdict))
	s.metrics.Observe(telemetry.OpEmit+telemetry.LatencySuffix, time.Since(start).Seconds())
	return &engrampb.EmitResponse{Verdict: wireVerdict(verdict), Id: id, FlaggedId: flagged}, nil
}

func (s *Server) Recall(_ context.Context, req *engrampb.RecallRequest) (*engrampb.RecallResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Now()
	caller, err := parseCaller(req.GetAclContext())
	if err != nil {
		return nil, ToStatus(err)
	}
	if req.GetLatencyBudgetMs() < 0 {
		return nil, ToStatus(E(CodeInvalidRecord, "latency budget must be >= 0"))
	}
	if req.GetBudget() <= 0 {
		return nil, ToStatus(E(CodeInvalidRecord, "recall budget must be positive"))
	}
	recs, abstained, err := s.ports.Recall(RecallSpec{
		Subject:   strings.Join(req.GetTerms(), " "),
		Caller:    caller,
		Budget:    int(req.GetBudget()),
		Threshold: s.thresh,
		Beta:      s.cfg.Beta,
		TrustLow:  s.cfg.TrustLow,
		TrustUser: s.cfg.TrustUser,
		TrustSys:  s.cfg.TrustSystem,
	})
	if err != nil {
		return nil, ToStatus(s.maybeRebuild(err))
	}
	s.metrics.Inc(telemetry.OpRecall)
	s.metrics.Observe(telemetry.OpRecall+telemetry.LatencySuffix, time.Since(start).Seconds())
	if abstained {
		s.metrics.Inc(telemetry.AbstainTotal)
		return &engrampb.RecallResponse{Abstain: true}, nil
	}
	out := &engrampb.RecallResponse{}
	for _, r := range recs {
		out.Records = append(out.Records, wireRecord(r))
	}
	return out, nil
}

func (s *Server) Correct(_ context.Context, req *engrampb.CorrectRequest) (*engrampb.CorrectResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, pred, obj, err := splitHint(req.GetHint())
	if err != nil {
		return nil, ToStatus(err)
	}
	correction, err := wireInput(req.GetCorrection())
	if err != nil {
		return nil, ToStatus(err)
	}
	id, verdict, err := s.ports.Correct(sub, pred, obj, correction)
	if err != nil {
		return nil, ToStatus(s.maybeRebuild(err))
	}
	s.metrics.Inc(telemetry.OpCorrect)
	return &engrampb.CorrectResponse{Id: id, Verdict: wireVerdict(verdict)}, nil
}

func (s *Server) Attest(_ context.Context, req *engrampb.AttestRequest) (*engrampb.AttestResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(req.GetId()) == "" {
		return nil, ToStatus(E(CodeInvalidRecord, "attest requires a record id"))
	}
	conf, promoted, err := s.ports.Attest(req.GetId(), req.GetConfirm(), req.GetSource(), req.GetSession())
	if err != nil {
		return nil, ToStatus(s.maybeRebuild(err))
	}
	s.metrics.Inc(telemetry.OpAttest)
	return &engrampb.AttestResponse{NewConfidence: conf, Promoted: promoted}, nil
}

func (s *Server) Resolve(_ context.Context, req *engrampb.ResolveRequest) (*engrampb.ResolveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	verdict, err := wireResolveVerdict(req.GetVerdict())
	if err != nil {
		return nil, ToStatus(err)
	}
	if strings.TrimSpace(req.GetIdA()) == "" || strings.TrimSpace(req.GetIdB()) == "" {
		return nil, ToStatus(E(CodeInvalidRecord, "resolve requires both record ids"))
	}
	newID, err := s.ports.Resolve(req.GetIdA(), req.GetIdB(), verdict, resolveProvenance)
	if err != nil {
		return nil, ToStatus(s.maybeRebuild(err))
	}
	s.metrics.Inc(telemetry.OpResolve)
	return &engrampb.ResolveResponse{Id: newID}, nil
}

func (s *Server) AsOf(_ context.Context, req *engrampb.AsOfRequest) (*engrampb.AsOfResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(req.GetTerms()) == 0 {
		return nil, ToStatus(E(CodeInvalidRecord, "as_of requires subject/predicate terms"))
	}
	subject := req.GetTerms()[0]
	predicate := ""
	if len(req.GetTerms()) > 1 {
		predicate = req.GetTerms()[1]
	}
	axis, err := wireAxis(req.GetAxis())
	if err != nil {
		return nil, ToStatus(err)
	}
	recs, err := s.ports.AsOf(subject, predicate, req.GetT(), axis)
	if err != nil {
		return nil, ToStatus(s.maybeRebuild(err))
	}
	budget := int(req.GetBudget())
	if budget > 0 && len(recs) > budget {
		recs = recs[:budget]
	}
	s.metrics.Inc(telemetry.OpAsOf)
	out := &engrampb.AsOfResponse{}
	for _, r := range recs {
		out.Records = append(out.Records, wireRecord(r))
	}
	return out, nil
}

func (s *Server) Consolidate(_ context.Context, req *engrampb.ConsolidateRequest) (*engrampb.ConsolidateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Now()
	cons, pruned, abstain, _, err := s.ports.Consolidate(req.GetTick())
	if err != nil {
		return nil, ToStatus(err)
	}
	s.thresh = abstain
	s.metrics.Inc(telemetry.OpConsolidate)
	s.metrics.Add(telemetry.ConsolidatedTotal, int64(cons))
	s.metrics.Add(telemetry.PrunedTotal, int64(pruned))
	s.metrics.Observe(telemetry.OpConsolidate+telemetry.LatencySuffix, time.Since(start).Seconds())
	return &engrampb.ConsolidateResponse{
		Tick:         req.GetTick(),
		Consolidated: int32(cons),
		Pruned:       int32(pruned),
	}, nil
}

// maybeRebuild implements the DESIGN §8 index-corruption trigger
// (pre-v1 required): a CORRUPT_INDEX failure discards the serving
// index and rebuilds from the truth log, then reports the ORIGINAL
// error — the rebuild restores findability, it never papers over the
// failure that exposed the corruption. Non-corruption errors pass
// through untouched. Consolidation errors never trigger (a corrupt
// tick fails closed without a rebuild masking it).
func (s *Server) maybeRebuild(err error) error {
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeCorruptIndex {
		return err
	}
	if rerr := s.ports.Rebuild(); rerr != nil {
		return err
	}
	return err
}

// Compile-time surface check: *Server serves the generated 7-op surface.
var _ engrampb.EngramServer = (*Server)(nil)
