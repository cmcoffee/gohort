```
      ____       _                _
     / ___| ___ | |__   ___  _ __| |_
    | |  _ / _ \| '_ \ / _ \| '__| __|
    | |_| | (_) | | | | (_) | |  | |_
     \____|\___/|_| |_|\___/|_|   \__|
```

# Gohort

**A local-first AI toolkit you build *in* the browser, not just *for* it.**

Gohort is a Go-based framework + apps for building LLM-powered agents and multi-stage pipelines. The chat IS the authoring surface — describe what you want and a Builder agent assembles it from primitives (new agent? plug a pipeline? attach a skill?). No code, no visual flow editor, no separate IDE.

The framework runs **local-first**: the worker tier is your own GPU (Ollama / llama.cpp) and handles the bulk of the work; an optional precision tier escalates to a frontier model only for stages that earn it. Privacy is structural — apps inherit constraints like `ForcePrivate` and `Private:true` route stages, so sensitive data (SSH credentials, internal docs, system facts) never accidentally leaves the box.

Apps register themselves via `init()` and compose framework primitives (`FormPanel`, `Table`, `ChatPanel`, `PipelinePanel`, `ChipPicker`, …) so adding a new app rarely requires writing HTML / CSS / DOM JavaScript. Each shipped app either uses existing primitives or proves a new one needs to exist — the toolkit compounds rather than bloating with app-specific exceptions.

## Features

### Authoring surface
- **Chat-is-authoring** — Builder is the front door. Tell it "make me an agent for X" or "set up a workflow that does Y" and it creates/updates/clones/deletes the right thing. Runtime-enforced exclusivity keeps authoring out of other agents.
- **Four primitives** to compose: **agents** (persona + tool surface), **skills** (conditional prompt addendums with optional vector corpus that self-trains via post-turn closer), **collections** (RAG-attachable document buckets, deployment-wide or per-user), and **pipelines** (declarative multi-stage workflows — author once, attach to any agent as a callable tool).
- **Pipelines** — a saved, ordered stage list (each stage a worker LLM step or a dispatch to one of your agents; outputs thread forward via `{input}`/`{prev}`/`{stage:NAME}` templating). Author from chat via Builder, attach to an agent with `attached_pipelines` (it surfaces as a callable `run_<pipeline>` tool), run it, export/import the recipe as portable JSON, and view/delete from the admin panel. The recipe is plain data with no identity baked in, so it travels.
- **Hidden + allowlist controls** — agents can be hidden from the fleet's `agents(action="run")` dispatch, or restricted to a specific allowlist of callers. Per-(user, agent) memory + knowledge stores keep tenants isolated.
- **Runtime-defined tools** — Builder authors shell-mode and api-mode tools mid-conversation; persistence requires admin approval through the pending-tool queue. Multi-stage work is authored as a declarative **pipeline** (above), not a tool.
- **Sandboxed shell tools with a narrow gohort hook** — script bodies run in a network-isolated `bwrap` sandbox. The Python shim exposes `gohort.fetch_url`, `gohort.browse_page`, `gohort.log` by default and `gohort.fetch_via("<credential>", url)` / `gohort.secret("<credential>")` when declared. Same names + same behavior as the LLM-callable tools, so a URL the LLM probed with `fetch_url` works identically in a script. Network libraries that can't reach the network (urllib, requests, curl, wget, socket-dialing) are refused at authoring time so the rewrite-to-curl spiral can't happen.
- **Sandbox-isolated shell tools with a narrow callback** — shell-mode scripts run in a network-isolated `bwrap` sandbox with one capability-gated callback channel back into gohort. The Python shim exposes `gohort.fetch_url` / `gohort.browse_page` / `gohort.log` (default-on for any tool with a `script_body`) and `gohort.fetch_via("<credential>", url)` / `gohort.secret("<credential>")` (explicit declaration required, gated by per-credential allow-list). urllib / requests / curl / wget / socket-dialing are refused at authoring time — the gohort hook is the only network path, and every script-side call uses the same HTTP client, headers, auto-routing, and audit log as the LLM-callable equivalents.
- **Tool groups + classifier-trim** — admin-curated bundles collapse related tools into one expandable catalog entry; the runtime vector-classifier surfaces only the top-K most relevant tools per turn when the catalog gets large.

### LLM infrastructure
- **Multi-provider** — Anthropic, OpenAI, Google Gemini, Ollama, llama.cpp.
- **Hybrid (worker + lead) with route stages** — `RegisterRouteStage` declares the per-stage default; admins can flip any stage between worker / lead / worker (thinking) from the web UI. `Private:true` stages hard-lock to worker tier regardless of admin setting.
- **Granular no-think control** — per-signal toggles for `enable_thinking` kwarg, `thinking_budget_tokens` cap, and `/no_think` placement so operators can tune for whichever combination their model honors.
- **Local-model fair-queueing** — configurable global parallelism caps for Ollama and llama.cpp with round-robin dispatch across caller sessions.
- **Agent loop with framework guardrails** — round-budget awareness (the LLM is told its budget upfront), midpoint and wrap-up nudges, failure-streak pivot when N consecutive rounds all error, action-promise correction for "let me try" Qwen-style stalls, tool-round discipline (no full answer until the final, tool-free step — prevents double replies), and a **repeated-failure loop-guard** that stops any tool re-called with identical args once it errors 3× (so a small model can't burn its whole round budget hammering one dead call).
- **Source hooks** — admin-managed external sources (PubMed, OpenAlex, EDGAR, or any custom API/RAG endpoint, from templates or hand-rolled). Each can be exposed as a per-hook agent tool (`pubmed_search`, …) that surfaces live in every agent's catalog, and/or auto-queried by topic in research/debate pipelines. Auth stored encrypted; paywall hooks transparently add headers to `fetch_url`.
- **API credentials (credential-first, secret never reaches the LLM)** — register an authenticated external API once; the secret is stored encrypted and injected server-side at call time. Types: bearer, custom header, query param, HTTP basic, and OAuth2 (`client_credentials` / `jwt_bearer` / `refresh_token` / `password` grants, with access tokens minted and refreshed automatically). The allow-list is a **Base URL + an add/remove list of Allowed Endpoints** (or a legacy single glob); any request outside it is refused before the secret is attached. A per-credential **skip-TLS-verify** toggle handles self-signed / IP-addressed LAN appliances (firewalls, NAS, switches), where no certificate can validate. Authoring agents (Builder, Chat) scaffold the credential config themselves via `draft_api_credential` / `draft_oauth_credential` — it lands disabled, the admin pastes the secret in the admin UI and enables it, and the LLM drives the wiring without ever seeing the key. A universal framework rule forbids any agent from soliciting a key/secret in chat.
- **Remote MCP servers (server-side client)** — admin-registered Model Context Protocol servers reachable over Streamable HTTP. Each server's tools surface as native `<server>.<tool>` agent tools and, optionally, as a reference source in the writer/research source picker. Auth modes: static bearer, SecureAPI OAuth2 (client_credentials / jwt_bearer), and per-user OAuth 2.1 — authorization_code + PKCE + dynamic client registration — for hosted SaaS like Atlassian Cloud. Tokens are stored encrypted; OAuth connections are per-user so results respect each user's own permissions.
- **Vision + multimodal** — image and video attachments flow through to vision-capable models with sensible per-call defaults.

### Web platform
- **Declarative UI framework** (`core/ui`) — `FormPanel`, `Table`, `ChatPanel`, `PipelinePanel`, `DisplayPanel`, `ChipPicker`, `Stack`, `RecordView`, `JSONView`, plus per-field affordances like `Presets` (static one-click fills), `ChipsSource` (dynamic chips), `SuggestURL` (per-field AI fill), `TestURL` (connectivity check button), and `Templates` (a "start from template" picker that prefills a create-form from named presets).
- **Channel agents (Master Control)** — a fleet-capable agent gets a persistent **Master Control** home thread pinned above its ordinary sessions, where its event-monitor wakes and standing-agent reports land as distinct titled cards (producer + fire time) rather than chat bubbles. A pinned **Permissions** page is the agent's permission center: a Claude-Desktop-style three-state policy control per delegation target / contact (Always allow · Needs approval · **Blocked**, the last enforced server-side as auto-deny) plus the live approval queue, all on one page. Fleet-management views collapse into a topbar **Manage** menu; the rail stays a clean list of threads.
- **Reserved internal marker** — anything an agent wraps in `<gohort-meta>…</gohort-meta>` is scrubbed from user-facing output (both the saved/exported copy and the client render), so framework-internal directives and stray delivery markers can never leak into a reply.
- **Web-based admin** — most operator config (LLM provider/model/key, embeddings, STT, image gen, web search, SMTP, cost rates, routing, worker thinking) lives in the admin web UI with inline test buttons. `--setup` is now mostly first-boot bootstrap (TLS, listen addr, admin user).
- **Data-driven tunables** — framework knobs that used to be hardcoded constants (timeouts, caps, budgets, thresholds across the core) self-register via `RegisterTunable` and read through `TuneInt` / `TuneFloat` / `TuneDuration`; the admin **Tuning** tab generates an editable row per knob, grouped by area, each with revert-to-default. Add a tunable in one line and it appears in the UI automatically.
- **Cost telemetry** — a dedicated **Costs** tab: per-day spend chart with per-tier breakdown (worker in/out, lead in/out, search calls, image calls), inline per-tier rates, and a **cost-by-source** table. Apps plug in record scanners via `RegisterCostRecordScanner` so the chart stays generic; source hooks and API credentials carry an optional **per-call cost** that accrues per-source via `RecordExternalCost`, so external API spend shows up next to model spend.
- **Auth + access** — cookie sessions, signup with admin approval, forgot/reset password, per-user app access, auto-lockout, optional shared API key for machine-to-machine.
- **Per-user data isolation** — namespaced sub-stores; admin can reassign or purge an account's data via registered `UserDataHandler`s before deletion.
- **TLS + notifications** — self-signed cert generation or explicit paths; email for signups/approvals/task completion.

### Resilience + ops
- **Tool-result spill + targeted query actions** — any tool result over ~100 KB spills to `<workspace>/.tool_spill/` with a stub showing first/last samples + path; the LLM follows up with `workspace(action="head"/"tail"/"grep"/"read_lines"/"stat")` for the slice it actually needs. Same shape for large PDF/DOCX attachments (`.attachments/`). Prevents one 2 MB log read or verbose sub-agent transcript from blowing the context window in a single round.
- **Detached agent runs** — turns survive client disconnect. The HTTP request's context drives only the SSE delivery leg; the agent loop runs against an independent context and tees every frame into a per-run ring buffer. A reconnecting client picks up where it left off via `/api/runs/<id>/stream`. Active runs show a pulsing indicator on their session in the rail, and sessions with live background work (event-monitor watchers or in-flight dispatched agents) lift into an "Active" group at the top of the rail with a count badge, so ongoing work stays findable.
- **Pipeline framework** — `RunPipelineAsync` / `RestorePipeline` for long-running tasks: session registration, persistent queue (survives restarts), per-app concurrency cap, completion notifications without duplicates.
- **Mid-flight interjections** — queue notes via `/api/inject` while a turn is running; drained at per-step boundaries and folded into the next worker brief.
- **Encrypted config + credentials** — AES-CFB kvlite database with hardware-locked storage; each credential carries a Base URL + add/remove allowed-endpoints allow-list, audit log, rate limits, and an optional per-credential TLS-skip for self-signed / IP-addressed LAN appliances.
- **Maintenance functions** — apps register one-shot repair operations (re-embed, migrate, dedupe); admin UI surfaces them as Run-button rows.

## Quick Start

### Build

```bash
go build -o /tmp/gohort .
```

### First-boot setup

```bash
/tmp/gohort --setup
```

The wizard covers what the web admin can't bootstrap itself: TLS, listen address, first admin account, and a minimal LLM provider so chat works on first launch.

### Run

```bash
# Web dashboard (the primary surface)
gohort serve :8080

# Web dashboard with self-signed TLS
gohort serve :8443 --tls

# Interactive chat with tool access (CLI)
gohort

# Run a specific app from CLI
gohort techwriter --serve :8080
```

### Configure (web admin)

Sign in to the web dashboard and visit **/admin**. Almost every operational setting lives here:

- LLM routing (per-stage worker / lead / worker-thinking)
- Worker LLM thinking defaults
- Embeddings, STT, image generation, web search, SMTP — each with a **Test connectivity** button
- Costs tab — spend chart, per-tier rates, cost-by-source breakdown
- Tuning tab — framework knobs (timeouts, caps, budgets) with revert-to-default
- Ollama proxy, vector index stats
- Maintenance one-shots, scheduled tasks
- User management, default apps, signup policy, lockout

`--setup` is now the thin wizard for things the web can't change about itself.

## Architecture

### App Registration

Apps register themselves in `init()`. The framework discovers CLI and web capabilities from the type:

```go
package myapp

import . "github.com/cmcoffee/gohort/core"

func init() { RegisterApp(new(MyApp)) }

type MyApp struct {
    AppCore
}

func (T MyApp) Name() string         { return "myapp" }
func (T MyApp) Desc() string         { return "Does a thing." }
func (T MyApp) SystemPrompt() string { return "" }
func (T *MyApp) Init() error         { return T.Flags.Parse() }
func (T *MyApp) Main() error         { return nil }
```

Add `WebPath()`, `WebName()`, `WebDesc()`, and `Routes()` to get a web dashboard automatically. No separate registration needed.

### Optional Interfaces

Apps gain capabilities by implementing optional interfaces:

| Interface | Methods | Purpose |
|-----------|---------|---------|
| `SimpleWebApp` | `WebPath`, `WebName`, `WebDesc`, `Routes` | Web dashboard with the framework-managed sub-mux (recommended). Register handlers via `T.HandleFunc`. |
| `WebApp` | `WebPath`, `WebName`, `WebDesc`, `RegisterRoutes(mux, prefix)` | Legacy shape — escape hatch for apps that need direct mux control. |
| `WebAppOrder` | `WebOrder() int` | Dashboard sort position |
| `WebAppRestricted` | `WebRestricted(r) bool` | Hide app from unauthorized requests |
| `WebAppAccess` | `WebAccessKey`, `WebAccessCheck` | Expose access flags to other apps |
| `CLIApp` | `CLI()` | Opt-in marker — exposes the app on the CLI menu. Default is web-only. |

### Pipeline Framework

For apps that run long tasks, `FuzzAgent` provides pipeline lifecycle management:

```go
id := T.RunPipelineAsync(PipelineConfig{
    App:        "myapp",
    Label:      question,
    NotifyUser: notify_user,
    LinkPath:   "/myapp/?id=",
    OnRegister: func(id string, cancel context.CancelFunc) { sessions.Register(id, question, cancel) },
    OnEvent:    func(id, status string, done bool) { sessions.UpdateStatus(id, status) },
    OnCleanup:  func(id string) { sessions.ScheduleCleanup(id) },
}, func(ctx context.Context, pc *PipelineCtx) error {
    // business logic
    pc.SetRecordID(result_id)
    return nil
})
```

This handles: session registration, persistent queue (survives restarts), queue slot acquisition, notification on completion (user + admin, no duplicates), and cleanup. Use `RestorePipeline` in your `RegisterQueueHandler` to re-run tasks after server restart.

### Self-Registering Config

Apps contribute their own `--setup` sections:

```go
func init() {
    RegisterSetupSection(SetupSection{
        Name:  "My App Settings",
        Order: 100,
        Build: func(db Database) *Options {
            // build interactive menu
        },
        Save: func(db Database) {
            // persist values
        },
    })
}
```

### Hybrid LLM

Two tiers with transparent fallback:

| Role | Used For |
|------|----------|
| **Worker** | Bulk token work, summaries, answers, quality gates |
| **Precision** | High-stakes decisions, final analysis |

Route stages self-register via `RegisterRouteStage`. Each stage can be independently assigned to worker or precision tier via `--setup` without code changes.

For Ollama models without native tool support, set Native Tool Calling to "no" in `--setup`. The agent loop automatically falls back to prompt-based tool calling.

## Built-in Apps

| App | Purpose |
|-----|---------|
| `admin` | Administrator panel — user management, app permissions, secure-API credentials, remote MCP servers (incl. per-user OAuth connect), pending-tool approval, skills curation, tool groups, pipeline view/delete (across all users), **all service config** (LLM, embeddings, STT, image gen, search, SMTP, network, cost rates), maintenance one-shots, scheduled tasks |
| `orchestrate` | Agency — central agent fleet runner. Chat with seed agents (Chat, Builder, Research, Code Reviewer, …) or user-authored ones; per-(user, agent) memory across four layers (always-in-prompt facts, vector-grown reference memory, semantic knowledge, and a **graph layer** of entities + relationships via `link_entities` / `recall_about`); plan-driven multi-step authoring; sub-agent dispatch with per-caller allowlists; attachable pipelines surfaced as callable tools; SSE streaming + interjections |
| `agents` | Dashboard per-agent surface — agents an admin publishes from Agency ("Publish App to Dashboard") get individual `/agents/<slug>/` URLs. Streamlined chat-first; permission-gated (a granted user gets a chat surface scoped to that one agent, with their own per-(user, agent) sessions + data). Not management — config lives in admin-only Agency |
| `knowledge` | Document Collections — shared / per-user RAG buckets agents attach to. Upload PDFs/DOCX/text; autofill from web with optional LLM judge; FilterRules-driven scope |
| `bridges` | Messaging transport — connect a messaging service (iMessage, Telegram, …) to a channel agent: inbound routes to the bound agent, its replies route back out. Gatekeeper, per-conversation curation, auto-reply policy, outbound de-markdown at the single send chokepoint, and per-service key management. Pure transport — the agent intelligence is an `orchestrate` channel agent; the macOS iMessage relay runs in `gohort-desktop`. (Replaces the retired `phantom` app, whose own agent engine was folded into `orchestrate`.) |
| `servitor` | SSH-based system investigator with plan-driven flow (set_plan / execute / revise / gap-detect / skip-and-revisit), persistent technique recording, mapping runs saved as sessions, exportable knowledge brief (`.md`, secrets redacted), private-only routing, xterm terminal pane |
| `techwriter` | Technical documentation co-editor |
| `codewriter` | Script/query co-author with saved snippets, reusable values, and saved context blocks |
| `hello` | Minimal scaffold app — canonical reference for authoring a new app with the declarative `core/ui` framework |
| `ollama_proxy` | Ollama-compatible HTTP proxy for clients that expect that API shape |

## Companion clients

| Client | Purpose |
|--------|---------|
| `gohort-desktop` | Native desktop host (Wails), shipped as **two apps**: **Gohort.app** — the viewer window (reverse-proxies the gohort web UI; no special permissions) — and **Gohort-Bridge.app** — an always-on menu-bar daemon that owns the host's OS permissions and exposes local capabilities (filesystem read/write, screenshot, contacts) plus, on macOS, the iMessage relay that feeds the **Bridges** transport app, all dispatched from the gohort server over a per-user WebSocket. One unified API key authenticates both the tool bridge and `/bridges/api/*`. Per-invocation approval (auto-approve toggle), read/write folder consent, MCP host. See `gohort-desktop/README.md`. |

## Where it's going

The toolkit composes today around **agents + skills + collections + pipelines**. Pipelines have landed as a first-class primitive: declarative multi-stage workflows (decompose → stages → synthesize) authored from chat, attached to any agent as a callable `run_<pipeline>` tool, run inline, exported/imported as portable JSON, and governed from the admin panel — Builder picks the shape (agent or pipeline) based on intent.

Still ahead: parallel **fanout** stages (run one stage across N inputs and collect, e.g. one per sub-question), a **source-hook registry** for pluggable cached upstream APIs, and a **multi-agent turn primitive** for parallel-agent stages with synthesis. With those, an end-user could compose a full multi-stage research-style workflow end to end from chat.

The framework's mission: each new app either uses existing primitives or proves a new one needs to exist. The toolkit compounds — the next app is faster to build than the last.

## Adding Private Apps

Create a `private/` directory (gitignored) and a `private.go` file:

```go
package main

import (
    _ "github.com/cmcoffee/gohort/private/myapp"
)
```

Private apps use the same registration pattern. The framework discovers them at startup alongside the built-in apps.

## CLI Flags

Top-level flags (work before or after a subcommand — kitebroker-style):

| Flag | Description |
|------|-------------|
| `--setup` | Run configuration wizard |
| `--config <path>` | Override the INI lookup (default: `<binary-dir>/gohort.ini`) |
| `--debug` / `--trace` / `--snoop` / `--serial` | Diagnostic modifiers |
| `--version` | Show version |

The `serve` subcommand starts the web dashboard and has its own flags:

| Flag | Description |
|------|-------------|
| `gohort serve [addr]` | Start the dashboard on the given address (default `:8080`) |
| `--max_concurrent <n>` | Max simultaneous tasks (default: 1) |
| `--tls` | Enable TLS with auto-generated self-signed certificate |
| `--tls_cert <path>` | Path to TLS certificate file (PEM) |
| `--tls_key <path>` | Path to TLS private key file (PEM) |

## Project Structure

```
gohort/
├── gohort.go            # Entry point, CLI flags, global state
├── agents.go            # App registration (blank imports)
├── tools.go             # Tool registration (blank imports)
├── config.go            # Configuration wizard
├── chat.go              # Interactive chat REPL
├── menu.go              # CLI menu system
├── core/                # Framework core
│   ├── common.go            # FuzzAgent, Agent interface, LLM calls
│   ├── agent_loop.go        # Agent loop with native + prompt-based tools
│   ├── registry.go          # Registration: apps, tools, setup sections, route stages
│   ├── webapp.go            # Web framework, dashboard, live sessions, task queue
│   ├── auth.go              # Authentication, sessions, signup, lockout, password reset
│   ├── notify.go            # Email notifications (user, admin, service name)
│   ├── tls.go               # TLS support with self-signed cert generation
│   ├── pipeline.go          # Pipeline lifecycle (RunPipelineAsync, RestorePipeline)
│   ├── pqueue.go            # Persistent queue for server-restart survival
│   ├── llm.go               # LLM abstraction and provider config
│   ├── llm_anthropic.go     # Anthropic provider
│   ├── llm_openai.go        # OpenAI + Ollama providers
│   ├── llm_gemini.go        # Gemini provider
│   ├── access.go            # IP-based access control
│   ├── search.go            # Search cache and source classification
│   ├── pdf.go               # Markdown to PDF rendering
│   ├── sse.go               # Server-Sent Events helpers
│   ├── ollama_scheduler.go  # Fair-queueing scheduler for local Ollama calls
│   ├── userdata.go          # Per-user data namespacing + admin reassign/purge registry
│   ├── skills.go            # Skill records + classifier (triggers + embedding similarity) + activation injection
│   ├── skill_knowledge_tool.go # skill_knowledge tool — read/write the skill's vector corpus slice
│   ├── factstore.go         # Flat per-namespace memory primitive (text + semantic dedup at save, plus supersession: a changed fact replaces the stale one)
│   ├── graphstore.go        # Graph memory primitive — entities + typed relationships per namespace (link_entities / recall_about), with a query-time graph→vector bridge
│   ├── tunables.go          # Data-driven tunables registry (RegisterTunable + TuneInt/TuneFloat/TuneDuration) backing the admin Tuning tab
│   ├── cost_ledger.go       # Per-source external-cost ledger (RecordExternalCost) for source-hook / API-credential per-call costs
│   ├── vector_store.go      # Embedded-chunk index, source-prefixed search, ingestion helpers
│   ├── embeddings.go        # Embedding provider config + API
│   ├── document_extract.go  # PDF / DOCX text extraction for attachment uploads
│   ├── tool_groups.go       # Admin-curated tool bundles (collapse related tools to one catalog entry)
│   ├── secure_api.go        # Encrypted credential store (bearer/header/query/basic/oauth2), Base URL + endpoints allow-list, agent-draftable drafts, per-credential TLS-skip, auto-generated fetch_url_<credential> tools
│   ├── editor/              # Shared editor primitives (clipboard, resize, import, diff widget)
│   ├── ui/                  # Declarative UI framework — FormPanel, ChatPanel, PipelinePanel, ChipPicker, etc.
│   └── webui/               # Shared web UI theme + embedded Orbitron wordmark font
├── apps/                # Built-in apps
│   ├── admin/               # Administrator panel — users, settings, credentials, pending tools, skills, tool groups, all service config
│   ├── agents/              # Dashboard per-agent surface — one URL per published agent
│   ├── bridges/             # Messaging transport — services (iMessage/Telegram/…) → channel agents (Mac relay lives in gohort-desktop)
│   ├── codewriter/          # Script/query co-author with snippets, values, saved contexts
│   ├── hello/               # Minimal scaffold app — canonical core/ui reference
│   ├── knowledge/           # Document Collections — RAG buckets agents attach to (upload, autofill, filter rules)
│   ├── ollama_proxy/        # Ollama-compatible HTTP proxy
│   ├── orchestrate/         # Agency — agent fleet runner, plan-driven authoring, memory + knowledge per (user, agent), skill activation, sub-agent dispatch
│   ├── servitor/            # SSH-based system investigator with plan-driven flow + xterm terminal pane
│   └── techwriter/          # Technical documentation editor
└── tools/               # Chat tools (built-in, registered via init)
    ├── attach/              # attach_file: send workspace files to user
    ├── browser/             # browse_page, screenshot_page (headless browser)
    ├── calculate/           # calculate: arithmetic
    ├── comedian/            # get_joke
    ├── datemath/            # date_math: diff/add operations
    ├── email/               # send_email
    ├── files/               # local: sandboxed read/write/run + file metadata
    ├── findtools/           # find_tools: classifier-based catalog lookup
    ├── imagefetch/          # fetch_image, find_image, generate_image
    ├── keepgoing/           # keep_going: agent-loop continuation
    ├── localexec/           # run_local: sandboxed shell exec
    ├── localtime/           # get_local_time
    ├── orchestrator/        # delegate: hand off multi-step work
    ├── silent/              # stay_silent: suppress reply this turn
    ├── status/              # send_status: progress notes mid-turn
    ├── temptool/            # tool_def: runtime-defined shell + api tools
    ├── transcribe/          # transcribe_audio: STT via configured Whisper endpoint
    ├── video/, videodl/, videofind/   # video attach + yt-dlp wrappers
    ├── watcher/             # watcher: poll-and-alert framework
    ├── websearch/           # web_search, fetch_url + article extraction
    └── workspace/           # workspace state primitives — create/use/ls/cat/write/run + head/tail/grep/read_lines/stat query actions for spilled / large files
gohort-desktop/         # Native macOS host (Wails) — separate module, see its README
```

## Dependencies

- [snugforge](https://github.com/cmcoffee/snugforge) -- logging, config, flags, kvlite database, concurrency primitives
- [go-pdf/fpdf](https://github.com/go-pdf/fpdf) -- PDF generation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) -- terminal text editor (Options textarea)
