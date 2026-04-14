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
- **Multi-provider LLM support** -- Anthropic, OpenAI, Google Gemini, Ollama (local models)
- **Hybrid LLM architecture** -- worker model for volume, precision model for critical decisions, with transparent fallback
- **Web dashboard** -- unified UI with SSE streaming, live session tracking, task queue, and shareable links
- **Authentication** -- cookie-based login, signup with admin approval, forgot/reset password, per-user app access, auto-lockout
- **Administrator panel** -- user management, app permissions, default apps, system settings
- **TLS support** -- self-signed certificate generation or explicit cert/key paths
- **Notifications** -- email alerts for signups, approvals, task completion with configurable service name and links
- **Persistent queue** -- tasks survive server restarts with notification preferences preserved
- **Pipeline framework** -- `RunPipelineAsync` / `RestorePipeline` on FuzzAgent for lifecycle management
- **Agent loop** -- autonomous tool-calling loop with native and prompt-based tool modes
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
gohort --web :8080

# Web dashboard with TLS
gohort --web :8443 --tls

# Run a specific app
gohort techwriter --web :8080
```

## Architecture

### App Registration

Apps register themselves in `init()`. The framework discovers CLI and web capabilities from the type:

```go
package myapp

import . "github.com/cmcoffee/gohort/core"

func init() { RegisterApp(new(MyApp)) }

type MyApp struct {
    FuzzAgent
}

func (T MyApp) Name() string         { return "myapp" }
func (T MyApp) Desc() string         { return "Does a thing." }
func (T MyApp) SystemPrompt() string { return "" }
func (T *MyApp) Init() error         { return T.Flags.Parse() }
func (T *MyApp) Main() error         { return nil }
```

Add `WebPath()`, `WebName()`, `WebDesc()`, and `RegisterRoutes()` to get a web dashboard automatically. No separate registration needed.

### Optional Interfaces

Apps gain capabilities by implementing optional interfaces:

| Interface | Methods | Purpose |
|-----------|---------|---------|
| `WebApp` | `WebPath`, `WebName`, `WebDesc`, `RegisterRoutes` | Web dashboard and HTTP routes |
| `WebAppOrder` | `WebOrder() int` | Dashboard sort position |
| `WebAppRestricted` | `WebRestricted(r) bool` | Hide app from unauthorized requests |
| `WebAppAccess` | `WebAccessKey`, `WebAccessCheck` | Expose access flags to other apps |

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
| `admin` | Administrator panel (web only) -- user management, app permissions, system settings |
| `chat` | Interactive LLM chat with tool access and SSE streaming |
| `techwriter` | Technical documentation co-editor |

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

| Flag | Description |
|------|-------------|
| `--setup` | Run configuration wizard |
| `--web <addr>` | Start web dashboard (e.g., `:8080`) |
| `--max_concurrent <n>` | Max simultaneous tasks (default: 1) |
| `--tls` | Enable TLS with auto-generated self-signed certificate |
| `--tls_cert <path>` | Path to TLS certificate file (PEM) |
| `--tls_key <path>` | Path to TLS private key file (PEM) |
| `--repeat <duration>` | Repeat agent on an interval |
| `--quiet` | Minimal output for non-interactive use |
| `--version` | Show version |

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
│   └── webui/               # Shared web UI theme
├── apps/                # Built-in apps
│   ├── admin/               # Administrator panel (users, settings, status)
│   ├── chat/                # Tool-tester chat UI
│   └── techwriter/          # Technical documentation editor
├── tools/               # Chat tools
│   ├── calculate/           # Arithmetic
│   ├── command/             # Shell execution
│   ├── email/               # SMTP
│   ├── localtime/           # Current time
│   └── websearch/           # Search providers + article fetching
└── extras/              # Templates and examples
```

## Dependencies

- [snugforge](https://github.com/cmcoffee/snugforge) -- logging, config, flags, kvlite database, concurrency primitives
- [go-pdf/fpdf](https://github.com/go-pdf/fpdf) -- PDF generation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) -- terminal text editor (Options textarea)
