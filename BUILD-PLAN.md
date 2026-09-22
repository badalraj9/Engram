# Build Plan — Engram v2 (testable chunks)

Each phase exits with a runnable, tested slice. No test-compat hacks — phases use real outputs of prior phases. v2 scope: queryable, auditable memory with ABSTAIN; SUMMARY distillation ships, SEMANTIC production and any learning loop defer to v1.1 with FHRR. Recall is lexical in v2 (term hits + ranker); semantic recall is the v1.1 slot.

## P0 contracts — compile gate
- `proto/engram.proto`, `internal/rpc/types.go`, `internal/truth/store_iface.go`, `internal/config/`
- Locked: ACL tiers `low/user/system`; resolve verdict `keep-a/keep-b/merge`; tick `int64` monotonic counter, host-incremented, single-writer (C1 single-node, single-writer per shard); record type `EPISODIC/SUMMARY/SEMANTIC` + `derived_from` ids; opaque gate-invisible `payload`; `caller_ts_meta` persisted, never ordering truth.
- Exit: shapes compile (server clock, id-refs, status enum, 7 op signatures).

## P1 truth — log is law
- `internal/truth/store.go, keys.go, compaction.go`
- Locked: IDs UUIDv4-opaque + sequence==txn (one clock, global seq namespace across all prefixes); group commit with ack-after-sync; supersede as one atomic entry; CRC32 per entry, validate-at-our-boundary, loud truncation; codec hand-rolled versioned binary (golden-bytes test + reject-unknown-version, fail-closed); versioned secondaries (latest via reverse-seek, history via prefix scan); `as_of` exact-key + interval filter only (term search is P3).
- Exit: append / close-interval / `as_of` reads + M8a (kill-9 → replay → bit-identical; torn-supersede truncation with no partial apply; key-uniqueness property across appends and supersedes).

## P2 write — verdicts before index
- `internal/processing/gate.go, confidence.go, clusterer.go, matcher.go, acl.go`
- Pure-Go tests, no DB/RPC, no P3 import. Gate rules: STAGED invisible to matcher; MERGE unions payload monotonically (missing keys only); never derive from SUMMARY/SEMANTIC inputs; matcher is pure (in-memory fixtures only); confidence.go is side-effect-free (scores only — verdicts from fixed gate rules, thresholds via FR16 only). {NEW AFTER CONSIDERATION 2026-09-22 — PROVISIONAL CONSTANTS (v1-fixed, recalibrate without rebuild): three numbers are agent-set, not doc-sourced — the docs are structurally silent here (FR16's calibrated list is closed: abstention/acceptance/eviction only; BKT ships parameterless; "n-gram" ships without n). (1) `FuzzyMergeFloor` (matcher MERGE-band floor): behaviorally anchored — "Raj"/"Rahul" must fall below (SRS FR4/DESIGN §4), "Acme"/"Acme Corp" above; the value is a setting, the pair-fixtures are the spec. (2) `AttestGain` (BKT fixed step in `AttestUpdate`): monotonic, bounded [0,1], no other constraint. (3) trigram `n=3`: least load-bearing, any reasonable n works. All three are named consts (never literals), marked in-code with recalibration path dogfood (UC5) → M-gates/FR16 → TECH §2.10 config; tests pin behavior-at-the-setting with both-sides cases, so recalibration changes one line, never logic. Future sessions: do not re-dig the docs for these — this note is the record.}
- Exit: 5 verdicts, STAGED invisible to matcher, session-cluster counts, FEEDBACK replay, ACL pre-filter helper tested. {NEW AFTER CONSIDERATION 2026-09-22: exit tests are complete-case assertions over the locked DESIGN §4 table — session-split SKIP/RESTRENGTHEN on the (Source, Session) composite, disjoint-time append, content-delta MERGE (payload-add and valid-time-widen directions, narrowing-ack rejection), max-severity fold with multi-candidate fixtures, tier-authority row (subordinate content-write → BOTH), witness-set spanning lineage + FEEDBACK.}

{BUILT 2026-09-23 — P2 implementation: `internal/processing/` (`acl.go`, `clusterer.go`, `matcher.go`, `confidence.go`, `gate.go` + 5 mirror test files), rpc + stdlib imports only. 25/25 tests green; `build + vet + fmt` clean; P1 suite untouched and green. Baselines (i5-12450H, 200 candidates): Match 48µs exact-path, Decide 763µs mixed (trigram-dominated unindexed scan — P3's index replaces it). Guards shipped: union-cap fail-closed at decision time, BOTH-join without stacking, unknown-tier fail-closed both sides, floor anchors pinned as computed values (0.25 derived from the (0.2, 0.286] window). Outcome→P1 execution mapping verified against store code (INSERT→Append, UPDATE/MERGE→Supersede, RESTRENGTHEN→AppendFeedback, SKIP→nothing, tier-blocked→Append+Restatus×2).}

{NEW AFTER CONSIDERATION 2026-09-22 — P2→P3 GATE (do not start P3 code until blockers 1–3 close): BLOCKERS: (1) Staged representation + index visibility — P2 holds quarantine as an out-of-band set, but P1 stores staged records ACTIVE with no marker, so a P3 rebuild-from-log cannot distinguish them: decide the set's durable home (Meta prefix vs separate Badger prefix vs query-time filter) and whether the index includes staged records (filtered at query) or excludes them (filtered at build). {DECIDED 2026-09-22 (owner): Option A — quarantine set under the Meta prefix (`M/quarantine/<id>`), staged Append + quarantine entry commit in ONE Badger txn (never one without the other); the index EXCLUDES staged records at build (a missing entry can leak nothing); promotion peels the entry and the next index update includes the record. Separate-prefix tray and memory-only set rejected as heavier and crash-unsafe respectively.} (2) Candidate-set completeness — P3 lookups must return FULL live-candidate sets per key, not top-1: the gate fold needs every severity (conflicts and corroborations) to route by max severity; a top-1 lookup starves the fold. {DECIDED 2026-09-22 (owner): full sets — the gate routes what it is shown, and any top-N rule has a failing Tuesday; per-key sets are a handful of versions, so completeness is cheap.} (3) P2 committed (process gate). SCHEDULED, explicitly not blockers: provisional-constants recalibration (UC5 → gates → config); tick-sweeper spec (P5); trust-floor adversarial pass (pre-P4); index-corruption trigger (pre-v1); NFR1 X value (needs M5/M7). RECOMMENDED pre-P3 but optional: outside adversarial review of P2 code (the pass that hardened P1) — run before P3 or accept P2-rework risk under P3 conformance.}

## P3 find — rebuildable index
- `internal/index/invert.go, roaring.go, minhash.go, rebuild.go`
- Exit: emit → index → snapshot-offset replay rebuild; chained lookup for FR8 (multi-hop without graph). {NEW AFTER CONSIDERATION 2026-09-22: P3 conformance — the index MUST apply P2's `Normalize` when building and looking up keys, or gate-match and index-lookup diverge (P1 secondaries use raw strings; normalization lives in the gate). P3's exit test replays P2's matcher fixtures and asserts identical verdicts.}

## P4 read — score or abstain
- `internal/processing/ranker.go, abstain.go, resolve.go`
- `internal/intake/emit.go (incl. FR15 rate limit, pre-gate token bucket), correct_attest.go`
- `internal/query/recall.go (ACL pre-filter before ranker per FR13), asof.go`
- Exit: FR6 score curve, per-subject dedup, ABSTAIN gate, `resolve()` Both-state transitions, M6 suite.

## P5 serve + maintain — transport plus serialized tick
- `internal/rpc/server.go, router.go` (payload-shape dispatch: content → `correct`, verdict → `attest`, new → `emit`; `resolve` routed to `resolve.go`)
- `internal/maintenance/consolidate.go, prune.go, calibrate.go` (serialized tick: consolidate → prune → calibrate, no ticker)
- `internal/telemetry/`
- Exit: 7 ops over UDS, tick ordering proven, no background goroutine. Prune invariant: never removes a live EPISODIC record or any record reachable from a live DerivedFrom chain. RPC review checklist (structural ack rule): single handler path append→Sync→respond with no early return. Emergency repair op (operator-only mid-log truncation) specified here when P5 is built — the unexported primitive has no other future caller. {NEW AFTER CONSIDERATION 2026-09-22: plus the tick invariant-audit sweeper — the serialized consolidate pass audits the DESIGN §4 gate invariant (no pre-existing diff-object overlaps outside BOTH-state) as defense-in-depth at host cadence (FR10), routing violations to BOTH; never a substitute for the gate.}

## P6 harden — full gates
- `internal/dynamics/` stubbed (FHRR deferred to v1.1+), `cmd/`, `client/`
- Exit: M1–M5, M7, M8b (snapshot-consistent reads under concurrent writes). Permanent suite carries reject-path cases for every cap (64-key, blank-key, 4KB-value).

## Locked resolutions
1. `resolve()` logic lives in `processing/resolve.go` (P4); transport only in P5.
2. M8 split: M8a exits P1, M8b exits P6.
3. ACL helper owns in `processing/acl.go` (P2); enforced in `query/recall.go` before ranker.
4. FR15 rate limit lives in `intake/emit.go` (P4), pre-gate.
