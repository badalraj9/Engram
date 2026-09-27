# Engram

**Memory that knows what it knows.**

Engram is a next-gen memory substrate for AI agents — persistent, truth-aware recall with explicit `ABSTAIN` instead of hallucinated guesses, bi-temporal audit (`as_of` any date), and forgetting as a default. All at infrastructure speed with zero LLM calls and zero vector DB.

> Status: P0–P4 built and hardened (truth log, write gate, rebuildable index, read path with ranker + ABSTAIN + resolve + quarantine). Full tree `build + vet + fmt + test` green. Next: P5 serve (UDS/gRPC daemon, serialized tick). See docs before contributing.

## Why not RAG / vector memory

| | RAG / vector similarity | LLM-mediated (mem0-class) | Engram |
|---|---|---|---|
| Returns | topically-similar chunks | model-ranked text | currently-true ranked facts or explicit `ABSTAIN` |
| Truth tracking | none | prompt-based | bi-temporal records + supersede, never overwrite |
| Confidence | cosine score | opaque | noisy-OR + session clustering, config-loaded thresholds (FR16 calibration lands in P5) |
| Forgetting | manual cleanup | manual cleanup | decay on read + quarantine; economics-based pruning lands in P5, validated at the M-gates in P6 |
| Dependencies | vector DB | LLM in the loop | embedded KV only (BadgerDB), no model calls |
| Audit | no | no | every fact traceable to source, session, time |

## Docs (read in this order)

- `SRS-engram-v2.md` — what/why (requirements, wedge, measurement gates)
- `DESIGN.md` — how (record schema, write gate, ranker, seams)
- `TECH-STACK.md` — with-what (BadgerDB, gRPC/UDS, pure-Go processing)
- `BUILD-PLAN.md` — testable build phases P0–P6
- `BUILD-RANKS.md` — file importance tiers (SSS → E)

## Quickstart (P0–P4)

```sh
go build ./...
go vet ./...
go test ./...
```

Shipped: `proto/engram.proto` (7 ops), `internal/rpc` (domain types),
`internal/truth` (append-only Badger log + quarantine set), `internal/config` (TOML + env),
`internal/processing` (five-way gate, matcher, confidence, ranker, abstention, resolve),
`internal/index` (rebuildable inverted + trigram find layer),
`internal/intake` (emit/correct/attest/resolve execution + FR15 rate limit),
`internal/query` (recall with ACL pre-filter, `as_of`, M6 suite).

## Roadmap

- [x] P0 contracts — compile gate
- [x] P1 truth — append-only log + replay (M8a) + quarantine set
- [x] P2 write — five-way gate, confidence, ACL
- [x] P3 find — rebuildable index (stdlib postings behind a bitmap seam)
- [x] P4 read — ranker, abstention, resolve (M6)
- [ ] P5 serve — UDS/gRPC daemon, serialized tick (owns FR16 calibration + feedback-by-ID secondary, required by bench: 111.5ms recall @100K)
- [ ] P6 harden — full gates M1–M8 (incl. fuzzy-lane worst-case strategy, open risk)

## License

Apache-2.0 — see `LICENSE`; attributions live in `NOTICE`.
