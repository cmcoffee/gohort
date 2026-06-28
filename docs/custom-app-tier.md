# Custom App Tier — sandboxed LLM-written apps + primitive API

Status: **design / target** (not built). The declarative app tier (app_def →
`core/ui` components → `apps/customapps`) is the default and stays so. This doc
sketches a SECOND tier for when an app needs a shape the primitives can't
express: the LLM writes a self-contained page directly, served sandboxed, wired
to gohort only through a small, scoped primitive API.

## Why a second tier (and why not a pivot)

The declarative tier composes pre-built primitives (`Table`, `FormPanel`,
`WorkbenchPanel`, …). Its wall: a genuinely new shape needs a new primitive added
to `core/ui` by hand. A custom tier dissolves that wall — the LLM writes the page
instead of waiting for a primitive.

But declarative stays the DEFAULT because composed apps get, for free and
consistently: theming, auth, the data store, mobile layout, and **live framework
upgrades** (e.g. a spacing or security fix improves every declarative app on
reload — a frozen custom page never gets that). So:

- **Declarative** = default. Consistent, safe, self-upgrading. Most apps.
- **Custom** = escape hatch. Unlimited shapes, sandboxed, frozen code.

The leverage is NOT "let the LLM write HTML" (models are already good at that).
It's the **primitives**: a custom page is only useful if it's *connected* (data
+ SSE) and *consistent* (theme). The same primitives also sharpen the declarative
runtime, so the work isn't tier-specific.

## The non-negotiable: containment

LLM-written JS in gohort's own origin is XSS-by-design — worse here because
channels/phantom expose agents to untrusted outside messages (prompt injection
could steer what the page contains). So the custom page is ALWAYS untrusted:

- Served in a **sandboxed iframe**: `sandbox="allow-scripts"` (NOT
  `allow-same-origin`) → an opaque origin with no gohort cookies, no parent DOM,
  no shared storage.
- A strict **CSP** on the iframe document: `default-src 'none'; script-src
  'unsafe-inline'; connect-src 'none'`. The page literally cannot make network
  calls — every privileged action goes through the bridge.
- The **outer page (broker)**, on the gohort origin, holds the real session. It
  receives bridge calls over `postMessage`, enforces grants + scoping, performs
  the actual fetch to `customapps` endpoints, and relays results back.

The untrusted iframe never holds credentials or unscoped access. That property is
what lets us say "the LLM can write literally anything" safely.

```
┌ gohort origin ─────────────────┐     ┌ sandboxed iframe (opaque origin) ┐
│ broker page                    │     │  LLM-written HTML/CSS/JS         │
│  - real session/cookies        │◄───►│  window.gohort.{data,sse,theme} │
│  - enforces AppGrants + scope  │ pm  │  (no cookies, no net, no parent) │
│  - fetches customapps endpoints│     │                                  │
└────────────────────────────────┘     └──────────────────────────────────┘
        │ scoped fetch
        ▼
  /custom/<slug>/records, /record, /chat/* (existing customapps endpoints)
```

## AppSpec extension

```go
type AppSpec struct {
    // ... existing: Slug, Name, Owner, AgentID, Page, RecordKey, BodyField ...
    Kind   string    // "" | "declarative" (default) | "custom"
    HTML   string    // custom: the page the LLM wrote (head + body)
    Grants AppGrants // what the custom page may reach
}

type AppGrants struct {
    Data  bool     // gohort.data — this app's record store (scoped to owner+slug)
    Agent string   // bound agent id → gohort.sse.chat targets it
    SSE   []string // extra event channels allowed (e.g. "monitor:<id>")
    Net   []string // external fetch allowlist (empty = none; relaxes CSP connect-src)
}
```

Declarative apps ignore `HTML`/`Grants`; custom apps ignore `Page`. The record
store + chat endpoints are shared by both tiers.

## The primitive API (`window.gohort`, injected into the iframe)

A small bridge shim the iframe loads. Every method is a `postMessage` round-trip
to the broker, which scopes + enforces. The page never sees a URL or a cookie.

### `gohort.data` — the record store (scoped to this app)

```js
gohort.data.list()              // → Promise<record[]>
gohort.data.get(id)             // → Promise<record>
gohort.data.create(fields)      // → Promise<record>  (POST records, allocates id)
gohort.data.update(id, fields)  // → Promise<record>  (upsert)
gohort.data.delete(id)          // → Promise<void>
gohort.data.onChange(cb)        // local writes + co-author writes fire this
```

Wraps the existing `records` / `record` endpoints. The broker injects owner+slug;
a custom page cannot address another app's data.

### `gohort.sse` — live streams

```js
// Stream a reply from the app's bound agent (wraps chat/send SSE).
gohort.sse.chat(message, {
  onChunk(text), onTool(call), onResult(r), onDone(final), onError(e)
}) // → { cancel() }

// Generic event subscription — only channels named in Grants.SSE.
gohort.sse.subscribe(channel, onEvent) // → { unsubscribe() }
```

The broker owns the `EventSource`/fetch-stream and relays parsed events via
`postMessage`; the iframe never holds the raw connection. This is the primitive
the declarative `ChatPanel`/`AgentLoopPanel` would also be refactored onto.

### `gohort.theme` — consistency without hardcoding

```js
gohort.theme.tokens()    // → { bg0, bg1, bg2, text, textHi, accent, danger, ... }
gohort.theme.css()       // → a <style> string to drop in (or auto-injected)
gohort.theme.onChange(cb)
```

The broker auto-injects the active theme's token CSS into the iframe, so a custom
page uses `var(--accent)` etc. and looks native + follows theme switches.

### `gohort.ui` — optional later convenience

```js
gohort.ui.toast(msg)
gohort.ui.confirm(msg)   // → Promise<bool>, styled by the parent
gohort.ui.modal(node)    // routed to the parent for consistent chrome
```

Keeps custom pages from reinventing dialogs. Optional; ship after the core three.

## Authoring (app_def)

```
app_def(action="create", kind="custom",
        name="…", agent_id="…",
        html="<!doctype html>…",
        grants={ data: true, agent: "<id>", net: [] })
```

Builder writes the HTML (its strength), declares grants. Guidance: use
`window.gohort.data/.sse/.theme`; do NOT hardcode storage or colors; you have no
direct network — everything goes through the bridge. Same anti-pattern rule as
the workbench agent: never improvise a private store; the app's record store is
the data layer.

## Reused vs new

Reused: `customapps` host + `AppSpec` storage, record CRUD endpoints, `chat/*`
SSE (`PublicHandle*`), theme tokens, `app_def`.

New: the broker page + sandboxed iframe, the `gohort.*` bridge shim, AppSpec
`Kind`/`HTML`/`Grants`, the CSP/sandbox wiring, the `app_def` custom kind.

## Sequencing

1. **Lock the declarative workbench** (data/SSE/agent plumbing proven end to end).
2. **Extract `gohort.data` / `gohort.sse` / `gohort.theme`** as a clean client lib
   — usable by the declarative runtime too (refactor `ChatPanel` onto `gohort.sse`).
3. **Broker + sandboxed iframe + bridge** — the containment layer.
4. **`app_def` custom kind + Builder guidance.**
5. **Later:** `gohort.ui` helpers, granular grants, per-app CSP tuning, a
   "re-generate with Builder" path for frozen custom apps.

## Open questions / tradeoffs

- **Origin isolation:** `srcdoc` + `sandbox` (no `allow-same-origin`) already
  yields an opaque origin (strong). A distinct subdomain (e.g. a per-app token
  origin) is belt-and-suspenders if we want hard storage partitioning.
- **Frozen code:** custom apps don't inherit framework upgrades. Offer a
  regenerate path; surface a "built on version X" marker.
- **Mobile:** declarative gets responsive layout free; a custom page owns its own
  layout and may not be responsive. Document as a tier tradeoff.
- **Cost/perf:** every `gohort.data` call is a postMessage + a scoped fetch; fine
  for app interactions, not for tight loops. The `onChange`/SSE paths cover live
  updates without polling.
