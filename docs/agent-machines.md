# Machines — session-resident phase state for an agent

Status: **St1-St3 built** (v0.6.087). First live run confirmed 2026-08-13: a machine authored through the `machine` tool, attached, and driving a real conversation. The guard, `change_phase`, and the browser surfaces (pill, diagram, editor) are still unconfirmed individually. Decision locked: **a
Machine replaces the agent's brain** (see Decisions).

Landed in St1: `core/machine_def.go` (recipe + validator + storage + export/import),
`core/machine.go` (cursor, driver, templating, pinned block),
`apps/orchestrate/machine.go` (turn wiring), plus `AgentRecord.Machine` and the three
`ChatSession` cursor fields. St2 adds `core/machine_guard.go`, `AppCore.ChangePhase`, and the
`change_phase` tool in `apps/orchestrate/machine.go`. Tests: `core/machine_test.go`,
`apps/orchestrate/machine_test.go`.

St3 adds `apps/orchestrate/machines_http.go` (CRUD/import/export + the phase-pill endpoint),
`apps/orchestrate/machine_def_tool.go` (the `machine` grouped tool), the picker on the agent editor
(`machineSelectField`), and `AgentLoopPanel.StatusURL` in core/ui. Where the built shape differs
from the original spec, the difference is called out inline under **St1 note** / **St2 note** /
**St3 note**.

## Using one

**From chat** (the intended path): ask Builder for one. The `machine` tool authors it and
`attach_to_agents` points an agent at it in the same call. `machine(action="help")` prints the full
phase spec.

**From the agent editor**: the Phase machine picker sits under Persona. It renders only when you
have at least one machine — a select whose only option is None teaches nothing.

**Over HTTP**, if you would rather write the JSON yourself:

```sh
# 1. Create the machine. The response carries its id.
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/machines \
  -H 'Content-Type: application/json' -d '{
    "name": "Triage",
    "start": "decompose",
    "phases": [
      {"name": "decompose", "desc": "Split the question up.",
       "prompt": "Break the user request below into its parts.\n\n{input}",
       "next": "route",
       "output": [{"name": "parts", "type": "list", "desc": "the distinct questions being asked"}]},

      {"name": "route", "desc": "Pick a lane.",
       "prompt": "Given these parts:\n{state:decompose.parts}\n\nPick the phase that should answer.",
       "next_from": "target", "next": "answer",
       "output": [{"name": "target", "type": "string", "desc": "either answer or deep"}]},

      {"name": "answer", "desc": "Reply directly.", "resident": true,
       "prompt": "Answer plainly, working from what is settled below.",
       "guard": "the user has moved on to a subject the earlier breakdown does not cover",
       "guard_to": "decompose"},

      {"name": "deep", "desc": "Long-form work.", "resident": true,
       "prompt": "Take your time. Show your reasoning and cite what you used.",
       "guard": "the user has moved on to a subject the earlier breakdown does not cover",
       "guard_to": "decompose"}
    ]
  }'

# 2. Point an agent at it (partial update — safe, touches nothing else).
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/agents/<agentID> \
  -H 'Content-Type: application/json' -d '{"machine": "<machineID>"}'

# Detach with {"machine": ""}.
```

Then open a NEW session with that agent (the machine is pinned per session at creation). Turn 1
runs decompose and route before replying; turns 2+ go straight to the resident phase. What to watch:

- The **phase pill** in the chat toolbar names the current phase; hover it for the phase's
  description and how much state is pinned.
- The **⚠ diagnostics trail** carries every transition, guard verdict, and fallback.
- `GET /orchestrate/api/sessions/{id}?agent_id=<agentID>` shows `Phase` and `MachineState`.
- Ask something unrelated to see the guard trip, or watch the model reach for `change_phase`.

A machine cannot be edited into a session that is already parked in it — re-point the agent and
start a new session. Deleting a machine leaves live sessions alone; their next turn runs as an
ordinary agent turn with a `machine_missing` breadcrumb.

## Why

gohort has two ways to run an LLM and they sit at opposite corners:

- **Pipelines** (`core/pipeline_def.go`, `core/pipeline_interp.go`). Durable control flow, zero
  memory. `RunPipelineDefSync(ctx, def, input string, ...) (string, error)`: a run starts empty,
  walks its stages, returns a string, and dies. An agent that invokes one through
  `AttachedPipelines` is making a subroutine call, and the stage chatter never lands in the
  session.
- **Agents** (`core/agent_loop.go`). Durable memory, zero control flow. One system prompt, one
  tool catalog, one tier, and the LLM re-decides the whole approach from scratch on every turn.

There is no third corner: **durable control flow plus durable memory**. Nothing in the tree holds a
position between user turns. `StandingAgent` is a scheduler. A pipeline's `branch` / `loop` stages
only move within a single run.

The shape people keep hand-rolling instead:

> A session opens, runs a decomposition phase, hands off to a router, and settles into an answering
> phase. It **stays** there for the rest of the conversation, unless a later question falls outside
> what the router scoped, at which point it re-routes or re-decomposes.

**A correction, made while attempting St4 (2026-08-13).** An earlier draft of this document claimed
Builder's intake-to-build routing, debate's phase progression, and servitor's scout-then-cluster
were three hand-rolled instances of this pattern. Checked properly, none of them is:

- **Builder** deliberately has no intake phase. The lean rewrite (v0.5.535) made its persona
  action-first — "act, don't interrogate. Understand the ask in a message or two, then BUILD it" —
  and what looks like intake is a decision table (`WHAT TO BUILD — first match wins`) inside one
  prompt, resolved per request rather than held as state. `AgentRecord.IntakeForm` is a form spec
  for public surfaces, not a phase. `builder_shadow_state_test.go`, cited as evidence, is about
  seed-record rebasing and has nothing to do with phases.
- **debate** is `private/debate/pipeline.go` — 3000 lines of imperative Go behind `PipelineWork`,
  the stages-are-code path. It has ordered sections, but it runs start to finish and returns. No
  user turn lands inside it. It is a pipeline, correctly.
- **servitor**'s `scoutWorkspace` is one function called once from `workspace_session.go:43` to
  enrich a prompt before a turn. A pre-turn step, not a position the session holds.

So the provenance argument was wrong and is withdrawn. What justifies this primitive is the case it
was actually asked for: a conversation that decomposes once, routes once, and settles — which now
runs live. Retrospective archaeology is not evidence, and it should not have been written as though
it were.

## The model

A **Machine** is an ordered set of **Phases**. Each phase is a mini-agent: its own prompt layer,
tool subset, tier, think setting, and optional structured output. A session persists **which phase
it is in** and a **state blackboard** of what earlier phases decided.

Two kinds of phase:

- **Transient** — it runs, produces a structured result, and immediately hands off. Decompose,
  route, plan. The user never takes a turn inside one.
- **Resident** — subsequent user turns land here directly. Answer, converse, execute. A machine has
  at least one, or it is just a pipeline.

The distinction is the whole feature. Transient phases chain **inside one turn**; the turn ends when
control reaches a resident phase and it replies. Turn 1 runs decompose → route → answer. Turns 2..N
enter at answer with the decomposition and the route pinned as context.

### State, not transcript

A transient phase's output does **not** become chat history. It is decoded into
`ChatSession.MachineState` and rendered into the system prompt as a pinned block. The transcript
gets one collapsed card (the same treatment a pipeline stage gets today via `openBlock`, see
`pipeline_interp.go:190`).

This is deliberate and it is what makes the pattern cheap:

- Turn 8 does not re-read the router's reasoning, because the reasoning was never history.
- The pinned block changes only when a transient phase writes to it. Across a resident run of N
  turns the system prompt is byte-identical, so the prompt-cache prefix holds and cold prefill is
  paid once. Putting phase state in the system prompt is *cheaper* than putting it in the user turn
  (compare the deliberate opposite choice for volatile per-turn hints in `runner.go` around line
  7215, where the hints ride the user message precisely because they change every turn).

## Data model

New file `core/machine_def.go`, sibling to `pipeline_def.go` and reusing its field vocabulary.

```go
type MachineDef struct {
	ID          string        `json:"id,omitempty"`     // storage key; stripped on export
	Owner       string        `json:"owner,omitempty"`  // owning user; stripped on export
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Start       string        `json:"start"`            // phase entered on a fresh session
	Phases      []MachinePhase `json:"phases"`
	Global      bool          `json:"global,omitempty"`
	Created     time.Time     `json:"created,omitempty"`
	Updated     time.Time     `json:"updated,omitempty"`
}

type MachinePhase struct {
	Name string `json:"name"` // unique; also the state key its Output lands under
	Desc string `json:"desc,omitempty"` // one line; shown in the UI and given to the guard

	// Prompt is this phase's directive, layered ON TOP of the agent's persona.
	// Supports {state:PHASE.field} templating against the blackboard.
	Prompt string `json:"prompt"`

	// Tools narrows the agent's catalog for this phase. Empty inherits the full
	// catalog, matching PipelineStage.Tools semantics (pipeline_interp.go:1126).
	Tools []string `json:"tools,omitempty"`

	// Model / Think override the tier and reasoning mode for turns spent here.
	// Model maps onto AgentLoopConfig.TierOverride, which already exists.
	// St1 note: Think shipped as a tri-state STRING ("on"/"off"/""), not a
	// *bool. kvlite encodes with gob, gob omits zero values, and a *bool
	// pointing at false decodes back as nil — so "off" would silently read as
	// "inherit" on every load. (PipelineStage.Think has this bug today.)
	Model string `json:"model,omitempty"`
	Think string `json:"think,omitempty"`

	// Output declares the structured handoff, decoded by the SAME machinery
	// pipelines use (decodeStageOutput / coerceField). Decoded fields land at
	// MachineState[Name]. Empty output = a prose phase that writes no state.
	Output []PipelineField `json:"output,omitempty"`

	// Resident marks the phase user turns come back to. A turn ENDS here.
	//
	// St1 note: Validate rejects Output and NextFrom on a resident phase. Its
	// reply IS the user-facing message, so wrapping it in a JSON contract would
	// hand the person a decoded envelope instead of an answer — structure
	// belongs on the transient phases that feed it. It also rejects {input} and
	// {prev} in a resident prompt: that prompt renders into the cacheable system
	// prefix, where anything that varies per turn re-pays cold prefill to say
	// what the conversation already carries.
	Resident bool `json:"resident,omitempty"`

	// Next is the phase to enter when this one finishes. Empty on a resident
	// phase means "stay". Empty on a transient phase ends the turn (degenerate,
	// rejected by Validate).
	//
	// St1 note: Next on a RESIDENT phase turned out to be meaningful and is
	// implemented — it means "this phase gets ONE turn, then hands off", which
	// is the intake-then-build beat. The driver can't do it (the handoff is only
	// correct AFTER the phase has had its turn, and AdvanceMachine returns
	// before that), so it lands in a companion — MachineDef.CompleteTurn,
	// called at end of turn. CompleteTurn writes nothing to the blackboard: a
	// resident phase's product is the conversation, which is already history,
	// and pinning its reply into the system prompt forever is the one thing
	// this design exists to avoid.
	Next string `json:"next,omitempty"`

	// NextFrom names one of this phase's own declared STRING output fields whose
	// VALUE is the next phase name. This is how a router routes. Validate proves
	// the field is declared and of type string; an unknown phase name at runtime
	// falls back to Next and drops a breadcrumb.
	NextFrom string `json:"next_from,omitempty"`

	// Guard is the re-entry condition, evaluated on each user turn while this
	// phase is resident. Prose, judged by a cheap structured call (see below).
	// Empty = no guard, transitions only via the change_phase tool.
	Guard   string `json:"guard,omitempty"`
	GuardTo string `json:"guard_to,omitempty"` // default: Start

	// Keep lists the MachineState keys that survive a re-entry into this phase.
	// Empty keeps everything, which is the safe default: a re-route that wipes
	// the decomposition is the expensive mistake, not the other way round.
	Keep []string `json:"keep,omitempty"`
}
```

Routing follows the discipline `PipelineStage.When` already established (`branchTaken`,
`pipeline_interp.go:373`): **a reference to a declared field, never an expression language**.
`NextFrom` is validated at save time against both the phase's output declaration and the phase set.
No parser, no new syntax.

## Session state

Three fields on `ChatSession` (`apps/orchestrate/types.go:845`). There is precedent for a persisted
turn-spanning gate right there: `AwaitingUserConfirm`, which exists so an ask-then-answer invariant
survives a restart. Same reasoning.

```go
// MachineID pins the machine this session is running. Copied from the agent at
// session creation, NOT read live, so editing an agent's machine does not
// re-shape conversations already in flight.
MachineID string `json:"machine_id,omitempty"`

// Phase is where this session is sitting. Empty means "not started" and
// resolves to MachineDef.Start on the next turn.
Phase string `json:"phase,omitempty"`

// MachineState is the blackboard: one entry per phase that declared Output,
// keyed by phase name, holding its decoded fields.
MachineState map[string]map[string]any `json:"machine_state,omitempty"`
```

Phase history (what transitioned when and why) belongs in the existing per-session diagnostics
trail, not a fourth field. Every transition already has to drop a breadcrumb.

## The turn

In `chatTurn`, before prompt assembly (`runner.go:6082`, where `persona` is resolved):

1. `agent.Machine == ""` short-circuits the whole thing. Non-machine agents take zero new code paths
   and zero new calls.
2. Resolve the def. Session `Phase` empty resolves to `Start`.
3. **Guard.** If the current phase is resident and has a `Guard`, evaluate it (below). A trip sets
   `Phase = GuardTo`, trims `MachineState` to `Keep`, and drops a breadcrumb.
4. **Run the phase.** Assemble as today, with the phase layered on:
   ```go
   persona := t.gatedPersona(t.agent.OrchestratorPrompt)
   persona = roundShapePreamble(resolveMaxPlanSteps(t.agent)) + persona
   persona += renderPhaseBlock(phase, sess.MachineState) // directive + pinned state
   sys := prependAgentContext(persona, t.agent, facts, notes)
   ```
   The phase block goes **after** the persona for the same reason the round-shape preamble goes
   before it: recency weights, and the phase is the most authoritative instruction in the turn.
   Tools filter to `phase.Tools`; `AgentLoopConfig.TierOverride` takes `phase.Model`.
5. **Decode and advance.** If the phase declared `Output`, decode it into `MachineState[phase.Name]`
   through `runDeclaredStage` / `decodeStageOutput`, then compute the next phase from `NextFrom` or
   `Next`.
6. **Loop while transient.** If the next phase is not resident, go to step 4 without ending the turn.
   Cap at **4 transitions per turn**; hitting the cap ends the turn where it stands with a
   breadcrumb, so a decompose-route-decompose cycle cannot grind.
7. **Persist** `Phase` + `MachineState` on the session, then reply from the resident phase.

Steps 4 through 6 are the interpreter. It is a smaller loop than `runList` because there is no
fan-out, no nesting, and no loop stage: those live in pipelines, and a phase that needs them calls
one.

## The guard

> **St2 note (built).** Both mechanisms exist and converge on `MachineCursor.moveTo`, so a phase
> reached either way is indistinguishable. The guard runs only on a **resumed** resident phase, never
> on one the walk just entered — there is nothing to re-decide about a routing call made one line
> ago. It **fails open** on every uncertain path (call error, unparseable verdict, unresolvable
> target) and breadcrumbs each one: the cost of a wrong stay is one turn answered from a slightly
> stale phase, while the cost of a wrong move is discarding the state the conversation is built on.
> Target resolution falls back verdict → `GuardTo` → `Start`. The guard prompt deliberately excludes
> the conversation — it gets the author's condition, the blackboard, the phase list, and the new
> message — because a check handed the history it is meant to judge from outside tends to agree with
> whatever that history was already doing.
>
> `change_phase` takes effect **immediately**: it runs any transient phases the move passes through
> and returns the new phase's block as the tool result, since the turn's system prompt is already
> assembled and the tool result is the only thing that can supersede it mid-turn. Tools and tier
> catch up on the next turn. Capped at two changes per turn, and offered only when the machine has
> somewhere else to go.

Two transition mechanisms, converging on one function so the breadcrumb and the UI treatment are
identical:

- **`change_phase(name, why)`**, a framework tool granted to a resident phase whenever the machine
  has more than one reachable phase. Free, LLM-judged.
- **`Guard`**, a structured call that runs before the resident phase gets the turn. Worker tier,
  tiny context (the guard text, the pinned state, the last user message, not the history), returning
  `{stay bool, to string, why string}`. Deterministic, and costs one small call per turn.

**The tool is the default; the guard is opt-in per phase.** The reason to want a machine at all is
that the LLM stops re-deciding, so a machine whose only transition mechanism is LLM judgment gives
back some of what it bought. But a guard on every turn is a latency tax on every turn, and for many
machines the resident phase noticing "this is a different question now" is entirely adequate. Let
the author choose per phase, and make the cost visible in the editor.

Structurally the guard is the premise gate from speaker grounding pointed at a different question:
does this incoming turn still sit inside the domain the last routing decision claimed.

## Reasoning, per phase

Three different answers, and the split is deliberate:

- **Transient phases: no reasoning by default.** They are paid before the user sees a word, so the
  base is off and an author opts in per phase with `think: "on"`. That default is WRONG for a phase
  that genuinely judges — decomposing an ambiguous request, routing between close options — and the
  `machine` tool's help now says so, because the pipeline tool has always advised turning thinking
  on for decomposition and the two surfaces were quietly giving opposite advice.
- **The guard: never.** Hardcoded `worker` tier, thinking off. It is a cheap check standing in front
  of the turn; a guard that needs to deliberate is really a transient phase.
- **The resident phase: whatever the agent does.** It runs the ordinary turn, where think resolves
  route default → per-agent → phase override. Set `think` on a resident phase only to differ from
  the agent it belongs to.

So the reply reasons; the machinery in front of it does not, unless told to.

## Replacing the brain

`AgentRecord` gains one field:

```go
// Machine names the MachineDef that drives this agent's turns. Empty (the
// default, and every existing record) = today's behavior exactly: one prompt,
// one catalog, the LLM decides the approach fresh each turn.
Machine string `json:"machine,omitempty"`
```

"Replaces the brain" means **demoted, not discarded**. `OrchestratorPrompt` stays and keeps doing
what it does: identity, voice, standing rules, memory framing. The machine supplies procedure. An
agent is still who it is in every phase; what changes is what it is currently doing and what it can
reach while doing it.

This matters for the framework's mission. Every existing agent is upgradeable in place by picking a
machine from a dropdown, and there is one conversational surface, one session list, one memory
model, one guardrail path. A machine is not a new kind of thing you chat with. It is a property of
an agent, the way Cortex and Fleet are.

## Decisions

1. **A machine is an agent property, not a new primitive with its own surface.** Rejected: a
   separate "machine" object you converse with. It would have forked sessions, memory, guardrails,
   and publishing, and the whole point is that an existing agent gets better.
2. **Transient output is state, not transcript.** The context and cache argument above.
3. **Routing is a declared-field reference, not an expression language.** Follows `branchTaken`.
   Anything more complex is a pipeline call from inside a phase.
4. **The session pins the machine at creation.** Editing an agent's machine affects new sessions.
   Live sessions keep the shape they started in, and an unknown phase name (machine edited
   underneath a live session anyway) falls back to `Start` keeping state, with a breadcrumb. Same
   posture as broken-dependency safety: keep the thing, surface the break, do not silently discard.
5. **Guard opt-in, tool by default.** Cost is per-turn and the author is the one who knows whether
   determinism is worth it here.
6. **Every transition leaves a breadcrumb.** A framework decision that reshapes the turn and leaves
   no trace is the exact failure the diagnostics trail exists to prevent.

## Surfaces

> **St3 note (built).** The editor page was NOT built, because there is no pipeline editor page to
> share: pipelines are authored from chat through the `pipeline` grouped tool, and the only UI they
> have is an attach picker. Machines follow that house pattern rather than inventing a bespoke page
> — a `machine` grouped tool for authoring, a select on the agent editor for attaching. The HTTP
> routes exist for anyone who would rather write the JSON.
>
> The phase pill landed as a GENERIC `AgentLoopPanel.StatusURL`: the app serves
> `{label, title, tone}` for the active session and the panel renders a pill, refreshed on session
> switch and after each turn. It names nothing about phases or machines, exactly like the
> `DiagnosticsURL` contract it sits next to. Empty label renders nothing, which is every session not
> running a machine. Transitions do NOT yet render as cards in the transcript.

- **orchestrate**: authoring lives in the `machine` tool (Builder-only, same gate as `pipeline`).
- **Agent settings**: the Phase machine picker under Persona, hidden until you have one.
- **Chat**: the phase pill in the toolbar, and **Configure → Machines** (list every machine, what
  it is made of, who runs it; pick one for this agent; show its diagram; edit; delete). A machine is
  single-select, so the modal is a radio list rather than the chip picker Pipelines uses.

**Editing is a JSON textarea, not a form**, and that is a deliberate stop-gap. A machine gets TUNED
in a way a pipeline does not — guard wording, phase prompts, whether a phase should be resident —
so "author it in chat and never touch it again" was the wrong read. But the phase schema is still
allowed to move (St4), and a form built against a moving schema is the expensive thing to throw
away. The definition is the editor until the fields people actually keep editing are known; the
validator reporting every problem in one response is what makes that survivable.

A graph view of a machine's phases and transitions is **built** (see
[workflow-graph.md](workflow-graph.md)) — "Show diagram" on each row of the Machines modal, with the
open conversation's path highlighted on the machine it is running — a list of phase names implies an order a machine does not
have, and the runtime overlay is what turns the ⚠ trail into something you can look at.

## Staging

| Stage | Scope |
|---|---|
| **St1** ✅ | `core/machine_def.go` + interpreter + `Validate` + session persistence. No UI. |
| **St2** ✅ | `core/machine_guard.go` (guard evaluation) + `ChangePhase` + the `change_phase` tool. Breadcrumbs and the transition cap moved into St1 — the degradation paths needed them to be honest from the first line. **The phase pill moved to St3**, with the rest of the UI: the chat surface is a `ui.AgentLoopPanel` with no generic per-session badge, and inventing one for this would be exactly the core/ui leak CLAUDE.md forbids. Until then the phase is visible in the ⚠ trail (every transition breadcrumbs) and on `GET /api/sessions/{id}`. |
| **St3** ✅ | The `machine` tool (authoring), the agent-editor picker, HTTP CRUD + export/import, and the phase pill via a generic `StatusURL`. No editor PAGE — see the St3 note under Surfaces. |
| **St4** | ~~Port Builder's intake-to-build routing.~~ Withdrawn — there was nothing to port; see the correction under Why. Replaced by a troubleshooting machine as the stress test: [troubleshooting-machine.md](troubleshooting-machine.md). Its success condition is a written-down change to St1-St3 that came from USE rather than design, or a defensible statement that none is needed. |

### What St1 left on the table, deliberately

- **No UI, so no way to author one from the app.** A machine is created today by calling
  `SaveMachineDef` from Go. St3 is the CRUD layer.
- **Transient phases get no tools.** They run during system-prompt assembly, before the turn has
  built its `ToolSession`, and the live catalog is wired to that session. Handing a phase a
  half-wired copy is worse than handing it none, and decompose / route / classify want none anyway.
  The seam is `chatTurn.machineCatalog`, which returns nil and says why.
- **No guard.** `Guard` / `GuardTo` are declared and validated (so the schema doesn't move under
  St2) but nothing evaluates them. Until St2, a machine transitions only via a transient phase's
  routing or a resident phase's one-beat `Next`.
- **Dispatch is unchanged.** Dispatched sub-agents and channel turns reach the loop by other paths
  and have no session to hold a cursor, so they run exactly as they always did. See Open.


## The fields ARE most of the instruction

A step's declared fields are not a schema bolted onto a prompt — each one is sent to the model with
the description the author wrote. So a description is a directive:

```
"the single best explanation, stated so it could be wrong"     ← does the work
"the hypothesis"                                                ← does not
```

Which means the prompt should carry what a list of fields cannot: where to look first, what a good
answer requires, and the mistake this step tends to make. *"Read enough to have a real hypothesis
rather than a plausible one"* belongs in the prompt; *"return a hypothesis field"* belongs nowhere,
because declaring the field already said it.

The editor labels a transient step's prompt **"How to go about it"** for that reason, and a step with
no fields yet is told the box is currently the whole instruction.

## What a step is actually told

The prompt box is only part of it. `PhaseBlock` composes the rest mechanically, every turn:

```
## Current phase: verify
Test it.

Go and look.                          ← your instructions

## Established earlier in this conversation
Settled. Work from it rather than re-deriving it, and do not re-ask what it already answers.

### triage — Is there something to explain?
Observation: …
Next phase: …

## Other phases in this workflow
Reachable with change_phase, and only when the request has genuinely moved on.
- triage: Is there something to explain?
- hunch: Form one hypothesis.
```

Plus the declared fields, requested separately as a validated schema.

**So do not repeat any of it in the prompt.** Earlier steps' findings arrive pinned and labelled; the fields a step must return are asked for on their own. A prompt that pastes `{state:triage.observation}` into a sentence is paying for those tokens twice and keeping a copy that drifts from the field name. Reach for a reference only when the phrasing genuinely matters — "What they saw: X" reading better than a labelled block.

The editor shows this under **"What this step actually receives"**, rendered by calling `PhaseBlock` itself with placeholder findings. Not a description of the composition and not a re-implementation — the same bytes a live turn produces, so it cannot quietly stop being true.

### Declared routing targets

A routing field can declare which steps it may name:

```json
{"name": "next_phase", "type": "string", "enum": ["hunch", "answer"]}
```

One declaration replaces a hand-written list in three places:

- **The instruction is generated.** `PhaseBlock` states *"Put exactly one of these in next_phase"*
  and explains each choice in the TARGET's own words (its `desc`), so one description serves the
  phase and every router that can reach it. The fallback is stated too — a model choosing badly
  should know what happens rather than discover it.
- **The diagram draws those arrows** instead of one to every phase. Undeclared, a dynamic route
  honestly fans out to everything it could pick; declaring is what turns that into the shape you
  actually built.
- **The validator refuses a target that is not a phase**, at SAVE time. That is the point: without
  it, a name that resolves to nothing falls back silently at run time and routes somewhere nobody
  chose.

The generated contract also carries the set, and a reply outside it is rejected where
`runDeclaredOutput` can still repair it — one retry with the error, rather than a bad route.
Capitalisation is not an error: a model answering "Answer" for "answer" has chosen correctly.

Leaving it undeclared keeps the old behaviour exactly, so nothing existing changes.

## What the framework supplies

The routing above began as three settings and a variable for one idea, and a step that forgot to
place `{input}` in its prompt ran against no message at all. Both are things the definition already
knows, so the framework provides them (`core/machine_builtins.go`).

**A step that decides just names the steps it may choose between:**

```json
{"name": "triage", "prompt": "Work out what kind of turn this is.",
 "choices": ["hunch", "answer"], "next": "answer"}
```

No field to declare, no field to point at, no list of legal values kept in sync by hand. From
`choices` the framework derives the routing field (`next_step`, a required string whose enum is the
choices), the instruction naming each destination and what that step is for, the arrows in the
diagram, and a save-time error when a name stops resolving. `next` stays the fallback.

`next_from` is unchanged and still wins where it is set: a routing value that is ALSO a real finding
("severity", say) is worth naming yourself.

**A step routes ONE way, and the form now says so before the fact.** The two mechanisms hide each
other live — pick a field to route on and the choices list goes away under your hand; tick a choice
and the hand-wired controls do. `Problems()` remains the backstop for the JSON door and the tool,
which can still write both and should still be told which wins. Neither door ever silently clears
the other's setting: hide, refuse, explain.

That symmetry needed one generic fix in core/ui: `show_when: "!field"` used plain JavaScript
truthiness, and an empty ARRAY is truthy — so a checklist with nothing ticked read as answered and
"!choices" could never fire. An empty list, an empty string and an absent key now all mean the same
thing to a form.

**The person's message arrives whether the prompt asks for it or not.** A transient step's prompt is
a template, so one that never says `{input}` used to be sent without the message — the model
answering confidently about nothing, which reads as it ignoring instructions. Now the framework
prepends the message when the prompt is silent about it, and stays out of the way when the author
placed it themselves.

**What earlier steps established arrives the same way.** A resident step has always been handed the
whole blackboard, composed into its block; a transient step was handed nothing, so its author
hand-copied `{state:triage.observation}`, `{state:triage.source}`, `{state:triage.asked}` one
reference at a time — three chances to typo a name the definition already knows, and three lines to
fix after a rename. Now it is prepended unless the prompt places a `{state:…}` reference of its own.

The vocabulary is FIXED, and that is what makes it a built-in: no name to choose, nothing to
declare, the same meaning in every machine. It lives in one table (`MachineVars`), which the
resolver, the editor's help and the `machine` tool's spec all read — a variable documented in one
place and implemented in another is how one of them silently stops being true.

| | | |
|---|---|---|
| `{input}` | what the person said this turn | supplied when the prompt places none |
| `{original_input}` | the message that opened the CONVERSATION, unchanged on turn nine | |
| `{established}` | everything earlier steps worked out, with their names | supplied when the prompt places no `{state:…}` |
| `{prev}` | what the step before produced, this turn | |
| `{now}` | the date and time where the PERSON is | |
| `{user}` · `{agent}` | who is talking, and which agent they opened | |
| `{step}` · `{machine}` | where the prompt is, by name | |
| `{state:NAME.field}` | one field another step established | not a built-in: it names a step |

**A field can take its value from one of these instead of being asked for it.** Name it after a
built-in — `original_input`, `now`, `user`, `agent`, `prev`, `step`, `machine` — and it is filled
from that built-in. The name IS the choice: `original_input` cannot mean anything else, and a
machine where it did would be one where the same field name means the opening message here and
whatever a model wrote there.

That rule lives in core (`MachinePhase.normalized`), not in the editor that offers the wheel,
because the editor is one of four doors: the `machine` tool writes these, `extras/` ships them,
imports carry them. A rule enforced at one door is a rule that is not true.

An explicit `from` still wins, for the case the name rule cannot express — a field with its own name
taking a built-in's value: `{"name": "asked", "from": "{original_input}"}`. The value is already known, so asking a model to copy it across is three ways worse than
taking it: it costs tokens, it can be paraphrased, and it can be left out. Filled fields are left
out of the output contract entirely (the model is never shown a field it is not being asked for)
and merged into the result afterwards, so the blackboard carries them exactly like answered ones. A
step that asks for nothing and says nothing does not call a model at all.

**All of them are text.** That is not an accident of the current set: a variable holds what the
framework can hand a prompt, and a prompt takes words. So a filled field is normalized to a string —
declaring it a list describes something that cannot happen — and `Advice()` says so rather than
`Problems()` refusing a machine that runs correctly. If you need a list, let the step work it out.

`{input}` and `{original_input}` are the WORDS of a message. Images and files attached to a turn are
not in them, so a turn that arrived as a photo and nothing else resolves to nothing; the driver
leaves a `machine_static_empty` breadcrumb rather than a silently empty field.

Adding a field asks WHAT KIND first: a choice between each built-in (by the name a field takes when
it holds one, with what it means beside it) and **Variable** — the ordinary case, named in the word
an author would use to a colleague. A combo box asked both questions at once: click it and you are
typing, with the built-ins behind a dropdown arrow nobody looks for. Only a Variable reveals a name
box, a type, a Required toggle and an instruction; a built-in row is the choice and nothing else.

The choice is never locked. It settled the instant it was picked once, which turned a mis-click
into a remove-and-re-add; the row reshapes on every change anyway. Switching a built-in row to
Variable hands back an EMPTY name box on purpose — a variable named after a built-in is read as
that built-in at every door, so pre-filling "now" there would make the switch appear to do nothing.

The kind column is DERIVED, never stored: core decides that a field named after a built-in is that
built-in (at every door), so the form is told what the definition already means rather than carrying
a second copy that could disagree with it.

**Picked when the field is added, settled after that.** The moment a row names a built-in it has
nothing left to configure — the name is the choice, the value comes from the framework, the type is
text — so the name locks and the type, required and instruction cells go away. That uses two more
generic row conditions (`LockWhen` / `HideWhen` on a rows column, the ShowWhen grammar evaluated
against the row), because the alternative was three controls somebody could change and be ignored
for. A setting that silently does nothing is worse than one that is not offered.

The last three of the turn facts come from the host (`MachineTurn`), because the driver has no idea
who is talking or what timezone they are in. `{now}` is stamped where the PERSON is: a machine
triaging "this started about an hour ago" against a server-zone clock is worse than one with no
clock at all.

`{original_input}` is kept on the cursor, written once on the first walk. A step judging its work
against "what they actually asked" needs the original, and a value that quietly became the latest
message would answer a different question under the same name.

**A resident step's prompt resolves the stable subset.** Its prompt is pinned in the cacheable
prefix, so only values that hold still may appear there: `{original_input}`, `{user}`, `{agent}`,
`{step}`, `{machine}` all resolve (they are fixed for the session), while the volatile three —
`{input}`, `{prev}`, `{now}` — are refused at save time, each with its reason, and zeroed inside
`PhaseBlock` regardless so no call site can break the cache by passing a clock in. `{established}`
is refused there too: the block already composes it. Before this, a resident prompt containing
`{user}` silently rendered a blank.

## Travelling

**In and out from the page where machines live.** Export is a row action on the list (the reason to
take a copy is usually that you are about to change it, so it should not require opening the machine
first) and a button in the editor. Import is a modal with a file field — `core/ui` reads the chosen
file as TEXT in the browser and submits its contents, so this is a form rather than an upload path,
and the imported machine opens in the editor the way a draft or a duplicate does.

That last part needed the endpoint to accept both shapes it legitimately arrives in: the recipe
itself (what a script or the tool posts) and `{"recipe": "…"}` (what a form posts). Accepting only
the first is why `POST /api/machines/import` sat fully implemented and unreachable from any page
since machines shipped.



Machines are a bundle artifact type ("machine", `machine_artifact.go`), so they ride the unified
export/import surface next to agents, pipelines, tools and the rest. The recipe keeps its ID — an
agent's `Machine` pointer is an ID, and the pointer travels in the agent's own recipe — which is
what lets an agent+machine bundle land wired. The agent's dependency walk names its machine, so
exporting an agent folds the machine in automatically; before this, an exported agent arrived
pointing at a machine that was never in the box, and walked and talked while quietly not being what
its author built.

Delegate references normalize to agent NAMES on export (an imported agent is reborn under a fresh
ID), a machine's own walk folds in the agents its steps delegate to and the exportable tools its
steps narrow to, and bundle import SKIPS a same-ID or same-named machine rather than copying — the
existing one already serves the pointer the traveled ID exists for.

## Keeping the arms of a branch apart

A machine is a graph, and every waiting step offers every other step to `change_phase` — which is
right for a conversation that genuinely changed subject, and wrong for a machine that BRANCHES,
where the model can cross from one arm to the other because it judged the request close enough.

`exits_to` on a step names where the conversation may be moved FROM it. Empty means anywhere (the
default, and the right one for most machines). It bounds what the AGENT decides on its own, by
either door — `change_phase` mid-turn, and a guard's verdict naming somewhere to go — because those
are the same decision arriving two ways, and bounding one would leave the other open. A step's own
`next` and the `guard_to` its author declared stay legal whatever the list says.

It is offered only on a step the conversation WAITS in: those are the only steps a turn is ever
parked in, so on a step that passes on it could never apply.

Enforced in the driver (`ChangePhase`), not in the tool, so every path that moves a turn obeys it;
refused with the list of where the conversation MAY go, because "no" without an alternative just
gets tried again; and `PhaseBlock` offers exactly the legal exits, since listing one the tool would
refuse teaches the model to spend a round being told no.

## Delegating a step

A transient phase can name another agent:

```json
{"name": "verify", "agent": "Log analyst", "prompt": "Test the hypothesis…", "next": "report"}
```

The other two ways a phase differs from the agent running the conversation — a narrowed tool
catalog, a different tier — are configurations of the SAME agent. This one is not: a delegate has
its own persona, tools and memory. It is the shape servitor uses, where something conducts and
something with different reach does the work.

The seam is `PhaseRunner`, which already meant "run this phase's prompt, hand me its declared
fields". Delegation is a different runner, not different machinery.

**Two calls when the phase declares fields.** The delegate is a whole agent and answers in prose —
it plans, uses tools, reports. Asking it for JSON as well would put a decoder's constraints on the
thing whose value is that it is not a decoder. So it reports, and the phase's own worker shapes that
report into the declared fields through the same path a non-delegating phase uses, told to take the
findings as given rather than re-do the work. A phase declaring nothing costs one call.

**One continuing thread per (conversation, phase)**, keyed `machine:<session>:<phase>`. Continuing,
so a re-entered phase builds on what the delegate already established here; per-conversation, so two
investigations never share a delegate's context.

**Transient phases only.** A resident phase is where the conversation lives, and delegating it would
mean the person is talking to something they did not open. `Problems()` reports it.

**A name that does not resolve runs the phase inline and says so** (`phase_delegate_missing` in the
session diagnostics). A machine is portable and the agent it names may not exist in this deployment;
failing the turn would be worse, and failing silently would be worse still — a machine that quietly
stops delegating has quietly stopped being what its author built. Delegating to yourself does the
same, since that is a second turn of the same agent with all of the cost and none of the benefit.

**A delegate that errors fails the phase.** It does not fall back: answering the question with the
wrong thing wearing the right name is the one outcome worse than an error.

## Tools in a step

The two kinds reach tools differently, and the difference is not cosmetic:

- A step the conversation **waits in** runs as the turn itself, so it has the agent's whole
  catalog and its tool list NARROWS that. Empty means everything.
- A step that **passes on** runs before the turn has a catalog at all — it happens during
  system-prompt assembly, hundreds of lines before the round's tool session exists — so it reaches
  exactly what it names and nothing otherwise. Empty means NO tools.

That second half was inert until v0.6.171: the control existed, the tool's spec documented it, and
the runtime handed every passing step an empty catalog, so a step told to "go and look" could not.
It now builds a session of its own the way a pipeline's sub-run does (shared caches and dispatch
counts, staged files folded back into the turn), and only when a step actually names tools — a step
that names none pays nothing.

**A step's tools go through the turn's approval gate.** The turn's own loop stops for a tool whose
credential is marked RequiresConfirm and renders the approval card; a worker stage auto-approves,
because a pipeline runs with nobody watching and a prompt would hang forever. A machine step runs
with somebody waiting, so it takes the turn's hook (`PhaseWorkerConfirm` — the host seam the
PhaseRunner doc always promised). Giving steps tools without this would have been a hole underneath
the card rather than a feature.

`Advice()` catches the mismatch that motivated this: a step whose instructions send it looking, with
no tools named and no delegate. The shipped investigation recipe has exactly that shape, on purpose,
and says so in its description — its `hunch` step is meant to be given whatever search tools the
deployment has.

## What the person sees while it runs

A machine's transient steps run at the HEAD of a turn — before the persona is assembled, before a
single token reaches the browser. A decompose-then-route machine spends two model calls there, and
a guarded step pays a third on every turn. That was silence, and silence during work somebody is
waiting on reads as hung whatever the reason for it.

Each step now announces itself on the activity surface before it runs, in the AUTHOR's words: the
step's own `desc` is what the rail and the routing instruction already show, so there is no second
copy to keep in sync. A guard says what it is ("checking whether this is still the same job") —
that call is paid on every turn spent in the step, and naming it is the only honest account of
where the second went.

## Editing one

**Or describe one.** "Describe one…" next to New machine takes a paragraph — what kinds of turns
arrive, what should happen to each — and drafts a complete machine, landing you in the editor to
adjust it. The drafter reads the machine tool's own spec (`machineHelpText`) and its output goes
through the tool's own decoder, so a drafted machine cannot be a third dialect. A draft with
problems still saves: the checklist phrases them as work remaining, and an imperfect draft beats an
empty editor.

**Removing a step takes its references with it.** `RemoveStep` drops the deleted name from every
`choices`, `keep`, `exits_to` and routing-target list, clears a `guard_to` that pointed at it (an
empty one means "back to the start", the least surprising landing), and writes down the new start if
the deleted step was the beginning. Leaving those behind made the checklist report a deletion as
work to do — blaming the author for doing what they meant.

Two references are deliberately NOT rewritten, because only a person can answer them: a step whose
`next` pointed there is left with nowhere to go, and a prompt reading `{state:gone.field}` keeps its
text. Both surface in the checklist as questions rather than as damage, and the removal's confirm
names them beforehand — computed by the same walk that will do the removing, so the warning cannot
promise something else.

**Renaming a step is one edit.** `RenameStep` rewrites every reference the definition holds —
`next`, `choices`, `keep`, `guard_to`, routing targets, `start`, and `{state:old.…}` in prompts,
guards and fills — so a rename never sends you hunting through the checklist. A rename onto an
existing step is refused (its references would silently re-point), the name field reloads the
editor (everything on the page carries the old name until it does), and a form still addressing
the old name 404s rather than resurrecting the step. Live sessions parked in the old name heal
through `resume()` with a breadcrumb, untouched by the editor.

**Configure → Machines → Edit** opens the structured editor: the machine's name and
starting phase, a table of phases, and a form per phase.

**One panel per step, not one form behind a table.** Every choice is computed from the step it
belongs to: `next_from` offers only that step's own TEXT fields (a list cannot name a phase, and
another step's field is not readable there), `keep` offers the other steps by name, the delegate is
picked from your agents, `tools` is a checklist of the user's actual pool (the same list the agent
editor's Tools modal shows, grouped and filterable), and the prompt's help SPELLS OUT the
`{state:…}` references available. Nothing in the editor is typed from memory.

A tool an imported machine names that this deployment does not have stays on the list, labelled —
a checklist only persists what it can show, so a name left off would be silently dropped by the
next save. Broken-dependency posture: keep it visible, let the person uncheck it on purpose.

A resident step is not offered `output`, `next_from` or a delegate at all. The form cannot express
what `Problems()` would reject, so there is nothing to explain and nothing to undo.

The steps are also the page's left rail (`SectionNav`), so a machine is navigated the way it is read:
one step at a time, in order.

**The map redraws when a step changes shape.** It is rendered server-side, so an edit that changes
the machine without reloading the page — ticking a choice adds an arrow — would leave the picture
describing the machine as it was a moment before, while you look at it. It re-fetches itself
(`/graph?links=1`, the map form with the section anchors) on the same invalidation broadcast the
preview uses, coalesced so a checklist's save-per-box is one redraw, and re-lights the step you are
on afterwards. Structural edits that reload the page (add, remove, rename, reorder, kind) never
reach it.

**The map is pinned above the steps** (`Page.Sticky`), because `SectionNav` shows one section at a
time — a picture in a section of its own could never be on screen with the step being edited, which
is exactly when you need it: a step's form lists the names it may choose between, while the SHAPE of
that choice lives in the arrows. The step whose section is open is lit ("you are here"), driven by
the same URL hash the rail navigates by, so there is one answer to "where am I". Collapsible,
because a four-step machine is nearly 300px of permanent screen.

**The picture navigates.** The graph is inlined into the editor (links inside an `<img>` SVG are
inert) and every node links to its step's section — the graph is the rail, drawn. The anchors ride
the section nav's new hash support: each section has a URL (`#verify`, `#try-it`, same slug
transform in Go and JS, pinned together by a test), so a deep link or the back button lands on a
section, on any `SectionNav` page in the product.

**Adding a step lands you on it.** The sections, the rail and every other step's selects are built
server-side from the phase list, so a step added in the browser exists nowhere on screen until the
page is rebuilt — the add dialog used to close and appear to do nothing. The add form redirects to
the editor carrying the new step's section anchor, so it reopens with that step's form already
open.

**The preview refreshes itself.** "What this step actually receives" is the surface that teaches
what the framework composes, so showing the pre-edit composition after a save is the same lie as an
added step that never appears. It refreshes in place rather than reloading: the prompt box saves on
a typing debounce, and a reload would yank the page out from under someone mid-sentence. It rides
the framework's own invalidation broadcast — a phase form writes to `…/phases?name=<step>`, so the
stale step is named in the event — and re-fetches from the same function that drew it.

A step the conversation waits in establishes NOTHING, and the form says so where the section would
be rather than removing it silently: its reply goes to the person, so there is no decoder to hand
fields to, and `CompleteTurn` never pins that reply to the blackboard (it would paste it into every
later step's prompt, forever). Anything later steps need is worked out by the step that FEEDS the
waiting one — which is why the shipped recipe establishes in `triage` and `hunch`, and only talks in
`verify` and `answer`.

**Changing a step's KIND rebuilds the form.** The sections under "What kind of step is this?" are
built from that answer server-side, so toggling it without a rebuild left a step showing controls
its new kind cannot use — an output contract on a step that now waits, a guard on one that no
longer does. Same rule as the rename field: a control whose answer changes which controls exist
reloads, and (being a toggle) it commits on change, so nothing typed is at risk.

**Export and Duplicate** sit on the editor page, next to the machine's own fields — the portable
recipe and the safe-experiment copy both existed as endpoints and neither was reachable from the
page where you author. Duplicate opens the copy, because working on the copy is the point.

The derived **What a turn costs** line keeps up with all of this: a step with tools is a short agent
loop (one call per round, up to `StageToolRounds`), a delegated step spends another agent's whole
turn, a step that only pins values is free, and a guarded step charges a check on every turn spent
in it.

**A branch shows up in the STEPS too, not only in the map.** The steps a decision picks between are
nested under it in the rail (`Section.Indent`, a generic one-level nesting for any SectionNav page)
and each says what it is an alternative to in its own heading: "one of the ways triage can go". A
flat rail draws two steps that are alternatives exactly like two that run one after the other, which
is the distinction such a list most needs to make. Only a real fork nests — a step that hands off to
exactly one place is a sequence, and drawing it as a branch would make every machine look forked.

**The map's shape comes from the ARROWS, not the step list.** `layout()` ranks steps by distance
from the entry over forward edges, so a step sits below whatever leads to it. Within a row, steps are
ordered by the mean position of their PARENTS (barycentre) — which is what keeps an arm of a branch
under its own branch two steps down. Declaration order decides only where that genuinely ties: the
arms of one split, reached from the same step, sit left-to-right in the order they are declared, and
that is what the ↑↓ buttons act on. Reordering a chain changes nothing in the picture, correctly.

Before the barycentre pass, a row was ordered by declaration alone: write the right arm's second step
first and it sat under the LEFT arm with the arrows crossing, with nothing on screen saying the fix
was to reorder a list.

**Steps reorder** (↑↓ on each step's toolbar). The order is not cosmetic: it is the rail and the
reading order, the tie-break that arranges same-depth steps in the map, the order earlier findings are pinned into a
prompt (`establishedBlock` renders in declared order, deliberately, so the cacheable prefix holds),
and which waiting step catches a step that hands off nowhere (`firstResident`). It is ALSO the entry
point when `start` was never set — so a move pins `start` to whatever it currently resolves to
before reordering, because the editor shows the resolved value and a machine that never chose one
would otherwise have its beginning moved by a button that says nothing about beginnings.
machines **duplicate** from the list (numbered copies, landing in the copy's editor — iterating on
the working one in place is how the working one stops working), and **What a turn costs** is derived
from the definition: which steps cost a model call when they run, which only pin values and are
free, and which turns pay a guard check. Per piece rather than per turn, because a deciding step
makes the path dynamic and a guessed total would sometimes lie.

**Who runs it** is on the page too: a checklist of your agents, checked = attached. An unattached
machine does nothing, and the only place to attach one used to be a different surface (the chat
toolbar's Configure → Machines — still there, still works). An agent runs one machine at a time, so
an agent already running another is labelled with what checking it would move.

**Try it** sits with the picture, and it holds a CONVERSATION: send a message, see which steps ran
and what each handed on, then keep sending — the cursor rides back through the browser, so a later
turn resumes the parked step exactly as a live turn would. That is the only way a guard, a
re-entry, or a one-turn handoff can ever be watched, because all of them exist only across turns.
Each message appends its own block (turn 4 firing a guard sits under the three turns that led to
it); Start over forgets the rehearsal. It runs the real driver (`AdvanceMachine`), with two
differences stated in the reply every time: no tools, and it stops AT the resident step rather
than running it. Where a turn GOES is the question here; what it would say is the agent's job.

The form asks questions rather than naming fields. *"The conversation waits here"* is
`resident`, with the consequence spelled out where the choice is made. *"Then go to"* is a
select of phases that actually exist, so a typo is not something the form can produce.
*"…or let this step choose between"* is a checklist of the machine's real steps, because picking
the destinations IS the routing decision; the field that carries it is the framework's problem.
Hand-wiring a field lives under *Routing by hand*, collapsed unless it is in use.

The step's instructions box carries a **✨** that opens the shared assist workbench
(`machine_suggest.go`). It is the one box in the app whose right answer depends on parts the author
cannot see — what the framework composes around it, and what the other steps already establish — so
the drafter is given all of it, plus the rules the help text spends its words on (write the method,
not the output; never ask for JSON).

The spec is built server-side (`machine_editor.go`) for two reasons: the selects need the
machine's own phase names and declared fields, and the help text is the part that carries
the concepts — it belongs where it can be reviewed and tested, not in a string inside a
browser file.

**The checklist stays true while you fix things.** It lives in the section body rather than its
heading so it can be refreshed, and it refetches on the same broadcast the map and the preview use.
This is the one place staleness actually cost something: it is the list somebody works against, one
fix at a time, and it said "3 to fix" until a reload however many had been fixed. Both wordings
exist in Go and in the browser and are pinned together by a test — a refresh phrased differently
reads as the page changing its mind rather than as the same list one item shorter.

**The checklist is `Validate`'s own findings**, shown as work remaining rather than as a
refusal. `Problems()` is the same function the save path uses, so the list can never
disagree with what a save will accept. A half-built machine has problems by definition;
an editor that reported them as failure would be arguing with somebody mid-thought.

**Partial saves are safe.** The meta form holds three fields and the record has phases;
a phase form holds one section and the phase has others. Both merge rather than replace —
the same failure `patchAgent` exists to prevent on the agent record.

**An incomplete machine still saves.** Refusing to store the third field until the tenth
exists is how an editor becomes a puzzle. `enterMachine` already degrades to an ordinary
agent turn with a breadcrumb rather than breaking a conversation, so a half-built machine
attached to an agent is a visible no-op, not a failure.

**The JSON editor stays**, behind *Edit as JSON*. It is what the `machine` tool writes,
what `extras/` ships, and the fastest path for someone who already knows the shape. Two
doors, not a replacement.

## Open

- Does a phase get its own memory scope, or does memory stay agent-wide? Agent-wide for St1. A
  research machine wanting per-phase facts is a real request but it is a second feature.
- Should a resident phase be able to run a machine (nesting)? No for St1. Reject in `Validate` so
  the answer is explicit rather than accidental.
- Dispatched turns (scheduled fires, delegations, phantom, sub-agent calls) run WITHOUT the
  machine. Those paths assemble their own system prompt in `agent_dispatch.go` /
  `scheduled_updates.go` and never enter one, and a dispatch has no session to hold a position in
  anyway. Since v0.6.174 that is at least VISIBLE: `beginDispatchDiag` — the wiring every dispatch
  path already did by hand — records a `machine_not_on_dispatch` breadcrumb naming the machine the
  turn ran without. Whether a dispatch should instead run the machine one-shot from `Start` with an
  ephemeral cursor (the same shape a rehearsal uses) is the open question; it is a real change to
  the most delicate path in the app, so it wants a live test rather than a confident patch.
