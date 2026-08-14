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
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/api/machines \
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
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/api/agents/<agentID> \
  -H 'Content-Type: application/json' -d '{"machine": "<machineID>"}'

# Detach with {"machine": ""}.
```

Then open a NEW session with that agent (the machine is pinned per session at creation). Turn 1
runs decompose and route before replying; turns 2+ go straight to the resident phase. What to watch:

- The **phase pill** in the chat toolbar names the current phase; hover it for the phase's
  description and how much state is pinned.
- The **⚠ diagnostics trail** carries every transition, guard verdict, and fallback.
- `GET /api/sessions/{id}?agent_id=<agentID>` shows `Phase` and `MachineState`.
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

Builder's intake-to-build routing, debate's phase progression, and servitor's scout-then-cluster are
all this pattern, built three times with three different ad-hoc state hacks (see
`builder_shadow_state_test.go`).

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
| **St4** | Port Builder's intake-to-build routing to a machine and delete its shadow state. This is the proof the abstraction is real, and it is the one that should be allowed to send St1-St3 back for changes. |

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

## Open

- Does a phase get its own memory scope, or does memory stay agent-wide? Agent-wide for St1. A
  research machine wanting per-phase facts is a real request but it is a second feature.
- Should a resident phase be able to run a machine (nesting)? No for St1. Reject in `Validate` so
  the answer is explicit rather than accidental.
- Dispatched sub-agents and channel turns hit `AgentLoopConfig` through other paths
  (`agent_dispatch.go`, `scheduled_updates.go`). A dispatch has no session, so it has nowhere to
  persist a phase. St1 runs dispatched turns at `Start` as a one-shot with no persistence, which
  degrades to pipeline behavior. Whether that is right, or whether a dispatch should get a synthetic
  session, is a St3 question.
