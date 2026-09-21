// Package rpc defines Engram v2's internal domain types.
//
// P0 contract: every later phase (P1-P6) builds against these shapes.
// They intentionally do not import generated protobuf code — the wire
// schema in proto/engram.proto maps onto these at the RPC boundary
// (P5), so the core never depends on the transport.
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

// Valid reports whether v is a known gate verdict.
func (v Verdict) Valid() bool {
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

// Valid reports whether s is a known lifecycle state.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusScheduled, StatusBoth, StatusTombstoned:
		return true
	}
	return false
}

// AclTier is the trust/access level (P0 locked: low/user/system).
type AclTier string

// Trust tiers.
const (
	ACLLow    AclTier = "low"
	ACLUser   AclTier = "user"
	ACLSystem AclTier = "system"
)

// Valid reports whether a is a known tier.
func (a AclTier) Valid() bool {
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

// Valid reports whether r is a known resolution.
func (r ResolveVerdict) Valid() bool {
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

// Valid reports whether a is a known axis.
func (a Axis) Valid() bool {
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

// Interval is a half-open [From, To) range over unix-nanos.
// To == OpenEnd means unbounded (still current).
type Interval struct {
	From int64
	To   int64
}

// Valid reports whether the interval satisfies 0 <= from < to.
func (iv Interval) Valid() bool { return iv.From >= 0 && iv.From < iv.To }

// Open reports whether the interval is still current.
func (iv Interval) Open() bool { return iv.To == OpenEnd }

// Contains reports whether t falls in [From, To).
func (iv Interval) Contains(t int64) bool { return t >= iv.From && t < iv.To }

// Provenance identifies who said it and in which session (FR9 id-refs).
type Provenance struct {
	Source  string
	Session string
}

// Valid reports whether both id-refs are present.
func (p Provenance) Valid() bool {
	return strings.TrimSpace(p.Source) != "" && strings.TrimSpace(p.Session) != ""
}

// InputRecord is the caller-supplied shape for emit (SRS §1.3).
// It carries no ID and no transaction-time: both are server-assigned
// at commit (server-side clock authority).
type InputRecord struct {
	Subject        string
	Predicate      string
	Object         string
	Valid          Interval
	Provenance     Provenance
	ACL            AclTier
	BaseConfidence float64
	CallerTSMeta   int64 // caller-supplied timestamp, metadata only
}

// Validate checks the caller-supplied shape.
func (in InputRecord) Validate() error {
	if strings.TrimSpace(in.Subject) == "" || strings.TrimSpace(in.Predicate) == "" {
		return E(CodeInvalidRecord, "subject and predicate are required")
	}
	if !in.Valid.Valid() {
		return E(CodeInvalidRecord, "valid interval must satisfy 0 <= from < to")
	}
	if !in.Provenance.Valid() {
		return E(CodeInvalidRecord, "provenance source and session are required")
	}
	if !in.ACL.Valid() {
		return E(CodeInvalidRecord, "unknown ACL tier")
	}
	if math.IsNaN(in.BaseConfidence) || in.BaseConfidence < 0 || in.BaseConfidence > 1 {
		return E(CodeInvalidRecord, "base confidence must be in [0,1]")
	}
	return nil
}

// Record is a stored truth record (SRS FR3).
type Record struct {
	ID             string // generated, immutable
	Subject        string
	Predicate      string
	Object         string
	Valid          Interval // when true in the world
	Txn            Interval // when Engram learned it (server-assigned)
	Provenance     Provenance
	ACL            AclTier
	Status         Status
	BaseConfidence float64
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
