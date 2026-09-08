```
      ____       _                _
     / ___| ___ | |__   ___  _ __| |_
    | |  _ / _ \| '_ \ / _ \| '__| __|
    | |_| | (_) | | | | (_) | |  | |_
     \____|\___/|_| |_|\___/|_|   \__|
```

# Gohort — deputies, not tools

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Single binary](https://img.shields.io/badge/deploy-single%20binary-success)](#quick-start)
[![Local-first](https://img.shields.io/badge/LLM-local--first-6366f1)](#1-the-harness)

**Many specialized agents that delegate to each other. A local tier and a lead tier on one harness, a single Go binary, and the model never sees a credential.**

<!-- Screenshots: take these from a FRESH deployment with demo data, never from a
     live instance — a real dashboard carries private app names, contacts and
     chat content, and a README image is in git history for good. Drop the files
     at the paths below and these render as-is. -->

![The Gohort dashboard: every installed app on one page, running from a single binary](docs/images/dashboard.png)

You still talk to one thing. The specialization is underneath: a front agent reads what you want and hands it to whichever agent owns that job, each with its own tools, its own memory, and the right to delegate further. Think of an executive with a cabinet rather than a jack-of-all-trades fixer — the executive's actual skill is knowing who to turn to.

That shape is not decoration. **Tool selection degrades as the tool surface widens**, and an agent choosing among four relevant tools beats the same model choosing among forty, while the context it never loads is context left for the work. Which is also the honest reason gohort runs on local models: not because small models became clever, but because a narrow job is a job they can do. **Specialization and local-first are the same argument.**

```bash
make build && ./build/gohort --setup && ./build/gohort serve :8080
```

That's the whole install. One static binary, no runtime, no venv — or skip the toolchain entirely and [download one](#download-a-release). Ten direct dependencies, and source you can read end to end.

## Give Claude Desktop deputies, not tools

The lowest-friction way to try this: add a few lines to an MCP config you already have open, and delete them if you hate it.

Most MCP servers expose flat tools and the client does the reasoning. **gohort exposes agents as endpoints.** Claude Desktop talks to `servitor`; servitor runs its own plan-driven loop under gohort's harness and returns a synthesized answer. Two loops, not one — and the consequences are the point:

- **It remembers your systems** between sessions, because the memory belongs to the agent, not to the chat.
- **The investigation is governed by gohort's loop** — round budget, loop-guard, failure-streak pivots — rather than by the client's tool-calling behaviour.
- **Credentials never surface to the client at all.** They attach server-side, at call time.
- **Twenty rounds of work happen on your hardware** and only the summary crosses the wire.

It also fills a gap the desktop clients leave: one assistant, one persona, one tool set, one instructions box. You cannot scope tools per workflow, keep separate memory per domain, or route stages by sensitivity. gohort is where you configure that; Claude Desktop stays the client.

The worked case is **servitor**: Claude Desktop investigating your machines over SSH, plan-driven, behind a governed agent, with credentials the model never sees.

## What actually makes it different

Single binary, local-first, multi-provider, sandboxing, memory, no-code authoring — real, all present below, and all claimed by bigger projects. These four are the ones worth choosing gohort for.

### 1. The harness

Everyone else's reliability story is "use a better model". This one is architectural: the model is told its round budget up front, a loop-guard kills any tool re-called with identical arguments after it errors three times, tool-round discipline prevents double replies, failure-streak detection pivots the approach, and runs survive client disconnect.

The checkable version of the claim: **a frontier model and a local Qwen run on the same harness with no model-specific prompt branches** — nothing in the loop reads a model name. Where behaviour differs it is *configuration* (a no-think signal, a thinking budget), not a code path.

### 2. Credential isolation, where the model orchestrates and never holds

"Credential isolation" is a phrase others use too, so here is the design instead. Keys are registered once and stored encrypted. They are injected **server-side at call time**, never rendered into a prompt. Every outbound call is checked against a base-URL and endpoint allowlist **before** the secret attaches. A universal rule forbids any agent from asking for one in chat. Shell work runs in a bubblewrap sandbox whose only network path is an audited hook, with `urllib` / `requests` / `curl` / `wget` refused at authoring time.

Most projects mean the key stays on your disk. This means **the model demonstrably cannot exfiltrate it**. There has been no external security review of that design — it is set out above, and in the source, so you can judge it rather than take it on faith.

One thing to be straight about, because it is the question a careful reader asks: gohort **skills can carry tools**, and activating one is the opt-in that lets its bundled scripts run. They are not merely prompt text. What bounds them is that they are per-user and authored in-product rather than installed from a public registry, and that anything they bring still runs inside the sandbox and under the same credential allowlist as everything else.

### 3. Machines

Pipelines are everywhere. A **machine** is the other thing: a shape a *conversation* sits in across turns, where what an earlier step established is state rather than transcript to be re-read. A pipeline runs start to finish; a machine is where the conversation stays. See [the primitives](#the-primitives-and-everything-else-in-the-box).

### 4. The router pattern

Because a step can narrow its own tools, a router agent can be built with **no capability except dispatch** — and that has two consequences.

**It runs local, cheaply.** Routing is the one job a small model is genuinely good at: classify, pick from a short list, almost no context. So the front door of the whole system runs on your own GPU, and only the specialist behind it needs to be expensive.

**It cannot be injected into doing anything.** A router with no tools has nothing to be turned against. The worst case is picking the wrong specialist — and that specialist has its own allowlist. Most systems have the opposite shape: the agent receiving untrusted input is the one holding every tool.

## Bringing your own keys

gohort registers credentials **per user**: each person brings their own API key, and no key is shared between accounts. It does not support subscription OAuth tokens for programmatic use — API keys only.

That is a design choice, and it also happens to be the shape that keeps you on the right side of most providers' terms, which generally expect each end user to authenticate with their own credential and treat subscription plans as individual usage rather than a backend for automation. Check your provider's current terms rather than taking a README's word for it — especially for scheduled or autonomous agents, which are exactly the case those limits are written about.

## Three ways to think about it

**🏗️ A platform.** A web dashboard that runs a fleet, not a single bot. Multi-agent dispatch with per-caller allowlists, declarative multi-stage **pipelines** (parallel fan-out, bounded loops, branching, and direct tool calls — attached to an agent as a callable tool, or mounted as a page of their own), **machines** that hold a conversation in one shape across turns, messaging **channels**, scheduled + event-triggered agents, real multi-user auth with per-user data isolation, cross-user sharing, and cost telemetry — all first-class, not bolted on.

**⚙️ A harness.** An agent loop engineered so local and small models stay reliable: the model is told its round budget up front, a loop-guard kills any tool re-called with identical args after it errors 3×, tool-round discipline prevents double replies, failure-streak detection pivots the approach, and runs survive client disconnect. The reliability doesn't come from reaching for a frontier model — it comes from the loop.

**🧩 An SDK.** A Go framework you build *on*. Registering a new app is ~20 lines and one blank import; its CLI command and web dashboard are discovered from the type. Compose `FormPanel`, `Table`, `ChatPanel`, `PipelinePanel`, `ChipPicker` and friends — new apps rarely touch HTML, CSS, or DOM JavaScript. Every app either uses an existing primitive or proves a new one should exist, so the toolkit compounds instead of bloating.

## What it looks like in practice

You tell the Builder agent:

> *"Watch our status page and post to the team channel when it changes."*

It drafts an API credential for the status endpoint, authors a poll connector that checks it on a schedule, wires the result to a channel agent that speaks to your team's messaging service, and hands your admin a single approval to paste the secret into. You never wrote code, never opened a flow editor, and never pasted a key into a chat window — and the model that assembled the whole thing still can't read that key.

That's the loop: **describe it, approve it, it runs.**

![An agent turn: the reply alongside the tool calls that produced it](docs/images/agent-turn.png)

## The primitives, and everything else in the box

- **One binary. No Python, no venv, no dependency tree.** `make build` produces a single static executable (cgo off, so it carries no libc floor onto the machines it lands on) — the web dashboard, the agent runtime, the database layer, and every built-in app are compiled in. Deploying is copying one file; upgrading is replacing it. There's no runtime to install on the host, no lockfile to resolve, and no drift between your machine and the box. For self-hosters, this is the difference between a five-second deploy and an afternoon.

- **Build it in the browser, not just for it.** The chat *is* the authoring surface. Tell the Builder agent "make me an agent for X" or "set up a workflow that does Y" and it assembles the right thing from primitives — a new agent, an attached pipeline, a skill, a runtime-defined tool. No code, no visual flow editor, no separate IDE. Persistence of anything consequential goes through an admin approval queue.

- **Your keys never reach the model.** Credentials are registered once, stored encrypted, and injected **server-side** at call time — the LLM drives the wiring but never sees the secret, and a universal rule forbids any agent from asking for one in chat. Every external call is checked against a Base-URL + endpoint allow-list before the secret is attached. Shell tools run in a network-isolated `bwrap` sandbox whose *only* path to the network is a narrow, audited gohort hook (urllib/requests/curl/wget are refused at authoring time). An agent that holds your keys in its memory is a liability; here the model orchestrates access it can't exfiltrate.

- **Memory that's governed, not just persistent.** "Memory that grows with you" is table stakes; the real question is whether you control it. Each agent gets several distinct layers — always-in-prompt facts (with semantic dedup and supersession, so a changed fact *replaces* the stale one instead of piling up), vector-grown reference memory, a graph layer of entities and relationships, a rewritable working-notes scratchpad, and drillable conversation history that archives on compaction rather than collapsing into a lossy summary. Every layer is toggled **per agent**, isolated **per (user, agent)**, and bounded by admin-tunable caps and a background prune sweep. And because credentials are injected server-side, **secrets never land in a memory layer** — the failure mode where a persistent store quietly accumulates your API keys simply can't occur.

- **Local-first, not local-only.** By default the worker tier is your own GPU (Ollama / llama.cpp) and does the bulk of the work; an optional precision tier escalates to a frontier model only for the stages that earn it. Any stage can instead point at a hosted provider — run fully local, fully hosted, or any mix. Privacy is structural: `ForcePrivate` agents and `Private:true` route stages hard-lock to the local tier, so sensitive data (credentials, internal docs, system facts) never leaves the box even by accident.

- **Compose, don't hardcode.** Five primitives — **agents** (persona + tools), **skills** (conditional prompt addendums with a self-training vector corpus), **collections** (RAG buckets), **pipelines** (declarative workflows that run start to finish), and **machines** (workflows a conversation SITS in).

  A **pipeline** is authored once and runs to an end: stages fan out for breadth, loop for depth, branch to stop or skip, and call tools directly for the deterministic parts, threading typed fields between each other. It attaches to any agent as a callable tool, *or* becomes a page — a submit form whose fields are the run's parameters, the stages streaming in as each finishes, and every past run kept to re-read.

  A **machine** is the other half of that idea, and the one a chat needs: a set of steps a conversation moves through and then *stays* in. Work out what is being asked once, pick an approach once, then answer in that frame for the rest of the thread — re-deciding only when the subject genuinely changes. What earlier steps established is **state, not transcript**, so turn eight is not re-reading turn one's reasoning; a step can delegate to another agent, narrow its own tools, or be guarded so the conversation leaves when the job does. Both have an editor with the workflow drawn above it — every box a link into that step's form — a rehearsal or a real run on the page, and the same "describe a change" door that redrafts from a sentence and can be taken back.

  Export any primitive as portable JSON; the recipe carries no identity, so it travels between deployments.

## Quick start

A local GPU (Ollama / llama.cpp) is optional — point the worker tier at a hosted provider instead if you'd rather.

### Download a release

Binaries for linux, macOS and Windows (amd64 and arm64 where the platform has both) are on the [releases page](https://github.com/cmcoffee/gohort/releases), each archive carrying the binary, `LICENSE`, `NOTICE` and the third-party notices for that build. No toolchain, no runtime, nothing to install beside it.

```bash
# Verify what you downloaded — SHA256SUMS sits beside the archives
sha256sum --ignore-missing -c SHA256SUMS     # macOS: shasum -a 256 -c SHA256SUMS

tar xzf gohort_<version>_linux_amd64.tar.gz
cd gohort_<version>_linux_amd64

./gohort --setup                     # TLS, listen addr, admin account (LLM + the rest: web UI)
./gohort serve 127.0.0.1:8080        # the web dashboard
```

On macOS the download is unsigned, so Gatekeeper quarantines it: `xattr -d com.apple.quarantine gohort` before the first run.

### Build from source

**Requires:** Go 1.25+.

```bash
# Build — one static binary, plus the notices it has to travel with
make build

# First-boot setup (TLS, listen addr, admin account — the rest is configured in the web UI)
./build/gohort --setup

# Run the web dashboard (the primary surface)
./build/gohort serve :8080
./build/gohort serve :8443 --tls     # with a self-signed cert

# Or talk to it from the terminal
./build/gohort chat                  # interactive chat with tool access; bare `./build/gohort` does the same
./build/gohort --version
```

Then sign in and visit **/admin** — nearly all operator config (LLM routing, embeddings, STT, image gen, web search, SMTP, cost rates, tunables) lives there, each with an inline **Test connectivity** button.

**Do `--setup` first, and on loopback.** An instance with no users configured has nobody to authenticate, so the dashboard's front door is open and `/admin/` leads to the wizard that mints the first admin. That is how the first account gets created and it is fine on `127.0.0.1` (the setup default is `127.0.0.1:8181`); it is not fine on a public interface. Create the admin account, then widen the bind address.

**Optional host tools.** The binary needs nothing else to run, but a few features shell out to programs it expects on `PATH`: `ffmpeg`/`ffprobe` (audio and video), `pdftotext` and `pandoc` (document extraction), `yt-dlp` (video downloads), `git` (repository appliances), `python3` (sandboxed scripts), and `bwrap` (the shell sandbox, Linux). Each is optional and only the feature that uses it degrades when it is missing; **Admin → System Dependencies** shows what is present, what version, and what it enables.

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

Built and maintained by one person, in the open, at the pace of something used daily rather than demoed occasionally.

The toolkit composes today around **agents + skills + collections + pipelines + machines**, and the through-line is that every new app either uses an existing primitive or proves a new one should exist — so the next app is faster to build than the last. Machines are the most recent instance of that rule: they exist because a *conversation* needed a shape a start-to-finish pipeline could not hold.

Two things that used to sit on this list have since shipped, and are worth knowing about because they are unusual:

- **A panel is a multi-agent turn.** A `panel` stage puts several voices on the same question, in parallel, for as many rounds as you ask for — and each round reads the last. One round is a poll; two is the smallest thing that can honestly be called a debate, because until the second round nobody has replied to anybody. The roster is declarative, so a debate-shaped workflow is a list of names rather than one stage per participant.
- **A pipeline is a callable target.** It did not land as an agent's body, which is what this list used to predict. It landed better: a pipeline is dispatchable as a peer (with its own per-turn dispatch ACL), schedulable on its own, and shareable — a workflow that is an *actor*, callable and with a result, without pretending to be a persona. Machines remain the other half: a machine gives an agent's **conversation** a shape, parks between turns, and returns nothing to a caller.

On deck:
- **Artifact marketplace** — every artifact type already exports as a portable, identity-free bundle; next is a remote catalog with signing and provenance, so a pipeline or agent recipe can travel between deployments the way a package does.
- **Scoping parity across primitives** — collections and tools have per-user *and* shared tiers; skills are catching up, so "governed" means the same story everywhere.
- **The dispatched-turn gap in machines** — a schedule firing, a delegation or a sub-agent call runs *without* the machine, because those paths have no session to hold a position in. That is the one place where the two halves above do not meet.

## Learn more

- **[docs/REFERENCE.md](docs/REFERENCE.md)** — the full feature surface, SDK interfaces, CLI flags, and project layout
- **[core/ui/AUTHORING.md](core/ui/AUTHORING.md)** — how to write a new app from scratch
- **[core/README.md](core/README.md)** — framework core types and internals
- **[docs/agent-machines.md](docs/agent-machines.md)** — machines: the model, the editor, and the decisions behind both
- **[docs/pipeline-surfaces.md](docs/pipeline-surfaces.md)** — the pipeline list, page and per-stage form, and where they deliberately differ from machines
- **[docs/workflow-graph.md](docs/workflow-graph.md)** — one picture for both, and what a fanout, a branch and a loop are drawn as

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Product names are used descriptively, to say what gohort interoperates with. Claude and Claude Desktop are trademarks of Anthropic, PBC; Ollama, llama.cpp and every other project named here belong to their respective owners. gohort is an independent project and is not affiliated with, endorsed by, or sponsored by any of them.

`make release` builds an export of `HEAD` for every supported platform with
`GOWORK=off`, and packages each binary with `LICENSE`, `NOTICE` and a
`THIRD_PARTY_NOTICES` generated for *that* platform from the modules actually
linked into it. Those three files are what a distributed binary has to carry.
A module that ships no license text of its own has its terms vendored under
[`licenses/`](licenses/), together with where they were obtained, and the
generated notices say so rather than passing them off as the module's own.

Versions before v0.6.314 were released under the MIT License and stay available
under those terms; the change applies from that version onward. Apache 2.0 was
chosen for its express patent grant, which MIT does not provide.

## Dependencies

- [snugforge](https://github.com/cmcoffee/snugforge) — logging, config, flags, kvlite database, concurrency primitives
- [go-pdf/fpdf](https://github.com/go-pdf/fpdf) — PDF generation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) — terminal text editor (Options textarea)
