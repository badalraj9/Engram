# Tech Stack & Component Architecture — Engram v2

**Status:** v1.3 — includes ratified `attest` operation, confidence-as-computed-value (resolves FR3/FR17 conflict), four Badger storage rules, and the payload-shape router. No implementation in this document — component responsibilities and exact technology choices only.

---

## 0. Ground rules this stack must satisfy

Every choice below is constrained by decisions already made in the SRS — nothing here contradicts them:
- No LLM call anywhere in the core path (NFR2)
- No vector database (NFR3)
- Go as the implementation language (NFR7)
- Embeddable library OR daemon+RPC, decided as daemon+UDS+RPC for v1 because consumers are polyglot (C3)
- p99 latency target at scale, sub-ms working-bundle ops (NFR1)

---

## 1. Component Layout (top-level)

```
┌─────────────────────────────────────────────────────────────┐
│                        RPC / API LAYER                       │
│         (UDS, gRPC — exposes the 7 operations)               │
└───────────────┬───────────────────────────────┬─────────────┘
                │                               │
        ┌───────▼────────┐              ┌───────▼────────┐
        │  INTAKE PATH    │              │  QUERY PATH     │
        │ (emit/corr/att) │              │  (recall/as_of) │
        └───────┬────────┘              └───────┬────────┘
                │                               │
        ┌───────▼────────────────────────────────▼────────┐
        │              PROCESSING LAYER                     │
        │  Write Gate · Matcher · Ranker · Abstention Gate  │
        └───────┬──────────────────────┬───────────────────┘
                │                      │
        ┌───────▼───────┐      ┌───────▼───────┐
        │  TRUTH STORE   │◄────►│  INDEX LAYER   │
        │ (append-only)  │      │ (derived,      │
        │                │      │  rebuildable)  │
        └───────┬───────┘      └───────┬───────┘
                │                      │
                └──────────┬───────────┘
                           │
                  ┌────────▼─────────┐
                  │  DYNAMICS LAYER   │
                  │  (VSA working     │
                  │   bundle + cleanup)│
                  └────────┬─────────┘
                           │
                  ┌────────▼─────────┐
                  │  MAINTENANCE      │
                  │  (tick-only,      │
                  │  no scheduler)    │
                  │ (consolidate only)│
                  └───────────────────┘

        ┌───────────────────────────────────────┐
        │        TELEMETRY (cross-cutting)        │
        │  attaches to every layer above          │
        └───────────────────────────────────────┘
```

---

## 2. Component-by-component decisions

### 2.1 RPC / API Layer
**What it does:** exposes `emit`, `recall`, `correct`, `attest`, `resolve`, `as_of`, `consolidate` to external consumers over the process boundary decided in C3.

**[RATIFIED] `attest(id, verdict: confirm|disconfirm, source, session) → {new_confidence, promoted?}`** — a lightweight confidence adjustment on an already-identified record (id from a prior `recall`'s result set). No content rewrite, no labile window — distinct from `correct()`, which replaces content. Confirm applies a BKT-style confidence update plus a strength jump (confirmation is stronger evidence than mere use); disconfirm applies the mirrored BKT update without touching strength. Verdict space is binary only in v1 — no free-form caller-supplied confidence weights (that's a confidence-poisoning surface). Promotion of a STAGED/quarantined record via attest requires the attesting source/session to differ from the original write's — a self-attestation must never promote (reuses the existing session-clustering independence rule from noisy-OR, FR9).

**Router (dispatch key = payload shape, not intent classification):** contains replacement content targeting a prior claim → `correct()`; pure verdict on a prior claim → `attest(id)`; new propositional content → `emit()`. Bias: ambiguous short verdict-like utterances route to `attest` over `emit` — a missed attestation costs one skipped confidence bump; a verdict wrongly emitted as a new statement manufactures a garbage record. Asymmetric cost, resolved toward the cheaper mistake.

**[RATIFIED] Server-side clock authority:** transaction-time is assigned by Engram at commit — caller-supplied timestamps are stored as metadata only, never as ordering truth. This is also what makes crash-and-retry safe with zero new idempotency machinery: a retried `emit` carries identical (subject, predicate, object) and is absorbed by the existing RESTRENGTHEN case in the write gate (§2.4), not treated as a duplicate.

**Stack:**
- **Transport:** Unix Domain Socket (not TCP — no network stack needed for a local-only daemon, lower latency, filesystem-permission-based access control fits the ACL model naturally)
- **Protocol:** gRPC over protobuf. Decision, not the JSON-RPC alternative — protobuf gives a typed schema shared between the Go server and any client language, and grpc-go + grpc clients support UDS transport directly. This auto-generates per-language client stubs instead of hand-writing a wire protocol.
- **Why not HTTP/REST:** unnecessary serialization overhead and no schema enforcement for a local-only daemon; protobuf's binary format matters at the latency budget you're targeting. Client stubs are generated per consumer language (Python wrapper is one example).

### 2.2 Intake Path (emit, correct, attest-router)
**What it does:** receives structured records/verdicts from the RPC layer, never touches raw text (extraction ownership sits outside Engram per SRS §1.3). `emit`/`correct` route into the Processing Layer's write gate; `attest` bypasses the gate entirely and goes direct to confidence resolution as a FEEDBACK event (gate input would corrupt verdict counting).

**Stack:** pure Go, no external library — this is orchestration logic (validate shape, attach source/session/timestamp metadata, hand off to the gate). A single-writer goroutine per logical shard (see 2.5) preserves append-order guarantees without needing a distributed lock.

### 2.3 Query Path (recall, as_of)
**What it does:** receives a query, drives it through indexed lookup → ranking → abstention check → optional dynamics-layer touch, returns records or ABSTAIN.

**Stack:** pure Go orchestration; read path uses concurrent goroutines fanned out across index shards, merged before ranking (Go's native concurrency primitives — goroutines + channels — no external concurrency library needed).

### 2.4 Processing Layer
This is the layer with the most sub-components — breaking it out explicitly since "processing" was named directly.

| Sub-component | Responsibility | Tech choice |
|---|---|---|
| **Write Gate** | Five-way decision (SKIP/INSERT/MERGE/UPDATE/**RESTRENGTHEN**) per FR4 | Pure Go decision tree, no dependency. RESTRENGTHEN (identical subject+predicate+object arriving again) bumps strength/retrievability instead of falling through undefined between MERGE and UPDATE — also the FR14 promotion trigger for staged records, and what makes crash-retry safe (see §2.1) |
| **Confidence resolution** | Resolves the FR3 ("confidence is a record field") vs. FR17 (append-only, never mutate) conflict | **[RATIFIED]** Confidence is not stored as mutable state — it's computed at read time from a base value plus appended FEEDBACK events (from `attest`/`correct`), lazily, same pattern as decay. Keeps `as_of` auditability intact (historical confidence is reconstructable by replaying events up to a point in time); compaction folds old events into snapshots without losing this |
| **Matcher** | Exact-key match + MinHash n-gram fuzzy dedup (MERGE band only) | Custom Go MinHash implementation (small, ~1 file's worth of logic — no need for a heavy external library given the scoped-down v1 matching rules) |
| **Ranker** | Computes `score(m\|q)` per FR6's formula | Pure Go, operates directly on index hit-counts + strength values already in the truth store — no separate service |
| **Abstention Gate** | Applies the calibrated threshold from FR16 against the ranker's own score curve | Pure Go; threshold value itself is a config-loaded float, refreshed by the maintenance tick's calibration job |
| **Session Clusterer** | Groups pulses by session before noisy-OR combination (FR9's ordering requirement) | Pure Go, keyed lookup against provenance fields already on each record |

### 2.5 Truth Store (the append-only layer)
**What it does:** the authoritative record log — id, bi-temporal fields, provenance, derived-from links, ACL, status, type, base confidence, caller meta, opaque payload (FR3), append-only (FR17).

**Stack:**
- **Embedded KV engine: BadgerDB.** Decision over Pebble and over SQLite:
  - Pure Go (no cgo) — matches NFR7 and avoids exactly the fragility C3 was designed to dodge
  - LSM-tree structure fits the write-heavy, point-read-heavy access pattern (every `emit` is a write; every `recall` hits point/prefix reads, not range scans or joins)
  - Built-in multi-versioning (Badger keeps multiple timestamped versions per key) maps naturally onto transaction-time versioning — the append-only/close-interval-then-append pattern (FR17) sits on top of this cleanly instead of fighting it
  - Own WAL and crash-recovery already built in (NFR5) — no need to hand-roll a durability layer
- **Key layout:** primary key = logical prefix + commit sequence (uint64, big-endian) - every version is a distinct key, so compaction can never merge multi-version history (by construction, not by flag). The sequence namespace is global across all prefixes (one clock, one reverse-seek recovery). Secondaries are versioned: key = `(subject,predicate)+seq` / `(object,predicate)+seq` with length-prefixed framing (never naive concatenation) - history lives everywhere by construction; latest is a reverse-seek on the secondary prefix, historical lookup a prefix scan + interval filter, satisfying FR8's "multi-hop via chained indexed lookups" without any graph structure. Record IDs (UUIDv4, opaque) live in the value; the ID→seq lookup is derived state - rebuilt with the index, never logged, never required for replay. [SUPERSEDED 2026-09-21: this bullet previously read "secondary key prefixes" with no versioning semantics; replaced per the P1 versioned-secondaries lock — old wording kept here so nothing is lost.]
- **Sync boundary:** group commit over the WAL with an explicit sync boundary; acknowledgment implies durability (nothing confirmed before its covering sync returns). Pinned engine: `dgraph-io/badger/v4` — the exact sync primitive (per-txn sync flag vs `db.Sync`) is verified against the pinned version's docs in P1 and recorded there, not assumed.

**[RATIFIED] Four storage rules governing Badger's use, each load-bearing:**
1. **Log-only separation** — the Truth Store holds only truth records; every derived structure (inverted index, MinHash tables, bitsets) lives separately and is rebuildable from this log alone. Never let a derived index's data live in the same keyspace as truth.
2. **Anti-densification gate under compaction** — Badger's compaction must not be allowed to silently collapse multi-version history in a way that breaks bi-temporal replay; compaction settings are validated by M7/M8 (below), not assumed safe by default.
3. **MVCC ≠ transaction-time** — Badger's internal version timestamps are a mechanism for snapshot isolation and multi-versioning, never Engram's own transaction-time field. The two coexist cleanly (snapshots give free consistency for concurrent reads — this is what resolves the correction-vs-consolidation seam with zero locking — but Engram's bi-temporal semantics are tracked in application-level fields, not inferred from Badger's internal clock).
4. **Escape-hatch interface** — the Truth Store is accessed through an internal interface, not called directly everywhere, so swapping the embedded engine later (if Badger ever proves wrong) doesn't require touching every call site.

### 2.6 Index Layer (derived, rebuildable)
**What it does:** everything that makes `recall` fast — must be fully reconstructable from the Truth Store alone, per the design doc's explicit requirement that "rebuildable" be demonstrated, not claimed.

| Sub-component | Tech choice | Note |
|---|---|---|
| **Inverted index** (term → record IDs) | Custom Go structure, backed by Badger secondary keys or a separate in-memory map snapshotted periodically | Rebuild = replay the Truth Store's log from the start |
| **Postings-list compression / set ops** | `github.com/RoaringBitmap/roaring` | Needed for fast intersection when chaining multi-hop lookups and for visited-tracking during any bounded traversal |
| **MinHash/LSH tables** (fuzzy dedup candidate generation) | Custom Go, small, purpose-built to the exact matching rule in FR4 | No general-purpose LSH library needed given the narrow v1 scope |

### 2.7 Dynamics Layer (VSA working bundle + cleanup)
**What it does:** the transient, per-query working memory — activation, superposition, decay-as-damping, cleanup/decode.

**Stack:**
- **Vector representation:** Go's native `complex128` type — FHRR is elementwise complex multiplication, which is a direct fit for Go's built-in complex arithmetic with no external math library required.
- **No FFT dependency** — this was the whole point of choosing FHRR over HRR (circular convolution needs FFT; FHRR doesn't).
- **Cleanup/codebook:** an in-memory map from atom hash → phasor vector, pre-clustered via the same LSH machinery as 2.6 so cleanup scans touch one bucket, not the whole codebook.
- **Lifecycle:** entirely in-process, in-memory, per-query or per-session — never persisted (per the litmus test: dynamics are recomputable, not stored).

### 2.8 Maintenance Operations (tick-only — the daemon does not schedule itself)
**What it does:** performs consolidation, pruning, and threshold recalibration — but only ever when explicitly invoked via the `consolidate(tick)` RPC operation. **No internal timer, no `time.Ticker`, no background goroutine that wakes itself up.** This is a frozen SRS requirement (FR10: "manually-triggered... no hidden scheduler"), not a stack-doc preference — the host (leo-jarvis's cron, a test harness, a CLI) owns the clock; Engram is a passive organ that runs maintenance work only when asked, exactly like `emit`/`recall`. An earlier draft of this document added an automatic in-process ticker "for convenience in production" — that silently contradicted FR10 and has been removed. If automatic cadence is ever wanted, it's a one-line addition on the host side (a cron entry calling the RPC op), not a capability the daemon grows for itself.

**Stack:** no scheduling library of any kind. Three jobs, invoked synchronously by whatever calls `consolidate(tick)`:
- **Consolidation job** — clusters by session+subject co-occurrence from the index (no embeddings), applies the probe-distortion acceptance test, writes derived SUM records via deterministic template composition (never generation)
- **Pruning job** — economics-based eviction per FR11
- **Calibration job** — refreshes FR16's ablation-probe-derived thresholds (abstention, acceptance, eviction) on a rolling window per NFR10

### 2.9 Telemetry (cross-cutting)
**What it does:** the observability the request named explicitly — latency, throughput, calibration drift, decay/pruning activity — attached to every layer above, not a separate pipeline.

**Stack:**
- **Structured logging:** Go's standard `log/slog` — no external logging library needed
- **Metrics:** an in-process counter/histogram registry (Go's `expvar` or a minimal custom registry) tracking per-operation latency (feeds NFR1's p99 measurement directly), write-gate verdict counts, abstention rate, consolidation acceptance/rejection counts, pruning eviction counts, and calibration drift (ECE-style tracking on the ranker's own score curve)
- **Exposure:** metrics readable via the `inspect` operation (deferred in SRS §3 but this is its natural home) and optionally exported in Prometheus text format for anyone running Engram with external monitoring — this is additive, not a dependency the core system needs to function
- **No external telemetry service required** — this stays local-only, consistent with the no-managed-service scope decision

### 2.10 Configuration
**What it does:** holds the tunable constants that the SRS and WM spec both flagged as fit-not-guessed (β, thresholds, d=2048, cap=64, etc.) so they're recalibratable without a rebuild.

**Stack:** a single TOML config file loaded at daemon startup, overridable via environment variables — Go standard library or a minimal parser (`BurntSushi/toml`), no heavier config framework justified at this scope.

---

## 3. What's deliberately absent

Stated explicitly so it's a decision, not an oversight:
- **No message queue / event bus** — single-process daemon, in-memory channels between components are sufficient at this scale
- **No separate cache layer** — Badger's own block cache plus the in-memory index structures cover this; adding Redis or similar would violate NFR3's spirit even though it's not literally a vector DB
- **No container orchestration concern at this stage** — this is a single-binary daemon; deployment topology beyond "runs as a local process" is out of scope until multi-tenant/distributed use cases are revisited (explicitly deferred in SRS §2.3)
- **No ORM or query-builder** — Badger is a KV store; the Index Layer's structures are hand-built for the exact access patterns needed, not general-purpose querying
- **No self-initiated scheduling of any kind** — the daemon never wakes itself up. Maintenance work happens only when a host explicitly calls `consolidate(tick)`. This was a frozen SRS decision (FR10); a background ticker was drafted into an earlier version of this document and has been reverted (see §2.8) — recorded here as a standing reminder, since "it's just one convenient ticker" is exactly the failure mode that grew MemoryThread to 35 service modules

---

## 4. Summary table

| Layer | Technology |
|---|---|
| RPC/API | gRPC over UDS, protobuf schema |
| Intake/Query orchestration | Go stdlib concurrency (goroutines/channels) |
| Write Gate / Matcher / Ranker | Pure Go, custom logic |
| Truth Store | BadgerDB (embedded, pure Go, LSM-tree, built-in WAL + MVCC) |
| Index Layer | Custom inverted index + `RoaringBitmap/roaring` + custom MinHash/LSH |
| Dynamics Layer | Go native `complex128`, custom FHRR ops, no FFT |
| Maintenance ops | None (host-triggered via `consolidate(tick)` RPC only — no in-process scheduler) |
| Telemetry | Go stdlib `log/slog` + minimal metrics registry, optional Prometheus text export |
| Config | TOML file + env override |
| Client | Generated gRPC stubs per language (Python wrapper is one example) |
