// Generation provenance (P5 — why proto/engrampb exists and how to
// regenerate it).
//
// proto/engram.proto is the schema authority. proto/engrampb/ is
// genuine protoc output — never hand-edited (an earlier session
// hand-transcribed the schema as v1 struct-tag messages; the new
// protobuf runtime cannot marshal those — it dereferences codegen
// init state — so the transcription was deleted and replaced with
// real codegen; the attempt is recorded so it is not repeated).
//
// Regenerate with a protoc ≥ 30 + matching plugins:
//
//	protoc -I proto --go_out=. --go_opt=module=engram \
//	  --go-grpc_out=. --go-grpc_opt=module=engram proto/engram.proto
//
// then `go mod tidy` (grpc + protobuf are TECH §2.1-mandated deps,
// not stdlib-first violations). Pinned at build: libprotoc 30.2,
// protoc-gen-go v1.36.7, protoc-gen-go-grpc v1.5.1.
package rpc
