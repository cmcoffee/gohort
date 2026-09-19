# Notes that belong to a task

A scratchpad scoped to the work, not to the agent. Same store, same cap, same
tool: a different namespace and a lifetime that ends.

## The question this answers

> Working notes exist, but no agent ever seems to use them. Should the
> scratchpad be presented as a memory space for tasks instead, scoped per task?

Yes. What is wrong with the layer is not the content and not the cap. It is the
scope, and every defect it has traces back to a block that outlives the work it
describes.

## What is there today

`core/notes` holds one `OperatingNotes{Text, UpdatedAt, History}` row per
namespace in table `core_notes`, capped at 1500 runes, with a 3 deep history
ring and a section splice (`ApplyNoteSection`). Orchestrate passes it
`factsNamespace(agentID)`, which is `"agent:" + id` (`apps/orchestrate/facts.go:27`),
so the notes are per (user, agent) and permanent. `prependAgentContext`
(`apps/orchestrate/memory.go:53`) injects the rendered block into the SYSTEM
prompt, and `update_notes` (`apps/orchestrate/notes.go:164`) rewrites it.

Two places in the tree set `EnableNotes = true`: the first run assistant in the
wizard (`page_agent_wizard.go:753`), seeded with the "About you" answers, and
Anvil (`private/anvil/web.go:654`), seeded with build commands, what is half
done, and decisions already made.

## The evidence that the scope is wrong

**Both real users forged a scope the layer does not have.** Anvil rewrites
`agent.ID` per project so its notes land in a per project namespace, which is
why `AgentStateNamespace` had to be exported at all: the app scopes memory by
project and the panel has to address the same row the runtime wrote to.
Servitor does the same thing on the other axis, synthesizing a scope user
(`app:servitor:<applianceID>`) so each appliance gets its own memory against one
shared template.

Nobody wanted an agent wide scratchpad. Two apps wanted a per thing scratchpad
badly enough to build the scope by hand.

**The wizard's use is not a scratchpad at all.** "About you" is durable
personalization, which is what the fact layer is for. It renders full and stable
forever and is never rewritten, so the one high traffic agent carrying notes
demonstrates nothing about whether the layer works.

**The defects are all lifetime defects.** A stale note steers every turn with
nowhere to look (the reason the owner panel was built at v0.5.949). A parked
tool call outlives the tool it names (`project_tool_unreachable_improvisation`).
Notes not rewritten in 60 days need an audit rule to catch
(`memory_audit.go`, `stale_notes`). Each of those is a consequence of state with
no end, and each disappears when the scope ends with the work.

**The boundary against pinned facts is currently about write mechanics.** From
`RenderOperatingNotesBlock`: "the difference is not what they hold but what it
costs to REPLACE". That is true and it is a weak line to ask a model to hold.
Lifetime is a stronger one: a fact is kept, a task note is discarded when the
task is done, and the model does not have to be told which because the block
simply is not there afterwards.

## The model

> A task's notes are what this task knows about itself so far. They are written
> by the fire, read by the next fire, and deleted when the task is.

Nothing about the storage changes. `core/notes` already takes an arbitrary
namespace string, so this is a caller change:

```
agent:<agentID>            the layer today, per agent, permanent
task:recurring:<id>        per recurring task
task:standing:<id>         per standing agent
task:monitor:<id>          per event monitor
```

The surface is part of the key because ids are only unique within a surface, and
a collision would hand one task another's state. The row lives in the task
OWNER's per user store, the same axis facts use, so the two axis split in
`project_subject_scoped_memory` (whose memory versus whose run) is not disturbed:
this is the memory axis only.

## Not a new noun

An objective earned its place by being a field on records that already existed
rather than a fifth trigger kind, and the same discipline governs here. There is
no "create a memory space" step, no registry, no attach action, and no new table.
The namespace is DERIVED from the task's identity, so a task has notes the way it
has a name: by existing.

That is also the answer to the obvious alternative. A notes record with its own
identity would need a schema, a lifecycle, an owner, a picker, an audit surface
and a retention rule, to hold one string that is already held.

## Where it renders

**In the fire's volatile tail, beside `objectiveAttemptsBlock`, not in the
system prompt.**

The agent wide block rides the cached prefix, which is defensible for something
that rarely changes. A task note changes every fire by definition, and the tail
is where the objective block already lives for exactly this reason
(`objective_judge.go:384`: "the volatile tail that never caches anyway, so it
costs no prefix reuse").

The cost is real and worth naming: up to 1500 uncached runes per fire. That is
acceptable because a fire is not a chat turn. It happens once per interval, not
once per message, and the alternative is the fire rediscovering what the last one
already learned.

The two blocks are complementary and both belong:

- `[Objective: ...]` is the JUDGE's record. What was tried, the verdict, why.
- `[Notes: ...]` is the ATTEMPT's record. What it learned that the verdict has
  no room for: the command that works, the id it had to look up, the half
  finished state it is resuming from.

## The tool

The same `update_notes`, with the same section semantics, mounted BY THE FIRE.

This is the pattern `set_next_attempt` already establishes (`core/pacing`,
`docs/objective-pacing.md`): the lever belongs to the turn that can use it, and
it is never an action on `recurring`, which a Fleet agent never gets. The mount
points already exist and already carry the pacing tool:

- `pacingTool` (`objective_pacing.go:80`), recurring
- `standingPacingTool` (`objective_pacing.go:170`), standing agents
- `monitorPacingTool` (`objective_pacing.go:229`), mounted at
  `operator_wake.go:256` via `AppTools`

**One tool, one scope at a time.** When a fire is running under a task,
`update_notes` writes the TASK's notes and the agent wide block is not mounted.
Two scratchpads in one prompt with no rule for choosing between them is how the
model writes running state into the wrong one, and we already know it cannot
reliably pick between six memory layers without a paragraph per layer.

Cost is unchanged from today: a string splice, no judge, no embedding, no lock.
That is the property that makes this layer different from `store_fact`, whose
write path is an embedding plus a judge round trip on a 20 second timeout plus a
per namespace lock held across it. Per fire state cannot afford that, and the
fact gate would reject it anyway: `judgeFactWrite` is instructed to refuse
"anything EPHEMERAL ... transient state", which is precisely this content.

## Lifecycle

**Born with the task, seeded from its goal.** On first read, when nothing is
stored, `ResolveOperatingNotes` returns an ephemeral seed built from the task's
own `Until` line plus the empty section headings the work will fill. The cold
start that silently disables the agent wide layer today cannot happen here:
`RenderOperatingNotesBlock` returns "" for empty text, and nothing else in the
prompt mentions the scratchpad, so an agent with notes enabled and nothing
written is never told it has one. A task always has a goal, so it always has a
seed.

**Deleted when the task retires.** A met objective stops the task; the notes go
with it. This is what retires the `stale_notes` audit rule for this scope: there
is no old note to find.

**Kept when the task parks.** An unmet objective at its attempt cap parks,
visible and stopped, with its reason. The notes are the other half of that
explanation, and Resume needs them. They are deleted only when the parked task
itself is.

**Never copied.** A cloned task starts empty. Notes are what this run learned,
and the most likely thing in them is an id, a path or a state that belongs to
the original.

## The panel

The task's own card, not the agent Memory modal. You look at a task and you see
what it knows, next to its objective, its attempts and its next fire. That is
the whole invisibility fix: the layer had no owner surface for seven hundred
versions because it was filed under the agent, where nobody was looking for it.

The Memory modal keeps the agent wide panel for as long as the agent wide layer
exists (see Migration).

## Cap, sections and refusal

Unchanged. One document, 1500 runes, sections competing inside that budget,
an over cap write refused rather than truncated with the largest sections named
(`OverCapAdvice`). Everything in `docs/working-notes-sections.md` applies as
written; only the namespace and the render site differ.

The seed splice bug fixed once on the agent path applies here too and must be
kept: a section write splices into the RESOLVED notes, seed included, or the
first write silently discards the seed the task was set up with.

## Migration

Nothing to migrate. No existing row moves, no format changes, and the agent wide
namespace keeps working exactly as it does now.

The sequencing question is what happens to `EnableNotes` afterwards, and the
answer is: decide it with data, not now.

1. Build task notes on the objective bearing surfaces.
2. Live with them. The number that settles it is how often a task's notes are
   rewritten by its own fires, which `UpdatedAt` already carries.
3. If task notes fire and the agent wide layer still does not, retire
   `EnableNotes` rather than fixing it, and move the wizard's "About you" seed
   into the fact layer where that content belongs.
4. Anvil stays on its per project namespace either way, or moves to a
   `task:project:<id>` key if the lifetime rule turns out to fit a project.

Retiring a layer that never fired is a better outcome than patching its empty
state, and this ordering keeps that option open without betting on it.

## Staging

*(All five built, v0.6.911. See "What the build changed" above.)*

1. **The namespace.** `taskNotesNamespace(surface, id)` and the resolve/save
   helpers keyed by task, plus the seed builder from `Until`. No storage change.
2. **The render.** `[Notes: ...]` in the fire's tail beside the attempts block,
   at `standing_runner.go:76`, `scheduled_updates.go:531`, and
   `monitorWakeMessage` in `operator_wake.go` (whose own objective block landed
   first: see "The monitor gap" below).
3. **The tool.** `update_notes` mounted on the fire at the three pacing mount
   points, with the agent wide block suppressed for the duration of the fire.
4. **The lifecycle.** Delete on retire, keep on park, empty on clone.
5. **The card.** The notes on the task's detail, editable, with the same
   character counter and section sizes the Memory modal already serves from
   `notes.SectionSizes`.

Steps 1 through 3 are the feature. 4 and 5 are what keep it from becoming a
thing only the model knows about, which is how the agent wide version went seven
hundred versions without a panel.

## The monitor gap, found while specifying this (now fixed)

Event monitors carry objectives, keep an attempt history, and mount the pacing
tool, and their wake turn was told none of it. Verified before the fix:

- The wake prompt (`operator_wake.go:235`) is the event summary, plus
  `m.WakeBrief` as "What to do:", plus the react instruction. Nothing else.
  `WakeBrief` is independently authored and is never populated from `Until`.
- `objectiveAttemptsBlock` has two call sites, `standing_runner.go:76` and
  `scheduled_updates.go:531`. Neither is the monitor path.
- `monitorObjective(m)` is used only for UI labels (`console_goals.go:136`,
  `console_monitors.go:524`, `operator_tools.go:1929`). The attempt history is
  written by `settleMonitorObjective` (`objective_judge.go:220`) and read only
  by people, never back into a prompt.
- `pacing.ToolSpec` carries no goal text, and the shared description says "Move
  the NEXT attempt at this goal", so the agent is handed a lever that refers to
  a goal the prompt never states.

What this is NOT: the monitor judge reads the EVENT SUMMARY, not the woken
agent's turn (`settleMonitorObjective` passes `observed`), so nobody is being
graded on a sentence they could not see. The cost is narrower and still real: a
woken agent cannot tell a first sighting from a fifth, cannot see why the four
before it were judged "not yet", and is asked to decide when the next check is
worth making without either. "Do not repeat an attempt that already failed for
the same reason", which the other two surfaces carry, is absent here.

The monitor surface needed the objective block BEFORE it needed task notes: a
notes block on a surface that could not see its own attempt history would paper
over the older gap with a newer feature. So that half shipped first, separately.
`monitorWakeMessage` (`operator_wake.go`) now appends
`objectiveAttemptsBlock(monitorObjective(m))` last, matching both other
surfaces, with the assembly extracted from the wake closure so it can be tested
without standing up a tick.

**Still open, and deliberately not changed there:** the block is empty on a
first fire (`objectiveAttemptsBlock` returns "" with no attempts), so a
monitor's first wake still never states its goal. That is shared behaviour
across all three surfaces, and recurring and standing both restate the work in
their own prompt or mission while a monitor's `WakeBrief` is optional. Giving
the block a goal line on attempt one is a change to all three and wants its own
decision.

## What the build changed (v0.6.911)

Four decisions in the sketch above did not survive contact, and each is worth
recording because the reasoning is not obvious from the code.

**A recurring task has no stable id, so one had to be minted.** The namespace
above reads `task:recurring:<id>`, and the obvious id is the one the console
actions a row by. It cannot serve: a recurring task has no record, it lives as
its scheduler entry, and `ScheduleTask` mints a fresh UUID on every arm. So the
id a row carries names the next OCCURRENCE, and notes keyed on it would be
written by one fire where no later fire could look. `orchUpdatePayload` gains a
`UID`, minted at create and copied forward by every re-arm (an arm copies the
whole payload), and carried on `RecurringSpec` so an edit-in-place preserves it
the way it already preserves `CreatedAt`. Tasks armed before the field fall back
to `SessionID|CreatedAt`, the pair that has always travelled untouched.

**The owner is in the key.** `task:<surface>:<owner>:<id>`, not
`task:<surface>:<id>`. Standing agents and monitors are keyed by name within an
owner, so two people's schedules can share one.

**No seed.** The sketch seeded a task's notes from its `Until`. The build
renders a one-line EMPTY STATE instead, and stores nothing: notes are what runs
wrote, never a restatement of configuration. It closes the cold start for the
same cost (the first fire is told the register exists), and it avoids a block
that restates a goal the objective block already carries and that drifts the
moment a run rewrites around it.

**The store is RootDB, not the owner's per-user store.** A schedule is not a
per-user document: the records these notes belong to (`StandingAgent`,
`EventMonitor`) live in RootDB keyed by owner, and putting the notes beside them
means `DeleteStandingAgent` and `DeleteEventMonitor` can drop the row with the
handle they already have, covering all nine call sites at once instead of nine
places each remembering.

### What is wired

- `core/notes/tasknotes.go` — `TaskNamespace`, the surfaces, `RenderTaskNotesBlock`.
- `apps/orchestrate/task_notes.go` — the scope, the context carry, the tool, the
  per-surface identity, the owner's handler.
- The write path is SHARED with the agent-wide tool (`notesWriteHandler` in
  `notes.go`): same cap, same splice, same refusal, for the model and for the
  owner's Save alike.
- Mounted and rendered on all three fires: `scheduled_updates.go` (recurring),
  `standing_runner.go` (standing), `operator_wake.go` (monitor).
- Dropped on delete and on retirement; KEPT on park.
- `api/console/scheduler/notes` plus a Notes action on every Scheduler row.

## Tests worth pinning

Mostly what must stay ABSENT, in the house pattern:

- A fire with no notes renders no block, and a chat turn never mounts the tool.
- What fire N writes appears in fire N+1's tail, and not in another task's.
- The three surfaces cannot collide on a shared id.
- Retire deletes the namespace; park keeps it; clone starts empty.
- A section write against a seeded but unstored note keeps the seed.
- An over cap section write is refused with the largest sections named, and
  nothing is written.

## Explicitly out of scope

- **Per run notes.** A single dispatch or machine run usually still has its own
  transcript, which is more accurate than any summary the model writes about it.
  The scratchpad earns its place only where the context that produced the state
  is gone.
- **Sub agent inheritance.** A fire's dispatched sub agents do not write the
  task's notes. The fire owns the record of its own attempt; a sub agent
  reporting into it is the existing dispatch result.
- **Searchability.** Task notes are injected, not recalled. `recall` spans
  knowledge, finding, pinned and history, and adding a fifth layer to that
  surface is a different proposal with a different cost.
- **Concurrency.** `SaveOperatingNotes` has no per namespace lock (facts do),
  so this assumes one fire at a time per task. Worth checking on monitors before
  they poll fast enough to overlap.
- **Sections anywhere else.** Unchanged from `working-notes-sections.md`.
