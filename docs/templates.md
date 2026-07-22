# Templates: connectors, tools, and the const→data path

**Status:** vision spec, pending lock (2026-07-21). Extends
`docs/connector-templates.md` (connector templates, Stages 1–2 shipped).

## Goal

Adding a new connector or tool should be as easy as **declaring "what options are
needed"** — nothing more for the common case. The image generator is the model:

- **ComfyUI** needs: a URL, a workflow, a node map, default size/steps, a suffix.
- **Automatic1111** needs: a URL, default size/steps, a suffix.

You "drop in" that declaration and the backend exists. Hardcode declarations as
**const values now**; keep them shaped so they can become **editable/shareable
data later** without a redesign.

## The model: declaration (data) + strategy (code)

A template is two separable parts:

- **Declaration** — *what options are needed*: the field list, groups, help,
  defaults. Pure data. This is what you write for a new integration.
- **Strategy** — *the logic that can't be data*: how options map to the artifact
  (`BuildSpec`), and optional `Detect` (auto-fill). Named + reusable.

```go
Template{
  Name: "a1111", Label: "Automatic1111", Target: "connector", Kind: "rest_image",
  Options: []Field{ {Key:"base_url", ...}, {Key:"default_steps", Type:"number"}, ... },
  Strategy: "rest_image_simple",          // <- named, reusable code hook
  // Detect: "" (none)
}
```

A new integration = **a declaration + the name of a strategy**. If an existing
strategy fits, the declaration is *all* you write:

- **a1111** → declaration only; reuses a shared `rest_image_simple` strategy.
- **comfyui** → declaration + the `comfyui` strategy (graph surgery + a `Detect`
  that parses the workflow into the node map).
- **Flux / hosted SD** → another declaration, reusing a strategy.

The only time you write *code* is a genuinely new **shape** (a new strategy). New
*instances* of a known shape are declarations.

> This is a refinement of what shipped: today `comfyuiTemplate()`/`a1111Template()`
> each carry their own `BuildSpec`/`Detect` funcs. Stage A factors those into
> **named strategies** so the declaration is the const, and the strategy is the
> only code — and only when the shape is new.

## Const now, data later

The declaration is data-shaped, so *where* it lives is a storage choice, not a
redesign:

- **Now:** declarations are Go values registered at startup (const-like). Adding
  one = a small declaration + rebuild.
- **Later:** declarations move to **DB records** — admin-curated, editable without
  a rebuild, and shareable through the bundle export/import we already have.
  Records reference a **strategy by name**. The engine + renderer already treat
  declarations uniformly, so nothing else changes.
- **Always code:** the strategies (and `Detect` hooks) stay in the binary. A data
  record borrows logic by naming a strategy; it never *contains* logic. Forcing
  ComfyUI's graph surgery into a data record would mean inventing a mini-language —
  worse than a Go function. So: **logic is code, declarations are data.**

## Extending to tools (the second target)

Same model, different `Target`/output:

- A **tool template** declares its options; its strategy emits a tool definition
  (an `api`/`toolbox` `tool_def`) instead of a connector spec.
- The **Detect analog is OpenAPI/Swagger import**: paste a spec → auto-generate the
  toolbox actions (endpoints, params, credential) — exactly like ComfyUI's
  workflow → node map. This is the single biggest reason to extend the pattern.
- Common tool templates: GitHub, Jira, a generic authenticated REST call, a
  webhook poster — each a declaration + a strategy (`rest_tool`, `openapi_tool`).

**Governance is unchanged.** A template makes *authoring* easier; it grants no new
power. A tool authored from a template still passes credential binding, tool
scope, the verify gate, and approval — same as a hand-authored `tool_def`. See
`docs/secured-credential-tool-binding.md`.

## Who maintains what

- **Devs** maintain **strategies** (code) and any declaration that must ship.
- **Admins** maintain **data declarations** (curate, edit, share) once the DB
  registry exists. A user-defined template is a config surface + a strategy
  reference — admin-curated for the same review reason tools are.
- **Users** *use* templates (pick + fill, or the Builder picks from intent). They
  don't author strategies.

## Staging

- **Stage A — declaration/strategy split (connectors). SHIPPED.** `ConnectorTemplate`
  is now pure data (Name/Label/Category/Kind/`Strategy`/`Params`/`Fields`, all
  JSON-tagged — serializable, ready for Stage C). `ConnectorStrategy` +
  `RegisterConnectorStrategy` hold the code; the template resolves its strategy via
  methods (`BuildSpec`/`ReadValues`/`Detect`/`HasDetect`), so the renderer is
  untouched. Strategies: `rest_image_preset` (generic, preset-driven) + `comfyui`
  (workflow/map/Detect). **a1111 is a pure declaration** (Strategy
  `rest_image_preset`, Params `{preset:a1111}`, no code); a test registers a new
  `sdnext` backend as a declaration-only value and proves it builds + round-trips.
- **Stage B1 — tool-target engine + rest_tool. SHIPPED (uncommitted).** The engine
  is now generic: `Template` + `Strategy` are the real types with a `Target`
  (`connector` | `tool`); registries are keyed by (target, name), so the same name
  can exist per target. `ConnectorTemplate`/`ConnectorStrategy` are aliases and the
  connector funcs are thin wrappers — shipped connector code is untouched. Added a
  `rest_tool` strategy (declaration → an api-mode `TempTool`; args DERIVED from the
  `{placeholders}` in URL/body; URL/method/body from the form or the declaration's
  Params) + two declarations: `rest_call` (user-specified) and `github_issue`
  (fixed-shape, credential-only). Tests prove both builds, the derived params,
  ReadValues round-trip, and target isolation. Governance untouched — the artifact
  is a `TempTool` that still goes through the tool approval flow.
- **Stage B2 — admin tool surface. SHIPPED (uncommitted).** The generic renderer
  is now target-aware (`uiTemplateForm` derives its endpoints from `cfg.target`).
  Tool endpoints (`/api/tool-templates`, `/api/tool-template`, `/api/tool-detect`,
  `/api/tool-config`) mirror the connector ones but the save routes to
  `AdminPersistTempTool` (the tool governance), not `SaveConnector`. Admin → Global
  Tools → **“Add tool from template…”** → pick `rest_call`/`github_issue` → fill →
  persists a `TempTool` for the acting admin. One renderer, two targets.
- **Stage B3 — Builder integration + tool provenance. SHIPPED (uncommitted).**
  `TempTool.Template` records the authoring template (the tool-side analog of
  `Connector.Template`), set by both the admin tool-config and the Builder. New
  Builder tool **`tool_template`** (orchestrate, wired in `builder_tools.go`):
  `list` surfaces the templates + their fields so the LLM knows what to fill;
  `create` builds a `TempTool` from a template and attaches it to the focused agent
  — governed exactly like `add_tool` (agent-owned, verifiable via `test_args`). So
  the LLM authors a tool by picking a template + filling options instead of
  hand-crafting a `tool_def`.
- **Stage B4 — OpenAPI importer. SHIPPED (uncommitted).** `openapi_tool` strategy
  (`core/template_tool_openapi.go`): parses an OpenAPI 3.x (`servers[]`) or Swagger
  2.0 (`host`/`basePath`/`schemes`) spec → a **toolbox** `TempTool` (one action per
  operation); params derived from path/query, a `requestBody` becomes a `body`
  arg; a path `filter`; `Detect` previews the base URL + operation list. Because
  it's just another tool template, it's **already reachable** through the B2/B3
  surfaces (admin "Add tool from template…" and the Builder `tool_template`) — no
  new wiring; the Detect button appears in the generic renderer. Governance
  unchanged (the toolbox goes through the tool approval flow).
- **Stage B5 — NEXT (smaller).** (a) A user-facing tools/Gateways entry so a user
  reaches tool templates without the Builder; (b) Configure-existing-tool using the
  `TempTool.Template` provenance (field exists; the edit flow isn't wired).
- **Stage C — data declarations.** DB registry for declarations (strategy-by-name),
  admin-curated, bundle-shareable, catalog-listed. Migrates the const declarations
  with no engine change.

## Open questions

1. **One registry or per-target?** Likely one `Template` type with a `Target`
   (`connector` | `tool`) and a strategy registry keyed by `(target, name)`, so the
   renderer stays single and generic.
2. **Strategy signature.** `BuildSpec(vals) → artifact` differs by target
   (connector spec vs tool def). Keep the return `json.RawMessage` + a target the
   caller routes on, so one renderer serves both.
3. **Forward-compat still holds** — `MergeSpec` on save preserves unknown fields
   for both targets (a newer strategy's extra output survives an older reader).
