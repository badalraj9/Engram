# issues.md — live defect / rough-edge ledger

Running list. Add entries as they are uncovered; each gets a line on what
it breaks and the candidate fix. Nothing here is scheduled until the
owner moves it to BUILD-PLAN.

## 1. Engram not auto-started with the host session

**Symptom:** starting Claude Code / OpenCode auto-spawns the MCP entry
(`engramd --mcp` dialer), but it reports "server error" — the dialer
dials a socket and opens no store (`cmd/engramd/main.go:98`), so with no
daemon holding the socket the tools fail.

**Why:** the daemon (serve holder) must be started manually with pinned
env (`ENGRAM_DATA_DIR`, `ENGRAM_UDS_PATH`); nothing starts it, nothing
restarts it. This is parked P4 (holder permanence) + P6 (first-run UX).

**Candidate fixes:**
- point the host MCP config at `engramd --both` (daemon + MCP in one
  process, spawned by the host) — works for one session; a second
  concurrent session cannot bind the socket (one-writer rule), so
  multi-session needs the holder model;
- or a supervisor/holder that survives sessions and is idempotent to
  start (bare `engramd` "just works" — P6).

**Status:** open, owner-confirmed 2026-11-13.

## 2. Future-dated facts stayed hidden forever (F1 — FIXED 2026-11-13)

**Symptom:** tester finding. A SCHEDULED fact (valid-time in the future)
never surfaced after its window opened — write-side labels SCHEDULED
(`internal/intake/emit.go:200`), recall hard-skips the status
(`internal/query/recall.go:121`), and nothing ever flipped the status
back.

**Fix:** the validity sweep now also activates — EPISODIC SCHEDULED
with `valid_from <= wallNow < valid_to` flips ACTIVE in the tick
(`internal/maintenance/expire.go`), expiry outranking activation;
`Result.Activated` counts it; DESIGN §6 amended. Tests pin both
boundary sides. Deployed to the live holder pending rebuild.

**Status:** fixed in code, awaiting rebuild/deploy.

## 3. Corrected pair flagged as contradiction (F2 — NOT REPRODUCIBLE)

**Symptom:** tester claimed a correction later reached BOTH-state via
the tick's conflict detector.

**Verified by reading:** both correct paths tombstone the superseded
row (`internal/intake/correct_attest.go:93,104`) and the detector skips
TOMBSTONED (`internal/maintenance/consolidate.go`). Pinned by
`TestTick_CorrectedPairNotFlagged` (passes on old code, kept as
regression pin). If the tester hit BOTH, the likely real path is the
locate-floor miss (correction INSERTs as new content, old row stays
live by design) — needs the tester's exact transcript to confirm.

**Status:** closed as not-reproducible; transcript wanted.

## 4. Concurrent agents fight over the global tick counter (owner-confirmed scenario, 2026-11-13)

**Symptom:** two agents (owner's re-test runner + tester agent) share one
holder; `consolidate(tick)` is monotonic-global, so the second agent's
ticks get rejected (`tick must exceed last accepted tick`) while the
first keeps advancing. Same store, interleaved writes.

**Why:** one clock, one atomicity unit is correct for writes — but the
tick namespace has no per-caller scoping, so multi-agent dogfood on a
shared holder needs an operator convention (one ticker) or holder
isolation per agent.

**Status:** open — needs a decision when multi-agent use becomes real.
