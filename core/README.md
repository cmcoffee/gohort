# core

Framework foundation for all fuzz agents. Provides the agent interface, LLM abstraction, web infrastructure, and shared utilities.

## Key Types

### FuzzAgent

Base struct embedded by all agents. Provides:

- `LLM` — Primary (worker) model for volume calls
- `LeadLLM` — Optional precision model for critical calls (falls back to LLM if nil)
- `DB` — Agent-private database bucket
- `Cache` — Ephemeral per-run cache
- `Report` — Token usage tracking
- `Flags` — CLI flag parsing

### Key Methods

```go
// Worker LLM call with token tracking
resp, err := agent.WorkerChat(ctx, messages, opts...)

// Precision LLM call — uses LeadLLM if available, falls back to LLM
// Also falls back on error or empty response (safety filter)
resp, err := agent.LeadChat(ctx, messages, opts...)

// Get the precision model (or worker if not configured)
llm := agent.GetLeadLLM()
```

## LLM Providers

All providers implement the `LLM` interface:

```go
type LLM interface {
    Chat(ctx, messages, opts...) (*Response, error)
    ChatStream(ctx, messages, handler, opts...) (*Response, error)
}
```

| File | Provider | Notes |
|------|----------|-------|
| `llm_openai.go` | OpenAI + Ollama | Ollama uses OpenAI-compatible endpoint with `num_ctx` warmup |
| `llm_gemini.go` | Google Gemini | Thinking model support (2.5+), safety settings, auto thinking budget |
| `llm_anthropic.go` | Anthropic | Claude models with streaming and tool use |

## Web Infrastructure

### LiveSessionMap[T]

Generic concurrent session manager for SSE-based agents:

```go
sessions := NewLiveSessionMap[MyEvent](3) // max 3 concurrent

// Register a session (returns nil if at limit)
s := sessions.Register(id, label, cancelFunc)

// Buffer events for reconnecting clients
sessions.AppendEvent(id, event, isDone)

// HTTP handlers (mount directly on routes)
mux.HandleFunc("/api/cancel", sessions.HandleCancel("Agent"))
mux.HandleFunc("/api/reconnect", sessions.HandleReconnect())
mux.HandleFunc("/api/live", sessions.HandleLive())

// Auto-cleanup after 10 minutes
sessions.ScheduleCleanup(id)
```

### HistoryHandlers[R, S]

Generic CRUD handlers for agent history:

```go
list, detail, delete := HistoryHandlers[Record, Summary](dbFunc, tableName, transformFunc)
```

### Other Web Utilities

- `NewSSEWriter(w)` — Creates SSE stream writer
- `ServeHTMLWithBase(w, html, prefix)` — Serves HTML with base href injection
- `MountSubMux(mux, prefix, sub)` — Mounts a sub-mux under a path prefix

## Search Utilities

```go
// Parallel consensus searches (expert opinion + systematic reviews + fact-checks)
results, combined := RunConsensusSearches(cache, shortTopic, jurisdiction)

// Search with caching and deduplication
result := CachedCrossSearch(cache, query)
result := CachedSearch(cache, query)
```

## Tunables Registry

Framework knobs self-register instead of living as hardcoded constants, so they
surface in the admin **Tuning** tab automatically:

```go
// Register once (typically in an init()), then read through the typed accessor.
RegisterTunable(TunableSpec{
    Key: "document_extract_timeout", Category: "Timeouts",
    Kind: KindSeconds, Default: 30,
    Label: "Document extract timeout", Help: "Max wall-clock per PDF/DOCX extract.",
})

timeout := TuneDuration("document_extract_timeout") // honors the admin override, else Default
maxK    := TuneInt("knowledge_top_k")
ratio   := TuneFloat("hybrid_search_alpha")
```

Each knob gets an editable, revert-to-default row in the admin UI; no per-knob UI code.

## Memory Primitives

Per-namespace stores agents layer on top of:

- `factstore.go` — flat always-in-prompt facts (semantic dedup + supersession at save).
- `graphstore.go` — entities + typed relationships (`UpsertGraphEntity` / `LinkGraphEdge`, queried via the agent's `link_entities` / `recall_about` tools), with a query-time graph→vector bridge that folds the top related knowledge passages into a recall.
- `vector_store.go` — embedded-chunk semantic index (reference memory + knowledge).

## External-Cost Ledger

Source hooks and API credentials carry an optional per-call cost; record it once
and it accrues per-source for the admin **Costs** tab:

```go
RecordExternalCost(sourceID, label, costPerCall) // accrues per-source; read via CostBySource
```

## Connectors

`connector.go` is a domain-agnostic "bridge type" primitive: a `Connector`
record an authoring agent drafts and an admin approves, materializing a real
capability with no code change. core owns only the record + draft→approve
lifecycle; each kind supplies a handler:

```go
RegisterConnectorKind(kind, handler) // handler: Validate / Materialize / Teardown / Summary
```

Shipped kinds live in sibling files: `connector_mcp.go` (`remote_mcp`, wraps
`MCPServerConfig`), `connector_restpoll.go` (`rest_poll`, a watch-monitor;
implements the optional `ConnectorAutoApprover` so it goes live on create),
`connector_desktop.go` (`desktop_mcp`) and `connector_command.go`
(`desktop_command`) — the last two push a `DesktopInstall` to the owner's
desktop bridge via `InstallToDesktop`. The LLM-facing `connector` tool and the
**Admin › Connectors** surface live in `apps/orchestrate` and `apps/admin`.
