// Package client is Engram v2's thin Go stub (P6 — BUILD-RANKS E).
//
// Transport-only: dials the UDS, speaks the 7 ops, converts wire to
// domain on return. No caching (a cache is a second source of truth),
// no truth, no guessing — unreachable daemon returns an error
// (fail-closed, the adapter rule from IDEAS §1), never invented
// memories. The core never imports this package; dogfood (UC5) and
// future framework adapters build on it.
package client

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"engram/internal/rpc"
	"engram/proto/engrampb"
)

// Client wraps the generated stub with domain-typed helpers.
type Client struct {
	conn *grpc.ClientConn
	stub engrampb.EngramClient
}

// Dial opens a client over the Unix socket at sock. Failure is
// fail-closed (an error, never a degraded client).
func Dial(sock string) (*Client, error) {
	conn, err := rpc.DialUDS(sock)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, stub: engrampb.NewEngramClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Emit appends a structured record (SRS §1.3: callers structure text
// before calling — extraction lives outside Engram).
func (c *Client) Emit(ctx context.Context, in rpc.InputRecord) (rpc.Verdict, string, string, error) {
	w, err := inputToWire(in)
	if err != nil {
		return "", "", "", err
	}
	res, err := c.stub.Emit(ctx, &engrampb.EmitRequest{Record: w})
	if err != nil {
		return "", "", "", err
	}
	return verdictFromWire(res.GetVerdict()), res.GetId(), res.GetFlaggedId(), nil
}

// Recall queries for ranked, currently-true facts or ABSTAIN.
func (c *Client) Recall(ctx context.Context, terms []string, budget int, aclContext string) ([]rpc.Record, bool, error) {
	res, err := c.stub.Recall(ctx, &engrampb.RecallRequest{
		Terms: terms, Budget: int32(budget), AclContext: aclContext,
	})
	if err != nil {
		return nil, false, err
	}
	if res.GetAbstain() {
		return nil, true, nil
	}
	out := make([]rpc.Record, 0, len(res.GetRecords()))
	for _, w := range res.GetRecords() {
		out = append(out, recordFromWire(w))
	}
	return out, false, nil
}

// Correct replaces wrong content located via hint.
func (c *Client) Correct(ctx context.Context, hint string, correction rpc.InputRecord) (string, rpc.Verdict, error) {
	w, err := inputToWire(correction)
	if err != nil {
		return "", "", err
	}
	res, err := c.stub.Correct(ctx, &engrampb.CorrectRequest{Hint: hint, Correction: w})
	if err != nil {
		return "", "", err
	}
	return res.GetId(), verdictFromWire(res.GetVerdict()), nil
}

// Attest records a confirm/disconfirm verdict on a prior recall id.
func (c *Client) Attest(ctx context.Context, id string, confirm bool, source, session string) (float64, bool, error) {
	res, err := c.stub.Attest(ctx, &engrampb.AttestRequest{
		Id: id, Confirm: confirm, Source: source, Session: session,
	})
	if err != nil {
		return 0, false, err
	}
	return res.GetNewConfidence(), res.GetPromoted(), nil
}

// Resolve settles a BOTH-state pair with a ruling verdict.
func (c *Client) Resolve(ctx context.Context, aID, bID string, v rpc.ResolveVerdict) (string, error) {
	w, err := resolveToWire(v)
	if err != nil {
		return "", err
	}
	res, err := c.stub.Resolve(ctx, &engrampb.ResolveRequest{IdA: aID, IdB: bID, Verdict: w})
	if err != nil {
		return "", err
	}
	return res.GetId(), nil
}

// AsOf serves bi-temporal history (terms[0]=subject, terms[1]=predicate
// — the documented v1 narrowing, shared with the server).
func (c *Client) AsOf(ctx context.Context, terms []string, t int64, axis rpc.Axis) ([]rpc.Record, error) {
	w, err := axisToWire(axis)
	if err != nil {
		return nil, err
	}
	res, err := c.stub.AsOf(ctx, &engrampb.AsOfRequest{Terms: terms, T: t, Axis: w})
	if err != nil {
		return nil, err
	}
	out := make([]rpc.Record, 0, len(res.GetRecords()))
	for _, r := range res.GetRecords() {
		out = append(out, recordFromWire(r))
	}
	return out, nil
}

// Consolidate runs the host-triggered maintenance tick.
func (c *Client) Consolidate(ctx context.Context, tick int64) (cons, pruned int, err error) {
	res, err := c.stub.Consolidate(ctx, &engrampb.ConsolidateRequest{Tick: tick})
	if err != nil {
		return 0, 0, err
	}
	return int(res.GetConsolidated()), int(res.GetPruned()), nil
}

// --- conversions: domain onto wire and back (client edge mirror of
// rpc/server.go's boundary — tested by the live-daemon walk, so drift
// breaks loudly, never silently). ---

func aclToWire(a rpc.AclTier) (engrampb.AclTier, error) {
	switch a {
	case rpc.ACLLow:
		return engrampb.AclTier_LOW, nil
	case rpc.ACLUser:
		return engrampb.AclTier_USER, nil
	case rpc.ACLSystem:
		return engrampb.AclTier_SYSTEM, nil
	default:
		return engrampb.AclTier_ACL_TIER_UNSPECIFIED, rpc.E(rpc.CodeInvalidRecord, "unknown ACL tier")
	}
}

func aclFromWire(w engrampb.AclTier) rpc.AclTier {
	switch w {
	case engrampb.AclTier_LOW:
		return rpc.ACLLow
	case engrampb.AclTier_SYSTEM:
		return rpc.ACLSystem
	default:
		return rpc.ACLUser
	}
}

func inputToWire(in rpc.InputRecord) (*engrampb.InputRecord, error) {
	acl, err := aclToWire(in.ACL)
	if err != nil {
		return nil, err
	}
	return &engrampb.InputRecord{
		Subject: in.Subject, Predicate: in.Predicate, Object: in.Object,
		ValidFrom: in.ValidTime.From, ValidTo: in.ValidTime.To,
		Source: in.Provenance.Source, Session: in.Provenance.Session,
		Acl: acl, BaseConfidence: in.BaseConfidence,
		CallerTsMeta: in.CallerTSMeta, Payload: in.Payload,
	}, nil
}

func recordFromWire(w *engrampb.StoredRecord) rpc.Record {
	if w == nil {
		return rpc.Record{}
	}
	var st rpc.Status
	switch w.GetStatus() {
	case engrampb.Status_ACTIVE:
		st = rpc.StatusActive
	case engrampb.Status_SCHEDULED:
		st = rpc.StatusScheduled
	case engrampb.Status_BOTH:
		st = rpc.StatusBoth
	default:
		st = rpc.StatusTombstoned
	}
	var typ rpc.RecordType
	switch w.GetType() {
	case engrampb.RecordType_SUMMARY:
		typ = rpc.RecordSummary
	case engrampb.RecordType_SEMANTIC:
		typ = rpc.RecordSemantic
	default:
		typ = rpc.RecordEpisodic
	}
	return rpc.Record{
		ID: w.GetId(), Subject: w.GetSubject(), Predicate: w.GetPredicate(),
		Object:      w.GetObject(),
		ValidTime:   rpc.Interval{From: w.GetValidFrom(), To: w.GetValidTo()},
		Txn:         rpc.Interval{From: w.GetTxnFrom(), To: w.GetTxnTo()},
		Provenance:  rpc.Provenance{Source: w.GetSource(), Session: w.GetSession()},
		DerivedFrom: append([]string(nil), w.GetDerivedFrom()...),
		ACL:         aclFromWire(w.GetAcl()), Status: st, Type: typ,
		BaseConfidence: w.GetBaseConfidence(), CallerTSMeta: w.GetCallerTsMeta(),
		Payload: w.GetPayload(),
	}
}

func verdictFromWire(w engrampb.Verdict) rpc.Verdict {
	switch w {
	case engrampb.Verdict_SKIP:
		return rpc.VerdictSkip
	case engrampb.Verdict_INSERT:
		return rpc.VerdictInsert
	case engrampb.Verdict_MERGE:
		return rpc.VerdictMerge
	case engrampb.Verdict_UPDATE:
		return rpc.VerdictUpdate
	case engrampb.Verdict_RESTRENGTHEN:
		return rpc.VerdictRestrengthen
	case engrampb.Verdict_STAGED:
		return rpc.VerdictStaged
	default:
		return rpc.Verdict(fmt.Sprintf("UNKNOWN(%d)", int32(w)))
	}
}

func resolveToWire(v rpc.ResolveVerdict) (engrampb.ResolveVerdict, error) {
	switch v {
	case rpc.ResolveKeepA:
		return engrampb.ResolveVerdict_KEEP_A, nil
	case rpc.ResolveKeepB:
		return engrampb.ResolveVerdict_KEEP_B, nil
	case rpc.ResolveMerge:
		return engrampb.ResolveVerdict_MERGE_PAIR, nil
	default:
		return engrampb.ResolveVerdict_RESOLVE_VERDICT_UNSPECIFIED,
			rpc.E(rpc.CodeInvalidRecord, "unknown resolve verdict")
	}
}

func axisToWire(a rpc.Axis) (engrampb.Axis, error) {
	switch a {
	case rpc.AxisValid:
		return engrampb.Axis_VALID, nil
	case rpc.AxisTransaction:
		return engrampb.Axis_TRANSACTION, nil
	default:
		return engrampb.Axis_AXIS_UNSPECIFIED, rpc.E(rpc.CodeInvalidRecord, "unknown axis")
	}
}
