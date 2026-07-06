# SDK decoupling scope

Goal: let someone build an agent app by importing `github.com/cmcoffee/gohort/core`
without booting the whole gohort server. This is the "gohort as an SDK, not the
whole framework" path.

## The two facts that frame everything

1. **The agent loop is already SDK-shaped.** `RunAgentLoop` reads `RootDB` and
   `AppContext` zero times, and every ambient it does touch (agent-loop tuning,
   route stages, LLM schedulers) has a compiled no-boot default. A basic agent
   app is `core.NewAgent(cfg)` + `RunOnce` (or `AppCore{LLM: ...}` +
   `RunAgentLoop` with no `RouteKey`), no boot required. See
   `extras/sdk-agent/main.go` and `core/sdk.go`.
2. **The real coupling is one family: `RootDB` and its derivatives**
   (`VectorDB`, `CollectionsDB()`, `AuthDB`, plus `embedCfg`). ~200 ambient reads
   across ~19 files, all in the *richer* subsystems (memory, RAG, skills,
   collections, triggers, scheduler, desktop), none on the loop. The leaf
   functions (`StoreMemoryFact`, `ListMemoryFacts`, `SearchCollections`, graph
   store, `UserDB`) already take a `Database` param, so the seam exists; it's the
   globals as *ambient defaults* that need threading. Boot already lives in
   `package main` (gohort.go, config.go), not `core`.

## Phases

### Phase 0 — Agent-loop SDK. ~1-2 days. DONE (this is the packaging).
Import core, build an LLM, run loops with your own tools. Shipped as
`core.NewAgent` + `AppCore.RunOnce` + the `extras/sdk-agent` example. Minimal
public surface: `AppCore`, `RunAgentLoop`, `AgentLoopConfig`, `LLM`,
`NewLLMFromConfig`, `LLMProviderConfig`, `ChatOption` helpers, `AgentToolDef` /
`Tool` / `ToolParam`, `Database`, `Message`, `Response`.

### Phase 1 — Decouple the DB family. The real project.
Make memory/RAG/collections/graph usable with injected state instead of the
`RootDB` globals. Work: thread an env/runtime struct (carrying `RootDB`,
`VectorDB`, `CollectionsDB`, `AuthDB`, `EmbeddingConfig`) through the ambient
read sites, and fix `SearchCollections`'s deployment-scoped path (it reaches
globals even though it takes a `base` param).

- **Scoping decision:** decouple all 19 files, or just the **data subsystems an
  SDK consumer needs** (memory, RAG, collections, graph) and leave the
  product-integration globals (desktop, scheduler, triggers, messaging) coupled?
  Those aren't SDK-relevant; recommend leaving them.
- **Effort:** ~3-5 days for the SDK-relevant data subset; ~2 weeks for the whole
  DB family.

**Pattern (established, option #1).** For each subsystem: extract a
`xxxWith(env, ...)` internal that takes the injectable state; keep the package
`Xxx(...)` as a thin wrapper passing the *globals* (so server behavior is
unchanged); add an `AppCore` method passing the *instance* fields, falling back
to the globals for anything unset. Proven on `SearchCollections` (0.5.59):
`collectionsEnv` + `globalCollectionsEnv` + `AppCore.collEnv()` +
`AppCore.SearchCollections` + `searchCollectionsWith`, plus `EmbedWith(ctx, cfg,
text)` and the `AppCore.VectorDB` / `AppCore.EmbedCfg` fields.

**Remaining subsystems (replicate the pattern):** `FetchCollectionDoc` (same
file, same globals), the memory-fact embedding/dedup path (`StoreMemoryFact` ->
`EmbedWith`), chunk ingest (writes to the `VectorDB` global), and the graph
store. Each is a mechanical application of the same three-part split.

### Phase 2 — Stable API surface + docs. ~3-5 days.
Draw the line across the ~1305 exported core symbols: what's SDK vs internal
(move internals behind `internal/`, or document the surface), version it, write
docs + examples. This is the deferred "external audience" work — the difference
between "works if you know the internals" and "a real SDK."

### Phase 3 — Multi-instance isolation. Skip unless needed.
A few globals (scheduler singletons, route registry, tunables cache) are
process-wide. Fine for one SDK consumer; only matters for multiple isolated
gohort instances in one process. Defer until a concrete use case.

## Bottom line
- "Just the agent loop" SDK: done (Phase 0).
- "Loop + tools + memory/RAG with injected state" SDK: ~2-3 weeks (Phase 0 +
  scoped Phase 1 + Phase 2).
- It's threading-and-hygiene work, not a redesign: the leaf seams already take
  `Database` and the loop is already clean. The `RootDB` family is the whole
  ballgame.
