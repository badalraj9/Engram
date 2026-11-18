# Operator Channel — SPEC v1 (read-only, JSON-over-UDS)

Status: **spec awaiting owner ratification.** Implementation starts only after sign-off.

Owner ruling (2026-11-16): the operator channel is **strictly read-only**. It carries
exactly five things — explain (per-candidate reasons), per-status counts, txn-close
receipts, all-tier history, tick dry-run. `tick` and `resolve` mutate and keep their
existing paths (`engramd -tick N` over the live socket; the `resolve` MCP verb). The
frozen gRPC wire stays untouched: no protoc, no new tooling, no new dependency.

---

## 1. Transport

| Item | Decision |
|---|---|
| Socket | `<data dir>/engram-op.sock` — derived by the same rule as the main socket (the data dir derives it), so it moves with the store |
| Served by | the holder process (same daemon, same store, same single writer) |
| Protocol | JSON, one request per line, one response per line, UTF-8, LF-terminated |
| Reachability | local filesystem only — a UDS is unreachable from a browser, so the panel token remains the anti-web-page control for the panel itself |
| Permissions | inherits the data dir's ACL; on Unix the socket is created 0600 |
| Writes | none — the handler set contains no mutating path by construction (it calls only the daemon's read ports) |

Two transports, one reader (owner ruling): the **CLI dials** the socket; the **panel
reads in-process** through the same provider functions and the same types. The panel
never self-dials.

## 2. Wire shapes

Request (one JSON object per line):

```json
{"op":"v1","id":"c1","method":"explain","params":{ ... }}
```

Response (one JSON object per line):

```json
{"op":"v1","id":"c1","ok":true,"result":{ ... }}
{"op":"v1","id":"c1","ok":false,"error":{"code":"invalid_param","message":"budget must be positive"}}
```

- `op` is the channel version (`"v1"`). A client that sees a different `op` fails closed.
- `id` is echoed verbatim; the client correlates on it.
- `ok:false` carries a stable `code` from the table below — never a Go error string.
- Unknown method → `unknown_method`. Malformed JSON → `invalid_request`. A method that
  does not exist in a future server version must never be silently accepted.

Error codes: `invalid_request`, `unknown_method`, `invalid_param`, `not_found`,
`internal`. Failures are loud: the channel returns the error, it never returns a partial
or invented funnel.

## 3. Endpoints

### 3.1 `explain` — the recall funnel

Params: the recall query, verbatim from the operator's point of view —
`subject`, `predicate`, `object`, `terms[]`, `caller_tier`, `budget`.

Result:

```json
{
  "funnel": {
    "candidates": 412,
    "acl": {"candidates_by_tier": {"low": 12, "user": 380, "system": 20}, "rejected_tier": 9},
    "eligibility": {"tier": 9, "expired": 3, "scheduled": 1, "quarantined": 0,
                    "superseded": 2, "below-threshold": 380},
    "eligibility_total": 395,
    "eligible": 17,
    "selection": {"shadowed": 0, "outranked": 6, "budget": 10},
    "selection_total": 16,
    "returned": 1,
    "abstained": false,
    "sum_ok": true
  },
  "reasons": [{"id":"rec-7f3…","code":"below-threshold","raw":null}],
  "records": [ ...the same records recall would return... ],
  "scores": [ ... ],
  "confidences": [ ... ]
}
```

`reasons` carries one entry per **rejected** candidate (eligibility + selection),
never per returned record. `raw` is non-null only for codes outside the ratified
vocabulary (see §4).

### 3.2 `status` — per-status counts and store footprint

```json
{"counts": {"active": 120, "scheduled": 4, "tombstoned": 18, "quarantined": 2, "disputed": 3},
 "bytes": {"used": 8123456, "reserved": 104857600},
 "tick": {"last": 41, "age_ms": 612000},
 "holder": {"pid": 21912, "socket": "D:\\engram-data\\engram.sock"}}
```

- `disputed` = records in BOTH state (the unresolved-pair count).
- `bytes.used` vs `bytes.reserved`: badger preallocates, so an empty store is ~161MB
  on disk. The panel and CLI report USED vs RESERVED — never a raw "162MB" for an
  empty store (owner ruling, Step 6).
- `tick.age_ms` = wall time since the last accepted tick; `-1` when no tick has ever
  run.

### 3.3 `receipts` — txn-close receipts

Params: `limit` (default 20, max 200).

```json
{"receipts": [{"tick": 41, "closed": ["rec-7f3…","rec-91a…"], "counts": {"expired": 2, "retired": 1, "synthesized": 3}}]}
```

**Requires a storage addition** — see §6. The closing tick is not currently stored.

### 3.4 `history` — all-tier bi-temporal read

Params: `subject`, `predicate`, `axis` (`valid`|`transaction`), `t` (unix nanos).

Result: the same record list shape `as_of` returns today, but read across **all
tiers** (the v1 `as_of` is low-tier history only — the operator lane is not tier-
bound). This is what makes `engram history` and a future Timeline possible; the
Timeline screen itself stays cut from v1 launch.

### 3.5 `tick_dry_run` — what the next tick would collect

Params: none (uses wall clock now).

Result: the existing `maintenance.Preview` payload — live rows, live checksum,
expiry/retirement victims, and the calibration verdict. **Zero writes.** The gate is
the live-row count plus a checksum of live records — never file byte counts (benign
compaction changes byte counts; a spuriously failing gate is a gate people learn to
ignore).

## 4. Reason vocabulary and the funnel contract

### 4.1 Categories and precedence

Eligibility rejections: `tier` · `expired` · `scheduled` · `superseded` ·
`quarantined` · `below-threshold`
Selection drops: `shadowed` · `outranked` · `budget`
Outcome: `returned`
Compatibility: `other` (+ the raw code; never dropped, never merged)

Precedence applies **within** a category: the first gate that fires in pipeline order
wins. Across categories, pipeline order decides which row a candidate lands in.

### 4.2 Candidate definition

**CANDIDATES = the pre-ACL candidate pool** (`query/recall.go:93`,
`poolCandidates` — Exact ∪ Fuzzy ∪ Terms, deduplicated by ID). The pool is taken
before any filter, so `tier` can fire, and every matched row's fate is accounted
for. The funnel carries the ACL split explicitly (`acl.candidates_by_tier`) so the
denominator is never a mystery.

### 4.3 Gate order and the code that produces it

| # | Gate | Reason | Code site |
|---|---|---|---|
| 1 | store lookup fails (stale index copy) | `other` (raw `vanished`) | `recall.go:105-108` |
| 2 | staged / quarantine set | `quarantined` | `matcher.go:102-104` |
| 3 | derived record type | `other` (raw `derived`) | `matcher.go:105-107` |
| 4 | tombstoned | `superseded` (proposed — see §7.2) | `matcher.go:108` |
| 5 | ACL pre-filter (FR13) | `tier` | `recall.go:112-114`, `recall.go:211-216` |
| 6 | status SCHEDULED | `scheduled` (proposed — see §7.1) | `recall.go:121-123` |
| 7 | valid window not yet open | `scheduled` (proposed) | `recall.go:124` |
| 8 | valid window closed | `expired` | `recall.go:124` |
| 9 | zero term hits | `other` (raw `no-hit`) | `recall.go:127-129` |
| 10 | score below the calibrated threshold | `below-threshold` | `abstain.go:23-27` |
| — | **eligible set** | | |
| 11 | dedup by subject — loser is a BOTH member | `shadowed` | `ranker.go:90-113` |
| 12 | dedup by subject — loser is not a BOTH member | `outranked` | `ranker.go:90-113` |
| 13 | truncated by budget | `budget` | `recall.go:146-148` |
| — | **returned** | `returned` | |

Gate 10 currently sits *after* the dedup in code (`recall.go:141-142`). See §7.4 —
this is the one ordering question the spec cannot decide alone.

### 4.4 The invariant

```
eligibility_total + selection_total + returned == candidates
```

Checked on every explain response. If it does not hold, the channel **fails loud**:
`{"ok":false,"error":{"code":"internal","message":"funnel sum mismatch: …"}}`.
A funnel that does not sum is never published — not clamped, not padded, not shown
"approximately". The same check runs in the CLI and in the panel (same reader), so the
two surfaces cannot disagree.

### 4.5 The funnel's two claims (never one merged number)

```
412 matched → 380 ineligible (below-threshold 380) → 32 eligible → 1 returned (budget 10)
```

Eligibility rejections and selection drops are separate rows. "Below-threshold" means
the fact was never good enough; "budget" means the fact was fine and the caller did
not ask for enough. The skeptic's question — "of the 32 eligible, where are the other
31?" — is answered by the `selection` row.

## 5. One reader, two transports

New package `internal/operator`:

- Wire types (`Request`, `Response`, per-method params/results).
- `type Reader interface { Explain(ctx, ExplainReq) (ExplainResp, error); Status(ctx, StatusReq) (StatusResp, error); Receipts(ctx, ReceiptsReq) (ReceiptsResp, error); History(ctx, HistoryReq) (HistoryResp, error); TickDryRun(ctx, TickDryRunReq) (TickDryRunResp, error) }`
- `func Dial(path string) (Reader, error)` — the CLI's transport.
- `func Serve(l net.Listener, p Provider) error` — the daemon's transport.
- `Provider` — the in-process side; the panel calls it directly.

The funnel logic exists once. The CLI and the panel call the same reader and render
the same numbers; a divergence is impossible without changing the reader.

## 6. Receipts: storage addition required (checked, not assumed)

**Verdict: the closing tick is NOT stored and NOT derivable.**

Evidence:
- `rpc.Record` has no closing-tick field (`internal/rpc/types.go` — the record struct
  carries ID, content, valid/txn intervals, provenance, ACL, status, type,
  base confidence, payload).
- `truth.Restatus(id, status)` copies the **author's** provenance into both the closed
  version and the new one (`internal/truth/store.go:455-499`); nothing about the
  closing tick is recorded.
- `M/tick` holds only the 8-byte high-water mark (`internal/truth/tick.go:36-92`) —
  the last accepted tick number, no mapping to closed rows.
- Tick-synthesized records carry `Provenance.Session = "tick-<n>"`
  (`internal/maintenance/consolidate.go:313`), but that covers records the tick
  *created*, not rows it closed.

So the owner's hypothesis ("derivable from the restatus record's transaction") is
**false**: the transaction interval records *when* (a sequence offset), not *which
tick*.

Proposed addition (small, additive, append-only — no record mutation, FR17 intact):

- Key: `M/receipt/<tick>` under the existing Meta prefix → JSON
  `{"tick":41,"closed":["rec-…"],"synthesized":["rec-…"],"expired":2,"retired":1}`.
- Written in the tick's existing Sync boundary, together with `CommitTick` — so the
  receipt and the high-water mark land in the same atomicity unit (never
  mark-without-receipt).
- Read API: `LastReceipts(limit)` on `*truth.Store` (additive concrete method; the
  P0-closed `TruthStore` interface stays untouched, same pattern as `tick.go`).

Cost: small (one key prefix, one write site, one read method).

## 7. Open items for ratification

### 7.1 `scheduled` — a needed code the vocabulary does not have
The read path drops two distinct kinds of "not currently true": a window that has not
opened yet, and a record whose status is SCHEDULED (`recall.go:121-126`). Neither is
`expired` (the fact has not expired — it has not started). Neither fits `other`
honestly: SCHEDULED rows are a first-class panel citizen ("Scheduled/upcoming gets a
home"). **Proposal:** add `scheduled` to the eligibility set. Without it, these
candidates render as `other(scheduled)`.

### 7.2 TOMBSTONED → `superseded`?
A tombstoned row was replaced by a newer version of the same claim — semantically the
family of `superseded`. **Proposal:** map it to `superseded`; the panel still shows
TOMBSTONED separately in the status counts. (In practice the serving index excludes
tombstoned rows, so this gate is defence-in-depth and rarely fires.)

### 7.3 `other` for the three non-referent drops
`vanished`, `derived`, `no-hit` have no ratified code. **Proposal:** keep them as
`other` + the raw code, never merged, never dropped.

### 7.4 Threshold placement: keep code order, or reorder? (recommendation: reorder)

Today the dedup runs **before** the threshold (`recall.go:141-142`):

```go
deduped := processing.DedupBySubject(scored)
kept, abstained := processing.ApplyThreshold(deduped, spec.Threshold)
```

So a same-subject duplicate is dropped as `outranked` even when it would also have
fallen below the threshold — which muddles the two rows the funnel promises to keep
apart ("the fact was never good" vs "the fact was fine and you did not ask for
enough").

**Option A — keep the order.** Zero change to the hot path. The funnel attributes a
duplicate below-threshold to `outranked`, so the `below-threshold` row undercounts.

**Option B — threshold first, then dedup.** The two orders **commute**: the dedup
keeps the maximum score per subject, so
- if the subject's maximum is below the threshold, every member is below it — both
  orders drop the whole subject;
- if the maximum clears the threshold, that record survives both orders and is the
  one dedup keeps in both.
Abstention is identical for the same reason (any record clearing the threshold ⇒ its
subject's maximum clears it ⇒ both orders return at least one row).

Returned set, scores, confidences, ABSTAIN behaviour: **identical**. Only the reason
attribution moves, into the row it belongs to.

**Recommendation: Option B** — it is what the funnel's two-claim shape promises, and
it is provably output-identical (a parity gate will assert it). It is still a reorder
of two calls in the verified read path, so it needs explicit sign-off.

## 8. Verification results that gate the vocabulary

### 8.1 Q1 — is there a per-record confidence floor? **No.**

- The recall score is `hits × retrievability^beta × trust × (1 + log(1 + corr))`
  (`internal/processing/ranker.go:62-73`). `corr` is the count of supporting witness
  clusters — **not** a confidence magnitude.
- `BaseConfidence` is validated to `[0,1]` on input (`internal/rpc/types.go:254`) and
  feeds only the read-time confidence that is *reported* (`recall.go:241-263`), plus
  the tick's synthesis pick-the-minimum (`consolidate.go:281-282`).
- It never enters the score and never gates anything.

Consequences: a fact written with `base_confidence 0.00` is served exactly like one
at 0.99 — the owner's black-box observation is explained. The only floor in the read
path is the **calibrated composite-score threshold**, a property of the result set,
not of any record. Per the owner's ruling the code therefore stays
**`below-threshold`**, and it must be defined as "score < calibrated threshold". No
floor is added to make the funnel look good. The panel copy must not claim a
per-record confidence floor.

### 8.2 Is `disputed` needed? **No — `shadowed` is the correct code.**
`IsLiveCandidate` accepts BOTH-state records (`matcher.go:101-109` rejects only
staged, derived, and tombstoned) — nothing gates a disputed row out as ineligible.
Both members of a live pair stay alive and attestable; the one recall does not serve
is dropped by the **dedup** (same subject, one winner) — a *selection* drop, not an
eligibility rejection. That is `shadowed`. The owner's black-box observation
(both rows alive, both resolvable, recall serves one, and which one flips around a
tick) matches this exactly: the flip is the score changing, hence the winner
changing.

### 8.3 Receipts — see §6.

## 9. Non-goals

- No new MCP verbs; no change to recall's external output, ABSTAIN text, or tool
  schemas; no change to `as_of`/`consolidate` semantics.
- No write verb on this channel, ever — `tick` and `resolve` stay on their existing
  paths.
- No trust-layer, window-semantics, quarantine, id-minting, or restatus changes.
- No gRPC wire change; no protoc; no new dependency.
- No Timeline screen in v1 (the all-tier history read is exposed; the screen is later).
