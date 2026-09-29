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
  {APPROVED 2026-09-29 (owner): first adapter, build now. Co-start rule:
  one command starts core + shim together — the shim supervises the
  core process (spawn, wait-for-socket, stop-on-exit); core stays
  single-writer, shim stays stateless/fail-closed.}
- Framework clients: thin stubs per agent framework (LangChain-class,
  CrewAI-class, raw loops) — distribution before features.
- Proactive layer: context volunteered before asked (the deferred
  ContextMonitor shape), host-triggered only — never a scheduler inside.
- Modalities: extraction stays outside Engram (SRS §1.3); one adapter per
  input shape when needed.
- Chat cockpit (CLI first, UI later): home chat over the core — per-turn
  structure→remember plus recall-before-reply; live-ingestion prover and
  dogfood cockpit. Post-v1 addon (core/presenter split: cf. Go core,
  separate TUI), not current work.
- Multi-agent belief spaces: explicitly deferred (SRS §2.3); listed so it
  isn't forgotten, not so it's started.

## 3. Distribution shape

One static binary per platform (`GOOS=` builds) + minimal config +
per-language stub. Sharing Engram is sharing the exe — no runtime, no
database to install, no keys.

## 4. Frozen N-gram embedding candidate lane — v1.1 (2026-09-28, parked, not decided)

Prompted by the DeepSeek Engram name collision (verified: their repo
exists; their thing is in-model parametric N-gram memory, ours is an
external truth substrate — no relation, no rename; README carries the
disclaimer iff locked). One transferable idea survived review:

**Why:** P6's feedback lookup makes scoring fast; this would make
finding wide — meaning-matches the lexical lanes miss (paraphrased
emits, "car"/"automobile") that trigram-Jaccard can't catch. Width,
not speed. Pattern-matching results with no model present: knowledge
compiled into lookup, same rhyme as the substrate itself.

**How:** a frozen N-gram→embedding table consulted as a fourth recall
lane beside Exact/Fuzzy/Terms, feeding candidates into the existing
ranker + abstention gate (which stay authoritative). Table is data,
never a call — but "arguably inside NFR2" does real work, so entry
requires explicit blessing, never drift.

**Where:** v1.1 semantic-recall slot (`BUILD-PLAN.md` line 3), behind
the FHRR bundle work — never v1, never the core.

**Price, stated upfront:** the table must come from somewhere
(corpus, vectors, versioning — a learning dependency the posture
currently forbids); FR4's "Raj"/"Rahul stay distinct" gets harder
(embeddings love exactly that near-miss — floor anchors re-proven);
more candidates shift the abstention curve (FR16 recalibrates); all
M-gates re-run. Entry conditions, all mandatory: beats the trigram
band on real vocabulary (M2), survives the distinctness anchors,
owner word. Otherwise it stays parked here.
