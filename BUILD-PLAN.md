# Build Plan — Engram v2 (testable chunks)

Each phase exits with a runnable, tested slice. No test-compat hacks — phases use real outputs of prior phases.

## P0 contracts — compile gate
- `proto/engram.proto`, `internal/rpc/types.go`, `internal/truth/store_iface.go`, `internal/config/`
- Locked: ACL tiers `low/user/system`; resolve verdict `keep-a/keep-b/merge`; tick `int64` monotonic counter, host-incremented.
- Exit: shapes compile (server clock, id-refs, status enum, 7 op signatures).

## P1 truth — log is law
- `internal/truth/store.go, keys.go, compaction.go`
- Exit: append / close-interval / `as_of` reads + M8a (kill-9 → replay → bit-identical).

## P2 write — verdicts before index
- `internal/processing/gate.go, confidence.go, clusterer.go, matcher.go, acl.go`
- Pure-Go tests, no DB/RPC.
- Exit: 5 verdicts, STAGED invisible to matcher, session-cluster counts, FEEDBACK replay, ACL pre-filter helper tested.

## P3 find — rebuildable index
- `internal/index/invert.go, roaring.go, minhash.go, rebuild.go`
- Exit: emit → index → snapshot-offset replay rebuild; chained lookup for FR8 (multi-hop without graph).

## P4 read — score or abstain
- `internal/processing/ranker.go, abstain.go, resolve.go`
- `internal/intake/emit.go (incl. FR15 rate limit, pre-gate token bucket), correct_attest.go`
- `internal/query/recall.go (ACL pre-filter before ranker per FR13), asof.go`
- Exit: FR6 score curve, per-subject dedup, ABSTAIN gate, `resolve()` Both-state transitions, M6 suite.

## P5 serve — transport, no new logic
- `internal/rpc/server.go, router.go` (payload-shape dispatch: content → `correct`, verdict → `attest`, new → `emit`; `resolve` routed to `resolve.go`)
- `internal/maintenance/consolidate.go, prune.go, calibrate.go` (serialized tick: consolidate → prune → calibrate, no ticker)
- `internal/telemetry/`
- Exit: 7 ops over UDS, tick ordering proven, no background goroutine.

## P6 harden — full gates
- `internal/dynamics/` stubbed (FHRR deferred to v1.1+), `cmd/`, `client/`
- Exit: M1–M5, M7, M8b (snapshot-consistent reads under concurrent writes).

## Locked resolutions
1. `resolve()` logic lives in `processing/resolve.go` (P4); transport only in P5.
2. M8 split: M8a exits P1, M8b exits P6.
3. ACL helper owns in `processing/acl.go` (P2); enforced in `query/recall.go` before ranker.
4. FR15 rate limit lives in `intake/emit.go` (P4), pre-gate.
