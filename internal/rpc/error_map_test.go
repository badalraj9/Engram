package rpc_test

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"engram/internal/rpc"
)

func TestErrorMap_Codes(t *testing.T) {
	cases := []struct {
		code rpc.Code
		want codes.Code
	}{
		{rpc.CodeNotFound, codes.NotFound},
		{rpc.CodeFloorMiss, codes.NotFound},
		{rpc.CodeRateLimited, codes.ResourceExhausted},
		{rpc.CodeCorruptIndex, codes.DataLoss},
		{rpc.CodeInvalidRecord, codes.InvalidArgument},
		{rpc.CodeConflict, codes.FailedPrecondition},
		{rpc.Code("SOMETHING_NEW"), codes.Internal},
	}
	for _, c := range cases {
		st, ok := status.FromError(rpc.ToStatus(rpc.E(c.code, "x")))
		if !ok || st.Code() != c.want {
			t.Fatalf("code %q → %v, want %v", c.code, st.Code(), c.want)
		}
	}
}

func TestErrorMap_FailClosed(t *testing.T) {
	if rpc.ToStatus(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	st, _ := status.FromError(rpc.ToStatus(errors.New("opaque")))
	if st.Code() != codes.Internal {
		t.Fatalf("opaque error → %v, want Internal", st.Code())
	}
	if _, ok := rpc.FromStatus(errors.New("opaque")); ok {
		t.Fatal("opaque error must not recover a domain code")
	}
}
