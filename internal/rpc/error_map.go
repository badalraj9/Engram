// Domain-to-gRPC error mapping (P5 — fail-closed across the boundary).
//
// The core speaks *rpc.Error (Code + message); the wire speaks gRPC
// status. This is the single translation point, so a new domain code
// that arrives unmapped degrades to Internal (never OK, never an ack)
// and breaks the exhaustiveness test loudly — the mapping cannot
// silently go stale.
package rpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToStatus converts a domain error to a gRPC status error. Non-domain
// errors pass through as Internal (fail closed — an uninterpretable
// failure is never translated into success). Nil stays nil.
func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	var de *Error
	if !errors.As(err, &de) {
		return status.Error(codes.Internal, err.Error())
	}
	switch de.Code {
	case CodeNotFound, CodeFloorMiss:
		return status.Error(codes.NotFound, de.Error())
	case CodeRateLimited:
		return status.Error(codes.ResourceExhausted, de.Error())
	case CodeCorruptIndex:
		return status.Error(codes.DataLoss, de.Error())
	case CodeInvalidRecord:
		return status.Error(codes.InvalidArgument, de.Error())
	case CodeConflict:
		return status.Error(codes.FailedPrecondition, de.Error())
	default:
		return status.Error(codes.Internal, de.Error())
	}
}

// FromStatus recovers the domain Code from a gRPC status error for
// client-side assertions. Non-status errors report false.
func FromStatus(err error) (Code, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return "", false
	}
	var de *Error
	if errors.As(err, &de) {
		return de.Code, true
	}
	switch st.Code() {
	case codes.NotFound:
		return CodeNotFound, true
	case codes.ResourceExhausted:
		return CodeRateLimited, true
	case codes.DataLoss:
		return CodeCorruptIndex, true
	case codes.InvalidArgument:
		return CodeInvalidRecord, true
	case codes.FailedPrecondition:
		return CodeConflict, true
	default:
		return "", false
	}
}
