```
      ____       _                _
     / ___| ___ | |__   ___  _ __| |_
    | |  _ / _ \| '_ \ / _ \| '__| __|
    | |_| | (_) | | | | (_) | |  | |_
     \____|\___/|_| |_|\___/|_|   \__|
```

# Gohort

A modular agent framework in Go for building LLM-powered applications with web dashboards, tool execution, and multi-provider support. Apps self-register via `init()` and the framework discovers capabilities at startup -- no wiring, no configuration files, no framework edits.

## Features

- **Self-registering apps** -- add a blank import, get a CLI command and web dashboard
- **Multi-provider LLM support** -- Anthropic, OpenAI, Google Gemini, Ollama, llama.cpp (local models)
- **Hybrid LLM architecture** -- worker model for volume, precision model for critical decisions, with transparent fallback
- **Privacy-aware route stages** -- per-app route registration with `Private:true` hard-locks an app to the worker tier, blocking accidental escalation to cloud providers regardless of admin UI setting
- **Granular no-think control** -- per-signal toggles for `enable_thinking` kwarg, `thinking_budget_tokens` cap, and `/no_think` placement (system / user prompt) so operators can tune for whichever combination their model honors
- **Vision + multimodal** -- image and video attachments flow through to vision-capable models with automatic per-call defaults (deterministic temperature, thinking off)
- **Runtime-defined tools** -- LLM can author shell-mode and api-mode tools mid-conversation; persistence requires admin approval via the pending-tool queue
- **Tool capability gating** -- per-app `AllowedCaps` filter (read / network / write / execute) prevents tools requiring capabilities the app doesn't grant
- **Plan-driven agentic investigations** -- structured plan / execute / revise / gap-detect lifecycle for long-running multi-step agents
- **Local-model fair-queueing** -- configurable global parallelism caps for Ollama and llama.cpp with round-robin dispatch across caller sessions
- **Session API** -- `CreateSession(WORKER|LEAD)` tags each unit of LLM work with a UUID for scheduler fairness
- **Web dashboard** -- unified UI with SSE streaming, live session tracking, task queue, and shareable links
- **Authentication** -- cookie-based login, signup with admin approval, forgot/reset password, per-user app access, auto-lockout
- **Per-user data isolation** -- each authenticated user gets a namespaced sub-store; admin can reassign or purge an account's data before deletion via registered `UserDataHandler`s
- **Administrator panel** -- user management, app permissions, default apps, system settings, per-app data footprint, secure-API credential vault, pending-tool approval queue
- **Encrypted credential store** -- per-credential URL allow-list, audit log, rate limits, no-auth public mode for unauthenticated public APIs
- **TLS support** -- self-signed certificate generation or explicit cert/key paths
- **Notifications** -- email alerts for signups, approvals, task completion with configurable service name and links
- **Persistent queue** -- tasks survive server restarts with notification preferences preserved
- **Pipeline framework** -- `RunPipelineAsync` / `RestorePipeline` on FuzzAgent for lifecycle management
- **Agent loop** -- autonomous tool-calling loop with native and prompt-based tool modes, dynamic per-round thinking budget, and reasoning-stream callback
- **Web search** -- DuckDuckGo, Brave, Google, Serper.dev, SearXNG with article fetching and source scoring
- **HTTP client** -- all outbound connections via snugforge `apiclient` with retries, auth, and rate limiting
- **Encrypted config** -- AES-CFB encrypted kvlite database with hardware-locked storage
- **Interactive setup** -- `--setup` wizard with conditional fields, model discovery, and app-contributed config sections

## Quick Start

### Build

```bash
go build -o /tmp/gohort .
```

### Setup

```bash
/tmp/gohort --setup
```

Configures LLM settings, external services (search, hooks, mail), web server (TLS, auth, notifications), and any app-contributed settings.

### Run

```bash
# Interactive chat with tool access
gohort

# Web dashboard
gohort serve :8080

# Web dashboard with TLS
gohort serve :8443 --tls

# Run a specific app
gohort techwriter --serve :8080
```

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
| `admin` | Administrator panel (web only) -- user management, app permissions, secure-API credentials, pending-tool approval, system settings |
| `chat` | Interactive LLM chat with tool access, SSE streaming, runtime-defined tool authoring, image/video attachments |
| `phantom` | iMessage assistant -- gatekeeper, per-conversation curation, attachment lookup, owner bypass, scheduled callbacks |
| `servitor` | SSH-based system investigator with plan-driven flow (set_plan / execute / revise / gap-detect), persistent technique recording, private-only routing |
| `techwriter` | Technical documentation co-editor |
| `codewriter` | Script/query co-author with saved snippets, reusable values, and saved context blocks |
| `dual` | Side-by-side multi-provider chat for comparison |
| `answer` | Single-shot answer endpoint for non-interactive integrations |

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
│   ├── editor/              # Shared editor primitives (clipboard, resize, import, diff widget)
│   └── webui/               # Shared web UI theme + embedded Orbitron wordmark font
├── apps/                # Built-in apps
│   ├── admin/               # Administrator panel (users, settings, credentials, pending tools)
│   ├── answer/              # Single-shot answer endpoint
│   ├── chat/                # LLM chat with tool authoring + multimodal attachments
│   ├── codewriter/          # Script/query co-author with snippets, values, saved contexts
│   ├── dual/                # Side-by-side multi-provider comparison
│   ├── ollama_proxy/        # Ollama-compatible HTTP proxy
│   ├── phantom/             # iMessage assistant (bridge in apps/phantom/_bridge)
│   ├── servitor/            # SSH-based system investigator with plan-driven flow
│   └── techwriter/          # Technical documentation editor
├── tools/               # Chat tools (built-in, registered via init)
│   ├── attach/              # attach_file: send workspace files to user
│   ├── browser/             # browse_page, screenshot_page (headless browser)
│   ├── calculate/           # calculate: arithmetic
│   ├── comedian/            # get_joke
│   ├── datemath/            # date_math: diff/add operations
│   ├── email/               # send_email
│   ├── files/               # local: sandboxed read/write/run + file metadata
│   ├── imagefetch/          # fetch_image, find_image, generate_image
│   ├── keepgoing/           # keep_going: agent-loop continuation
│   ├── localexec/           # run_local: sandboxed shell exec
│   ├── localtime/           # get_local_time
│   ├── orchestrator/        # delegate: hand off multi-step work
│   ├── silent/              # stay_silent: suppress reply this turn
│   ├── status/              # send_status: progress notes mid-turn
│   ├── temptool/            # tool_def: runtime-defined shell + api tools
│   ├── videodl/             # download_video, view_video (yt-dlp wrapper)
│   ├── watcher/             # watcher: poll-and-alert framework
│   ├── websearch/           # web_search, fetch_url + article extraction
│   └── workspace/           # workspace state primitives
└── extras/              # Templates and examples
```

## Dependencies

- [snugforge](https://github.com/cmcoffee/snugforge) -- logging, config, flags, kvlite database, concurrency primitives
- [go-pdf/fpdf](https://github.com/go-pdf/fpdf) -- PDF generation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) -- terminal text editor (Options textarea)
