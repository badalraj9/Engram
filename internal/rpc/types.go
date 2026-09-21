// Package rpc defines Engram v2's internal domain types.
//
// P0 contract: every later phase (P1-P6) builds against these shapes.
// They intentionally do not import generated protobuf code — the wire
// schema in proto/engram.proto maps onto these at the RPC boundary
// (P5), so the core never depends on the transport.
//
// Naming reservation: "valid" is owned by belief-state/temporal
// semantics. Structural checks are therefore named WellFormed, never
// Valid — the future liveness helper will be IsLive/BelievedAt.
package rpc

import (
	"math"
	"strings"
)

// OpenEnd marks an unbounded interval edge (valid-to / txn-to = still current).
const OpenEnd = math.MaxInt64

// Verdict is the write-gate outcome for emit (SRS FR4: five-way + STAGED).
type Verdict string

// Write-gate verdicts.
const (
	VerdictSkip         Verdict = "SKIP"
	VerdictInsert       Verdict = "INSERT"
	VerdictMerge        Verdict = "MERGE"
	VerdictUpdate       Verdict = "UPDATE"
	VerdictRestrengthen Verdict = "RESTRENGTHEN"
	VerdictStaged       Verdict = "STAGED"
)

// WellFormed reports whether v is a known gate verdict.
func (v Verdict) WellFormed() bool {
	switch v {
	case VerdictSkip, VerdictInsert, VerdictMerge,
		VerdictUpdate, VerdictRestrengthen, VerdictStaged:
		return true
	}
	return false
}

// Status is the record lifecycle state (SRS FR3 / DESIGN §1).
type Status string

// Record lifecycle states.
const (
	StatusActive     Status = "ACTIVE"
	StatusScheduled  Status = "SCHEDULED"
	StatusBoth       Status = "BOTH"
	StatusTombstoned Status = "TOMBSTONED"
)

// WellFormed reports whether s is a known lifecycle state.
func (s Status) WellFormed() bool {
	switch s {
	case StatusActive, StatusScheduled, StatusBoth, StatusTombstoned:
		return true
	}
	return false
}

// RecordType distinguishes episodic observations from derived records
// (P0 locked). Derivations must never feed new derivations without an
// explicit flag: the gate derives from evidence, not from cache.
type RecordType string

// Record classes. One store, not a tier (FR10).
const (
	RecordEpisodic RecordType = "EPISODIC"
	RecordSummary  RecordType = "SUMMARY"
	RecordSemantic RecordType = "SEMANTIC"
)

// WellFormed reports whether t is a known record class.
func (t RecordType) WellFormed() bool {
	switch t {
	case RecordEpisodic, RecordSummary, RecordSemantic:
		return true
	}
	return false
}

// Derived reports whether the record is recomputable from other records.
func (t RecordType) Derived() bool { return t != RecordEpisodic }

// AclTier is the trust/access level (P0 locked: low/user/system).
type AclTier string

// Trust tiers.
const (
	ACLLow    AclTier = "low"
	ACLUser   AclTier = "user"
	ACLSystem AclTier = "system"
)

// WellFormed reports whether a is a known tier.
func (a AclTier) WellFormed() bool {
	switch a {
	case ACLLow, ACLUser, ACLSystem:
		return true
	}
	return false
}

// Quarantined reports whether writes at this tier enter STAGED (FR14).
func (a AclTier) Quarantined() bool { return a == ACLLow }

// ResolveVerdict resolves a Both-state pair (P0 locked: keep-a/keep-b/merge).
type ResolveVerdict string

// Both-state resolutions.
const (
	ResolveKeepA ResolveVerdict = "keep-a"
	ResolveKeepB ResolveVerdict = "keep-b"
	ResolveMerge ResolveVerdict = "merge"
)

// WellFormed reports whether r is a known resolution.
func (r ResolveVerdict) WellFormed() bool {
	switch r {
	case ResolveKeepA, ResolveKeepB, ResolveMerge:
		return true
	}
	return false
}

// Axis selects the bi-temporal query axis (NFR8).
type Axis string

// Bi-temporal axes.
const (
	AxisValid       Axis = "valid"
	AxisTransaction Axis = "transaction"
)

// WellFormed reports whether a is a known axis.
func (a Axis) WellFormed() bool {
	switch a {
	case AxisValid, AxisTransaction:
		return true
	}
	return false
}

// Code is a fail-closed machine-readable error code.
type Code string

// Error codes.
const (
	CodeNotFound      Code = "NOT_FOUND"
	CodeFloorMiss     Code = "FLOOR_MISS"
	CodeRateLimited   Code = "RATE_LIMITED"
	CodeCorruptIndex  Code = "CORRUPT_INDEX"
	CodeInvalidRecord Code = "INVALID_RECORD"
	CodeConflict      Code = "CONFLICT"
)

// Error is a coded domain error.
type Error struct {
	Code Code
	Msg  string
}

// Error implements the error interface.
func (e *Error) Error() string { return string(e.Code) + ": " + e.Msg }

// E builds a coded domain error.
func E(code Code, msg string) error { return &Error{Code: code, Msg: msg} }

// Interval is a half-open [From, To) range over int64 ticks — unix-nanos
// on the valid axis (caller's world-time assertion), commit sequence on
// the transaction axis (server-assigned). To == OpenEnd means unbounded.
type Interval struct {
	From int64
	To   int64
}

// WellFormed reports whether the interval satisfies 0 <= from < to.
func (iv Interval) WellFormed() bool { return iv.From >= 0 && iv.From < iv.To }

// Open reports whether the interval is still current.
func (iv Interval) Open() bool { return iv.To == OpenEnd }

// Contains reports whether t falls in [From, To).
func (iv Interval) Contains(t int64) bool { return t >= iv.From && t < iv.To }

// Provenance identifies who said it and in which session (FR9 id-refs).
type Provenance struct {
	Source  string
	Session string
}

// WellFormed reports whether both id-refs are present.
func (p Provenance) WellFormed() bool {
	return strings.TrimSpace(p.Source) != "" && strings.TrimSpace(p.Session) != ""
}

// Payload limits: the map carries non-assertive metadata only — the gate
// never branches on payload values (merge unions keys monotonically,
// see P2 gate).
const (
	MaxPayloadKeys   = 64
	MaxPayloadKeyLen = 128
	MaxPayloadValLen = 4096
)

// checkPayload validates an opaque non-assertive metadata map.
func checkPayload(p map[string]string) error {
	if len(p) > MaxPayloadKeys {
		return E(CodeInvalidRecord, "payload exceeds 64 keys")
	}
	for k, v := range p {
		if strings.TrimSpace(k) == "" || len(k) > MaxPayloadKeyLen {
			return E(CodeInvalidRecord, "payload has a bad key")
		}
		if len(v) > MaxPayloadValLen {
			return E(CodeInvalidRecord, "payload value too large")
		}
	}
	return nil
}

// InputRecord is the caller-supplied shape for emit (SRS §1.3).
// It carries no ID and no transaction-time: both are server-assigned
// at commit (server-side clock authority).
type InputRecord struct {
	Subject        string
	Predicate      string
	Object         string
	ValidTime      Interval
	Provenance     Provenance
	ACL            AclTier
	BaseConfidence float64
	CallerTSMeta   int64 // caller-supplied timestamp, metadata only
	Payload        map[string]string
}

// Validate checks the caller-supplied shape.
func (in InputRecord) Validate() error {
	if strings.TrimSpace(in.Subject) == "" || strings.TrimSpace(in.Predicate) == "" {
		return E(CodeInvalidRecord, "subject and predicate are required")
	}
	if !in.ValidTime.WellFormed() {
		return E(CodeInvalidRecord, "valid interval must satisfy 0 <= from < to")
	}
	if !in.Provenance.WellFormed() {
		return E(CodeInvalidRecord, "provenance source and session are required")
	}
	if !in.ACL.WellFormed() {
		return E(CodeInvalidRecord, "unknown ACL tier")
	}
	if math.IsNaN(in.BaseConfidence) || in.BaseConfidence < 0 || in.BaseConfidence > 1 {
		return E(CodeInvalidRecord, "base confidence must be in [0,1]")
	}
	return checkPayload(in.Payload)
}

// Record is a stored truth record (SRS FR3).
type Record struct {
	ID             string // generated, immutable
	Subject        string
	Predicate      string
	Object         string
	ValidTime      Interval // when true in the world (caller's assertion)
	Txn            Interval // when Engram learned it (server-assigned)
	Provenance     Provenance
	DerivedFrom    []string // parent record IDs; empty for episodic
	ACL            AclTier
	Status         Status
	Type           RecordType
	BaseConfidence float64
	CallerTSMeta   int64 // persisted caller claim, never ordering truth
	Payload        map[string]string
}

// Validate checks a stored record's structural invariants.
func (r Record) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return E(CodeInvalidRecord, "record id is required")
	}
	in := InputRecord{
		Subject:        r.Subject,
		Predicate:      r.Predicate,
		Object:         r.Object,
		ValidTime:      r.ValidTime,
		Provenance:     r.Provenance,
		ACL:            r.ACL,
		BaseConfidence: r.BaseConfidence,
		CallerTSMeta:   r.CallerTSMeta,
		Payload:        r.Payload,
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if !r.Txn.WellFormed() {
		return E(CodeInvalidRecord, "txn interval must satisfy 0 <= from < to")
	}
	if !r.Status.WellFormed() {
		return E(CodeInvalidRecord, "unknown status")
	}
	if !r.Type.WellFormed() {
		return E(CodeInvalidRecord, "unknown record type")
	}
	if !r.Type.Derived() && len(r.DerivedFrom) != 0 {
		return E(CodeInvalidRecord, "episodic records carry no derived-from links")
	}
	for _, id := range r.DerivedFrom {
		if strings.TrimSpace(id) == "" {
			return E(CodeInvalidRecord, "derived-from holds a blank id")
		}
	}
	return nil
}

// FeedbackKind distinguishes confidence-only updates (FEEDBACK events).
type FeedbackKind string

// Feedback event kinds.
const (
	FeedbackAttestConfirm    FeedbackKind = "attest-confirm"
	FeedbackAttestDisconfirm FeedbackKind = "attest-disconfirm"
	FeedbackCorrect          FeedbackKind = "correct"
	FeedbackStrength         FeedbackKind = "strength" // RESTRENGTHEN bump
)

// FeedbackEvent is an appended confidence/strength update (FR17:
// replayed lazily at read, never a field overwrite).
type FeedbackEvent struct {
	RecordID string
	Kind     FeedbackKind
	Source   string
	Session  string
	Tick     int64
}

// Validate checks the feedback shape.
func (ev FeedbackEvent) Validate() error {
	if strings.TrimSpace(ev.RecordID) == "" {
		return E(CodeInvalidRecord, "feedback requires a record id")
	}
	switch ev.Kind {
	case FeedbackAttestConfirm, FeedbackAttestDisconfirm, FeedbackCorrect, FeedbackStrength:
	default:
		return E(CodeInvalidRecord, "unknown feedback kind")
	}
	if strings.TrimSpace(ev.Source) == "" || strings.TrimSpace(ev.Session) == "" {
		return E(CodeInvalidRecord, "feedback source and session are required")
	}
	if ev.Tick < 0 {
		return E(CodeInvalidRecord, "tick must be >= 0")
	}
	return nil
}
