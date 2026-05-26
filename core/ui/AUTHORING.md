# Authoring a gohort App

## What this framework is for

gohort is a platform for assembling small AI-backed apps. The goal of the `core/ui` framework is to make it **simple for an app developer with no prior web experience to add new capabilities to gohort** by reusing primitives and structures already in place. You should not have to write HTML, CSS, or DOM-manipulation JavaScript to ship a working app. You write a Go struct, declare a page in terms of pre-built components (Table, FormPanel, ChatPanel, PipelinePanel, …), and the framework renders it.

Every primitive in `core/ui` is intentionally generic — *no* primitive knows what "debate" or "research" or "techwriter" means. When an app needs behavior beyond what the primitives offer, it plugs into one of four extension registries (block renderer, markdown extension, client action, `ExtraHeadHTML`) from its own package. This separation is enforced; see `CLAUDE.md` at the repo root for the rule and `scripts/hooks/pre-commit` for the guard.

The payoff: each new app pulls from a growing toolkit of reusable parts. You don't redesign the page chrome, the sidebar, the form fields, the chat layout, or the SSE pipeline — you reach for the parts that already exist. The framework stays generic *so that* the toolkit keeps compounding.

## App anatomy

A gohort "app" is a Go package that:

1. Defines a struct embedding `core.AppCore`
2. Implements the `core.Agent` interface
3. Registers itself with `core.RegisterApp` in `init()`
4. Mounts HTTP routes that serve a `core/ui.Page`

The framework handles routing, auth, sessions, SSE, LLM wiring, cost tracking, and the entire frontend. You write Go code that returns a declarative page; the framework renders it.

---

## TL;DR — minimal working app

```go
package hello

import (
    "encoding/json"
    "net/http"

    . "github.com/cmcoffee/gohort/core"
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
    T.HandleFunc("/api/echo", T.handleEcho)
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

func (T *HelloAgent) handleEcho(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    var body struct{ Name string }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{
        "message": "hello, " + body.Name,
    })
}
```

Add a blank import in `apps.go` to register it:

```go
import _ "github.com/cmcoffee/gohort/apps/hello"
```

Run `gohort serve :8080`. The Hello page is live at `/hello/`.

### What's happening here

- `RegisterApp` (in `init()`) hands the agent to the framework's registry.
- The Agent methods (`Name`/`Desc`/`SystemPrompt`/`Init`/`Main`) cover CLI/registration.
- The SimpleWebApp methods (`WebPath`/`WebName`/`WebDesc`/`Routes`) cover the dashboard. The framework creates a per-app sub-mux, calls `Routes()` against it, and mounts it at `prefix` with cost-tracking middleware — no explicit `NewWebUI`/`MountSubMux` plumbing.
- `T.HandleFunc(pattern, handler)` registers against that pre-wired sub-mux. Patterns are relative to the prefix.
- `ui.Page{...}.ServeHTTP(w, r)` renders the page from its declarative spec. You never write HTML in app code.
- The form has no `Source` (read endpoint) — it's a blank-state form. `PostURL` is the only write target. The field key is `Field` (not `Name`).

If you need direct mux/prefix control (custom access wrappers, alternate mount semantics), implement the older `WebApp` interface with `RegisterRoutes(mux, prefix)` instead. SimpleWebApp is the default; WebApp is the escape hatch.

---

## Concepts in 5 minutes

### App = Agent + Routes
An app is one struct that implements `core.Agent`. Its `RegisterRoutes` mounts HTTP handlers on a sub-mux. The framework gives it user auth, a per-user kvlite database (`T.DB`), and LLM sessions (`T.WorkerChat()`, optionally `T.LeadChat()`).

### Page = declarative tree of components
`ui.Page` is a Go struct describing the page. You don't write HTML or CSS. The framework's runtime JS (`/_ui/ui.js`) reads your page config and builds the DOM.

### Sections wrap Components
A `ui.Section` has a Title, Subtitle, and a Body (one Component). Common components: `Table`, `FormPanel`, `ChatPanel`, `PipelinePanel`, `DisplayPanel`, `Card`. List them in `Sections: []ui.Section{...}`.

### SSE for streaming, REST for the rest
Static data → JSON over plain HTTP. Live pipelines → SSE (`/api/send`, framework runtime handles the protocol). The `PipelinePanel` primitive does this for you — you write a *bridge* function that translates your app's events into framework SSE events.

### Apps stay in their lane
The shared runtime (`core/ui/`) knows nothing about debate, research, blogger, etc. Anything app-specific lives in the app's package — CSS, custom block renderers, markdown extensions. Apps inject them via `Page.ExtraHeadHTML`. This is enforced; new code in `core/ui/` that mentions an app name is a bug.

### Backwards-compatibility tiers
Stable, ordered most → least stable:

1. **The `Agent` interface** + `RegisterApp` + `NewWebUI` — frozen.
2. **`ui.Page` + the well-known components** (Table, FormPanel, ChatPanel, PipelinePanel, FormField types) — adds-only; field renames go through a deprecation pass.
3. **The extension registries** (block renderer, markdown extension, client action) — frozen signatures; new registries are additive.
4. **The runtime helpers exposed on `window`** (`uiEl`, `uiMdToHTML`, `uiRegister*`) — frozen names.
5. **CSS class names** — generic `ui-pl-*`, `ui-chat-*`, `ui-form-*` classes are stable; app-specific class names belong in the app's package.

---

## Pick a primitive

| Want to build… | Use |
|---|---|
| A list of records with row actions (edit / delete / view) | `ui.Table` + `RowAction` (often with `ui.Expand` for inline edit) |
| A settings form auto-saving on blur | `ui.FormPanel` |
| A labeled key-value display (read-only) | `ui.DisplayPanel` |
| A chat with sessions sidebar | `ui.ChatPanel` |
| A pipeline — submit a job, watch SSE blocks stream in, view past runs | `ui.PipelinePanel` (with a bridge that emits `block`/`chunk`/`status` events) |
| A live-watch page for an in-flight pipeline (separate from submit) | `ui.PipelineWatchPanel` |
| An LLM-backed suggestion list ("Suggest topics") | `ui.SuggestPanel` |
| A single rotatable API key | `ui.ApiKeyPanel` |
| A pickable chip cloud (apps assigned to a user, etc.) | `ui.ChipPicker` |
| A multi-line list editor (each row a rule) | `ui.FormField{Type: "rules"}` inside `FormPanel` |
| A compact tag-array editor (chips with × removers) | `ui.FormField{Type: "tags"}` inside `FormPanel` |
| A toggle (boolean) on a form | `ui.FormField{Type: "toggle"}` |
| A bar chart | `ui.BarChart` |
| An article editor with full markdown + image insertion | `ui.ArticleEditor` |
| A code editor with diff + history | `ui.CodeWriterPanel` |

If your shape doesn't fit any of these cleanly, you may need a new primitive. Prefer combining existing ones first — most app surfaces are some mix of Table + FormPanel + a chat-shaped flow.

---

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
                    ui.Expand("Edit", ui.FormPanel{
                        Source:  "api/records/{id}",
                        PostURL: "api/records",
                        Fields:  recordFields(),
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

User submits a topic → server runs a pipeline → blocks stream into a panel → final report saved. See `private/research/page.go`, `private/debate/page.go`, `private/blogger/page.go` (manual mode — deprecated but still illustrative).

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

- `handleChatSessionsList` — GET, returns the sidebar-list shape
- `handleChatSessionLoad` — GET, returns a saved session as `{blocks: [...]}`
- `handleChatSessionDelete` — DELETE
- `handleChatSend` — POST, runs the pipeline + streams SSE events through a bridge struct that translates pipeline events to framework events (`block`, `chunk`, `chunk_replace`, `block_done`, `status`, `done`, `error`)

### 3. Chat app

User-and-assistant message thread, optionally with tools and attachments. Use `ui.ChatPanel`. See `apps/chat/` for the reference.

### 4. Live-watch page

Tracking an in-flight pipeline that was kicked off elsewhere. Use `ui.PipelineWatchPanel`. See `private/blogger/page.go`'s `handleBloggerWatchPage` for an example.

---

## Customizing the UI (extension registries)

When a primitive doesn't fit and you need app-specific UI without changing core, use one of these. All are loaded via `Page.ExtraHeadHTML` — a Go string of HTML injected into the page `<head>`.

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

### Markdown extension
A post-processor that runs after `mdToHTML`'s base passes. Use it to add app-specific syntax (e.g., colorize a heading pattern unique to your domain).

```js
window.uiRegisterMarkdownExtension(function(html) {
  return html.replace(/<h2>(MyHeader)([^<]*)<\/h2>/g,
    '<h2 class="ui-my-header">$1$2</h2>');
});
```

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

Every app gets a sub-mux scoped to its prefix (`/<app-name>/`). Inside `RegisterRoutes` you mount handlers on the sub. The framework:

- Gates everything behind the user auth check (`RequireUser`)
- Provides a per-user kvlite store (`T.DB`)
- Mounts the runtime CSS/JS at `/_ui/ui.css` and `/_ui/ui.js` (auto-injected by `Page.ServeHTTP`)
- Mounts your sub at `<prefix>/`

A typical `RegisterRoutes`:

```go
func (T *MyApp) RegisterRoutes(mux *http.ServeMux, prefix string) {
    sub := NewWebUI(T, prefix, AppUIAssets{})
    sub.HandleFunc("/", T.handlePage)
    sub.HandleFunc("/api/records", T.handleRecordsList)
    sub.HandleFunc("/api/records/", T.handleRecordSingle)
    sub.HandleFunc("/api/send", T.handleSend) // SSE pipeline
}
```

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
Anything you register via `window.uiRegister*` from `ExtraHeadHTML` runs *before* the runtime script at body-end has executed — `window.uiRegister*` won't be defined yet. Always wrap registration in a `DOMContentLoaded` deferral:

```js
function register() {
  if (!window.uiRegisterBlockRenderer) return;
  // ... your registrations
}
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', register);
} else {
  register();
}
```

The framework registers its own `DOMContentLoaded → mount()` handler from the runtime IIFE at body end. Yours registers from `<head>`, which fires first in handler order — so your registrations land before the panel mounts.

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
The shared runtime stays domain-agnostic. If you find yourself wanting to add an `if (type === "verdict")` to `core/ui/runtime.go`, that's a sign — it should be a registered renderer in your app's package instead.

### CLI-mode hiding
Apps that only make sense in the dashboard (most of them) should NOT implement `core.CLIApp`. Without that marker, they're hidden from `gohort --help` and from CLI dispatch, with a friendly "use serve" hint if anyone tries.

### Private (no-lead) apps
For apps handling sensitive data (techwriter article bodies, servitor SSH probes, phantom messages) call `T.Private()` in `Init()`. Sets `NoLead` on the AppCore — any reference to `T.LeadChat()` / `T.LeadLLM` silently routes to the worker instead. Combine with `Private: true` on registered route stages so the admin UI can't accidentally route the app to a remote LLM.

### Email-shaped usernames
gohort usernames ARE email addresses. `AuthCurrentUser(r)` returns the username and that's a valid email recipient. Use this instead of asking the user for their email on every form.

### Avoid hardcoding paths
URLs in components are relative to the page that serves them. `Source: "api/records"` resolves to `<prefix>/api/records`. Don't hardcode `/myapp/api/records` — it breaks if the app gets remounted at a different prefix.

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

Private apps live in `private/<name>/` with the same shape. Apps register themselves via `init()` and get loaded via blank imports in `apps.go` / `private.go`.

---

## Where to look for examples

| Example | What it shows |
|---|---|
| `apps/admin/page.go` | The cleanest reference for admin-style sections (Table + FormPanel + DisplayPanel). |
| `private/research/page.go` + `chat_endpoints.go` | Reference for a one-shot pipeline app with a bridge. |
| `private/debate/web_assets.go` | Reference for ExtraHeadHTML with `<style>` + `<script>` (custom block renderers + markdown extension + client action). |
| `apps/phantom/mobile.go` | Reference for nested CRUD (per-user conversations with per-conv overrides). |
| `private/blogger/page.go` | Reference for a multi-surface admin app (queue + rejected + schedule + settings + suggest panel). |

---

## When in doubt

- **Read an existing app** of the same shape and copy its structure.
- **Avoid editing `core/ui/`** unless you're adding a generic primitive everyone benefits from.
- **App-specific styling lives in the app's package**, injected via `ExtraHeadHTML`.
- **Per-block class hints + the registries** cover most customization.
- **If you need to add a knob to a primitive**, do it as an optional field with a sensible default — never break existing apps.
