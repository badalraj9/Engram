# Build Ranks — Engram v2 (importance order)

Tiers: SSS > S > A > B > C > D > E. Ranks reflect v1 correctness impact, not build sequence.

## SSS — loss is total loss
### `internal/truth/` (5)
- store.go
- keys.go
- compaction.go
- store_iface.go
- quarantine.go

## S — wrong verdict corrupts all
### `internal/processing/` (8)
- gate.go
- confidence.go
- ranker.go
- abstain.go
- clusterer.go
- matcher.go
- resolve.go
- acl.go

## A — speed, recovery, ordering
### `internal/index/` (4)
- invert.go
- roaring.go
- minhash.go
- rebuild.go
### `internal/rpc/` (3)
- server.go
- router.go
- types.go
### `proto/` (1)
- engram.proto

## B — tick order + plumbing
### `internal/maintenance/` (3)
- consolidate.go
- prune.go
- calibrate.go
### `internal/intake/` (2)
- emit.go
- correct_attest.go
### `internal/query/` (2)
- recall.go
- asof.go

## C — visibility + tuning
### `internal/telemetry/` (2)
- slog.go
- metrics.go
### `internal/config/` (2)
- config.go
- toml.go

## D — future, stub in v1
### `internal/dynamics/` (3)
- fhrr.go
- codebook.go
- cleanup.go

## E — thin shells
### `cmd/engramd/` (1)
- main.go
### `client/python/` (2)
- engram_pb2.py (generated)
- client.py (thin wrapper)
