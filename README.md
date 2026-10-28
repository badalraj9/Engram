# Engram

**Memory that knows what it knows.**

Engram is a memory system for AI agents that behaves like a good
witness: it remembers what happened, says when something changed its
mind, admits when it doesn't know, and forgets what's no longer worth
keeping. No model calls, no vector database — one small Go service
over a local socket, and every answer traceable to its source.

## What it does, in plain words

- **Remembers truthfully.** Facts go in as structured records. When
  the world changes, the old version is closed and a new one appended
  — history is never overwritten, so "what was true in June?" always
  has an answer.
- **Says "I don't know."** A query returns ranked, currently-true
  facts — or an explicit `ABSTAIN` when nothing clears the bar.
  Guessing is not a feature.
- **Forgets on principle.** Unused, low-value facts decay and are
  pruned by economics, not by cleanup scripts. Untrusted claims wait
  in quarantine until a second, independent source backs them.
- **Settles arguments.** Contradictions with no clear winner sit in
  an explicit both-state until ruled on — never silently overwritten.

Seven operations: `emit`, `recall`, `correct`, `attest`, `resolve`,
`as_of`, `consolidate`. The details live in the docs, not here.

## Status

> v1 complete on branch `v1(full)`: truth log, write gate,
> rebuildable index, read path (ranker + abstain + resolve +
> quarantine), UDS/gRPC daemon with serialized tick, full gates
> (M1–M5, M6, M7, M8a, M8b), feedback lookup, `cmd/engramd` + Go
> client + MCP shim. Post-v1 polish landed: valid-now enforcement
> on the read path (every recalled fact satisfies
> `valid_from <= now < valid_to`, SCHEDULED excluded from recall),
> tick-GC (expired-window expiry plus disuse-based retirement
> inside the host tick), and the seventh MCP verb `consolidate`.
> Full tree `build + vet + fmt + test` green (13 packages under
> test; 2 transport/schema packages ship without test files).
> Next: dogfood (UC5) — the running system decides what v1.1 earns;
> queued candidates and their entry gates live in `docs/BUILD-PLAN.md`.

Honest numbers (i5-12450H): recall 0.16ms at 1K facts, 0.43ms at
10K, 31.6ms at 100K — the P6 feedback secondary replaced the per-
recall log scan that made the old numbers linear (10ms and 111.5ms
before; 3.5-23x faster, one machine, one run). 1M extrapolates to
~0.3s and is pending its replay measurement (Q3 in the build plan).
Writes hold p99 0.56-1.1ms flat from 1K to 1M facts, measured (M7).
Nothing here claims scale it hasn't measured.

## Docs (read in this order)

All design docs live in `docs/`:

- `docs/SRS-engram-v2.md` — what and why (requirements, wedge, gates)
- `docs/DESIGN.md` — how (records, write gate, ranker, seams)
- `docs/TECH-STACK.md` — with what (BadgerDB, gRPC/UDS, pure Go)
- `docs/BUILD-PLAN.md` — build phases P0–P6, with per-phase records
- `docs/BUILD-RANKS.md` — file importance tiers (SSS → E)
- `docs/CONTRIBUTING.md` — how to contribute (docs first, tests prove clauses)
- `docs/IDEAS.md` — parking lot: explicitly not decisions

## Try it

```sh
go build ./...
go vet ./...
go test ./...
```

That's the whole suite — no services to start, no keys, no network.

One binary, four modes:

- `engramd` — serve the Unix-socket daemon only
- `engramd --mcp` — stdio MCP translator dialing a live daemon
  (tools: remember, recall, correct, attest, resolve, as_of,
  consolidate)
- `engramd --both` — daemon and MCP translator in one process
- `engramd --tick N` — one-shot maintenance tick through a live
  daemon (host-triggered cadence; the daemon never schedules itself)

Store and socket live in the working directory by default
(`./engram-data`, `./engram.sock`); pin them with `ENGRAM_DATA_DIR`
and `ENGRAM_UDS_PATH`. `client/go` is the thin Go stub; the MCP shim
serves any standard MCP host.

## The rules it won't break

No model in the loop. No vector database. Records are never edited
in place. Nothing runs on a hidden schedule — maintenance happens
only when the host asks for it. Nothing is confirmed before it's
durable. These aren't aspirations; they're build-failing constraints,
and every phase's tests prove them.

## Roadmap

- [x] P0 contracts — compile gate
- [x] P1 truth — append-only log + replay (M8a) + quarantine set
- [x] P2 write — five-way gate, confidence, ACL
- [x] P3 find — rebuildable index
- [x] P4 read — ranker, abstention, resolve (M6)
- [x] P5 serve — UDS/gRPC daemon, serialized tick, calibration refresh, repair op
- [x] P6 harden — measurement gates (M1–M5/M7/M8b), feedback lookup, `cmd/` + client
- [x] Post-v1 polish — valid-now read enforcement, SCHEDULED labeling,
  tick-GC (expire + calibrated retirement), 7th MCP verb `consolidate`
- [ ] v1.1 — dogfood (UC5) decides: emit curve at 1M (Q3), wire
  widening via protoc (Q4), latency cascade (Q5), semantic lane
  (Q7) — each has an entry gate in `docs/BUILD-PLAN.md`; nothing
  starts until its gate opens

## License

Apache-2.0 — see `LICENSE`; attributions live in `NOTICE`.
