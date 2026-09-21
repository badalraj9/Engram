# Software Requirements Specification — Engram v2

**Document status:** v1.3 — absorbs DESIGN deltas (five-way gate, `attest`, read-time confidence, server clock, serialized tick, M7/M8). Frozen sections marked; open items marked.

---

## 0. Positioning (locked)

**The wedge is the conjunction, not either side of a false binary:**

> Memory semantics that currently require an LLM in the loop — knows what's still true, knows when it doesn't know, forgets on principle — delivered at infrastructure speed with zero model dependency.

Speed alone is a commodity (any KV store is fast). Semantic memory alone is "mem0 with more prompts." The conjunction is architecturally hard for incumbents to retrofit because LLM-mediation *is* their architecture — removing it means rebuilding, not tuning.

- NFR1–NFR3 (speed, no-LLM, no-vector-DB) = **entry tickets** — necessary, not differentiating.
- Auditability, validated-forgetting, and calibration (NFR8, NFR9, new FR16) = **the differentiator class.**
- External message: "memory that knows what it knows." Internal posture: correctness-first, always — the measurement gate (§7) *is* that posture enforced.

---

## 1. Introduction

### 1.1 Purpose
Engram is an external, model-free memory substrate for AI agents: persistent, associative, self-decaying memory without an LLM call, a vector database, or unbounded growth in the read/write path.

### 1.2 Problem Statement
Current agent memory (context windows, RAG/vector similarity, LLM-mediated layers like mem0) returns topically-similar text, not meaningfully connected, truth-aware knowledge. No native concept of a fact's current truth, confidence, freshness, or relationship to other facts.

### 1.3 Scope
Single-tenant embeddable memory library for v1. **Not:** a vector database, an LLM wrapper, a multi-tenant platform, a general graph database.

**Extraction ownership (resolves G1):** Engram's `emit` accepts **structured records** (subject, predicate, object + metadata), not raw text. Turning raw agent/user text into structured records is the **caller's** responsibility, performed outside Engram's core (e.g., leo-jarvis does one cheap structured-output call before calling `emit`). This keeps NFR2 (no LLM in Engram's own path) intact — the boundary is the API signature, not a promise.

Out of scope for v1: multi-agent shared memory, RBAC beyond simple trust tiers, distributed/clustered deployment, Fellegi-Sunter-style probabilistic entity resolution (see G3).

---

## 2. Users and Use Cases

### 2.1 Primary User
A developer integrating persistent memory into their own AI agent.

### 2.2 Primary Use Cases (v1)
- **UC1:** Agent emits a structured fact; available in future sessions without restating.
- **UC2:** Agent queries for relevant context; gets a small, ranked, currently-true set — or explicit ABSTAIN, never a forced guess.
- **UC3:** A fact becomes false (state change); superseded via close-interval + append, never averaged or blind-overwritten.
- **UC4:** Old, unused, low-value facts fade and are pruned automatically.
- **UC5 (dogfood):** leo-jarvis integrates Engram as first real consumer — validates the API against a real, personal, multi-project workload, via the daemon+RPC path decided in §6, C3.

### 2.3 Explicitly Deferred
Multi-agent belief-sharing, enterprise RBAC/attestation, hosted managed service, probabilistic (FS-style) entity resolution.

---

## 3. Functional Requirements

**API surface (resolves G2 — every behavior below now has an invoking operation):**

| Operation | Signature | Purpose |
|---|---|---|
| `emit` | `emit(record: StructuredRecord, source, session) → {verdict, id}` | Write path; verdict ∈ {SKIP, INSERT, MERGE, UPDATE, RESTRENGTHEN, STAGED} |
| `recall` | `recall(query, budget) → records \| ABSTAIN` | Read path |
| `correct` | `correct(hint, correction) → {id, verdict}` | User-flagged error path (FR5) |
| `attest` | `attest(id, verdict: confirm\|disconfirm, source, session) → {new_confidence, promoted?}` | Verdict-only path on a prior claim (FR18); no replacement content, no caller-supplied weights |
| `resolve` | `resolve(record_a, record_b, verdict)` | Manually resolve a Both-state conflict (FR12) |
| `as_of` | `as_of(query, t, axis: valid\|transaction) → records` | Bi-temporal historical query (NFR8) |
| `consolidate` | `consolidate(tick)` | Manually-triggered consolidation pass — testable, no hidden scheduler (FR10) |
| `inspect` | *(deferred, post-v1)* | Introspection/debugging |

| ID | Requirement |
|---|---|
| FR1 | `emit` accepts a structured record (not raw text — see §1.3); never blocks on heavy processing. |
| FR2 | Deferred work (consolidation, pruning) happens out-of-band from `emit`/`recall`. |
| FR3 | Every stored record: id (immutable), subject, predicate, object, valid-time, transaction-time (server-assigned at commit, never caller-supplied), provenance (source + session as id-refs), ACL tier, status (ACTIVE / SCHEDULED / BOTH / TOMBSTONED), base confidence. Confidence is computed at read time (base + FEEDBACK events from `correct`/`attest`), never mutated in place — see FR17. |
| FR4 | Write path applies a five-way gate — SKIP / INSERT / MERGE (same object, attribute union) / UPDATE-supersede (different object → close old record's interval, append new, mark dependents dirty) / RESTRENGTHEN (identical subject-predicate-object → strength/retrievability bump, no new record; also the FR14 promotion trigger and the crash-retry absorber). **Match semantics (resolves G3):** v1 uses normalized exact-key matching on (subject, predicate) plus MinHash n-gram fuzzy dedup scoped only to the MERGE band (paraphrase catching, e.g. "Acme" vs "Acme Corp"). Probabilistic/Fellegi-Sunter resolution is explicitly deferred (structural-feature independence violations were identified in review; exact-first is the safer v1 default). "Raj" and "Rahul" are distinct records in v1 — no fuzzy cross-entity merging. STAGED records are invisible to the gate matcher; promotion re-runs the gate as fresh. |
| FR5 | Corrections follow a distinct path via `correct`: locate offending record via similarity floor (not always-argmax), labile rewrite within a time window, recompute confidence from new provenance. |
| FR6 | **v1 ranking function, explicit (anti-dud rule: v1 ships index-only, no WM bundle in the first month — left unspecified, this silently degrades to recency-only ranking, which would poison the dogfood verdict through no fault of the architecture):**<br>`score(m\|q) = hits(q,m) · R_m(t)^β · w_trust(source) · (1 + log(1 + corr))`<br>— index-hit count (relevance) × retrievability^β (recency/strength, β≈0.5 to start) × source trust tier × log-dampened corroboration boost. Fixed weights, no learning in v1. Plus: per-subject dedup enforced in top-k (never return multiple records about the same entity in one result set). Decay (`R_m(t)`) recomputed lazily on read, never via background sweep. This same score curve is what FR7's abstention threshold calibrates against — ranker and abstention gate share one curve, not two. |
| FR7 | `recall` abstains (returns ABSTAIN, not best-effort noise) when no candidate clears a calibrated confidence threshold. Threshold calibration is produced by FR16, not invented ad hoc. |
| FR8 | Multi-hop queries served via chained indexed lookups on records — no persistent graph structure built or maintained. |
| FR9 | Confidence combines via noisy-OR across independent corroborating sources, weakest-link (min) across derived/consolidated facts. **Session-clustering enforced first:** witnesses are counted per session-cluster, not per raw record, before noisy-OR is applied — prevents same-session restatements from inflating confidence. |
| FR10 | `consolidate(tick)` (host-triggered only, never self-scheduled) synthesizes derived records (SUM/SEM type) from episodic clusters. Acceptance gated by probe-distortion test (D_probe ≤ D_max). **One store, not a tier** — a derived record differs by type and provenance, not by living in separate storage (strength remains a timescale, not a storage tier — corrects earlier "longer-lived storage" phrasing). Consolidation runs first inside the serialized tick (FR10 → FR11 → FR16 refresh); clustering signal is session+subject co-occurrence, no embeddings; synthesis is deterministic template composition, never LLM generation. |
| FR11 | Pruning runs after consolidation completes in the same `consolidate(tick)`, not as an independently-triggered job. Eviction is economics-based: marginal retrieval value < marginal dilution cost (FR11) — no fixed-count or capacity-bound cutoff. |
| FR12 | Contradictions with no temporal resolution are flagged as an explicit Both-state (Belnap-Dunn), surfaced for resolution via `resolve` or an explicit ask-next-turn — never silently overwritten. |
| FR13 | Records are filtered by ACL **before** entering any working/active computation — no post-hoc filtering of already-combined data. |
| FR14 | Untrusted-source writes enter STAGED verdict (quarantine) until a second independent corroborating signal promotes them. Promotion arrives via RESTRENGTHEN or `attest`-confirm, and requires the promoting source/session to differ from the original write's session-cluster — self-attestation never promotes. |
| FR15 | Write path rate-limited per source/session against flooding. |
| FR16 *(new — resolves G5)* | A single calibration mechanism generates ablation-probes (remove a record, observe behavior change) and serves three consumers: `recall` abstention thresholds (FR7), `consolidate` acceptance testing (FR10), and pruning eviction scoring (FR11). One mechanism, three call sites — not three separate calibration paths. |
| FR17 *(new — resolves G8)* | **Append-only invariant, explicit:** records are never mutated in place. All revision is close-interval-then-append. Strength/retrievability bumps and confidence updates arrive as appended FEEDBACK events replayed lazily at read, not as field overwrites. This is load-bearing for bi-temporal correctness and must be enforced at the storage layer, not just convention. |
| FR18 | `attest` carries verdict-only (confirm/disconfirm) on a record id from a prior `recall`; binary verdict space, no caller-supplied weights. Confirm applies a strength jump; disconfirm does not touch strength. Dispatch: replacement content → `correct`, pure verdict → `attest`, new propositional content → `emit`. |

---

## 4. Non-Functional Requirements

Ordered by role per §0 — entry tickets first, differentiators second.

**Entry tickets (necessary, not sufficient for the wedge):**

| ID | Requirement | Target |
|---|---|---|
| NFR1 | Recall latency | **p99 recall < X ms @ 1M records, end-to-end** (resolves G7 — was untestable as "sub-ms to low-ms" with no scale stated). Bundle-op component already bounded: scalar Go complex multiply at d=2048 ≈ 8μs, so M5's <0.1ms target holds without requiring SIMD. |
| NFR2 | No LLM dependency | Zero model API calls in Engram's own `emit`/`recall`/`consolidate` code paths |
| NFR3 | No vector database dependency | Embeddable KV store only (Badger/Pebble) |
| NFR4 | Memory footprint | Bounded, predictable; working bundle L2-cache-resident |
| NFR5 | Durability | WAL; crash-safe (embedded store's own WAL acceptable) |
| NFR6 | Deployability | **Embeddable Go library OR local service** (weakened from library-only per C3's resolution — see §6) — single Go binary either way, no external service dependency beyond the process boundary |
| NFR7 | Language/runtime | Go |

**Differentiator class (the actual wedge, per §0):**

| ID | Requirement | Target |
|---|---|---|
| NFR8 | Auditability | Every record traceable to source, session, time; supports as-of queries on both time axes |
| NFR9 | Forgetting is default, validated | Demonstrated via M1–M8 (§7), not assumed |
| NFR10 *(new)* | Calibration integrity | Abstention/acceptance/eviction thresholds are all conformally calibrated from FR16's ablation protocol, not hardcoded constants — recalibrated on a rolling window as data drifts |

---

## 5. System Architecture Overview
*(Full detail deferred to the Engram design doc — now critical-path per §8, since G1–G3 resolve into its data-model definitions.)*

```
truth:    flat, discrete, append-only records (bi-temporal, provenance, ACL, strength)
recall:   inverted index + LSH + bitsets — O(terms) lookup
dynamics: VSA working bundle (FHRR, d=2048) — activation, superposition, decay-as-damping
bridge:   cleanup — codebook scan (SDM-style), sub-ms
```
No persistent graph. Litmus test: exact/auditable/deletable/permission-bounded → record; recomputable in bounded time → signal/dynamics.

---

## 6. Constraints and Assumptions

- **C1:** Single-node deployment for v1; no distributed consensus.
- **C2:** Correctness of confidence/activation math is validated empirically (§7), not assumed from theory — real entity vocabularies correlate in ways idealized proofs don't capture.
- **C3 *(resolved — was G6)*:** **Daemon + local RPC over a Unix domain socket, decided.** Consumers are polyglot (TS/Bun, Go, Python class — no single host language), so a pure-Go library would force per-language FFI/cgo fragility. Engram ships as a small Go binary exposing `emit`/`recall`/`correct`/`attest`/`resolve`/`as_of`/`consolidate` over UDS; thin generated stubs wrap it per language (Python wrapper is one example). Go-native consumers may still link it as a library directly.
- **A1:** Developer-consumer is comfortable with either a library import (Go) or a local RPC client (any language) for v1.
- **A2:** leo-jarvis is the reference integration, not the sole design target.

---

## 7. Validation / Measurement Protocol (pre-build gate)

**Working-memory math (M1–M5):**
- **M1:** Superposition capacity — decode accuracy vs. item count; confirm ≥45 items at d=2048.
- **M2:** Correlated real-vocabulary retest — rerun M1 against actual entity strings, not random atoms; recalibrate thresholds from this run, not theory.
- **M3:** Decay legibility — decode accuracy falls monotonically with damping.
- **M4:** Abstention correctness — ambiguous-pair probes trigger abstention at the calibrated rate.
- **M5:** Performance — bundle op < 0.1ms, cleanup < 1ms.

**Record semantics (M6, new — resolves the missing gate on the layer where the one real bug in this whole research arc already occurred: historical queries filtering on current-belief status):**
- **M6:** Bi-temporal property suite —
  - Supersede never mutates a record in place (tests FR17 directly).
  - `as_of(query, t, axis=valid)` results are unaffected by a record's current-belief/retraction status.
  - A Both-state persists until explicitly resolved via `resolve` — never silently collapses to one side.
  - Noisy-OR witness count equals session-cluster count, not raw record count (tests FR9's ordering requirement).

**Storage / durability (M7–M8, ratified from DESIGN):**
- **M7:** write-cost vs. history size — p99 `emit` latency stays flat at 1K/100K/1M records, including through compaction.
- **M8:** crash-and-snapshot suite — kill -9 mid-write, verify replay produces a bit-identical rebuild; verify snapshot-consistent reads under concurrent writes.

Failure at any M-item is a specification event — thresholds/design recalibrate, in that order, not a bug to patch silently.

---

## 8. Open Items

- **Critical path — the Engram design doc**, where G1–G3 finally resolve into concrete schemas. Skeleton (locked, ready to draft against):
  1. Record schema (DDL): fields per FR3, interval encoding, status enum (ACTIVE / SCHEDULED / BOTH / TOMBSTONED), provenance as id-refs, server-assigned transaction-time, read-time confidence
  2. Storage layout: append-only event log as the truth; inverted index / MinHash tables / bitsets as rebuildable derived artifacts — rebuild procedure specified, since "rebuildable" must be demonstrated, not claimed
  3. The v1 ranker (FR6) + its conformal calibration procedure
  4. Write path: five-way gate decision tree with exact thresholds; supersede mechanics; RESTRENGTHEN idempotency; STAGED quarantine flow
  5. `correct()` + `attest()`: locator-floor semantics, labile window, confidence recompute via FEEDBACK events
  6. `consolidate(tick)`: serialized consolidation → pruning → calibration in one tick; clustering signal for v1 is session+subject co-occurrence from the index — **no embeddings**; SUM record synthesis is deterministic template extraction from record fields, never LLM generation — this boundary is where NFR2 is most at risk of quietly eroding, so it's stated explicitly here rather than left implicit
  7. WAL format + recovery semantics (MemoryThread's existing test suite is the ancestor: recovery at N sizes, compaction atomicity)
  8. Failure modes: recall behavior under partial index corruption; emit behavior on log-write failure (answer: no write, no event — fail closed)
  9. RPC surface per C3: the seven operations over UDS; Python client sketch
- Public API beyond the operations in §3 (e.g. `inspect`) — deferred, not designed.
