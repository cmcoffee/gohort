# Apps as MCP tools

Status: proposed (2026-11-14). Nothing built.

## Why

Over MCP, gohort offers five fixed tools: `ask_agent`, `list_agents`,
`list_conversations`, `agent_result` and `recent_results`. Everything a
client wants done goes through `ask_agent`, as a message in prose. That
suits a conversation. It suits a job with defined inputs poorly: "here is a
HAR file, why is this page slow?" has a file and a question, and the
calling model has to describe both in a sentence to an agent that then has
to work out what it was given.

An app Builder makes is already a recipe for a job like that: an agent or a
pipeline, the instructions it runs with, and inputs. Letting an app also
offer itself as an MCP tool, with typed arguments, makes it callable from
Claude Desktop, Claude Code or any MCP client as `analyze_har(file,
question)`. It is the same goal as integrations built as data rather than
Go: a person (or Builder for them) adds a capability to their MCP offering
without anyone writing code.

## What exists

| Piece | Where |
|---|---|
| An app's recipe | `core/appspec.go` `AppSpec`: `AgentID` (a conversation lives here), `PipelineID` (a multi-stage run), `Actions` (script-backed, with `Inputs`) |
| Authoring | Builder's `app_def` tool, and the app editor |
| MCP tools, and what a key reaches | `apps/mcpserver`: the five tools; `token.AgentGrant()` and the key's Can reach list decide which agents |
| A key tick per app | `appFeatureKey` in `core/feature_access.go`, today for registered app agents only |
| A folder an agent searches with capped tools | `apps/filestore`, including temporary stores (docs/design/file-stores.md) |
| Reading a file on the person's computer | the desktop app's bridge (`core/desktop_bridge.go`, `filesystem_read_local_file`) |
| Long runs | `ask_agent` answers "Still working" with a job id; `agent_result` collects it |

## Proposal

### A. An app declares its tools

`AppSpec` gains `mcp_tools`, a list. Each tool:

- **name**: `analyze_har`. Shown to clients namespaced as
  `<app slug>_<name>`, so two apps, or an app and a built-in tool, never
  share a name.
- **description**: what it does and when to use it, written for the
  calling model.
- **inputs**: each with a name, a type (`text`, `number`, `choice`,
  `file`), whether it is required, and a line describing it.
- **runs**: the app's agent, its pipeline, or one of its actions.
- **for an agent**: the message it is sent, built from the inputs
  ("Analyse the HAR file attached and answer: {question}").

Builder authors it through `app_def` like the rest of an app, and the app
page gets a **Try it** form with the same inputs, so a tool is called once
by a person before a client calls it (Builder's verify gate applies).

### B. How a file gets there

This is the hard part. An MCP tool's arguments are written by the calling
model, so a file passed as an argument is a file the model reproduces token
by token: fine for a few kilobytes, impossible for a 20 MB HAR. Four ways,
in the order worth building:

1. **Inline, small.** The file's text as the argument, up to a cap (say
   256 KB). Above it the call is refused with a message naming the other
   ways.
2. **A path on the person's computer**, through the desktop app, which
   already reads local files for agents. The tool takes `path`; gohort
   reads it over the bridge. Refused, saying why, when the desktop app is
   not connected. For Claude Desktop or Claude Code on the same machine,
   this is the natural route: the person says "analyse ~/Downloads/x.har".
3. **An upload.** gohort hands back a short-lived upload link; the person
   drops the file there and the tool takes the reference.
4. **A URL** gohort can fetch, under the outbound rules.

Wherever it comes from, the file lands in a **temporary file store for the
run**, not in the agent's context. A HAR is large JSON; the agent searches
and reads it with the store's capped tools, which is what file stores were
built for, and the store goes when the run is done.

### C. Who can call it

- A tool appears in a key's `tools/list` only when the key reaches it: the
  key's Can reach list gains an **App tools** group, a tick per tool, off
  by default on a scoped key. A key from before scoping does not get them.
- It runs as the key's owner. For an app shared with them, the recipe is
  the app owner's and the records and run history are the caller's, as a
  shared app's pipeline already does; the app's spending caps
  (`AskDailyUSD`, `AskUserDailyUSD`) hold.
- When the set of tools a key reaches changes, the server sends
  `notifications/tools/list_changed`, so a connected client sees it.

### D. What comes back

Text, as `ask_agent` returns. A run that takes longer than the call can
wait answers "Still working" with a job id, collected with `agent_result`,
the same mechanism. A file the run produces (a report) comes back as a link
to it in gohort.

### E. Safety

- **The description is read by the calling model.** It is text an app's
  author wrote and another person's assistant will follow, so it is capped
  in length, and an app shared beyond its owner offers its tools only
  after an administrator approves, as publishing an app already requires.
- **The file is untrusted data.** It reaches the agent through the file
  store's tools, whose results are fenced as untrusted like any tool
  result.
- **Bounded.** The inline cap, the file store's caps, the run's timeout,
  and MCP's existing per-key rate limit.

## The HAR analyzer, end to end

1. Someone asks Builder for "a HAR analyzer I can use from Claude".
   Builder makes an app with an agent whose instructions are about reading
   HAR files (slow requests, failed ones, large payloads, third-party time),
   and declares one tool: `analyze_har`, inputs `file` (file, required) and
   `question` (text, optional).
2. They try it on the app page with a HAR they have, and tick the tool on
   their MCP key.
3. In Claude Desktop: "Look at ~/Downloads/checkout.har, why is checkout
   slow?" Claude calls `har_analyzer_analyze_har` with the path. gohort
   reads the file through the desktop app into a temporary store, the agent
   searches it, and the answer comes back to Claude.

## No migration

`mcp_tools` is a new optional field on an app; an app without it offers
nothing. The key's Can reach list gains new values in the list it already
keeps (`apptool:<slug>/<name>`). Nothing stored is rewritten.

## Steps

1. `mcp_tools` on `AppSpec`, authored by `app_def` and checked on save;
   `tools/list` and `tools/call` for agent-backed tools with text inputs;
   the App tools group on keys; Try it.
2. File inputs, inline up to the cap, into a temporary file store.
3. File inputs as a path, through the desktop app.
4. Pipelines and actions as what a tool runs.
5. Upload links, and URLs.

## Open questions for the owner

1. **Approval.** Should an app's tools need an administrator's approval
   only when the app is shared, as proposed, or always, or never (the
   owner's own tick on their own key being consent enough)?
2. **The inline cap.** 256 KB, or smaller? Larger costs the calling model
   real time and tokens to reproduce.
3. **Built-in apps.** Should apps that ship with gohort (Servitor, Scribe)
   offer tools the same way, for example a Servitor `investigate`?
4. **Naming.** `<slug>_<name>` keeps names unique but reads long; a client
   shows the description too, so the length may not matter.
