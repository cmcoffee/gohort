# Authoring a gohort App

## What this framework is for

`core/ui` lets an app developer with no web experience add capabilities to gohort from primitives that already exist. You should not have to write HTML, CSS, or DOM-manipulation JavaScript to ship a working app. You write a Go struct, declare a page in terms of pre-built components (Table, FormPanel, ChatPanel, PipelinePanel, …), and the framework renders it.

Every primitive in `core/ui` is generic: no primitive knows what a particular app means. When an app needs behavior beyond what the primitives offer, it plugs into one of four extension registries (block renderer, markdown extension, client action, `ExtraHeadHTML`) from its own package. This separation is enforced; see `CLAUDE.md` at the repo root for the rule and `scripts/hooks/pre-commit` for the guard.

## App anatomy

A gohort "app" is a Go package that:

1. Defines a struct embedding `core.AppCore`
2. Implements the `core.Agent` interface
3. Registers itself with `core.RegisterApp` in `init()`
4. Mounts HTTP routes that serve a `core/ui.Page`

The framework handles routing, auth, sessions, SSE, LLM wiring, cost tracking, and the entire frontend. You write Go code that returns a declarative page; the framework renders it.

---

## TL;DR: minimal working app

```go
package hello

import (
    "net/http"

    . "github.com/cmcoffee/gohort/core"
    "github.com/cmcoffee/gohort/core/jsonapi"
    "github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(HelloAgent)) }

type HelloAgent struct {
    AppCore
}

// Agent interface
func (T HelloAgent) Name() string         { return "hello" }
func (T HelloAgent) Desc() string         { return "Apps: A minimal hello-world app." }
func (T HelloAgent) SystemPrompt() string { return "" }
func (T *HelloAgent) Init() error         { return T.Flags.Parse() }
func (T *HelloAgent) Main() error {
    Log("Hello is a dashboard-only app. Start with:\n  gohort serve :8080")
    return nil
}

// SimpleWebApp interface — Routes() registers handlers against the
// pre-wired sub-mux. Framework owns the mux lifecycle so apps stay
// boilerplate-free.
func (T *HelloAgent) WebPath() string { return "/hello" }
func (T *HelloAgent) WebName() string { return "Hello" }
func (T *HelloAgent) WebDesc() string { return "A minimal hello-world app." }

func (T *HelloAgent) Routes() {
    T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
            http.NotFound(w, r)
            return
        }
        T.handlePage(w, r)
    })
    T.HandleFunc("/api/echo", jsonapi.Post(T.DB, T.handleEcho))
}

func (T *HelloAgent) handlePage(w http.ResponseWriter, r *http.Request) {
    if _, _, ok := RequireUser(w, r, T.DB); !ok {
        return
    }
    page := ui.Page{
        Title:     "Hello",
        ShowTitle: true,
        BackURL:   "/",
        MaxWidth:  "900px",
        Sections: []ui.Section{
            {
                Title:    "Greeting",
                Subtitle: "Type your name and submit — server echoes it back.",
                Body: ui.FormPanel{
                    PostURL: "api/echo",
                    Fields: []ui.FormField{
                        {Field: "name", Label: "Your name", Type: "text",
                            Placeholder: "Alex"},
                    },
                },
            },
        },
    }
    page.ServeHTTP(w, r)
}

func (T *HelloAgent) handleEcho(c *jsonapi.Call, body struct {
    Name string `json:"name"`
}) (any, error) {
    return map[string]string{"message": "hello, " + body.Name}, nil
}
```

Add a blank import in `agents.go` (the loader at the repo root) to register it:

```go
import _ "github.com/cmcoffee/gohort/apps/hello"
```

Run `gohort serve :8080`. The Hello page is live at `/hello/`.

### What's happening here

- `RegisterApp` (in `init()`) hands the agent to the framework's registry.
- The Agent methods (`Name`/`Desc`/`SystemPrompt`/`Init`/`Main`) cover CLI/registration.
- The SimpleWebApp methods (`WebPath`/`WebName`/`WebDesc`/`Routes`) cover the dashboard. The framework creates a per-app sub-mux, calls `Routes()` against it, and mounts it at `prefix` with cost-tracking middleware: no explicit `NewWebUI`/`MountSubMux` plumbing.
- `T.HandleFunc(pattern, handler)` registers against that pre-wired sub-mux. Patterns are relative to the prefix.
- `ui.Page{...}.ServeHTTP(w, r)` renders the page from its declarative spec. You never write HTML in app code.
- A JSON endpoint is a `core/jsonapi` handler. `jsonapi.Post` resolves the caller, refuses a signed-out request (401), decodes the body into the handler's second argument (a broken one is 400, one over 1 MiB is 413), and writes what the handler returns as JSON. Each answers its own method and no other: `jsonapi.Get`, `Post`, `Put`, `Patch` and `Delete`, and `jsonapi.Methods` puts more than one on a path. Any other method is a 405. The handler is only the part that is yours.
- The form has no `Source` (read endpoint): it's a blank-state form. `PostURL` is the only write target. The field key is `Field` (not `Name`).

If you need direct mux/prefix control (custom access wrappers, alternate mount semantics), implement the older `WebApp` interface with `RegisterRoutes(mux, prefix)` instead. SimpleWebApp is the default; WebApp is the escape hatch.

---

## Concepts in 5 minutes

### App = Agent + Routes
An app is one struct that implements `core.Agent`. Its `Routes()` registers HTTP handlers on the app's sub-mux. The framework gives it user auth, a per-user kvlite database (`T.DB`), and LLM sessions (`T.WorkerChat()`, optionally `T.LeadChat()`).

### Page = declarative tree of components
`ui.Page` is a Go struct describing the page. You don't write HTML or CSS. The framework's runtime JS (`/_ui/ui.js`) reads your page config and builds the DOM.

### Sections wrap Components
A `ui.Section` has a Title, Subtitle, and a Body (one Component). Common components: `Table`, `FormPanel`, `ChatPanel`, `PipelinePanel`, `DisplayPanel`, `Card`, `Frame`. List them in `Sections: []ui.Section{...}`.

`Card` splices raw HTML into the page; `Frame` gives a COMPLETE HTML document (a game, a canvas animation, an embedded mini-app) its own iframe so its CSS reset and `body` rules style only itself and its `100vh` measures its own box. Reach for `Frame` whenever the markup starts with a doctype or carries its own `<body>`: same origin either way, so relative fetches and storage keep working.

A `Card` whose content is DERIVED (a diagram, a plan, a list of findings your
handler worked out) should carry a `Source` and a `RefreshOn`, or it will go
on describing the record as it was before the edit that was made while looking
at it:

```go
ui.Card{
    HTML:      thingMapHTML(def),                       // the first paint, server-rendered
    Source:    "api/things/" + id + "/block?name=map",  // where to fetch it again
    RefreshOn: []string{"api/things/" + id + "/parts"}, // what changing means "redraw me"
}
```

`RefreshOn` entries match an invalidated source by PREFIX, so one entry covers
the per-record writes (`…/parts?name=x`) a form broadcasts when it saves. The
card coalesces a burst of saves into one fetch, keeps the last good content if
the fetch fails, and dispatches `ui-card-refreshed` on itself afterwards:
listen for that if your page decorates the card's elements. Serve the block
from the SAME function the page renders it with, so the refresh cannot drift
from the first paint.

### SSE for streaming, REST for the rest
Static data → JSON over plain HTTP. Live pipelines → SSE (`/api/send`, framework runtime handles the protocol). The `PipelinePanel` primitive does this for you: you write a *bridge* function that translates your app's events into framework SSE events.

### The public surface (what an app may depend on)

An app should build only against the surface below. Everything else in `package
core` is framework internals: it may change or move without notice, and it is
being migrated under `core/internal/` package by package (Go bars apps from
importing anything there). If you need a `core` symbol not listed here, either
(a) request it be promoted to the public surface, or (b) solve it in your own
package.

The public surface, ordered most → least stable:

1. **The `Agent` interface** + `RegisterApp` + the `WebApp`/`SimpleWebApp` interfaces + `AppCore`: frozen.
2. **The logging/aliasing helpers** re-exported from `core` (`Log`, `Fatal`, `Notice`, `Err`, `RequireUser`, config/DB/`LimitGroup` type aliases): frozen names.
3. **`ui.Page` + the well-known components** (Table, FormPanel, ChatPanel, PipelinePanel, FormField types) + **`ui.Head`**: adds-only; field renames go through a deprecation pass.
4. **The extension registries** (block renderer, markdown extension, client action) reached via **`ui.Head`**: frozen signatures; new registries are additive.
5. **The runtime helpers exposed on `window`** (`uiEl`, `uiMdToHTML`, `uiRenderMarkdown`, `uiOpenModal`, `uiRegister*`): frozen names.
6. **CSS class names**: generic `ui-pl-*`, `ui-chat-*`, `ui-form-*` classes are stable; app-specific class names belong in the app's package.

Deprecations: a symbol slated for removal is marked `// Deprecated:` for at
least one minor release before it goes.

Changes to this surface are recorded in the **commit log**, not a changelog
file: each commit message explains what moved and why, so `git log -- core/
core/ui/` is the authoritative history.

---

## Pick a primitive

| Want to build… | Use |
|---|---|
| A list of records with row actions (edit / delete / view) | `ui.Table` + `RowAction` (`ui.ModalAction` for an edit dialog, `ui.Expand` for inline detail) |
| A settings form auto-saving on blur | `ui.FormPanel` |
| A guided multi-step create flow (wizard with Back/Next) | `ui.FormPanel` with `Steps` (see below) |
| A labeled key-value display (read-only) | `ui.DisplayPanel` |
| Two related lists in one section (your own, and the ones shared with you) | `ui.Stack` of `ui.Subsection`s, each with its own Title / Subtitle / Detail / Body |
| A chat with sessions sidebar | `ui.ChatPanel` |
| A pipeline: submit a job, watch SSE blocks stream in, view past runs | `ui.PipelinePanel` (with a bridge that emits `block`/`chunk`/`status` events) |
| A live-watch page for an in-flight pipeline (separate from submit) | `ui.PipelineWatchPanel` |
| An LLM-backed suggestion list ("Suggest topics") | `ui.SuggestPanel` |
| ✨ Help filling in a form that is not plain (questions answered, fields filled for the person to check) | `FormPanel{Assist: true}`: wizards have it already; `AssistURL` + `core/assist.Handler` for app knowledge and tools (see below) |
| A form opened with that help already on it and a request sent (✨ Fix on a broken row) | `ui.AssistAction` / `ui.AssistPanel` (see below) |
| A single rotatable API key | `ui.ApiKeyPanel` |
| A pickable chip cloud (apps assigned to a user, etc.) | `ui.ChipPicker` |
| A multi-line list editor (each row a rule) | `ui.FormField{Type: "rules"}` inside `FormPanel` |
| A compact tag-array editor (chips with × removers) | `ui.FormField{Type: "tags"}` inside `FormPanel` |
| A toggle (boolean) on a form | `ui.FormField{Type: "toggle"}` |
| A field that must stay one line (handle, URL, slug, API key) | `ui.FormField{SingleLine: true}`: otherwise a multi-line paste grows the field into a textarea rather than flattening the content |
| A bar chart | `ui.BarChart` |
| An article editor with full markdown + image insertion | `ui.ArticleEditor` |
| A code editor with diff + history | `ui.CodeEditorPanel` |
| A document the person writes with an agent: select a passage, ask about it, review the proposed change as a diff | `ui.WorkbenchPanel{ProposeEdits: true}`, or `window.uiCoauthor` on your own textarea (see Co-authored documents below) |
| A button in your own HTML (ExtraHeadHTML, a client action's modal) | `class="ui-row-btn"`, with `primary` / `danger` / `ui-btn-sm` as needed. It takes the shared size, rounding and touch size; a hand-styled button takes none of them |
| A one-glyph button in your own HTML (remove ×, edit ✎, ✨) | `class="ui-icon-btn"` (add `danger` for a remove). Never padding and a font size of your own: on a phone that is a 20px target |
| A list of editable entries in your own HTML, some of them long (rules, notes, examples) | A `textarea` per entry through `window.uiExpandingLine(ta, {oneLine, buttonClass, onInput})`: it reads on one line and opens to the whole entry while edited, or from the arrow it returns. `oneLine` when each entry is one stored line |

If your shape doesn't fit any of these cleanly, you may need a new primitive. Prefer combining existing ones first: most app surfaces are some mix of Table + FormPanel + a chat-shaped flow.

### FormPanel wizards (`Steps`)

Setting `Steps` on a `FormPanel` turns it into a multi-step wizard: a numbered progress rail, one step's fields at a time, Back/Next navigation, and the submit button (plus Test/Reset if configured) on the final step. Everything still binds to ONE record: the final submit POSTs exactly what a flat form would, so the server side is unchanged. `Fields` is ignored when `Steps` is set.

```go
ui.FormPanel{
    PostURL:     "api/things",
    SubmitLabel: "Create",
    RedirectURL: "thing/{id}",
    Steps: []ui.FormStep{
        {Title: "Basics", Intro: "Name it and pick a kind.", Fields: []ui.FormField{
            {Field: "name", Label: "Name", Required: true},
            {Field: "kind", Label: "Kind", Type: "select", Options: kinds}, // include a blank "— choose —" option when Required
        }},
        // ShowWhen (same grammar as field ShowWhen) skips a whole step
        // based on an earlier answer.
        {Title: "Channel", ShowWhen: "kind:channel", Fields: channelFields()},
        {Title: "Review", Intro: "Check the summary, then create.", Fields: reviewFields()},
    },
}
```

`Required: true` on a field blocks Next (and the final submit) while it's empty, highlighting the offender. A step hidden by `ShowWhen` is skipped by navigation, dropped from the rail, and exempt from required gating. Use a wizard for guided create/onboarding flows; keep flat `Fields` for edit-in-place settings forms.

---

### Assist (✨)

Anything gohort offers the model's help with carries a ✨. For a form, that is
**✨ Get assistance** in the form's corner: every wizard (`Steps`) has it, and
any other form that is not plain to fill in turns it on with `Assist: true`.
It shows only where a worker model is set up.

It swaps a helper in where the form is: a conversation beside a small version of
the form, each field and what it holds, what it just changed lit, required fields
still empty marked "needed". The person asks what anything means, or says what
they are setting up, and the helper fills fields in. **Back to the form** swaps
the form back with what it filled marked and a bar offering Undo (and, on a form
that saves as you type, "Save what it filled in"). The conversation never saves.
Password fields travel only as set or empty, and are never filled.

By default the helper knows what the page says: the form's fields with their
`Help` and `Detail`, and the section or dialog around it. So **write the Help
and Detail well**: they are what the helper explains from. Where the app can do
better, give it `AssistURL`: an endpoint built with `assist.Handler` from
`core/assist`, with what only the app knows (`About`, read from the URL, never
from the browser) and tools of its own (`Tools`; one that writes is dropped).

A create form whose help is the point of it can name the button and start the
conversation: `AssistLabel: "Draft with AI"` shows **✨ Draft with AI** in the
corner, and `AssistAsk: "Assist me with setting this up."` is sent
as the helper first opens, so it starts on the setting up rather than on an
empty box.

To open a form with the helper already on it and a request already sent, use
`ui.AssistPanel` (in a dialog, `ui.AssistAction` on a row):

```go
ui.AssistAction("Fix", ui.AssistPanel{
    Work: ui.FormPanel{Source: "api/thing?id={id}", PostURL: "api/thing?id={id}",
        SubmitLabel: "Save", Fields: fields},
    URL:  "api/thing/assist?id={id}",
    Ask:  "Why does this fail, and what would fix it?",
})
```

`Attach` adds a 📎 for files the endpoint's tools use (a sample to try on), and
`Walk` goes through a list of records in turn. System Hooks' ✨ Fix is the worked
example (`apps/admin/system_deps_fix.go`).

For one long value rather than a form, `FormField.SuggestURL` on a textarea
opens the draft window (`uiOpenAssist`). Your own JS opening that window passes
`url` (a `core/assist` endpoint) in place of `send`.

Every assist endpoint is an `assist.Handler`, including the ones with
instructions of their own (an agent's fields, a workflow step, the rules lists,
a CodeWriter document). `Prompt` writes the system prompt yourself, used as
written; `Parse` reads the answer your way (a draft cleaned for its field, a
list of changes, returned through `Reply.Extra`); `Shot` answers a request with
no message (a one-shot suggestion); `Request.Get` and `Request.String` read the
keys your client sends. A client that asks for the stream
(`Accept: application/x-ndjson`, as `uiAssistPost` does) gets progress lines;
any other gets one JSON reply. Never write a second assist handler.

### Co-authored documents

A document the person writes, with an agent beside it. They select part of it
the way they would to copy it (a drag, a double-click, the handles on a phone),
and a bar beside the selection offers **Ask** and your quick actions. Ask pins
the passage: a chip over the chat says what the next message is about, and the
send carries it. The agent answers with a proposal, reviewed as a diff of that
passage alone, with Apply, Reject and Undo. Nothing is written until the person
applies it, and your own Save keeps it.

- **Whole app:** `ui.WorkbenchPanel{ProposeEdits: true, EditURL: ..., SelectionActions: ...}`.
  The open record is an editor from the start, every chat send carries the
  document as it is on screen (`"document": {id, text}`) and the pinned
  passage (`"selection"`). `EditURL` must merge what it is sent into the
  record: it gets the id and the edited field only. Builder's version is the
  workbench section's `editable: true`.
- **Your own textarea:** `window.uiCoauthor(textarea, {host, actions, setValue,
  showChat, showDocument})`, with an `AgentLoopPanel` inside `host`. It returns
  `{pin, unpin, pinned, propose, destroy}`; call `destroy` when the textarea goes.
- **Server:** give the turn `agenthost.ProposeEditTool(pub)` (`propose_edit`:
  `find`, `replace`, `summary`; it emits a `text_proposal` block) and put
  `agenthost.SelectionContext(sel)` in the turn with `agenthost.WithTurnContext`,
  sending through `PublicHandleSendWithAppToolsPublishing`. `find` is copied
  text, not offsets: it is also the staleness check, so a passage the person
  changed since is refused at Apply instead of written over.
- ✨ Assist drafts (`uiOpenAssist`) already do this: the selected passage rides
  the request as `selection`, and the reply's version opens on **Changes**.

docs/design/coauthor-proposals.md has the reasoning.

### Repeating rows

A `rows` field edits a LIST of small records inside one field of one record:

```go
{Field: "output", Type: "rows", Label: "What this step hands on",
 AddLabel: "+ Add output",
 Columns: []ui.FormField{
   {Field: "name", Label: "Field", Type: "text", Width: 2},
   {Field: "type", Label: "Type", Type: "select", Options: …},
   {Field: "required", Label: "Required", Type: "toggle"},
 }}
```

The value is a plain array of objects keyed by the columns' `Field` names, so an
endpoint keeps its natural shape (`output: [{name, type, required}]`).

Reach for it wherever a structured editor would otherwise be a JSON textarea:
phase outputs, pipeline stage outputs, an action's parameters, an intake form's
fields.

A column is itself a `FormField`, so cells use the same vocabulary as the rest of
the form (`text` / `number` / `select` / `toggle`). `Width` is a flex weight, so a
description can be wider than a type.

Order is preserved and editable (↑↓ per row), because in most of these the order
IS meaning. There is no drag-to-reorder: the buttons are keyboard-reachable, work
on a phone, and cannot drop a row somewhere nobody meant.

Adding a blank row does
NOT save (an empty row is not data, and persisting it makes validation complain
about a field nobody has reached yet), and a text cell commits on blur rather than
per keystroke, because the whole array is one save.

**Not a `Table`.** A table renders records the server owns, with its own source
and row actions. This edits an array that lives inside one field and saves with
it.

## The four common app shapes

### 1. Admin-style CRUD app

One Table listing records + one FormPanel below for adding/editing. See `apps/admin/page.go` or the autoblog dashboard in `private/blogger/page.go`.

```go
ui.Page{
    Sections: []ui.Section{
        {
            Title: "Records",
            Body: ui.Table{
                Source: "api/records", RowKey: "id",
                Columns: []ui.Col{...},
                RowActions: []ui.RowAction{
                    // ModalAction opens the form in a dialog prefilled from the
                    // record ({id} substitutes from the row); a submit-mode form
                    // (SubmitLabel set) closes the dialog on save. ui.Expand with
                    // the same body renders it inline below the row instead.
                    ui.ModalAction("Edit", ui.FormPanel{
                        Source:      "api/records/{id}",
                        PostURL:     "api/records",
                        SubmitLabel: "Save",
                        Fields:      recordFields(),
                        Invalidate:  []string{"api/records"},
                    }),
                    {Type: "button", Label: "Delete",
                        PostTo: "api/records/{id}", Method: "DELETE",
                        Confirm: "Delete this record?", Variant: "danger"},
                },
            },
        },
        {
            Title: "Add new",
            Body: ui.FormPanel{
                PostURL: "api/records",
                Fields:  recordFields(),
            },
        },
    },
}
```

The server-side endpoints (`/api/records` list+create+update, `/api/records/{id}` get+delete) are plain JSON HTTP handlers.

### 2. One-shot pipeline app

User submits a topic → server runs a pipeline → blocks stream into a panel → final report saved. See `private/research/page.go`, `private/debate/page.go`, `private/blogger/page.go` (manual mode: deprecated but still illustrative).

```go
ui.Page{
    Sections: []ui.Section{
        {
            NoChrome: true,
            Body: ui.PipelinePanel{
                SessionsListURL:  "api/sessions",
                SessionLoadURL:   "api/sessions/{id}",
                SessionDeleteURL: "api/sessions/delete/{id}",
                SubmitURL:        "api/send",
                CancelURL:        "api/cancel",
                ReconnectURL:     "api/sessions/reconnect/{id}",
                DeepLinkParam:    "session",
                SubmitLabel:      "Start",
                Fields: []ui.PipelineField{
                    {Name: "topic", Label: "Topic", Type: "textarea", Required: true},
                },
                Markdown: true,
            },
        },
    },
}
```

You write a *bridge* in a `chat_endpoints.go` that:

- `handleChatSessionsList`: GET, returns the sidebar-list shape
- `handleChatSessionLoad`: GET, returns a saved session as `{blocks: [...]}`
- `handleChatSessionDelete`: DELETE
- `handleChatSend`: POST, runs the pipeline + streams SSE events through a bridge struct that translates pipeline events to framework events (`block`, `chunk`, `chunk_replace`, `block_done`, `status`, `done`, `error`)

### 3. Chat app

User-and-assistant message thread, optionally with tools and attachments. Use `ui.ChatPanel`.

**A chat that runs on an agent** (the agent plans, calls tools, and the app hands it tools that write into the app's own data, as Scribe's Guide Author does): use `ui.AgentLoopPanel` and let orchestrate run the turns.

1. Register the agent with `appagents.RegisterAppAgent` (an `AppAgentSpec`: id, owning app, prompt, allowed tools; `Hidden: true` when it only makes sense inside your app). Each user gets their own copy layered over your definition. It is your app's: orchestrate does not list it, it cannot be deleted, and it is set up in your app (step 3).
2. Send through `orch.PublicHandleSendWithAppTools(w, r, agent, tools)`, with the agent from `orch.LookupAppAgent(user, id)` and your tools as `[]AgentToolDef`.
3. Wire everything else with `orchestrate.AppChat`. `chat.Panel(ui.AgentLoopPanel{...})` fills in every endpoint you leave empty (edit and retry on a message, rename, rejoining a running turn after a reload, tool confirmations, question cards, guard notices), and `orch.ServeAppChat(w, r, agent, chat, path, scope)` answers them. Set `Settings: true` and `Back` (your page's path) for "Agent settings" in the chat's toolbar: a settings page served under your prefix with the agent's rounds, plan steps, gap detection and reasoning, and Reset to default. It covers every app agent your app registers (one section each), plus any you list in `Agents` that another app owns but yours runs (Scribe lists Publishing's Publisher). The save takes those fields, for those agents, and nothing else. Do not hand-wire these URLs: a URL left off is a control that is silently missing. `apps/scribe/web.go` (`scribeChat`) is the reference.
4. **Declare what each tool does.** The guards act on declarations, not names: `Caps: []Capability{CapNetwork}` for a tool that reaches the network (Private mode drops it; its output is fenced and scanned), `FetchesExternal: true` for one that returns outside documents without a network capability of its own, `CapWrite` for one that changes stored data. A tool that declares nothing gets none of that. A handler that starts its own run passes the handler's `ctx` along, never `context.Background()`, or Private mode and Stop do not reach it.
5. **An app that runs its own agent loops** (not orchestrate's runner) applies `orch.AppLoopGuard(ctx, user, agentID).Apply(cfg)` to each loop config, one guard per run, or no guardrail sees those loops. `docs/design/app-agents.md` has the whole picture.

### 4. Live-watch page

Tracking an in-flight pipeline that was kicked off elsewhere. Use `ui.PipelineWatchPanel`. See `private/blogger/page.go`'s `handleBloggerWatchPage` for an example.

---

## Customizing the UI (extension registries)

When a primitive doesn't fit and you need app-specific UI without changing core, use one of these extension points: a **block renderer**, a **markdown extension**, a **client action**, or app **CSS**.

### Prefer the typed `ui.Head` builder

Compose extensions in Go with `ui.Head` and attach it to `Page.Head`. The framework assembles the `<script>`, the `window.uiRegister*` calls, the readiness-retry guard, the `<style>`/`<script>` wrapping, and `</script>` safety, so you never hand-write a head blob:

```go
page.Head = ui.NewHead().
    CSS(`.ui-myapp-note{color:var(--text-mute)}`).
    ClientAction("myapp_greet", myappGreetJS).      // JS function expression
    BlockRenderer("myapp_plan", myappPlanJS).
    MarkdownExtension(myappMdJS)
```

The JS you pass is a **function expression** (`"function(ctx){ … }"`): inline for small handlers, or from an embedded `assets/` file (`//go:embed`) for anything sizeable, matching the runtime's own asset convention. See `apps/hello/hello.go` for a complete button → client action → `window.uiOpenModal` example, and `core/ui/extensions.go` for the API.

Registration order is handled for you (the assembled block guards on the registries existing), so you do **not** need the manual `DOMContentLoaded` deferral the raw form below requires.

### Raw `Page.ExtraHeadHTML` (escape hatch / underlying mechanism)

`ui.Head` renders into the same `<head>` injection as `Page.ExtraHeadHTML`: a verbatim Go string of HTML. Reach for the raw string only for legacy blobs not yet ported to `ui.Head`, or head markup the builder doesn't model. The examples below show the raw shape each `ui.Head` method emits for you.

### Block renderer
For a new block type emitted on the SSE stream. Register a renderer that takes the block's data and returns DOM.

```html
<script>
(function() {
  function register() {
    if (!window.uiRegisterBlockRenderer) return;
    var el = window.uiEl;
    window.uiRegisterBlockRenderer('my_block_type', function(d, ctx) {
      var wrap = el('div', {class: 'ui-my-block'});
      wrap.textContent = d.title || '';
      return {wrap: wrap, body: null};
    });
  }
  // Defer until runtime has loaded — ExtraHeadHTML runs in <head>
  // before the runtime <script> at body end.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', register);
  } else {
    register();
  }
})();
</script>
```

A block that follows the running turn (a checklist, a progress card) can ask to stay in sight: set `data-ui-pin="live"` on its wrap while the work is under way, and `"done"` once it has finished. While a turn runs, the newest live block sticks to the top of the conversation, at most a third of the pane, and scrolls itself to keep the element you mark `data-ui-pin-focus` (the current step) in view. A block set to `"done"` stays up a few seconds so its ending is read, then returns to its place in the thread. Update the attributes in `onUpdate` as the state changes; the panel re-reads them after every update.

### Markdown extension
A post-processor that runs after `mdToHTML`'s base passes. Use it to add app-specific syntax (e.g., colorize a heading pattern unique to your domain).

```js
window.uiRegisterMarkdownExtension(function(html) {
  return html.replace(/<h2>(MyHeader)([^<]*)<\/h2>/g,
    '<h2 class="ui-my-header">$1$2</h2>');
});
```

### Rendering markdown into your own element
If a custom block renderer (or any app DOM) needs to show rendered markdown, use `window.uiRenderMarkdown(el, text)` rather than `el.innerHTML = uiMdToHTML(text)`. It stamps the `.ui-md` prose class on the target, which is what gives headings, code blocks, lists, and links the shared, proportionate type scale. Setting `innerHTML` from `uiMdToHTML` directly works too, but the element then falls back to the browser's default heading/monospace sizes. The styling lives entirely in `.ui-md`; never re-declare generic `h*`/`pre`/`code` rules in your app CSS, and override only app-specific bits.

### Client action
A browser-side action invoked by a `PipelineAction{Method: "client"}`. Use for things like `window.print`, custom clipboard handling, focus/scroll helpers.

```js
window.uiRegisterClientAction('my_action', function(ctx) {
  // ctx = {sessionId, sessionRec, button, action}
});
```

Then in your page:

```go
{Label: "Run my action", URL: "my_action", Method: "client"}
```

### App-specific CSS
A `<style>` block in `Page.ExtraHeadHTML` alongside your `<script>`. Namespace your CSS classes (`ui-debate-*`, `ui-research-*`) to avoid collisions. Apply the class to a block via the `class` field on text blocks, or via the wrap div in your custom renderer.

---

## Routing + auth

Every app gets a sub-mux scoped to its prefix (`/<app-name>/`). Inside `Routes()` you register handlers on it with `T.HandleFunc`. The framework:

- Requires a valid session, and checks the viewer has access to your app (`AuthMiddleware`)
- Provides a per-user kvlite store (`T.DB`)
- Mounts the runtime CSS/JS at `/_ui/ui.css` and `/_ui/ui.js` (auto-injected by `Page.ServeHTTP`)
- Mounts your sub at `<prefix>/`

### What the framework does NOT do: identify the caller

`AuthMiddleware` answers *is this a logged-in user who may reach this app*: a
question about the app, not about the request. Who is asking is the handler's
to resolve, and the answer is not a gate, it is a key.

A JSON endpoint is a `core/jsonapi` handler, which resolves the caller before
it runs: `c.User` is who is asking and `c.UDB` is their own store.

```go
func (T *MyApp) handleRecord(c *jsonapi.Call) (any, error) {
    rec, found := loadRecord(c.UDB, c.R.PathValue("id"))   // the caller's own store
    if !found {
        return nil, jsonapi.NotFound("no such record")   // "not yours" and "no such thing" get one answer
    }
    return rec, nil
}
```

A handler that is not JSON (a page, a stream, a download) calls `RequireUser`
itself, as the first thing it does:

```go
func (T *MyApp) handleSend(w http.ResponseWriter, r *http.Request) {
    user, udb, ok := RequireUser(w, r, T.DB)   // resolve the caller
    if !ok {
        return
    }
    // ...
}
```

The common mistake is resolving the caller and then looking the record up in a
store that is not scoped to them. It looks like a check and serves every user's
data to every user. If you take
an id off a request (a session id, a record id, a run id), either load it from
that user's own store (`c.UDB`, or the `udb` `RequireUser` hands you), or
compare an owner field against the user before you touch it. Answer 404 rather
than 403 for someone else's id, so a probe cannot confirm it exists.

`apps/hello/agent.go` is the worked example: `demoStore`'s methods all take a
user, so a handler that forgets to resolve one does not compile.

A typical `Routes()`, with a path pattern where an id is part of the URL:

```go
func (T *MyApp) Routes() {
    T.HandleFunc("/", T.handlePage)
    T.HandleFunc("/api/records", jsonapi.Methods{
        "GET":  jsonapi.Get(T.DB, T.handleRecordsList),
        "POST": jsonapi.Post(T.DB, T.handleRecordSave),
    }.Serve)
    T.HandleFunc("/api/records/{id}", jsonapi.Methods{
        "GET":    jsonapi.Get(T.DB, T.handleRecord),
        "DELETE": jsonapi.Delete(T.DB, T.handleRecordDelete),
    }.Serve)
    T.HandleFunc("/api/send", T.handleSend) // SSE pipeline: not JSON, resolves the caller itself
}
```

The handler's body type says what the body is:

| Body type | Means | A request with no body |
|---|---|---|
| `struct{}` | no body; it is never read | runs |
| `*T` | optional | gets nil |
| `json.RawMessage` | the handler decodes it itself (a patch merged onto what is stored, two shapes) | gets nil |
| any other `T` | required | 400 |

A body that is there and malformed is always a 400. `jsonapi.MaxBody(n)`
raises or lowers the 1 MiB cap. Return
`jsonapi.BadRequest`, `NotFound`, `Forbidden` or `Status(code, msg)` to refuse
with a message the browser sees; any other error is logged and the browser is
told only that something failed.

An app on the `WebApp` escape hatch does the same in `RegisterRoutes(mux, prefix)`, building its sub-mux with `NewWebUI(T, prefix, AppUIAssets{})`.

---

## Sessions + SSE protocol (for pipeline apps)

Server emits framework-level events via `sse.SendNamed(name, payload)`. The runtime consumes:

| Event | Payload | Effect |
|---|---|---|
| `session` | `{id: "..."}` | Marks the session id; URL gets `?session=<id>` |
| `status` | `{text: "..."}` | Updates the status pill |
| `block` | `{id, type, title?, body?, class?, ...}` | Adds a new block; type picks the renderer |
| `chunk` | `{id, text}` | Appends text to a block's body |
| `chunk_replace` | `{id, body}` (or `{id, text}`) | Replaces a block's body |
| `block_meta` | `{id, ...}` | Per-block metadata update (custom shape per renderer) |
| `block_done` | `{id}` | Finalizes a block (mdToHTML pass + renderer's onDone) |
| `block_remove` | `{id}` | Drops a block from the transcript |
| `done` | `{}` | Pipeline finished; URL updates; sidebar refreshes |
| `error` | `{message: "..."}` | Surfaces as a status-pill error |

Your bridge struct keeps the mapping from your app's domain events (e.g., `direct_answer`, `framing_started`, `synthesis`) to these. See `private/research/chat_endpoints.go` for a worked example.

---

## Gotchas

### Registry init order
Anything you register via `window.uiRegister*` from raw `ExtraHeadHTML` runs *before* the runtime script at body-end has executed: `window.uiRegister*` won't be defined yet. Wrap registration in a `DOMContentLoaded` deferral, as the block renderer example above does (`ui.Head` does this for you).

The framework registers its own `DOMContentLoaded → mount()` handler from the runtime IIFE at body end. Yours registers from `<head>`, which fires first in handler order, so your registrations land before the panel mounts.

### Class hint on text blocks
For app-specific styling on a generic text block, pass `class` in the block payload:

```go
b.sse.SendNamed("block", map[string]interface{}{
    "id":    id,
    "type":  "text",
    "title": "Final report",
    "body":  e.Body,
    "class": "ui-my-final-report",
})
```

The runtime appends `class` to the wrap div. Your CSS in `ExtraHeadHTML` targets `.ui-my-final-report .ui-pl-block-body p { … }`.

### Don't mention apps in `core/ui/`
The shared runtime stays domain-agnostic; new code in `core/ui/` that mentions an app name is a bug. An `if (type === "verdict")` you want to add to `core/ui/runtime.go` belongs in a registered renderer in your app's package instead.

### CLI-mode hiding
Apps that only make sense in the dashboard (most of them) should NOT implement `core.CLIApp`. Without that marker, they're hidden from `gohort --help` and from CLI dispatch, with a "use serve" hint if anyone tries.

### Private (no-lead) apps
For apps handling sensitive data (servitor SSH probes, phantom messages) call `T.Private()` in `Init()`. Sets `NoLead` on the AppCore: any reference to `T.LeadChat()` / `T.LeadLLM` silently routes to the worker instead. Combine with `Private: true` on registered route stages so the admin UI can't accidentally route the app to a remote LLM.

### Email-shaped usernames
gohort usernames ARE email addresses. `AuthCurrentUser(r)` returns the username and that's a valid email recipient. Use this instead of asking the user for their email on every form.

### Avoid hardcoding paths
URLs in components are relative to the page that serves them. `Source: "api/records"` resolves to `<prefix>/api/records`. Don't hardcode `/myapp/api/records`: it breaks if the app gets remounted at a different prefix.

---

## Project structure conventions

```
apps/<name>/
├── <name>.go        # Agent struct, Init/Main, RegisterApp(init)
├── page.go          # handlePage, ui.Page declaration
├── web.go           # HTTP handler functions
├── web_assets.go    # (optional) app-specific CSS/JS payload as Go const
└── chat_endpoints.go # (optional, for pipeline apps) bridge + sessions handlers
```

Private apps live in `private/<name>/` with the same shape. Apps register themselves via `init()` and get loaded via blank imports in `agents.go` / `private.go`.

---

## Where to look for examples

| Example | What it shows |
|---|---|
| `apps/admin/page.go` | The cleanest reference for admin-style sections (Table + FormPanel + DisplayPanel). |
| `private/research/page.go` + `chat_endpoints.go` | Reference for a one-shot pipeline app with a bridge. |
| `private/debate/web_assets.go` | Reference for ExtraHeadHTML with `<style>` + `<script>` (custom block renderers + markdown extension + client action). |
| `private/blogger/page.go` | Reference for a multi-surface admin app (queue + rejected + schedule + settings + suggest panel). |

---

## When in doubt

- **Read an existing app** of the same shape and copy its structure.
- **Edit `core/ui/` only** to add a generic primitive; per-block class hints and the registries cover most customization.
- **A new knob on a primitive** is an optional field with a sensible default: never break existing apps.
