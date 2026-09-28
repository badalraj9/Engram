# IDEAS — explicitly not decisions

Status: parking lot. Nothing here is designed, scheduled, or promised.
Nothing here overrides the authority order (SRS → DESIGN → TECH-STACK →
BUILD-PLAN). Items graduate only by owner word, into docs first, as text.

## 1. Core + adapters doctrine (2026-09-27, owner-stated)

Engram core stays what it is: frozen rules, single writer, no model,
no scheduler, no guessing. Everything user-facing lives in stateless
adapters above it — MCP shims, framework clients, proactive layers,
future modalities. Each adapter translates and passes through; none
holds truth, none schedules, none guesses.

Adapter rules (so middleware never eats the architecture):
1. Stateless — no caching (a cache is a second source of truth).
2. Fail closed — daemon unreachable means "unavailable," never invented memories.
3. Core stays single-writer — adapters queue, never write around the daemon.

Why: MemoryThread's 35 modules weren't wrong features, they were
features in the wrong layer. This time they get their own layer.

## 2. Candidate adapters (unscheduled, unordered)

- Claude Code connector: MCP shim (stdio) mapping remember/recall/correct
  onto emit/recall/correct over UDS. Transport-only, ~200 lines.
- Framework clients: thin stubs per agent framework (LangChain-class,
  CrewAI-class, raw loops) — distribution before features.
- Proactive layer: context volunteered before asked (the deferred
  ContextMonitor shape), host-triggered only — never a scheduler inside.
- Modalities: extraction stays outside Engram (SRS §1.3); one adapter per
  input shape when needed.
- Multi-agent belief spaces: explicitly deferred (SRS §2.3); listed so it
  isn't forgotten, not so it's started.

## 3. Distribution shape

One static binary per platform (`GOOS=` builds) + minimal config +
per-language stub. Sharing Engram is sharing the exe — no runtime, no
database to install, no keys.
