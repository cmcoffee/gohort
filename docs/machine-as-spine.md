# Machine as spine, pipelines as phases

**Status:** Changes 1 and 2 BUILT (v0.6.270), Change 3 BUILT (v0.6.271),
Change 4 BUILT (v0.6.273) with one part outstanding (see below); Change 5 is
spec. An unattended run can be STARTED from the machine page ("Run it",
v0.6.274), synchronously; a background door (a schedule, or a dispatch target)
is still to come and can lean on the same RunUnattended. Related: `core/machine.go`, `core/machine_def.go`,
`apps/orchestrate/machine_delegate.go`, `docs/agent-machines.md`,
`docs/pipeline-fanout-body.md`, `project_agent_machines`,
`project_pipeline_framework_future`.

Deep research is 22 phases (`private/research/pipeline.go:80-220`) that mutate a
working set: `Subs`, `Answers`, `Unanswered`, `LandscapeSources`. Phases mark,
refill, promote, and merge into those lists as the run proceeds.

A pipeline cannot hold that. It is dataflow: stage outputs are immutable, keyed
by stage name, and a reference to a loop body stage from outside is rejected
precisely because there is no accumulator for it to mean. Giving pipelines a
mutable working set would put a second state model in the framework.

A machine already has the state model. `MachineState` is a blackboard persisted
on the session, control flow is durable across turns, and `PhaseRunner` is a
seam that lets something other than an inline worker run a phase. What a machine
lacks is muscle inside a phase: fan over sub-questions, run several steps per
item, collect structured results.

So: **the machine is the spine and holds the working set; a phase may be run by
a pipeline; the pipeline does the parallel work and hands back structured
fields.** This spec is the five changes that makes that true.

## What already exists

- `MachineState map[string]PhaseResult`, one entry per phase, `{Text, Fields}`,
  persisted on the session (`core/machine.go:31-39`).
- `walk()` runs transient phases in a chain until one the conversation waits in,
  writing each result to the blackboard as it goes (`core/machine.go:207`).
- `PhaseRunner`, the seam, plus a worked example of using it: a phase naming an
  agent is dispatched instead of run inline, and the file that does it says the
  rule out loud, "delegation is a different runner, not different machinery"
  (`apps/orchestrate/machine_delegate.go`).
- Pipelines with `fanout`, `loop`, `branch`, `tool`, structured output, per
  stage tier and tools, guardrails, and a session store.
- A pipeline is already a dispatch target with an ACL (v0.6.265 to v0.6.268).

Five things are missing.

## Change 1: a phase may name a pipeline (BUILT)

```go
// Pipeline names a stored pipeline that runs this phase. Mutually
// exclusive with Agent: a delegate is another AGENT (its own persona,
// tools, memory), a pipeline is a RECIPE (fanout, loop, declared
// stages). Empty keeps the phase inline.
Pipeline string `json:"pipeline,omitempty"`
```

Runner, mirroring `runDelegatedPhase`:

- Input is the composed phase prompt, the same composition every phase gets
  (`def.phasePrompt`), so an author writes one kind of prompt.
- Output is the pipeline's final text.
- **One call, not two, when the shapes line up.** A delegate costs two calls
  because an agent answers in prose and the phase's own worker then shapes that
  into the declared fields. A pipeline whose terminal stage declared an `Output`
  is already structured, so when the phase declares the same field names the
  fields map straight across. Fall back to the delegate's shaping call only when
  they do not.
- Missing pipeline is a broken dependency, not an error: run the phase inline
  and say so, exactly as a missing delegate agent does today. A machine is
  portable and the pipeline it names may not exist in this deployment.
- ACL: the machine's OWNER is the authority, matching the delegate. A phase is
  authored, reviewed, and saved; it is not a model at runtime choosing a target,
  so the dispatch allow-list that governs `agents(run, pipeline=...)` is the
  wrong gate here. Worth stating in the code, because the two paths will look
  similar to the next reader.
- Status: `phaseStatusLine(ph)` first, then the pipeline's own stage status
  lines nested under it. The panel already renders both shapes.

## Change 2: unattended runs (BUILT)

This is the actual blocker, and it is not obvious from the outside.

A machine is built for turn-taking. `walk()` stops at the first resident phase,
`MaxPhaseTransitions` caps a turn at 4 hops, and `Problems()` refuses a machine
with no resident phase at all ("a machine with nowhere for a turn to land is a
pipeline, not a machine"). Research is 22 phases with nobody waiting.

So a machine needs a second way to run:

```go
// Unattended marks a machine that RUNS rather than converses: it is
// started once, walks its phases to a terminal one, and produces a
// result. No resident phase is required (and one is refused: there is
// nobody to wait for). Bounded by MaxPhases, wall clock, and cost.
Unattended bool `json:"unattended,omitempty"`
```

- The resident requirement inverts: an unattended machine must have at least one
  TERMINAL phase (no `next`, no `choices`) and may have no resident phase.
- The hop budget comes from the run, not the turn: `MaxPhaseTransitions` (4) is a
  conversational courtesy, meaningless here. Replace with a per-run ceiling
  (100 is a reasonable first number, since research is 22), plus wall clock and
  a token ceiling, refused at save time if the graph can cycle without a guard
  that reads a declared field.
- Entry points are a schedule, a standing agent, an app page, or a dispatch, not
  a chat turn. The "turn" is the request that started it, and `{original_input}`
  is that request.
- Progress is the status stream that already exists. The transcript is the
  phase results, which is what `MachineState` already holds.
- The result is the terminal phase's output, which is what a dispatcher or a
  schedule collects.

Without this change the rest of the spec is decorative: research cannot run as a
machine at all, whatever its phases are made of.

## Change 3: accumulators on the blackboard (BUILT)

`MachineState` is keyed by phase, one entry each. Research needs lists that
MANY phases contribute to: `TrackEmptyAnswers` marks, `RetryEmptyAnswers`
refills, `ApplyPromotions` rewrites, `mergeGapResults` appends a child's
findings to the parent's own.

Phase-keyed state cannot express that. A phase can read `{state:PHASE.field}`,
but there is nowhere for "the answers so far" to live.

```go
// Accumulates declares the run-scoped lists this phase contributes to,
// and how. Written to MachineState under the accumulator's name rather
// than the phase's, so many phases can add to one list.
//
//   name        the accumulator
//   from        this phase's declared output field to take
//   mode        append | replace | union   (union keys on `by`)
//   by          field name, for union
Accumulates []MachineAccumulator `json:"accumulates,omitempty"`
```

Declared rather than free-form, for the reason every other part of this
framework is declared: a phase that changes the working set says so on its own
record, the editor can show which phases touch which list, and Validate can
refuse a reference to an accumulator nothing writes. A free-form
`{state:answers}` write would be shorter to build and impossible to read.

Interaction with `Keep` (which prunes prior findings on re-entry) has to be
explicit: accumulators are NOT pruned by `Keep` unless named, because the whole
point is that they survive the loop that keeps revisiting them.

## Change 4: a child run, for the recursive case (BUILT, with one part outstanding)

`FillGaps` forks a child research run per detected gap and merges what comes
back. That is the one phase whose shape is recursion rather than composition.

The narrow version, rather than a general recursion facility:

```go
// Machine names a machine this phase runs as a CHILD, with its own
// state, whose result merges back through Accumulates. Depth 1 by
// default: a child may not itself run a machine.
Machine string `json:"machine,omitempty"`
```

- Depth is capped, and the cap is a number rather than a boolean, so a later
  case that genuinely needs two can raise it without a redesign. Research's own
  guard is a `ParentID` check that stops a child from filling gaps again, so
  depth 1 is exactly what it uses today.
- The child's terminal output merges into the parent through the same
  `Accumulates` declaration as any other phase. There is no second merge
  mechanism.
- Fan of children (one per gap) is the pipeline's job, not the machine's:
  the phase names a pipeline whose fanout body runs the child machine per item.
  Which is why `docs/pipeline-fanout-body.md` is a prerequisite for this one.

**What landed (v0.6.273):** `MachinePhase.Machine`, run through the same
`PhaseRunner` seam as the delegate and the pipeline phase. The child must be
`Unattended`, gets a fresh cursor and its own blackboard, and its result becomes
the phase's result, so the phase's own `Accumulates` folds it into the parent's
working set with no second merge mechanism. Depth rides the CONTEXT
(`WithMachineDepth` / `MachineDepth`, cap `MaxMachineDepth = 1`), because the
child runs through the host's same runner and the depth has to travel with the
call rather than with the caller.

**What is outstanding:** a phase runs ONE child. Fanning children (research's
gap filler starts one run per gap, in parallel) needs a pipeline stage kind that
can run a machine, so a fanout body can be a child run. Today the same work is
expressible SEQUENTIALLY, by routing back to the child-running phase once per
item, which is correct but not parallel. The missing piece is small and belongs
with the fanout body work rather than here.

## Change 5: run-scoped tool state

`SearchCache` dedupes identical searches across a research run, and
`filters.go` / `isWeakSource` drop junk consistently. Both are library code
inside the app today.

Eight parallel fanout branches searching the same landscape pay eight times for
the overlap. A run-scoped cache keyed by (tool, args) with the run's lifetime,
owned by the framework and consulted by tool dispatch, removes that without any
tool knowing about it. Source quality is a filter a `web_search`-family tool
should apply, not a framework concern.

This is the smallest of the five and the only one that pays for itself
immediately in cost.

## What this does not change

The presentation. `researchBridge` translates run events into debate clusters,
framing placeholders, and per-round headings; the descendants view shows a run's
children; the email has its own formatting. That is roughly the 4k of
`private/research/web.go`, and it stays app code. A custom app's `html` section
can render the finished record, but typed blocks streaming DURING a run are an
app-level concern by design (see `CLAUDE.md`: the four registries).

## Order, and what each unlocks

| Step | Unlocks | Depends on |
|---|---|---|
| Fanout bodies (`docs/pipeline-fanout-body.md`) | per-item multi-stage work, structured `items` | nothing |
| Change 2, unattended machines | any long non-conversational run | nothing |
| Change 1, pipeline phases | the spine calling the muscle | fanout bodies (to be worth it) |
| Change 3, accumulators | a working set that survives 20 phases | Change 2 |
| Change 4, child runs | `FillGaps` | Changes 1 and 3 |
| Change 5, run cache | cost | nothing |

Changes 2 and 5 are independently useful and could land first. Change 3 is the
one that decides whether this is a spine at all: without accumulators the
machine is just a router, and the working set goes back to being app code.

## Validation

- A phase may name at most one of `agent`, `pipeline`, `machine`.
- An unattended machine must have a terminal phase and must not have a resident
  one; a conversational machine keeps today's inverse rule.
- `Accumulates.from` must name a field the phase declares.
- A `{state:NAME}` reference to an accumulator nothing writes is refused.
- A child machine may not name a child machine (depth cap).
- An unattended machine whose graph can cycle without a guard reading a declared
  field is refused, since the run budget is a backstop and not a design.

## Testing

- Unattended run of a 6-phase machine reaches its terminal phase and returns
  that phase's output.
- The per-run hop ceiling fires and reports which phase it stopped at.
- A pipeline phase whose pipeline declares matching fields costs ONE call, and
  the fields land on the blackboard.
- A pipeline phase whose shapes do not line up falls back to the shaping call.
- A named pipeline that does not exist runs inline and says so.
- Three phases appending to one accumulator produce one list in run order.
- `Keep` prunes phase results but leaves accumulators alone.
- A child machine's result merges through `Accumulates`; a child naming a child
  is refused at save time.

## Files

- `core/machine_def.go`: `Pipeline`, `Machine`, `Unattended`, `Accumulates`,
  validation.
- `core/machine.go`: the unattended walk and its budget, accumulator writes.
- `apps/orchestrate/machine_delegate.go`: the pipeline runner beside the agent
  one (same file, same seam).
- `apps/orchestrate/machine_editor.go`: controls for the new fields, which is
  also where the missing body editor lives (`docs/pipeline-fanout-body.md`).
- `core/tool_cache.go` (new): run-scoped tool result cache.
