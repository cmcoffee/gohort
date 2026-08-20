```
      ____       _                _
     / ___| ___ | |__   ___  _ __| |_
    | |  _ / _ \| '_ \ / _ \| '__| __|
    | |_| | (_) | | | | (_) | |  | |_
     \____|\___/|_| |_|\___/|_|   \__|
```

# Gohort

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Single binary](https://img.shields.io/badge/deploy-single%20binary-success)](#quick-start)
[![Local-first](https://img.shields.io/badge/LLM-local--first-6366f1)](#what-makes-it-different)

**An agentic platform, harness, and SDK — build and run a *fleet* of AI agents on your own hardware, in Go.**

Most local agent frameworks give you one clever assistant: it lives on your machine, holds your keys, wires into your messaging apps, and does the work itself. Gohort is a tier up. It's the place you **build, run, and govern many agents** — authored from chat, extended in code, and kept honest by a runtime that makes even small local models behave. Local-first by default, multi-user by design, secret-safe by construction.

```bash
go build -o gohort . && ./gohort --setup && ./gohort serve :8080
```

That's the whole install. One static binary, no runtime, no dependency tree.

## Three ways to think about it

**🏗️ A platform.** A web dashboard that runs a fleet, not a single bot. Multi-agent dispatch with per-caller allowlists, declarative multi-stage **pipelines** (parallel fan-out, bounded loops, branching, and direct tool calls — attached to an agent as a callable tool, or mounted as a page of their own), **machines** that hold a conversation in one shape across turns, messaging **channels**, scheduled + event-triggered agents, real multi-user auth with per-user data isolation, cross-user sharing, and cost telemetry — all first-class, not bolted on.

**⚙️ A harness.** An agent loop engineered so local and small models stay reliable: the model is told its round budget up front, a loop-guard kills any tool re-called with identical args after it errors 3×, tool-round discipline prevents double replies, failure-streak detection pivots the approach, and runs survive client disconnect. The reliability doesn't come from reaching for a frontier model — it comes from the loop.

**🧩 An SDK.** A Go framework you build *on*. Registering a new app is ~20 lines and one blank import; its CLI command and web dashboard are discovered from the type. Compose `FormPanel`, `Table`, `ChatPanel`, `PipelinePanel`, `ChipPicker` and friends — new apps rarely touch HTML, CSS, or DOM JavaScript. Every app either uses an existing primitive or proves a new one should exist, so the toolkit compounds instead of bloating.

## What it looks like in practice

You tell the Builder agent:

> *"Watch our status page and post to the team channel when it changes."*

It drafts an API credential for the status endpoint, authors a poll connector that checks it on a schedule, wires the result to a channel agent that speaks to your team's messaging service, and hands your admin a single approval to paste the secret into. You never wrote code, never opened a flow editor, and never pasted a key into a chat window — and the model that assembled the whole thing still can't read that key.

That's the loop: **describe it, approve it, it runs.**

## What makes it different

- **One binary. No Python, no venv, no dependency tree.** `go build` produces a single static executable — the web dashboard, the agent runtime, the database layer, and every built-in app are compiled in. Deploying is copying one file; upgrading is replacing it. There's no runtime to install on the host, no lockfile to resolve, and no drift between your machine and the box. For self-hosters, this is the difference between a five-second deploy and an afternoon.

- **Build it in the browser, not just for it.** The chat *is* the authoring surface. Tell the Builder agent "make me an agent for X" or "set up a workflow that does Y" and it assembles the right thing from primitives — a new agent, an attached pipeline, a skill, a runtime-defined tool. No code, no visual flow editor, no separate IDE. Persistence of anything consequential goes through an admin approval queue.

- **Your keys never reach the model.** Credentials are registered once, stored encrypted, and injected **server-side** at call time — the LLM drives the wiring but never sees the secret, and a universal rule forbids any agent from asking for one in chat. Every external call is checked against a Base-URL + endpoint allow-list before the secret is attached. Shell tools run in a network-isolated `bwrap` sandbox whose *only* path to the network is a narrow, audited gohort hook (urllib/requests/curl/wget are refused at authoring time). An agent that holds your keys in its memory is a liability; here the model orchestrates access it can't exfiltrate.

- **Memory that's governed, not just persistent.** "Memory that grows with you" is table stakes; the real question is whether you control it. Each agent gets several distinct layers — always-in-prompt facts (with semantic dedup and supersession, so a changed fact *replaces* the stale one instead of piling up), vector-grown reference memory, a graph layer of entities and relationships, a rewritable working-notes scratchpad, and drillable conversation history that archives on compaction rather than collapsing into a lossy summary. Every layer is toggled **per agent**, isolated **per (user, agent)**, and bounded by admin-tunable caps and a background prune sweep. And because credentials are injected server-side, **secrets never land in a memory layer** — the failure mode where a persistent store quietly accumulates your API keys simply can't occur.

- **Local-first, not local-only.** By default the worker tier is your own GPU (Ollama / llama.cpp) and does the bulk of the work; an optional precision tier escalates to a frontier model only for the stages that earn it. Any stage can instead point at a hosted provider — run fully local, fully hosted, or any mix. Privacy is structural: `ForcePrivate` agents and `Private:true` route stages hard-lock to the local tier, so sensitive data (credentials, internal docs, system facts) never leaves the box even by accident.

- **Compose, don't hardcode.** Five primitives — **agents** (persona + tools), **skills** (conditional prompt addendums with a self-training vector corpus), **collections** (RAG buckets), **pipelines** (declarative workflows that run start to finish), and **machines** (workflows a conversation SITS in).

  A **pipeline** is authored once and runs to an end: stages fan out for breadth, loop for depth, branch to stop or skip, and call tools directly for the deterministic parts, threading typed fields between each other. It attaches to any agent as a callable tool, *or* becomes a page — a submit form whose fields are the run's parameters, the stages streaming in as each finishes, and every past run kept to re-read.

  A **machine** is the other half of that idea, and the one a chat needs: a set of steps a conversation moves through and then *stays* in. Work out what is being asked once, pick an approach once, then answer in that frame for the rest of the thread — re-deciding only when the subject genuinely changes. What earlier steps established is **state, not transcript**, so turn eight is not re-reading turn one's reasoning; a step can delegate to another agent, narrow its own tools, or be guarded so the conversation leaves when the job does. Both have an editor with the workflow drawn above it — every box a link into that step's form — a rehearsal or a real run on the page, and the same "describe a change" door that redrafts from a sentence and can be taken back.

  Export any primitive as portable JSON; the recipe carries no identity, so it travels between deployments.

## Quick start

**Requires:** Go 1.25+. A local GPU (Ollama / llama.cpp) is optional — point the worker tier at a hosted provider instead if you'd rather.

```bash
# Build — one static binary, no runtime to install
go build -o gohort .

# First-boot setup (TLS, listen addr, admin account, a minimal LLM provider)
./gohort --setup

# Run the web dashboard (the primary surface)
./gohort serve :8080
./gohort serve :8443 --tls       # with a self-signed cert

# Or drive an app from the CLI
./gohort                         # interactive chat with tool access
./gohort techwriter --serve :8080
```

Then sign in and visit **/admin** — nearly all operator config (LLM routing, embeddings, STT, image gen, web search, SMTP, cost rates, tunables) lives there, each with an inline **Test connectivity** button.

## Extend it — a whole app in ~20 lines

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

Add `WebPath()` / `WebName()` / `WebDesc()` / `Routes()` and it gets a web dashboard automatically — the framework discovers it from the type. One blank import (`import _ ".../apps/myapp"`) is the only wiring. Start from the [`hello`](apps/hello/) scaffold and read [`core/ui/AUTHORING.md`](core/ui/AUTHORING.md).

## Built-in apps

| App | What it is |
|-----|-----------|
| `orchestrate` | **Agency** — the agent fleet runner. Chat with seed agents (Chat, Builder, Research, …) or your own; multi-layer governed per-(user, agent) memory, plan-driven authoring, sub-agent dispatch, attachable pipelines and machines |
| `admin` | Operator panel — users, permissions, app groups, API credentials, MCP servers, connectors, tool/skill curation, and all service config |
| `agents` | One published agent, one URL — a permission-gated chat surface scoped to a single agent |
| `knowledge` | Document Collections — shared / per-user RAG buckets agents attach to |
| `bridges` | Messaging transport — wire iMessage / Telegram / … to a channel agent, with a wake-rule gatekeeper |
| `servitor` | SSH system investigator + git-repo Q&A, plan-driven, with an xterm pane; systems are shareable |
| `guides` | Living multi-section guide documents co-authored with an AI Guide Author; source-grounded, exportable, shareable |
| `techwriter` · `codewriter` | Documentation and script/query co-editors |
| `mcpserver` | Expose gohort agents to an external MCP client (e.g. Claude Desktop) |
| `customapps` | Host for Builder-authored apps at `/custom/<slug>/` — declarative sections (form, table, chart, chat, workbench, pipeline, or a raw HTML canvas), a per-app record store, sandboxed data/action scripts, schedules, and per-user sharing or an anonymous link |
| `hello` | Minimal scaffold for a new app |
| `ollama_proxy` | Ollama-compatible HTTP proxy |

Full descriptions in the [reference](docs/REFERENCE.md#built-in-apps).

## Companion client

**`gohort-desktop`** — a native Wails host (macOS): a viewer window plus an always-on menu-bar **Bridge** daemon that owns the host's OS permissions (filesystem, screenshot, contacts) and, on macOS, relays iMessage into the Bridges app. Its tool surface is expandable at runtime — the server can push an admin-approved, user-consented capability that lands as a new local tool without reshipping. See [`gohort-desktop/README.md`](gohort-desktop/README.md).

## Where it's going

The toolkit composes today around **agents + skills + collections + pipelines + machines**, and the through-line is that every new app either uses an existing primitive or proves a new one should exist — so the next app is faster to build than the last. Machines are the most recent instance of that rule: they exist because a *conversation* needed a shape a start-to-finish pipeline could not hold.

On deck:
- **Artifact marketplace** — every artifact type already exports as a portable, identity-free bundle; next is a remote catalog with signing and provenance, so a pipeline or agent recipe can travel between deployments the way a package does.
- **Scoping parity across primitives** — collections and tools have per-user *and* shared tiers; skills are catching up, so "governed" means the same story everywhere.
- **A multi-agent turn** — N agents seeing the same context, each contributing per round, with a synthesizer composing the result. Pipelines can express this today only by naming each participant as its own stage; making the roster declarative is what a debate-shaped app actually wants.
- **Pipelines as agents** — a pipeline is now expressive enough to describe a real multi-round workflow (typed fields between stages, breadth, depth, branching, direct tool calls), but it still has no identity of its own. Next is letting an agent's *body* be a pipeline, so a composed workflow gets a name, an ACL, dispatch, and a schedule for free instead of a second copy of that plumbing.

  *Didn't machines do that?* Nearly, and the difference is worth stating because the two look alike. A machine gives an agent's **conversation** a shape: its unit is the turn, it parks between them, and it never returns a value to a caller. A pipeline-as-agent wants the opposite — a workflow that is an **actor**: callable, dispatchable, schedulable, with a result. Machines proved the pattern (an agent's body can be a declared workflow, authored once and reused) and did *not* absorb the item. The clearest evidence is a known gap in machines themselves: a **dispatched** turn — a schedule firing, a delegation, a sub-agent call — runs *without* the machine, because those paths have no session to hold a position in. Dispatch and schedule are exactly the plumbing this item wants for free, and a machine does not get them.

## Learn more

- **[docs/REFERENCE.md](docs/REFERENCE.md)** — the full feature surface, SDK interfaces, CLI flags, and project layout
- **[core/ui/AUTHORING.md](core/ui/AUTHORING.md)** — how to write a new app from scratch
- **[core/README.md](core/README.md)** — framework core types and internals
- **[docs/agent-machines.md](docs/agent-machines.md)** — machines: the model, the editor, and the decisions behind both
- **[docs/pipeline-surfaces.md](docs/pipeline-surfaces.md)** — the pipeline list, page and per-stage form, and where they deliberately differ from machines
- **[docs/workflow-graph.md](docs/workflow-graph.md)** — one picture for both, and what a fanout, a branch and a loop are drawn as

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Versions before v0.6.314 were released under the MIT License and stay available
under those terms; the change applies from that version onward. Apache 2.0 was
chosen for its express patent grant, which MIT does not provide.

## Dependencies

- [snugforge](https://github.com/cmcoffee/snugforge) — logging, config, flags, kvlite database, concurrency primitives
- [go-pdf/fpdf](https://github.com/go-pdf/fpdf) — PDF generation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) — terminal text editor (Options textarea)
