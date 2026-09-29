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
| transaction-time interval `[k-, k+)` | when Engram learned it — **assigned server-side at commit, never caller-supplied** (this is also the mechanism that makes crash-retry safe — see §4). The commit sequence IS this axis: birth seq → `k-`, close seq → `k+)`, open end otherwise — one clock, born in one function, nothing to drift. |
| provenance | source + session id(s), as id-refs (supports session-clustering for FR9) {NEW AFTER CONSIDERATION 2026-09-22: "id(s)" was wrong, not underimplemented — provenance is **singular per version** by design; the version is the unit of authorship. Witness sets are plural and *computed* (full version lineage + FEEDBACK, deduped by (Source, Session) — FR9). Receipts live in version history: content's witness is the provenance of the version that introduced it, reachable in the log — no second place where witness-truth lives, competing with lineage.} |
| derived_from | parent record IDs for SUMMARY/SEMANTIC records; empty for EPISODIC (rollback + re-derive target) |
| ACL tier | trust/access level |
| status | enum above |
| type | `EPISODIC \| SUMMARY \| SEMANTIC` — one store, not a tier; gate never derives from derived records |
| caller_ts_meta | persisted caller timestamp claim; metadata only, never ordering truth |
| payload | opaque string map for non-assertive metadata only (raw excerpts, links); the gate never branches on it — MERGE unions it monotonically |
| base confidence | starting value at write time |

**Confidence is not a mutable field. [RATIFIED — resolves a real FR3/FR17 conflict.]** FR3 originally implied confidence lives as record state; FR17 forbids in-place mutation. Resolution: confidence is *computed at read time* — base confidence plus all FEEDBACK events (from `attest`/`correct`) appended against this record's id, replayed lazily, same pattern as decay. This preserves `as_of` auditability (historical confidence is reconstructable) and compaction folds old events into snapshots without losing that property.

---

## 2. Storage Layout

- **Truth Store:** append-only, BadgerDB-backed. Governed by the four storage rules in the tech-stack doc §2.5 (log-only separation, anti-densification under compaction, MVCC not transaction-time, escape-hatch interface). The commit-sequence namespace is global across all key prefixes — one clock, one reverse-seek recovery.
- **Derived structures** (inverted index, MinHash tables, roaring bitsets, ID→seq lookup): fully rebuildable from the Truth Store log alone. The ID→seq lookup is derived state by the same rule — never logged, never required for replay; logging it "for convenience" would reintroduce a second ordering through the back door.

{NEW AFTER CONSIDERATION 2026-09-22 — QUARANTINE-SET CONTRACT (resolves P2→P3 gate blocker #1; Option A as decided by owner). Staged records live in the truth log as ordinary versions (P1 `Append`, ACTIVE) — quarantine is a separate derived set, never a record field: (1) Key layout: `M/quarantine/<record-id>` → single `0x01` marker byte under the existing Meta prefix (no new prefix, no schema change; the staged record itself already carries provenance + txn-time, so the marker needs nothing). (2) Writer: P4 intake — the staged `Append` and the marker write commit in ONE Badger txn (atomic both-or-neither; a record without its marker reads as believed, so the window must not exist). (3) Readers: P2 gate `Match` (caller-supplied set, already shipped); P3 rebuild (prefix-scan the set at snapshot boundary, skip staged IDs — index excludes staged at build); P4 recall pre-filter (excludes staged as defense-in-depth beside index exclusion). (4) Promotion: delete the marker (same txn as any promotion revision); the record stays and re-runs the gate as fresh (FR14); the next index update includes it — inclusion mechanics are P3's. (5) Crash: single-txn atomicity inherits Badger's — reopen needs no quarantine repair (unlike value corruption, §8). (6) P1 impact: additive concrete methods on `*Store` only (e.g. set/clear/list) — the `TruthStore` interface is P0-closed and stays untouched. Method shapes land in the P3/P4 per-file plans; this contract is what they code against.}
- **Rebuild procedure — the replay-vs-live-writes rule [RATIFIED]:** replay begins by recording the log's current tail offset as a snapshot boundary. Replay processes only up to that offset. Live writes arriving during replay append normally, past the boundary, and are applied directly against the still-serving old index. Once replay reaches the snapshot offset, the new index atomically swaps in, then drains whatever accumulated past the boundary, in log order. This prevents both double-apply and silent drop — the failure modes an unspecified replay procedure would otherwise hit.

---

## 3. The v1 Ranker

```
score(m|q) = hits(q,m) . R_m(t)^beta . w_trust(source) . (1 + log(1 + corr))
```
— index-hit count (relevance) times retrievability^beta (beta ~ 0.5 to start, fixed, no learning in v1) times source trust tier times log-dampened corroboration boost. Per-subject dedup enforced in top-k (never return multiple records about the same entity in one result set). Decay (`R_m(t)`) computed lazily on read, never via background sweep.

**This score curve is what FR7's abstention threshold calibrates against directly** — ranker and abstention gate share one curve, not two independently-tuned ones.

{NEW AFTER CONSIDERATION 2026-09-27 — THRESHOLD SCALE (found in P4 build): FR6 scores are unbounded — hits × trust (up to 1.5) × log-boost routinely exceed 1 (a two-hit user record scores ≈2.0) — so the abstention threshold lives on the score scale, unbounded ≥ 0, never [0,1]. config.go's `abstain_threshold` validation (was `[0,1]`) and query Spec validation changed accordingly; default 0.5 stands (filters sub-0.5 scores: decayed/low-trust). FR16 calibrates on this scale in P5.}

**[PROPOSED — not yet resolved] Trust floor re-attack:** the multiplicative form can drive a once-stated, never-repeated, clearly-asserted user fact toward near-zero score (low corroboration times moderate decay), making it a prune candidate despite the user's single statement being a strong trust signal in its own right. Candidate fix: a floor on the trust term for user-sourced facts specifically. Needs an adversarial pass (hand-crafted edge-case queries against the formula) before it's ratified — flagged, not yet fixed.

{NEW AFTER CONSIDERATION 2026-09-28 — TRUST-FLOOR PROBE RUN (owner-executed 2026-09-28, `internal/processing/trustfloor_test.go` kept in-tree as the regression witness): 40 lines across 8 facts × 5 ages. Findings: (1) decay dominates — at ≥100k steps EVERYTHING prunes (birthday, sys-config, corroborated pump-reading, rumor, trivia); half-life 10k is the killer, not trust. (2) The formula cannot separate precious-once from trivial-once (birthday/trivia print identical lines) — no score-only floor keeps one without subsidizing the other. (3) Corroboration only delays (pump-reading KEEP to 100k, dead at 200k). VERDICT (b): no floor now — dogfood (UC5) decides; if once-stated user facts die wrongly in practice, revisit the half-life first (uniform lift, no trivia subsidy, no formula change), floor second. Probe stays green by construction (it asserts nothing — it prints).}

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

{NEW AFTER CONSIDERATION 2026-09-22 — EXHAUSTIVE GATE DECISION TABLE. Refines the five lines above (they stand, for cross-reference) and closes SRS §8.4's "exact thresholds" plus the review cells: unequal-overlap, MERGE/RESTRENGTHEN jurisdiction, multi-candidate fold, INSERT definition, and cross-tier authority. The gate's relationship to an incoming event is its **max severity across all candidates**, never first-match:

1. **Validate** — `InputRecord.Validate()` fails → INVALID_RECORD (fail closed).
2. **Entry quarantine** — ACL `low` → STAGED before any matching (FR14). Low never reaches the fold; the table's tier rows concern `user` vs `system`.
3. **Candidate set** — live, non-STAGED records. SUMMARY/SEMANTIC never gate candidates: derived records are inert in both directions — never built from other derivations (production end, `store_iface.go` AppendDerived) and never evidence here (consumption end). BOTH-status candidates: matchable for further conflict detection, never eligible as UPDATE targets (flagged pairs don't stack).
4. **Fold, by max severity across candidates:**
   a. *Conflict* (same key, different object, overlapping valid-time): incoming tier ≥ candidate → UPDATE — fresh conflicts always UPDATE; the incoming event is a witness and server txn-order records its recency. Incoming tier < candidate → BOTH (no **eligible** claimant — authority overrides recency; this is FR12's "no temporal resolution"). Discovered pre-existing overlap with no fresh event → BOTH, via the tick invariant-audit (see 10).
   b. *Same SPO, overlapping valid-time, no conflict branch fired*: content delta (adds payload keys or widens valid-time) → MERGE via Supersede. The merged version is authored by the incoming event (Provenance = incoming); retained content's witness remains on the superseded version — receipts in version history, one provenance field per version (§1). No content delta → same (Source, Session) → SKIP; different session → RESTRENGTHEN. **Narrowing** (incoming interval ⊆ existing) adds no content → acknowledged, never applied: the delta test rejects shrink by construction; scope retraction is `correct()`'s job (P4), never the gate's.
   c. *Same SPO, disjoint valid-time* → append as a new temporal fact (INSERT); both records legitimately live.
   d. *No candidate* → INSERT, defined here as "no **live** candidate match on key" (amends "no match" in the five-line list so the disjoint branch cannot be conflated with it).
5. **Tier rule** — content-writing verdicts (MERGE, UPDATE) require incoming tier ≥ candidate tier; below → BOTH. SKIP/RESTRENGTHEN fire from any tier: they write no content, and subordinate corroboration is discounted at read by `w_trust(source)` (FR6), not blocked at write. The gate cannot be escalated past the ACL — stated as a property, per-verdict, not an accident of ordering.
6. **Coexistence** — multiple live same-SPO records overlapping in time may legitimately coexist post-gate: that is what SKIP means (acknowledge, don't duplicate). The gate's job is no unresolved *contradictions*, not no duplicates; P4's ranker/resolve disambiguates coexisting versions.
7. **Crash-retry walk** — first send consumes the conflict (UPDATE closes old, appends new); the retry finds pure reassertion, same session → SKIP. Conflict-first *looks* like it breaks retry-skip; this walk is the proof it doesn't: a same-session retry can never bump confidence merely because a crash occurred.
8. **Witness-set footer** — confidence witnesses = distinct (Source, Session) pairs across the full version lineage AND the FEEDBACK events for the fact, verdict-independent. MERGE/UPDATE events register their session cluster like any committed event: corroboration-with-content raises confidence exactly as much as bare corroboration, through the clustering layer, never the verdict layer. (Counting only live records would make witness membership depend on gate outcomes — forbidden.) {NEW AFTER CONSIDERATION 2026-09-27 — WITNESS SCOPE (found in P4 build): "verdict-independent" spans GATE verdicts, never attest polarity. A disconfirming attester contributes only its BKT drop via ApplyFeedback — counting it as a noisy-OR witness lifted confidence for negative evidence (demonstrated: 0.5 → 0.6 on disconfirm), contradicting FR18/DESIGN §5's mirrored update. Intake's replayConfidence and recall's confidenceOf both exclude disconfirms from the witness set while folding their events.}
9. **Session identity** — `(Source, Session)` composite. Session string alone is not identity: a cross-source collision would merge witnesses (FR14 independence violated through the back door). v1 session-cluster ≡ one composite pair.
10. **Sweeper** — the serialized tick (P5) audits this table as **invariant-audit**: defense-in-depth at host cadence (FR10), not the primary conflict mechanism. Under conflict-first the gate should make pre-existing diff-object-overlaps unreachable; the audit catches `resolve()` outputs, future code paths, and plain bugs, routing violations to BOTH. It is never a substitute for the gate.

**Match semantics (v1):** normalized exact-key match on (subject, predicate), plus MinHash n-gram fuzzy dedup scoped *only* to the MERGE band (catches paraphrase, e.g. "Acme" vs "Acme Corp"). Fellegi-Sunter-style probabilistic resolution is explicitly deferred — structural-feature independence violations were identified during research and exact-first is the safer v1 default. "Raj" and "Rahul" remain distinct records in v1.

**RESTRENGTHEN is load-bearing, not cosmetic:** it's also the FR14 promotion trigger for STAGED/quarantined records (a second independent corroborating signal), and it's what makes crash-and-retry safe with zero new idempotency machinery — a retried `emit` after a crash-before-ack carries identical content and is absorbed here, provided transaction-time is server-assigned (§1). {NEW AFTER CONSIDERATION 2026-09-22: superseded for the same-session case — under the session-split (§4 table) a crash-retry is structurally same-session (correlation-ID-scoped, immune to timing) and lands in **SKIP**, a true no-op; a network-level retry must not bump confidence just because a crash occurred. RESTRENGTHEN remains load-bearing as the FR14 promotion trigger and cross-session corroboration absorber; the retry-walk (§4 item 7) proves both properties coexist.} Revision (supersede, correction, resolution) is written as a single atomic log entry via the `Supersede` primitive — close-interval and append are inseparable, so no partial revision state can exist.

**Three concurrency rules found during seam audit, all now fixed:**
1. **Corrections hold a priority lock over concurrent supersede** attempts on the same record during the labile correction window — supersede defers, never races.
2. **Staged/quarantined records are invisible to the write-gate matcher** — prevents a poisoned staged record from contaminating gate decisions on legitimate incoming writes. Promotion re-runs the gate as if the record were fresh.
3. **The labile correction window expires lazily, on next touch** — not via a timer, consistent with the tick-only/no-hidden-scheduler principle.

**Rate limiting** per source/session against flooding (FR15) sits here, unchanged from the SRS.

---

## 5. Correction and Attestation Paths

**`correct(hint, correction)`** — for content that's *wrong*. Locates the offending record via a similarity floor over the hint (not always-argmax, which risks silently rewriting an unrelated record — floor failure falls back to INSERT as new content instead). Labile rewrite within a time window; confidence recomputed from new provenance.

**`attest(id, verdict: confirm|disconfirm, source, session) -> {new_confidence, promoted?}`** [RATIFIED] — for a verdict on content that's *not* being replaced. Distinct from `correct` (see comparison table below). BKT-style confidence update; confirm also applies a strength jump (confirmation is stronger evidence than mere use), disconfirm doesn't touch strength. Binary verdict space only in v1 — no caller-supplied confidence weights (a confidence-poisoning surface otherwise). Promotion via attest requires the attesting source/session to differ from the original write's session-cluster — a self-attestation must never promote a STAGED record. {NEW AFTER CONSIDERATION 2026-09-29 — B1 CLOSED (dogfood probe: solo self-confirms walked 0.84→0.90): self-attestation now neither promotes NOR scores — same-composite confirms are recorded but discounted in both folds (no BKT delta, no strength event); self-disconforms always apply. Author's conviction is already in base.}

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

{NEW AFTER CONSIDERATION 2026-09-28 — DURABLE TICK (P5 scope completion, not a rule break): the tick-monotonic guard is durable, not in-memory — last accepted tick lives under the Meta prefix (`M/tick`), written in the same Sync boundary that commits the tick's effects, acked only after Sync; any `consolidate(tick)` with tick <= last is rejected. Additive derived key like `M/quarantine/*` (§2); no record-schema change, no second clock — the host still owns the counter, Engram only remembers the high-water mark to make restart safe.}

**Chained derivation refused (deliberate trade-off):** consolidation derives from episodic records only, never from SUMMARY/SEMANTIC outputs — this blocks summary-of-summary drift at the cost of consolidation work growing with the full episode corpus. If that becomes a scaling wall, the escape hatch is chained derivation with provenance re-verification (derive only from summaries whose DerivedFrom episodes are all still live).

**Consolidation vs. correction, concurrency:** resolved for free by Badger's MVCC snapshot isolation — consolidation reads a consistent snapshot as of tick-start; a correction landing mid-tick writes a new version that consolidation's already-open snapshot simply doesn't see. No lock required. The corrected version is naturally picked up by the next tick. **This is the first place Badger's snapshot isolation is credited as a load-bearing dependency, not just an implementation detail** — noted here explicitly since it wasn't named as a reason in the original tech-stack acceptance.

**Open-ended intent records as consolidation targets [PROPOSED]:** a present-tense intent record ("Raj intends to decide packaging later") is still subject to ordinary supersede when overtaken by events — no new mechanism, just the existing supersede pattern applied to intent-predicate records.

---

## 7. WAL Format and Recovery

Inherited from BadgerDB's own WAL and crash-recovery (per tech-stack §2.5) rather than hand-rolled. Governed by the four storage rules (tech-stack doc) — in particular, compaction must not silently densify/collapse multi-version history in a way that breaks bi-temporal replay. Validated by M8 (below), not assumed safe by default.

---

## 8. Failure Modes

- **`emit` fails to reach durability (log write fails):** no write, no event, no verdict returned. Fail closed — never partially commit.
- **`recall` under partial index corruption:** [OPEN — needs explicit resolution before v1 ships] the design doc's rebuildability guarantee (§2) implies a corrupted index can be discarded and rebuilt from the Truth Store log, but the exact trigger/detection mechanism for "the index is corrupt, rebuild now" has not been specified.
- **Crash mid-`emit`:** covered by RESTRENGTHEN + server-side transaction-time (§4) — a retry is indistinguishable from ordinary corroboration, no special-case idempotency logic needed. {NEW AFTER CONSIDERATION 2026-09-22: "covered by RESTRENGTHEN" superseded for the same-session case — the retry lands in SKIP (§4 session-split). The coverage claim itself (no special idempotency machinery needed) stands unchanged.}
- **Corrupt tail at open:** auto-amputated, loudly (stderr + `Truncated()`); store reopens writable at the truncated tail.
- **Mid-log corruption with intact successors:** open refuses fail-closed (CORRUPT_INDEX naming the offset) — amputating would silently delete the good suffix. Operator repair only.

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
| Disjoint valid-time, same SPO (Q6) | Identical-SPO ack branches would silently drop the later time region — a lost fact, not a wrong answer | New temporal fact → plain append (§4 table, branch c) |
| MERGE vs RESTRENGTHEN jurisdiction (Q6) | Same-object-with-content satisfied both branch definitions; two branches claimed one case | Content-delta principle: MERGE writes content, SKIP/RESTRENGTHEN acknowledge only (§4 table, branch b) |
| Multi-candidate fold (Q6) | Same-SPO-first precedence would ack an event whose conflict with another candidate went unprocessed | Max severity across candidates: conflict > same-SPO > disjoint > INSERT (§4 table, item 4) |
| Cross-tier conflict authority (Q6/Q7) | Verdict table let a subordinate tier close (UPDATE) *and* author (MERGE) higher-tier records — privilege escalation through the side door | Content-writing verdicts require incoming ≥ candidate tier; below → BOTH; low never reaches the fold (§4 table, item 5) |
| Crash-retry absorption wording (Q5) | "Absorbed by RESTRENGTHEN" claimed in three docs, six places; same-session retry must not bump confidence | Session-split: retry → SKIP; annotated in place in SRS FR4/§8.4, DESIGN §4/§8, TECH §2.1/§2.4 {NEW AFTER CONSIDERATION 2026-09-22: all six rows above are the review-cell closure set — disjoint-time, jurisdiction, fold, tier authority, retry wording; pre-existing ledger rows stand unchanged for cross-reference.} |

**Net finding:** of the boundaries audited, most resolved by recognizing existing mechanisms already covered the case (RESTRENGTHEN, MVCC) or by deleting a wrong premise (SCHEDULED-for-commitments) — only two produced genuinely new rules (the gate's concurrency sentences, the confidence representation fix). This is read as a sign the core architecture is nearing completion: late audits finding fewer, smaller things.

**Still open:** RPC-to-internals durability-ack ordering is resolved (§4/§8); remaining unaudited boundary is `recall`-under-index-corruption (§8, OPEN).

---

## 11. Validation Protocol Reference

M1-M6 per the SRS/WM spec (working-memory math + bi-temporal property suite). M7-M8 ratified into SRS v1.3 §7 (M8 enforced by two falsifiable tests: torn-supersede truncation and key-uniqueness):
- **M7:** write-cost vs. history size — p99 `emit` latency must stay flat at 1K/100K/1M records, including through compaction. This is the direct empirical test of whether Engram actually avoids MemoryThread's core failure (`_resolve_seeds()` degrading with graph size).
- **M8:** crash-and-snapshot suite — kill -9 mid-write, verify replay produces a bit-identical rebuild; verify snapshot-consistent reads under concurrent writes (this is the direct test of the MVCC dependency now credited in §6).

---

## Status Legend
[RATIFIED] = locked, in force. [PROPOSED] = reasoned, not yet locked - do not treat as settled elsewhere. [OPEN] = acknowledged gap, no resolution yet.
