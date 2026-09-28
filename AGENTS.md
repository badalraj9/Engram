# AGENTS.md — how to work in this repo

Read this first, every session. Then read all docs before touching code.
The owner directs; the agent executes. Never the reverse.

## 0. Listen first — do not jump to conclusions

- **The owner decides, the agent proposes.** Never treat a question as an instruction to build. "Is X solid?", "what do you think?", pasted reviews, and hypotheticals are requests for **opinion first** — answer carefully, end with a recommendation, and wait for an explicit go (`go`, `apply it`, `lock it`, `build P<n>`).
- **Do not infer scope.** If a request is ambiguous between answering / planning / implementing, pick the cheapest reading (answer), state the assumption in one line, and let the owner escalate. An unwanted implementation is expensive; an unwanted paragraph is cheap.
- **Do not fill blanks with guesses.** Unknown owner intent (naming, policy, scope, trade-offs) → ask via the question tool with a fitting recommendation and doc-grounded reasons. Never invent product decisions.
- **Do not "improve" beyond the ask.** Fix what was asked; surface adjacent findings as noted-not-changed. Extra refactors, extra files, and drive-by cleanups each need their own go.
- **Do not commit or push unless asked.** Commit only on explicit instruction; push only on the word `push`. Report what's staged and waiting instead.
- **When corrected, adopt the correction immediately** and record it if it is a standing rule (this file or the relevant doc). Do not relitigate.

## 1. Startup order (no exceptions)

1. Read this file.
2. Read `docs/SRS-engram-v2.md`, `docs/DESIGN.md`, `docs/TECH-STACK.md`, `docs/BUILD-PLAN.md`, `docs/BUILD-RANKS.md` **in full** before any code change. Claims about the tree are verified by reading the tree — never from summaries, memory, or other agents' pastes (those are interrogation material, not evidence).
3. Check `git status` / `git log` for uncommitted work and where HEAD is.
4. Restate the current phase and its exit criteria before proposing work.

## 2. Authority order

1. `docs/SRS-engram-v2.md` — what/why. Never reopened by a code change.
2. `docs/DESIGN.md` — how. Disputes land here as text first.
3. `docs/TECH-STACK.md` — with-what.
4. `docs/BUILD-PLAN.md` / `docs/BUILD-RANKS.md` — build order, importance, phase exits.
5. This file — session conduct. It governs behavior, never architecture.

## 3. Frozen rules (fail review)

- No LLM calls in `emit`/`recall`/`consolidate` paths (NFR2). No vector DB (NFR3).
- Append-only: records never mutated in place (FR17).
- No hidden scheduler: maintenance only inside `consolidate(tick)` (FR10).
- ACL pre-filter before ranking/combining (FR13).
- Acknowledgment implies durability: nothing confirmed before its covering sync returns.
- Unrepresentable beats forbidden: enforce invariants by construction (compiler/types), not comments.
- Stdlib-first: no new external dependency without discussion.
- One writer, one clock, one atomicity unit: no second source of truth at any layer.

## 4. Workflow (detail)

- **Plan before code.** New phase work starts with a per-file plan (what each file owns, its exit test) presented for approval. Code follows the approved plan verbatim; deviations are re-proposed, not smuggled in.
- **Opinion first, implementation on go.** Reviews, ratings, "is it solid", pasted third-party findings → careful answer + verdict + itemized proposal. Implementation begins only after explicit approval.
- **Decisions lock into docs the same turn.** Every agreed decision goes to `docs/BUILD-PLAN.md` (phase section) immediately — chat agreements evaporate. If the owner says "lock it", the lock edit is the deliverable.
- **Amendments append, never silently rewrite.** Changing agreed content or research: keep the old wording inline with a `[SUPERSEDED YYYY-MM-DD: what + why + where decided]` tag. Verify afterward that nothing was lost (targeted search for dropped terms).
- **Append-only editing, both senses.** Docs: new decisions land as tagged blocks beside the old text (same place, old kept for cross-reference) — never rewrite a locked section in place. Code: prefer editing the file that owns the concept; create a new file only when the approved per-file plan names it. Delete nothing without an explicit go.
- **Evidence before synthesis.** Read files before quoting them; paste verbatim with `path:line` when challenged. If a finding contradicts a claim, trust the file and say so plainly — including when the file contradicts the agent's own earlier statement.
- **Admit, don't smooth.** When a review catches a real gap (naming collision, untested boundary, changed-by-test-fix decision), state exactly what happened, what it risks, and the proposed fix. Never defend the gap.
- **Verify by execution.** `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...` after every code change. Failing tests are read fully (exact assertion + line) before fixing. Throwaway harnesses (`tmp_*`) are removed the same turn they pass.
- **Tests prove doc clauses.** New logic ships with tests citing the clause they enforce (FR/M-item in the test name where practical). Every numeric cap gets a reject-path test on both sides of the boundary.
- **Report deltas honestly.** Counts (tests passed, files changed, lines), what was verified, what remains open, and the single biggest residual risk. No "done" without the green commands' output.

## 5. Commits (never push unless asked)

- Split: infra (line-endings/deps) → implementation+tests → doc-sync. One concern per commit.
- Messages: `P<n> <what>: <scope>` for builds, plain summaries for docs.
- Tree must be `build + vet + fmt + test` green at every commit.
- After committing, report log lines + clean/dirty status. Leave pushing to the owner.

## 6. Communication

- Short, concise, factual. No emojis. No praise filler, no validation theater.
- Reference code as `path:line`. Answer the question asked; park the rest as noted-not-changed.
- When the owner is thinking out loud, think with them — don't convert musing into tasks.

## 7. Platform notes (Windows dev machine)

- PowerShell: no `&&` (use `;`), no `grep` (use `Select-String` / `Get-ChildItem ... | Select-String`). Check command output limits with `Select-Object -First N`.
- Line endings pinned LF via `.gitattributes` (`*.go`, `*.proto`, `*.md`, `*.toml`, `*.mod`, `*.sum`). If `gofmt -l` flags everything at once, suspect CRLF flip (usually after rebase/checkout) — `gofmt -w` + check attributes.
- Go toolchain via `go`; Badger pinned in `go.mod` (`dgraph-io/badger/v4`); `go mod tidy` after adding imports. Pinned-version API claims are verified against module-cache source before building on them.
- Kill-9-style crash tests use child-process + `Process.Kill()` (portable abrupt death); never fake them with clean closes.
- PowerShell quirk seen 2026-09-23: `-bench=.` is swallowed (benchmarks silently don't run); use `-bench=Benchmark` (or a fuller name) instead.
- Toolchain limit seen 2026-09-24: `-race` needs cgo and this machine's cgo has no 64-bit backend (`cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`) — race leg unrunnable here, not a code signal. Revisit on a machine with working gcc.
- Fuzz lesson seen 2026-09-24: run memory-hungry fuzz targets solo, never parallel — a parallel OOM produces collateral "failing inputs" in innocent targets (empty-seed bytes that pass in isolation). Confirm any fuzz failure solo before fixing.

## 8. Session handoff — P6 entry context (state as of 2026-09-28)

[SUPERSEDED 2026-09-28: P5-entry handoff below replaced — P5 built, tested, doc-synced, uncommitted on `v1/p0-p4`. Docs moved to `docs/`. Old text kept for cross-reference.]

Work happens on branch `v1/p0-p4` (P5 built on top, uncommitted); `v1/p0-p3` stays as the P3 snapshot; `main` is behind. P0–P5 built: P5 adds `internal/rpc/server.go+router.go+error_map.go`, `internal/daemon/daemon.go`, `internal/maintenance/*`, `internal/truth/tick.go+repair.go`, `internal/telemetry/*`, `proto/engrampb/` (genuine protoc) + mirror tests. Full tree `build+vet+fmt+test` green (9/9 pkgs, uncached). Next: P6 per-file plan, then code on go. P6 owns: M1–M5/M7/M8b gates, feedback-by-ID secondary (bench-required: 111.5ms recall @100K), fuzzy-lane worst-case strategy (open risk), `cmd/engramd`, thin client stub, trust-floor only if dogfood reopens it (verdict (b) locked, probe in-tree). Nothing else open.

### Prior handoff (P5 entry, 2026-09-27, superseded — see above)

[SUPERSEDED 2026-09-27: P4-entry handoff below replaced — P4 built, tested, doc-synced, uncommitted. Old text kept for cross-reference.]

Work happens on branch `v1/p0-p3` (P4 built on top, uncommitted); `v1/p1-p2` stays as the P2 snapshot; `main` is behind. P0–P4 built: P4 adds `internal/processing/ranker.go+abstain.go+resolve.go`, `internal/truth/quarantine.go`, `internal/intake/*`, `internal/query/*` + mirror tests. Full tree `build+vet+fmt+test` green. Next: P5 per-file plan (rpc/server.go+router.go, maintenance/consolidate.go+prune.go+calibrate.go, telemetry), then code on go. P5 owns the remaining open items: 7 ops over UDS, serialized tick, FR16 calibration (thresholds refresh on the score scale — see DESIGN §3 note), P5 emergency repair op, index-corruption trigger (DESIGN §8 OPEN). Known gaps carried: `-race` unrunnable here (cgo limit); `keyPrefix` deletion awaits owner go; trust-floor adversarial pass (DESIGN §3 PROPOSED) is a pre-P5 item.

### Prior handoff (P4 entry, 2026-09-24, superseded — see above)

[SUPERSEDED 2026-09-24: P3-entry handoff below replaced by the P4-entry handoff above — branch moved v1/p1-p2 → v1/p0-p3, P3 built + hardened. Old text kept for cross-reference.]

Work happens on branch `v1/p0-p3` (pushed, tracking `origin/v1/p0-p3`); `v1/p1-p2` stays as the P2 snapshot; `main` is behind. HEAD is the P3 doc-sync commit (`9f8e817`) on top of the H1–H7 hardening commit (`5341c95`) on top of the P3 implementation commit (`8f4fe75`). P0–P3 built, hardened (H1–H7), committed, pushed. Next: P4 per-file plan, then code on go. P4 scope (`docs/BUILD-PLAN.md` P4 section): ranker, abstain, resolve, intake (emit + FR15 rate limit, correct/attest), query (recall with ACL pre-filter, as_of) — includes the P4-side quarantine debt: durable M/quarantine prefix-scan + single-txn staged writer (P3 proves exclusion given the set; P4 wires the set).

To pick up full context, read in this order (docs first, then the exact code the docs point at):

1. `git status` / `git log` — confirm branch + HEAD + clean/dirty.
2. `docs/BUILD-PLAN.md` P3 section — `{BUILT}` + `{HARDENED}` records (what shipped, baselines, H1–H7 findings, scale numbers); P1 section codec count-bound note; P2 section provisional-constants note (three v1-fixed numbers + recalibration path — do NOT re-dig; the note is the record).
3. `docs/BUILD-PLAN.md` P4 section — exit criteria (FR6 score curve, per-subject dedup, ABSTAIN gate, `resolve()` transitions, M6 suite).
4. `docs/DESIGN.md` §2 quarantine-set contract — P4 owns the writer + durable scan now; §3 ranker (FR6 formula + trust-floor PROPOSED flag — adversarial pass still open, pre-P4 item); §4 gate table + §5 correct/attest dispatch (intake executes `Outcome` here).
5. SRS `FR5`/`FR6`/`FR7`/`FR12`/`FR15`/`FR18` + `docs/TECH-STACK.md` §2.2–§2.4 (intake/query/processing lanes, router payload-shape dispatch).
6. Code: `internal/processing/gate.go` (`Outcome` — the contract P4 executes), `internal/index/` (`Exact`/`Fuzzy`/`Chained`/`Terms` — what recall ranks over), `internal/truth/store_iface.go` (execution primitives), `internal/processing/confidence.go` (`Score` — read-time confidence).
7. Baselines: P2 Match 48µs / Decide 763µs; P3 Exact 407ns / Fuzzy 133µs (200 records); H7 100K append 1.36s (73.8k/s), scan 89ms (1.1M/s) — all i5-12450H.
8. Known gaps carried into P4: `-race` unrunnable here (cgo limit); `keyPrefix` dead-code deletion awaits owner go; trust-floor adversarial pass (DESIGN §3 PROPOSED) is a pre-P4 item, not P4 itself.

### Prior handoff (P3 entry, 2026-09-23, superseded — see above)

Work happens on branch `v1/p1-p2` (pushed, tracking `origin/v1/p1-p2`); `main` is behind. HEAD is the doc-sync commit on top of the P2 implementation commit. P2 is done-done (25/25 tests green); the P2→P3 gate is closed. Next: P3 per-file plan, then code on go.

To pick up full context, read in this order (docs first, then the exact code the docs point at):

1. `git status` / `git log` — confirm branch + HEAD + clean/dirty.
2. `docs/BUILD-PLAN.md` P2 section — `{BUILT}` record (what shipped, baselines), provisional-constants note (three v1-fixed numbers + recalibration path — do NOT re-dig; the note is the record), and the `P2→P3 GATE` block (blockers #1–#3 all closed: Option A quarantine, full candidate sets, P2 committed).
3. `docs/BUILD-PLAN.md` P3 section — exit criteria + the P3 conformance line (index must apply P2's `Normalize`; P3 exit replays P2 matcher fixtures).
4. `docs/DESIGN.md` §2 quarantine-set contract — key layout, single-txn writer, three readers, promotion, crash inheritance, P1 boundary (additive `*Store` methods only, iface P0-closed).
5. `docs/DESIGN.md` §4 gate table (items 1–10) + SRS `FR4`/`FR9`/`FR12`/`FR14` tagged blocks — the locked verdict semantics P3 serves (max-severity fold needs FULL candidate sets per key, never top-1).
6. `docs/TECH-STACK.md` §2.5/§2.6 — Badger rules, versioned secondaries, what the index layer owns.
7. Code: `internal/processing/gate.go` (`Outcome` — the contract P4 executes), `matcher.go` (`Normalize`/`Key` — P3 must share them), `internal/truth/store_iface.go` (execution primitives), `keys.go` (framing discipline).
8. Baselines: Match 48µs / Decide 763µs per 200 candidates (i5-12450H, unindexed scan — P3's index replaces it, these are the before-numbers).
