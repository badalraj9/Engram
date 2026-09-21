# DESIGN.md — Engram v2

**Status:** v1.1 — M7/M8 ratified into SRS v1.3, client stubs generalized to polyglot. Per the project's authority hierarchy: this document specifies *how*; it does not reopen *what/why* (SRS) or *with-what* (tech stack doc). Every item below is [RATIFIED] unless marked [PROPOSED] or [OPEN].

---

## 1. Record Schema

**Status field enum:** `ACTIVE | SCHEDULED | BOTH | TOMBSTONED`
- `ACTIVE` — current belief
- `SCHEDULED` — reserved for records where the *event itself* is future-dated (e.g. "I start in June") — **not** for present-tense statements of intent ("I'll run M1 next week" is an ACTIVE record now; its content merely references a future action). This distinction was corrected mid-design after an earlier draft wrongly planned SCHEDULED status for all future-referencing content.
- `BOTH` — Belnap-Dunn unresolved-contradiction state; persists until explicitly resolved via `resolve()`, never silently collapsed to one side
- `TOMBSTONED` — closed via correction or supersede; retained for historical/`as_of` queries, never deleted

**Fields per record:**
| Field | Notes |
|---|---|
| id | generated, immutable |
| subject, predicate, object | the claim |
| valid-time interval `[t-, t+)` | when true in the world |
| transaction-time interval `[k-, k+)` | when Engram learned it — **assigned server-side at commit, never caller-supplied** (this is also the mechanism that makes crash-retry safe — see §4) |
| provenance | source + session id(s), as id-refs (supports session-clustering for FR9) |
| ACL tier | trust/access level |
| status | enum above |
| base confidence | starting value at write time |

**Confidence is not a mutable field. [RATIFIED — resolves a real FR3/FR17 conflict.]** FR3 originally implied confidence lives as record state; FR17 forbids in-place mutation. Resolution: confidence is *computed at read time* — base confidence plus all FEEDBACK events (from `attest`/`correct`) appended against this record's id, replayed lazily, same pattern as decay. This preserves `as_of` auditability (historical confidence is reconstructable) and compaction folds old events into snapshots without losing that property.

---

## 2. Storage Layout

- **Truth Store:** append-only, BadgerDB-backed. Governed by the four storage rules in the tech-stack doc §2.5 (log-only separation, anti-densification under compaction, MVCC not transaction-time, escape-hatch interface).
- **Derived structures** (inverted index, MinHash tables, roaring bitsets): fully rebuildable from the Truth Store log alone.
- **Rebuild procedure — the replay-vs-live-writes rule [RATIFIED]:** replay begins by recording the log's current tail offset as a snapshot boundary. Replay processes only up to that offset. Live writes arriving during replay append normally, past the boundary, and are applied directly against the still-serving old index. Once replay reaches the snapshot offset, the new index atomically swaps in, then drains whatever accumulated past the boundary, in log order. This prevents both double-apply and silent drop — the failure modes an unspecified replay procedure would otherwise hit.

---

## 3. The v1 Ranker

```
score(m|q) = hits(q,m) . R_m(t)^beta . w_trust(source) . (1 + log(1 + corr))
```
— index-hit count (relevance) times retrievability^beta (beta ~ 0.5 to start, fixed, no learning in v1) times source trust tier times log-dampened corroboration boost. Per-subject dedup enforced in top-k (never return multiple records about the same entity in one result set). Decay (`R_m(t)`) computed lazily on read, never via background sweep.

**This score curve is what FR7's abstention threshold calibrates against directly** — ranker and abstention gate share one curve, not two independently-tuned ones.

**[PROPOSED — not yet resolved] Trust floor re-attack:** the multiplicative form can drive a once-stated, never-repeated, clearly-asserted user fact toward near-zero score (low corroboration times moderate decay), making it a prune candidate despite the user's single statement being a strong trust signal in its own right. Candidate fix: a floor on the trust term for user-sourced facts specifically. Needs an adversarial pass (hand-crafted edge-case queries against the formula) before it's ratified — flagged, not yet fixed.

**Cold vs. warm recall modes [PROPOSED, gated with the WM bundle, v1.1+]:**
- **Warm:** the working bundle's current state serves as the spreading seed — session-adjacent records found in one hop, looser abstention.
- **Cold:** query-terms-only seed, strict abstention, cascade-risk discipline.
- **Free v1 partial win:** records touched this session already carry elevated `R_m(t)` via the half-life math alone, so the ranker naturally favors warm context even before the bundle-based mode distinction ships.
- **Latency budget as an exposed parameter, not a fixed constant** — cleanup/settling quality is genuinely compute-time-dependent; the system should let callers trade latency for quality explicitly.

---

## 4. Write Path

**Five-way gate** (four from FR4 plus the case found during seam audit):
```
SKIP         - redundant, low novelty/surprise
INSERT       - new (subject, predicate), no match
MERGE        - matches, same object -> attribute-union
UPDATE       - matches, different object -> close old record's interval, append new, mark provenance successors dirty
RESTRENGTHEN - matches, IDENTICAL (subject, predicate, object) -> bump strength/retrievability, no new record
```

**Match semantics (v1):** normalized exact-key match on (subject, predicate), plus MinHash n-gram fuzzy dedup scoped *only* to the MERGE band (catches paraphrase, e.g. "Acme" vs "Acme Corp"). Fellegi-Sunter-style probabilistic resolution is explicitly deferred — structural-feature independence violations were identified during research and exact-first is the safer v1 default. "Raj" and "Rahul" remain distinct records in v1.

**RESTRENGTHEN is load-bearing, not cosmetic:** it's also the FR14 promotion trigger for STAGED/quarantined records (a second independent corroborating signal), and it's what makes crash-and-retry safe with zero new idempotency machinery — a retried `emit` after a crash-before-ack carries identical content and is absorbed here, provided transaction-time is server-assigned (§1).

**Three concurrency rules found during seam audit, all now fixed:**
1. **Corrections hold a priority lock over concurrent supersede** attempts on the same record during the labile correction window — supersede defers, never races.
2. **Staged/quarantined records are invisible to the write-gate matcher** — prevents a poisoned staged record from contaminating gate decisions on legitimate incoming writes. Promotion re-runs the gate as if the record were fresh.
3. **The labile correction window expires lazily, on next touch** — not via a timer, consistent with the tick-only/no-hidden-scheduler principle.

**Rate limiting** per source/session against flooding (FR15) sits here, unchanged from the SRS.

---

## 5. Correction and Attestation Paths

**`correct(hint, correction)`** — for content that's *wrong*. Locates the offending record via a similarity floor over the hint (not always-argmax, which risks silently rewriting an unrelated record — floor failure falls back to INSERT as new content instead). Labile rewrite within a time window; confidence recomputed from new provenance.

**`attest(id, verdict: confirm|disconfirm, source, session) -> {new_confidence, promoted?}`** [RATIFIED] — for a verdict on content that's *not* being replaced. Distinct from `correct` (see comparison table below). BKT-style confidence update; confirm also applies a strength jump (confirmation is stronger evidence than mere use), disconfirm doesn't touch strength. Binary verdict space only in v1 — no caller-supplied confidence weights (a confidence-poisoning surface otherwise). Promotion via attest requires the attesting source/session to differ from the original write's session-cluster — a self-attestation must never promote a STAGED record.

| | `correct` | `attest` |
|---|---|---|
| Locator | similarity floor over hint | id from a prior `recall`'s result set |
| Payload | replacement content | verdict only |
| Effect | rewrite content, recompute confidence from new provenance | confidence-only update via FEEDBACK event |
| Example | "it's Pebble, not Badger" | "yep, exactly" / "no, that's wrong" |

**Router dispatch key = payload shape**, not intent classification: replacement content targeting a prior claim -> `correct`; pure verdict on a prior claim -> `attest`; new propositional content -> `emit`. Ambiguous short verdict-like utterances bias toward `attest` over `emit` (asymmetric cost: a missed attestation costs one skipped confidence bump; a verdict wrongly emitted as a statement manufactures a garbage record).

**`resolve(record_a, record_b, verdict)`** — manual resolution of a BOTH-state conflict (Belnap-Dunn), per §1's status enum.

---

## 6. Consolidation and Pruning

**`consolidate(tick)`** — host-triggered only, never self-scheduled (frozen FR10). One maintenance pass performs, **in this order, serialized within the single tick:**
1. **Consolidation** — clusters by session+subject co-occurrence from the index (no embeddings). Synthesizes derived SUM/SEM records via **deterministic template composition from record fields — never LLM generation.** This boundary is the one most at risk of quietly eroding over time (template extraction "sounds" close enough to generation that a future contributor could reach for a model here) and is stated explicitly for that reason. Acceptance gated by probe-distortion test: `D_probe <= D_max`.
2. **Pruning** — runs *after* consolidation completes in the same tick, not as an independently-triggered job. This ordering is what prevents pruning from evicting a record an in-flight consolidation cluster is still reading — cheaper than a lock, and the reason pruning isn't its own separate trigger. Eviction is economics-based: marginal retrieval value vs. marginal dilution cost (FR11) — no fixed-count or capacity-bound cutoff (an earlier draft mistakenly applied a Hopfield-style capacity bound, P_crit ~ 0.138*N, here; that bound governs an attractor weight matrix, not a flat indexed table, and does not apply to this layer).
3. **Calibration** — refreshes FR16's ablation-probe-derived thresholds (abstention, acceptance, eviction) on a rolling window.

**Consolidation vs. correction, concurrency:** resolved for free by Badger's MVCC snapshot isolation — consolidation reads a consistent snapshot as of tick-start; a correction landing mid-tick writes a new version that consolidation's already-open snapshot simply doesn't see. No lock required. The corrected version is naturally picked up by the next tick. **This is the first place Badger's snapshot isolation is credited as a load-bearing dependency, not just an implementation detail** — noted here explicitly since it wasn't named as a reason in the original tech-stack acceptance.

**Open-ended intent records as consolidation targets [PROPOSED]:** a present-tense intent record ("Raj intends to decide packaging later") is still subject to ordinary supersede when overtaken by events — no new mechanism, just the existing supersede pattern applied to intent-predicate records.

---

## 7. WAL Format and Recovery

Inherited from BadgerDB's own WAL and crash-recovery (per tech-stack §2.5) rather than hand-rolled. Governed by the four storage rules (tech-stack doc) — in particular, compaction must not silently densify/collapse multi-version history in a way that breaks bi-temporal replay. Validated by M8 (below), not assumed safe by default.

---

## 8. Failure Modes

- **`emit` fails to reach durability (log write fails):** no write, no event, no verdict returned. Fail closed — never partially commit.
- **`recall` under partial index corruption:** [OPEN — needs explicit resolution before v1 ships] the design doc's rebuildability guarantee (§2) implies a corrupted index can be discarded and rebuilt from the Truth Store log, but the exact trigger/detection mechanism for "the index is corrupt, rebuild now" has not been specified.
- **Crash mid-`emit`:** covered by RESTRENGTHEN + server-side transaction-time (§4) — a retry is indistinguishable from ordinary corroboration, no special-case idempotency logic needed.

---

## 9. RPC Surface

| Operation | Signature |
|---|---|
| `emit` | `emit(record, source, session) -> {verdict, id}` |
| `recall` | `recall(query, budget) -> records \| ABSTAIN` |
| `correct` | `correct(hint, correction) -> {id, verdict}` |
| `attest` | `attest(id, verdict, source, session) -> {new_confidence, promoted?}` |
| `resolve` | `resolve(record_a, record_b, verdict)` |
| `as_of` | `as_of(query, t, axis: valid\|transaction) -> records` |
| `consolidate` | `consolidate(tick)` |
| `inspect` | *(deferred, post-v1)* — natural home for telemetry exposure per tech-stack §2.9 |

Transport: gRPC over Unix domain socket (tech-stack §2.1). Client stubs are generated per consumer language (Python wrapper is one example).

---

## 10. Seam-Audit Ledger

**Checklist (v2, seven questions):** (1) direction of flow — in and back; (2) who owns the clock; (3) who builds the artifact; (4) simultaneity; (5) assumption-false behavior; (6) the undefined case — what falls between defined branches; (7) the invisible influencer — can a filtered-out thing still affect outcomes.

**Closed cases:**
| Seam | Finding | Resolution |
|---|---|---|
| Write gate (Q6) | RESTRENGTHEN case undefined between MERGE/UPDATE | Fifth gate branch (§4) |
| Correction vs. supersede (Q4) | Race during labile window | Priority lock, correction wins |
| Quarantine vs. matcher (Q7) | Staged records could contaminate gate decisions | Staged records invisible to matcher |
| Labile window expiry (Q2) | No stated expiry mechanism | Lazy, on next touch |
| Index replay vs. live writes (Q4) | Double-apply or drop, undefined | Snapshot-offset boundary (§2) |
| Consolidate vs. prune (Q4) | Could evict a record mid-read | Serialized within one tick (§6) |
| RPC crash mid-emit (Q5) | Assumed new idempotency mechanism needed | Already covered by RESTRENGTHEN + server-side clock — no new component |
| Commitments/future-intent (Q6) | Assumed SCHEDULED status needed | Premise was wrong — present-tense intent needs no special status; deleted, not fixed |
| Correction vs. consolidation (Q4) | Assumed a lock was needed | Already covered by Badger MVCC snapshot isolation — no new component |
| Confidence field vs. append-only (Q2/Q5) | FR3 (mutable field) vs FR17 (never mutate) — real latent contradiction | Confidence computed at read from base + FEEDBACK events (§1) |

**Net finding:** of the boundaries audited, most resolved by recognizing existing mechanisms already covered the case (RESTRENGTHEN, MVCC) or by deleting a wrong premise (SCHEDULED-for-commitments) — only two produced genuinely new rules (the gate's concurrency sentences, the confidence representation fix). This is read as a sign the core architecture is nearing completion: late audits finding fewer, smaller things.

**Still open:** RPC-to-internals durability-ack ordering is resolved (§4/§8); remaining unaudited boundary is `recall`-under-index-corruption (§8, OPEN).

---

## 11. Validation Protocol Reference

M1-M6 per the SRS/WM spec (working-memory math + bi-temporal property suite). M7-M8 ratified into SRS v1.3 §7:
- **M7:** write-cost vs. history size — p99 `emit` latency must stay flat at 1K/100K/1M records, including through compaction. This is the direct empirical test of whether Engram actually avoids MemoryThread's core failure (`_resolve_seeds()` degrading with graph size).
- **M8:** crash-and-snapshot suite — kill -9 mid-write, verify replay produces a bit-identical rebuild; verify snapshot-consistent reads under concurrent writes (this is the direct test of the MVCC dependency now credited in §6).

---

## Status Legend
[RATIFIED] = locked, in force. [PROPOSED] = reasoned, not yet locked - do not treat as settled elsewhere. [OPEN] = acknowledged gap, no resolution yet.
