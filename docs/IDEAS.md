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

## 5. V2 lens — experience layer + memory loop (2026-10-28, parked, after owner discussion; shape deliberately undecided)

Nothing here is designed, named, or scheduled. The external framing:
V1 is the "what is true" layer; V2 adds "what happened, when, why,
and what came of it" — traceable belief (experience linked to fact),
then a control layer (the memory loop). The framing is kept; the ask
is filtered into what already exists, what is genuinely new, and
what belongs outside the core. Amended/renamed here, not adopted.

### 5.1 Already in the core under other names (do not re-add)

| Ask (external) | Engram today |
|---|---|
| episodic — "what happened?" (single observation) | `EPISODIC` record type (P0); every fact-write is an episodic observation |
| `contradicts` experience→fact link | write gate routes conflicting writes: UPDATE (fresh eligible claimant) or BOTH (DESIGN §4 table) |
| `derived_from` (episode → fact) | `Record.DerivedFrom` (SUMMARY/SEMANTIC lineage; FR10) |
| trace fact — "why do we believe this?" | partially: provenance + FR9 witness sets + FEEDBACK replay + `as_of` both axes |
| consolidation "episodes → few durable facts" | the serialized tick: EPISODIC clusters → SUMMARY, deterministic template, D_probe gate (DESIGN §6) |
| metamemory "knows what it doesn't know" | FR7/FR6 abstention — one curve, threshold-gated; never guesses |
| adaptive forgetting with interference + decay | FR11 economics + `R_m` half-life; tickGC E1/E2 (expire + calibrated retirement) |
| open loops "stays active until closed" | SCHEDULED status for future-dated content; BOTH until resolve; `as_of` reconstructs everything else |
| reconsolidation "memory updated when reality contradicts" | close-interval + append is Engram's only revision shape (FR17); corrections reset confidence lineage |

### 5.2 Naming collision (settled before any V2 build, not after)

The external "episodic" (session, start/end time, actors, goal,
actions, artifacts, outcome, summary) is a DIFFERENT entity from the
P0 `EPISODIC` type (one observation). Reusing the name would muddy
the type enum every test cites. Working placeholder until a schema
doc settles it: EXPERIENCE record or EVENT record. The owner has not
decided; nothing below assumes a name.

### 5.3 Genuinely new substrate asks (each needs SRS/DESIGN first)

1. **Experience records** — the event-shaped entity above, append-only
   like every other version; carries session/source/interval/ACL,
   links to artifacts it produced.
2. **Typed evidence edges, supports|contradicts|derived** — experience
   ↔ fact. Risks to design out, up front: append-only discipline means
   edges are EVENTS, not mutable record fields; FR18's verdict-only
   narrowing existed because weight-bearing feedback was a
   confidence-poisoning surface — a full experience offered as
   "supports" evidence has the same shape of risk and the same
   discipline applies (evidence influences rank/abstain, never
   mutates confidence in place).
3. **Experience retrieval** — by time, project, participant, outcome.
   New queries imply NEW WIRE OPS (recall_episodes class) — proto
   widening is Q4-blocked; no verb before that gate.
4. **Capture path: experience IN** without transcripts-as-memory.
   Extraction stays caller-side (SRS 1.3). Any in-core step is
   deterministic composition ONLY (same boundary where NFR2 is most
   at erosion, cited in SRS §8).
5. **Working memory, if ever substrate-side** — external says
   host-side/short-TTL. Engram's litmus test agrees
   (recomputable-in-bounded-time → signal, never record); dynamics
   layer stays tier-D stub until Q7 evidence.

### 5.4 Control-layer asks (candidates for the adapter layer, IDEAS §1 doctrine — outside the frozen core)

- salience — "what matters for this goal/risk right now"
- cue-based associative recall (partial input, not exact
  subject/predicate) — overlaps the parked N-gram lane, §4, and
  inherits its entry conditions verbatim
- context assembly — controller selecting a BUDGET of memories into
  working set, suppressing the rest (host-side)
- proactive surfacing — the parked proactive-layer candidate
- narrative/self model — "who am I to you, what's unfinished"
  — adapter-level, SRS 2.3 defer list applies

### 5.5 Standing don'ts (adopted as candidate frozen rules, pending owner ratification)

- No full transcripts as permanent memory.
- No embeddings rebranded as episodic memory.
- No procedural memory before evidence links (stale instructions
  with no explanation otherwise).
- No durable working memory — it expires by construction.

### 5.6 Sequencing note

The agreed lab order stands ahead of any of this: polish P1–P6,
Q2 dogfood, Q3 emit curve, Q4 wire widening. Experience-layer ops
require Q4 anyway. The lens is recorded so it is not lost to chat
evaporation — not so it is started.
