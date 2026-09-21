# Engram

**Memory that knows what it knows.**

Engram is a next-gen memory substrate for AI agents — persistent, truth-aware recall with explicit `ABSTAIN` instead of hallucinated guesses, bi-temporal audit (`as_of` any date), and forgetting as a default. All at infrastructure speed with zero LLM calls and zero vector DB.

> Status: pre-build. Contracts (P0) implemented; measurement gates M1–M8 pending. See docs before contributing.

## Why not RAG / vector memory

| | RAG / vector similarity | LLM-mediated (mem0-class) | Engram |
|---|---|---|---|
| Returns | topically-similar chunks | model-ranked text | currently-true ranked facts or explicit `ABSTAIN` |
| Truth tracking | none | prompt-based | bi-temporal records + supersede, never overwrite |
| Confidence | cosine score | opaque | noisy-OR + session clustering, calibrated thresholds |
| Forgetting | manual cleanup | manual cleanup | decay + economics-based pruning, validated (M1–M8) |
| Dependencies | vector DB | LLM in the loop | embedded KV only (BadgerDB), no model calls |
| Audit | no | no | every fact traceable to source, session, time |

## Docs (read in this order)

- `SRS-engram-v2.md` — what/why (requirements, wedge, measurement gates)
- `DESIGN.md` — how (record schema, write gate, ranker, seams)
- `TECH-STACK.md` — with-what (BadgerDB, gRPC/UDS, pure-Go processing)
- `BUILD-PLAN.md` — testable build phases P0–P6
- `BUILD-RANKS.md` — file importance tiers (SSS → E)

## Quickstart (P0)

```sh
go build ./...
go vet ./...
```

P0 provides contracts only: `proto/engram.proto` (7 ops), `internal/rpc` (domain types),
`internal/truth` (store interface), `internal/config` (TOML + env).

## Roadmap

- [x] P0 contracts — compile gate
- [ ] P1 truth — append-only log + replay (M8a)
- [ ] P2 write — five-way gate, confidence, ACL
- [ ] P3 find — rebuildable index
- [ ] P4 read — ranker, abstention, resolve (M6)
- [ ] P5 serve — UDS/gRPC daemon, serialized tick
- [ ] P6 harden — full gates M1–M8

## License

Apache-2.0 — see `LICENSE`; attributions live in `NOTICE`.
