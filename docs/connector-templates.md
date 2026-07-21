# Connector Templates — design

**Status:** spec, pending lock. **Author:** design pass, 2026-07-21.

## Problem

We build backend integrations three different ways today, each ad hoc:

1. **Presets** (`rest_image` comfyui/a1111, `rest_messaging` teams/slack) fill spec
   *defaults*, as hardcoded per-kind maps in `core`.
2. **The ComfyUI config panel** is bespoke admin JS: which fields to show, the
   node-map table, the auto-detect — all hardcoded per backend.
3. **The deferred "instance-vars + collect-on-import" plan** wants connectors to
   *declare* their per-instance values so import/catalog can prompt for them.

Adding the next backend (Flux, a hosted SD endpoint, a proper A1111 config panel,
Teams/Slack config) means writing another preset **and** another admin panel. The
config surface — "how to map out the values, what the Configure button does" —
isn't reusable.

## The abstraction

A **connector template** is a declarative integration built *on* a connector kind.
It owns the whole config surface: the fields, how they map to the spec, and
(optionally) how to auto-detect them. A **generic renderer** builds the Add and
Configure panels from the declaration — no per-backend admin JS.

A template is **not a new kind.** Kinds (`rest_image`, `rest_messaging`) are the
*runtime mechanism* (a Go `ConnectorHandler`). Templates are the *authored,
user-facing integration* with its config surface. Many templates ride one kind:
comfyui, a1111, flux, hosted-SD are all `rest_image`.

```go
// core/connector_template.go
type ConnectorTemplate struct {
    Name        string // "comfyui" — stable id, stored on the connector for provenance
    Label       string // "ComfyUI"
    Category    string // "Image generation" — groups the Add menu / catalog
    Description string
    Kind        string // base connector kind it materializes ("rest_image")

    Fields []TemplateField

    // BuildSpec maps collected field values → the connector Spec ("how to map").
    BuildSpec func(vals map[string]any) (json.RawMessage, []string, error)
    // ReadValues is the inverse — prefill the Configure panel from an existing spec.
    ReadValues func(spec json.RawMessage) map[string]any
    // Detect (optional) auto-fills fields from others, e.g. ComfyUI parses the
    // pasted workflow → the node map. Surfaced as a "Detect" button.
    Detect func(vals map[string]any) (map[string]any, []string, error)
}

type TemplateField struct {
    Key      string // key in the values map
    Label    string
    Help     string
    Type     string // text | textarea | number | bool | select | credential
    Group    string // section heading: "Connection" | "Node mapping" | "Defaults" | "Style"
    Options  []string
    Default  any
    Advanced bool   // collapsed by default
}
```

**Field types stay deliberately small.** The ComfyUI node map is *not* a special
"nodemap" widget — it's just a group of `text` fields (`prompt_nodes`,
`negative_nodes`, …) under a "Node mapping" group, auto-filled by `Detect`. The
only richness is grouping + the optional Detect button. This keeps the renderer a
small form engine, not a UI framework.

Registry mirrors `RegisterConnectorKind`:

```go
func RegisterConnectorTemplate(t ConnectorTemplate)
func ConnectorTemplates() []ConnectorTemplate
func ConnectorTemplate(name string) (ConnectorTemplate, bool)
```

## The two backends, as templates

**comfyui** (`rest_image`) — the hardest one, proving the shape:
- Fields: `base_url` (text, Connection), `workflow` (textarea, Connection),
  the node map (`prompt_nodes`/`negative_nodes`/`text_keys`/`width_nodes`/
  `height_nodes`/`steps_nodes`/`seed_nodes`/`seed_key`/`output_node`, text, Node
  mapping), `default_width`/`default_height`/`default_steps` (number, Defaults),
  `prompt_suffix` (textarea, Style), `credential` (credential, advanced).
- `Detect(vals)` = `ApplyComfyWorkflow(vals.workflow)` → the node map + size defaults.
- `BuildSpec(vals)` = `NewComfyImageSpec`-equivalent: preset endpoints from
  `base_url`, then `ComfyWorkflow` + `ComfyMap` + defaults + suffix.
- `ReadValues(spec)` = today's `comfyCfgFromSpec`.

**a1111** (`rest_image`) — falls out for free:
- Fields: `base_url` (text), `default_width`/`default_height`/`default_steps`
  (number), `prompt_suffix` (textarea), `credential` (credential, advanced).
- No `Detect`.
- `BuildSpec` = `ApplyRestImagePreset("a1111", …)` + defaults + suffix.

A future **teams** (`rest_messaging`) is just: fields `credential`, `team_id`,
`channel_id`; `BuildSpec` = `ApplyRestMessagingPreset("teams", vars)`. No new code
paths.

## Generic renderer

One admin panel + generic endpoints replace `add_image_backend` and
`configure_comfyui`:

- `GET /api/connector-templates` → `[{name,label,category,description}]` — powers a
  grouped "Add…" menu and the catalog.
- `GET /api/connector-templates/{name}` → the field schema.
- `POST /api/connector-templates/{name}/detect` → `Detect(vals)` → auto-filled vals.
- `POST /api/connector-templates/{name}` (optional `?name=` to edit) →
  `BuildSpec(vals)` → `SaveConnector` (+ `ApproveConnector` on create, admin is the
  approver).
- Configure: `GET /api/connectors/{name}` uses the connector's stored `Template`
  to pick the template, `ReadValues(spec)` to prefill.

The client action is **one** generic form renderer: read schema → render fields by
group → show a Detect button when the template has `Detect` → Save. It replaces the
two bespoke ComfyUI/add panels with a single data-driven one.

## Provenance + import (Stage 2)

Add `Template string` to the `Connector` record (which template authored it).
Then:
- **Catalog** lists templates by `Category`; installing renders the same generic
  form → draft → approve.
- **Bundle import** reads the connector's `Template` → prompts for the template's
  fields that are per-instance (everything `ReadValues` surfaces that isn't
  derivable) → `BuildSpec` → draft. This *is* the deferred instance-vars plan,
  delivered by the same declaration.

## Where things live (generalization discipline)

- `ConnectorTemplate` type + registry: **core** (like `RegisterConnectorKind`).
- Template *declarations* (comfyui, a1111): **core** next to their preset/kind, OR
  the owning app — a declaration is data, not a leak.
- `Detect`/`BuildSpec`/`ReadValues` hooks: **core** functions the template points
  at (`ApplyComfyWorkflow` etc.) — no per-backend UI.
- The generic renderer (endpoints + one client action): **admin app**, domain-
  agnostic — it renders *any* template's schema. No ComfyUI-specific JS survives.

Net: the bespoke ComfyUI panel becomes the first template; the admin app keeps one
generic renderer instead of a panel per backend.

## Staging

- **Stage 1 (this build):** `ConnectorTemplate` type + registry + generic renderer
  (endpoints + one client action). Re-express **comfyui** and **a1111** as
  templates. Delete the bespoke `add_image_backend` + `configure_comfyui` JS and
  the `/api/image-gen/backend` + `/api/image-gen/comfy` handlers, routing them
  through the generic path. Behavior parity, minus the hardcoding.
- **Stage 2:** `Connector.Template` provenance; wire the catalog + bundle-import
  preview to collect template fields (lands the instance-vars plan).
- **Stage 3:** more templates as pure declarations (Flux, hosted SD, teams/slack
  config) — no new renderer code.

## Open questions / risks

1. **Over-abstraction.** Keep field types to what comfyui + a1111 + one messaging
   case actually need (text/textarea/number/bool/select/credential). Resist a
   generic form engine. If a backend needs a widget the schema can't express, that
   backend keeps a bespoke panel — the template registry doesn't forbid it.
2. **Configure vs raw Edit spec.** Templates own the friendly panel; Edit spec
   stays the raw escape hatch (already comfy-workflow-aware).
3. **Reverse mapping.** `ReadValues` must round-trip `BuildSpec` for Configure to
   prefill correctly — covered by a per-template round-trip test.
4. **Migration.** Existing connectors created before `Template` provenance: infer
   the template from kind + shape (has `comfy_workflow` → comfyui), or leave them
   on Edit spec until re-saved through the panel.
