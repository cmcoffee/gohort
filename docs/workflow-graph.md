# Workflow graph — one picture for machines and pipelines

Status: **G1 + G2 built** (v0.6.086), unverified in a browser. Decision locked: **the renderer takes
a node/edge adapter, not a `MachineDef`**.

Landed: `core/workflow_graph.go` (types, layered layout, SVG renderer), `core/machine_graph.go` (the
machine adapter + `MachineCursor.Overlay`), `MachineCursor.Log` / `ChatSession.MachineLog` (the
structural transition trail the overlay reads), `GET /orchestrate/api/machines/{id}/graph`, and the diagram in
the Machines modal. Tests in `core/workflow_graph_test.go`.

Two things the drawing got wrong until the output was actually looked at, both now fixed and pinned
by tests: a resident phase's self-loop bowed outward straight through the neighbouring node (it is
now a badge inside the node's own corner, since a self-loop carries no routing information that
needs space), and a router's dashed run-time edge plus its static fallback to the same target drew
on identical curves with their labels stacked (they now merge into one arrow stating both). Neither
was reachable by reasoning about the code.

The renamed types are `WorkflowNode` / `WorkflowEdge` / `WorkflowOverlay`: `GraphEdge` was already
taken by the knowledge graph.

## Why

A machine is cyclic by construction. `guard_to` sends you backward, `next_from` routes sideways, a
resident phase self-loops every turn, and `change_phase` can jump from any node to any other. The
list of phase cards an editor would naturally produce is not a neutral presentation of that — it is
a wrong one, because it implies an order the machine does not have.

Pipelines are the milder case but the same kind of object: `branch` / `skip_to` are edges, `loop`
bodies are subgraphs, `fan_over` is a node that expands. They read top-to-bottom honestly today,
which is why nobody has missed a picture. That is an argument about urgency, not about kind.

The second reason, and the better one: **a graph is where a running conversation becomes legible**.
The ⚠ trail already records every transition, guard verdict, and routing fallback, in order, as
sentences. Draw the same information on the structure it happened in — this node is where you are,
these edges have fired, that one has never fired once — and "why is this agent behaving like that"
stops being a reading exercise.

## Decisions

1. **The renderer takes an adapter, not a def.** A `WorkflowGraph` of nodes and edges, with
   `MachineDef` and `PipelineDef` each supplying a small function that produces one. Machines get
   the picture first because they need it most, but the renderer never learns what a phase is.
   Retrofitting this after a machine-shaped renderer exists is the expensive version, and
   `core/ui/` has burned time on exactly this leak twice (see CLAUDE.md).
2. **Server-side SVG, not a JS graph library.** Rendered in Go from the adapter, returned as an
   image. Deterministic, unit-testable the way the rest of the machine work has been, no external
   dependency, and no browser-verification risk — which is currently the weakest link in this whole
   feature (the phase pill and the Machines modal are both unverified live).
3. **Read-only first.** Interactive editing is a separate, later decision. Authoring already works
   through the `machine` tool, and the comprehension problem is worth solving on its own.
4. **Layout is layered, not force-directed.** Deterministic output matters more than beauty: the
   same def must produce the same picture every time, or a diff of two renders is noise.

## The adapter

```go
// core/workflow_graph.go

type WorkflowNode struct {
	ID    string   // stable handle; the phase or stage name
	Label string   // what to draw
	Note  string   // one line under the label (Desc), optional
	Kind  string   // "entry" | "step" | "rest" | "exit"
	Tags  []string // small annotations: "lead", "tools: 3", "guarded"
}

type WorkflowEdge struct {
	From, To string
	Label    string // "next_from: target", "guard", "on: needs_more"
	Style    string // "solid" | "dashed" | "back"
	Note     string // hover text
}

type WorkflowGraph struct {
	Title string
	Nodes []WorkflowNode
	Edges []WorkflowEdge
	Entry string // node ID a run/session starts at
}
```

`Kind` is the one field worth arguing about, and it is deliberately NOT `resident bool`. The
renderer needs to know which nodes *hold* (draw them heavier, they are places) and which nodes
*pass through* (draw them lighter, they are steps). A pipeline's stages are all `step`; its terminal
stage is `exit`. A machine's resident phases are `rest`. Neither def leaks its vocabulary.

Adapters live with their defs:

```go
func (d MachineDef) Graph() WorkflowGraph   // core/machine_graph.go
func (d PipelineDef) Graph() WorkflowGraph  // core/pipeline_graph.go
```

### What a machine's adapter emits

- One node per phase. `rest` for resident, `step` for transient, `entry` marks `StartPhase()`.
- Solid edge for `Next`.
- Dashed edge per phase named in a `NextFrom` field's plausible targets — but the values are
  run-time, so the honest rendering is **one dashed edge from the router to every phase**, labeled
  with the field name. Drawing only the phases mentioned in a `Desc` would be a guess.
- A `back`-styled edge for each `Guard` → `GuardTo`, labeled with a truncated guard condition.
- A self-loop marker on a resident phase with no `Next` (it stays), and a solid edge when it has one
  (the one-beat handoff). As built the self-loop is a badge INSIDE the node, not a loop hanging off
  its side — see the note at the top.
- `change_phase` is NOT drawn. It connects every node to every other, so drawing it produces a
  complete graph and destroys the picture. It belongs in the legend as a sentence: *any phase can
  move to any other via change_phase.*

That last one is the layout decision that matters most, and it is the kind of thing that only shows
up once you try to draw it.

## Runtime overlay

The same renderer, given a `MachineCursor` (its `Log` is the structural trail; see the G2 note in
Staging for why not the diagnostics prose):

- The current node drawn as current.
- Edges that have fired this session drawn solid-highlighted; edges never taken left faint.
- A count on repeat-fired edges (a guard that has tripped four times is the shape of a machine whose
  resident phase is scoped too narrowly).

Served as `GET /orchestrate/api/machines/{id}/graph?session=<id>` — no session gives the plain structure. This
is the piece that makes the graph a debugging surface rather than documentation, and it should ship
in the same pass, not later: the structural render alone is a picture of something you could already
read from the def.

## Surfaces

- **The Machines modal** — the machine's structure, next to its row. It already lists phase names as
  `a → b → c`, which is the lie this replaces.
- **The chat toolbar** — the running session's graph, next to the ⚠ trail. Same modal shell.
- **`GET /orchestrate/api/machines/{id}/graph`** — `image/svg+xml`, so it can be linked, saved, or dropped into
  a doc.

## Staging

| Stage | Scope |
|---|---|
| **G1** ✅ | `WorkflowGraph` + `MachineDef.Graph()` + the SVG renderer + `/graph`. |
| **G2** ✅ | Runtime overlay. It reads `MachineCursor.Log`, NOT the diagnostics trail as originally sketched: "which edges did this take" is a structural question, and parsing framework-authored prose would break silently the first time someone reworded a message. |
| **G3** | `PipelineDef.Graph()` — proves the adapter, and pipelines get a picture for free. |
| **G4** | Interactive editing, only if G1-G3 show the layout holds up. Not committed to. |

## Open

- **Layout algorithm.** Layered (rank by distance from entry, resolve cycles by breaking back-edges)
  is the obvious choice and is ~150 lines. Worth checking whether a machine with four phases even
  needs ranking, or whether a fixed row with curved back-edges reads better at this size. Prototype
  both on the Triage example before committing.
- ~~**Node budget.**~~ Resolved by the `viewBox`: the SVG carries its natural size and the modal
  scrolls it horizontally. No refusal needed, and a large machine stays readable rather than being
  shrunk to fit.
- **Does the pipeline adapter want subgraphs?** A `loop` body is a nested stage list. Flattening it
  with a back-edge is probably right and definitely simpler; a real subgraph box is the correct
  drawing. Defer to G3, where it is a real question rather than a hypothetical.
- **Theming.** The SVG has to read on both light and dark. `currentColor` plus CSS variables in the
  markup rather than baked hex, matching how the rest of the UI themes.

## Not doing

A drag-and-drop editor before a machine has run a live conversation. St4 of
[agent-machines.md](agent-machines.md) — porting Builder's intake-to-build flow onto a machine — is
explicitly allowed to send the phase model back for changes, and building an editor against a schema
that may still move is the expensive ordering.
