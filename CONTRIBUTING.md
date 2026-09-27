# Contributing to Engram

## Docs first — code follows

Authority order, no exceptions:

1. `SRS-engram-v2.md` — what/why. Does not get reopened by a PR.
2. `DESIGN.md` — how. Disputes go here first, as text, before code.
3. `TECH-STACK.md` — with-what.
4. `BUILD-PLAN.md` / `BUILD-RANKS.md` — build order and importance.

If your change contradicts any of the above, update the doc first (small,
explicit edit) and get it acknowledged — otherwise the PR will be closed.

## Frozen rules (will fail review)

- **No LLM calls** anywhere in `emit` / `recall` / `consolidate` paths (NFR2).
- **No vector DB** — embedded KV only (NFR3).
- **Append-only**: records are never mutated in place (FR17).
- **No hidden scheduler**: maintenance runs only inside `consolidate(tick)` (FR10).
- **ACL pre-filter** before any ranking/combining (FR13).
- **No new external dependencies** without discussion — stdlib-first (see TECH-STACK).

## Try building — open phases

P0–P4 are done (built, hardened, green). Claim any slice of the next open phase in `BUILD-PLAN.md`:

- **P5 serve** — gRPC/UDS wiring, serialized tick, FR16 calibration, feedback-by-ID secondary (bench-required: 111.5ms recall @100K), P5 emergency repair op.
- **P6 harden** — M1–M8 measurement gates + fuzzy-lane worst-case strategy (open risk, see `BUILD-PLAN.md` P4 `{HARDENED}`).

Open an issue naming the file(s) you want (e.g. `internal/index/rebuild.go`) and
we'll mark it claimed. Small, finished slices beat large drafts.

## PR expectations

- `gofmt -l` clean, `go build ./...`, `go vet ./...` green.
- New logic ships with a test proving the doc clause it implements
  (cite the FR/M-item in the test name, e.g. `TestM6_SupersedeAppendOnly`).
- No test-compat shims: phases must test against real outputs of prior phases.
- Update the relevant doc if behavior changes — code and docs land together.

## Setup

```sh
go build ./...
go vet ./...
```
