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
- **Three primitives** to compose: **agents** (persona + tool surface), **skills** (conditional prompt addendums with optional vector corpus that self-trains via post-turn closer), **collections** (RAG-attachable document buckets, deployment-wide or per-user).
- **Hidden + allowlist controls** — agents can be hidden from the fleet's `agents(action="run")` dispatch, or restricted to a specific allowlist of callers. Per-(user, agent) memory + knowledge stores keep tenants isolated.
- **Runtime-defined tools** — LLM can author shell-mode, api-mode, and pipeline-mode tools mid-conversation via Builder; persistence requires admin approval through the pending-tool queue.
- **Tool groups + classifier-trim** — admin-curated bundles collapse related tools into one expandable catalog entry; the runtime vector-classifier surfaces only the top-K most relevant tools per turn when the catalog gets large.

### LLM infrastructure
- **Multi-provider** — Anthropic, OpenAI, Google Gemini, Ollama, llama.cpp.
- **Hybrid (worker + lead) with route stages** — `RegisterRouteStage` declares the per-stage default; admins can flip any stage between worker / lead / worker (thinking) from the web UI. `Private:true` stages hard-lock to worker tier regardless of admin setting.
- **Granular no-think control** — per-signal toggles for `enable_thinking` kwarg, `thinking_budget_tokens` cap, and `/no_think` placement so operators can tune for whichever combination their model honors.
- **Local-model fair-queueing** — configurable global parallelism caps for Ollama and llama.cpp with round-robin dispatch across caller sessions.
- **Agent loop with framework guardrails** — round-budget awareness (the LLM is told its budget upfront), midpoint and wrap-up nudges, failure-streak pivot when N consecutive rounds all error, action-promise correction for "let me try" Qwen-style stalls.
- **Vision + multimodal** — image and video attachments flow through to vision-capable models with sensible per-call defaults.

### Web platform
- **Declarative UI framework** (`core/ui`) — `FormPanel`, `Table`, `ChatPanel`, `PipelinePanel`, `DisplayPanel`, `ChipPicker`, `Stack`, `RecordView`, `JSONView`, plus per-field affordances like `Presets` (static one-click fills), `ChipsSource` (dynamic chips), `SuggestURL` (per-field AI fill), `TestURL` (connectivity check button).
- **Web-based admin** — most operator config (LLM provider/model/key, embeddings, STT, image gen, web search, SMTP, cost rates, routing, worker thinking, network timeouts) lives in the admin web UI with inline test buttons. `--setup` is now mostly first-boot bootstrap (TLS, listen addr, admin user).
- **Cost telemetry** — per-day spend chart with per-tier breakdown (worker in/out, lead in/out, search calls, image calls); apps plug in record scanners via `RegisterCostRecordScanner` so the chart stays generic.
- **Auth + access** — cookie sessions, signup with admin approval, forgot/reset password, per-user app access, auto-lockout, optional shared API key for machine-to-machine.
- **Per-user data isolation** — namespaced sub-stores; admin can reassign or purge an account's data via registered `UserDataHandler`s before deletion.
- **TLS + notifications** — self-signed cert generation or explicit paths; email for signups/approvals/task completion.

### Resilience + ops
- **Pipeline framework** — `RunPipelineAsync` / `RestorePipeline` for long-running tasks: session registration, persistent queue (survives restarts), per-app concurrency cap, completion notifications without duplicates.
- **Mid-flight interjections** — queue notes via `/api/inject` while a turn is running; drained at per-step boundaries and folded into the next worker brief.
- **Encrypted config + credentials** — AES-CFB kvlite database with hardware-locked storage; per-credential URL allow-list, audit log, rate limits.
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
- Cost history (chart) + per-tier rates
- Network timeouts, Ollama proxy, vector index stats
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
| `admin` | Administrator panel — user management, app permissions, secure-API credentials, pending-tool approval, skills curation, tool groups, **all service config** (LLM, embeddings, STT, image gen, search, SMTP, network, cost rates), maintenance one-shots, scheduled tasks |
| `orchestrate` | Agency — central agent fleet runner. Chat with seed agents (Chat, Builder, Research, Code Reviewer, …) or user-authored ones; per-(user, agent) memory + knowledge; plan-driven multi-step authoring; sub-agent dispatch with per-caller allowlists; SSE streaming + interjections |
| `agents` | Public per-agent surface — exposed agents from Agency get individual `/agents/<slug>/` URLs (admins flip Exposed=true; end-users get a chat surface scoped to that one agent) |
| `knowledge` | Document Collections — shared / per-user RAG buckets agents attach to. Upload PDFs/DOCX/text; autofill from web with optional LLM judge; FilterRules-driven scope |
| `phantom` | iMessage assistant — gatekeeper, per-conversation curation, chat-scoped vector knowledge, async agent dispatch with SMS-friendly digestion, scheduled callbacks |
| `servitor` | SSH-based system investigator with plan-driven flow (set_plan / execute / revise / gap-detect / skip-and-revisit), persistent technique recording, private-only routing, xterm terminal pane |
| `techwriter` | Technical documentation co-editor |
| `codewriter` | Script/query co-author with saved snippets, reusable values, and saved context blocks |
| `hello` | Minimal scaffold app — canonical reference for authoring a new app with the declarative `core/ui` framework |
| `ollama_proxy` | Ollama-compatible HTTP proxy for clients that expect that API shape |

## Where it's going

The toolkit composes today around **agents + skills + collections**. The next layer is **pipelines**: a declarative DSL for multi-stage workflows (decomposition → parallel sub-questions → synthesis), plus a **source-hook registry** for pluggable cached upstream APIs and a **multi-agent turn primitive** for parallel-agent stages with synthesis. With those three pieces in place, an end-user could compose a "janky version" of a research / debate / investigation pipeline from chat — Builder picks the shape (agent or pipeline) based on intent, then dispatches to a hidden **Pipeline Builder** sub-agent for multi-stage authoring.

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
│   ├── factstore.go         # Flat per-namespace memory primitive (text + semantic dedup at save)
│   ├── vector_store.go      # Embedded-chunk index, source-prefixed search, ingestion helpers
│   ├── embeddings.go        # Embedding provider config + API
│   ├── document_extract.go  # PDF / DOCX text extraction for attachment uploads
│   ├── tool_groups.go       # Admin-curated tool bundles (collapse related tools to one catalog entry)
│   ├── secure_api.go        # Encrypted credential store + auto-generated call_<credential> tools
│   ├── editor/              # Shared editor primitives (clipboard, resize, import, diff widget)
│   ├── ui/                  # Declarative UI framework — FormPanel, ChatPanel, PipelinePanel, ChipPicker, etc.
│   └── webui/               # Shared web UI theme + embedded Orbitron wordmark font
├── apps/                # Built-in apps
│   ├── admin/               # Administrator panel — users, settings, credentials, pending tools, skills, tool groups, all service config
│   ├── agents/              # Public per-agent surface — one URL per exposed agent
│   ├── codewriter/          # Script/query co-author with snippets, values, saved contexts
│   ├── hello/               # Minimal scaffold app — canonical core/ui reference
│   ├── knowledge/           # Document Collections — RAG buckets agents attach to (upload, autofill, filter rules)
│   ├── ollama_proxy/        # Ollama-compatible HTTP proxy
│   ├── orchestrate/         # Agency — agent fleet runner, plan-driven authoring, memory + knowledge per (user, agent), skill activation, sub-agent dispatch
│   ├── phantom/             # iMessage assistant (bridge in apps/phantom/_bridge)
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
    └── workspace/           # workspace state primitives
```

## Dependencies

- [snugforge](https://github.com/cmcoffee/snugforge) -- logging, config, flags, kvlite database, concurrency primitives
- [go-pdf/fpdf](https://github.com/go-pdf/fpdf) -- PDF generation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) -- terminal text editor (Options textarea)
