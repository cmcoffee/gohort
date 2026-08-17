# Pipeline surfaces — the list, the page, and why they match the machine editor

A pipeline had no UI at all. It was authored from chat through the `pipeline` grouped tool and
attached from a picker, so "what pipelines do I have, and is any of them live" had no answer outside
asking an agent — while `list`, `export`, `import`, `delete` and `run` all existed in the HTTP layer
with nothing calling them. This is what closed that (v0.6.221–231), and the decisions worth knowing
before changing it.

Its sibling is [agent-machines.md](agent-machines.md). A machine is a workflow a conversation SITS
in; a pipeline is one that runs to an end and returns a result. They are edited the same way on
purpose, and they differ in four places on purpose.

## The index (Extensions → Pipelines)

Under Machines, because they are the same kind of thing to somebody looking for one.

Each row says what the pipeline is MADE of — `2 worker, 1 fanout · 1 worth a look` — and **who can
call it**. That second column is the point: a pipeline reaches an agent as a tool named
`run_<name>`, so one attached to nothing is inert, exactly as an unattached machine is. Both facts
are computed server-side (`pipelineRow`) rather than in the page, so a row says the same thing
wherever it is shown.

Three ways in, on one line, because they are alternatives rather than steps:

- **New pipeline** mints `core.StarterPipeline` and lands you in it. It runs as written, uses no
  tools so it works in any deployment, and teaches the rule that is easiest to get wrong by
  demonstrating it: a later stage reads an earlier one by name (`{stage:plan.focus}`). A test asserts
  it validates AND carries no findings — a starter with a finding teaches the finding.
- **Describe one…** drafts from a paragraph, on its own page rather than in a dialog. That is not a
  style choice: `ui.ModalButton` opens a native `<dialog>` with `showModal()`, which renders in the
  browser's TOP LAYER above every z-index, so a failure toast raised inside one is invisible
  (v0.6.220). Drafting runs a model for up to a minute and can come back with nothing runnable, so it
  is precisely the door that must be able to say why.
- **Import…** reads a `.pipeline.json` recipe. A file field reads the file as TEXT in the browser and
  submits its contents, so this is a form rather than an upload path.

Row actions: **Assign** (below), **Export**, **Delete**.

## The page (`/orchestrate/pipeline?id=…`)

Sections in the SAME ORDER as the machine editor, asserted by a test that renders both pages and
compares them as sequences: identity, the wholesale change, the parts, adding one, trying it, what it
costs, who gets it, what is worth a look. They had drifted into two different orders within a day of
each other, which is what happens when a second surface is built to match a first without anyone
rendering both side by side.

`SectionNav` is on, so one section shows at a time and the rail IS the stage list.

**The picture is pinned above the sections**, not parked in one of them — with SectionNav a diagram in
its own section can never be on screen with the stage being read, and a branch or a fanout is exactly
what a stage's own form cannot show. Every box links to its stage's section, addressed by the slug
the rail computes from the section titles; `stageSectionTitle` is the one place that title is written,
so the rail, the anchor and the box cannot disagree. See
[workflow-graph.md](workflow-graph.md) for what the three hard shapes (fanout, branch, loop) are drawn
as and why.

## The per-stage form

One form per stage, with the controls that stage actually has and only those — a fanout has a
`fan_over` and a branch does not, and a form showing every field to every stage teaches that they are
all the same thing.

Two rules carried from the machine editor, both learned there the hard way:

**Saves MERGE.** A panel holds part of a stage, and posting the whole record from a form that only
loaded some of it is how a save clobbers what it never showed. Tested by sending one field and
asserting the kind, the `fan_over` and the tools all survive.

**A hidden control must not be holding a value.** Fields gate on kind, but a stage carrying something
its kind does not use keeps that control visible so it can be emptied (`keepWhileSet`). This matters
more here than on machines: a pipeline's validator REFUSES what it cannot resolve, so a hidden
`fan_over` on a stage that is no longer a fanout is a save nobody could make succeed.

What stays a card rather than becoming a control: the **declared output contract** and a **loop's
body**. Both are structures the `pipeline` tool writes well and a hand-rolled control here would write
badly — the same judgement that kept the machine editor a JSON box until the answer was known. The
page says so where the facts are shown rather than leaving the absence unexplained.

### Renaming rewrites every reference

`PipelineDef.RenameStage` walks prompts, `fan_over`, `until`, `when`, `skip_to`, tool args, filled
output fields, and INTO loop bodies (a body stage can read what ran before the loop). Without it a
rename is refused by the validator for a reason the author did not cause and cannot fix, because the
offending reference lives in another stage's prompt. `renameBare` keeps what follows a condition, so
`plan.done == true` survives with its comparison, and a FIELD that happens to share the stage's name
is left alone.

### Removing refuses, and says what is in the way

`RemoveStage` refuses while anything still reads the stage, naming every reader with the place it
reads from — `dig (fan_over)`, `answer (prompt)`, `calc (args.x)`.

This is the deliberate divergence from machines. A machine's `RemoveStep` silently drops every
reference to the deleted step, because those references live in FIELDS and dropping one is
unambiguous. A pipeline's live in PROSE, and rewriting somebody's sentence so a delete can succeed is
a worse answer than saying which sentences are in the way.

## Describe a change, and undo

Say what should be different and the pipeline is redrafted with that change made, keeping everything
it does not touch. It reuses the drafter, the decoder (`parsePipelineStages`) and the repair
discipline, so a revised pipeline cannot be a third dialect. The ID does not move, so every agent
that attached it keeps its `run_<name>` tool.

The reply names what changed in STAGES — added, removed, changed (instructions) or changed (wiring) —
never the word "revised", because a model that ignored the instruction and one that followed it
produce that word identically. That reporting earned itself during the build: the first version of
its test omitted a stage's `"model": "lead"` from the stub reply and the report caught it.

`PipelineDef.Previous` holds what a revision replaced, exactly one deep, set only by this door,
stripped on export, and undo is one step rather than a toggle. Every other control on the page changes
the field it names; this one can rewrite every prompt, and the prompts are the part somebody actually
wrote.

## Run it

`ui.PipelinePanel`, pointed at the pipeline's own `stream` / `sessions` endpoints — the framework's
panel, not a Run box. A custom app backed by a pipeline already mounts exactly this, so a pipeline
gets the streaming transcript, the run history, bulk pruning and cancel by pointing at its own
endpoints instead of growing a second, worse copy of all four.

The input field is named `topic` because that is what `PipelineRunInput` reads (it accepts
`input|topic`, and the panel titles a run from it) — a field named for the form rather than for the
endpoint submits into nothing.

And it says what it is. A machine's *Try it* is a REHEARSAL: no tools, and the step it lands in is
never run. This is a real run that spends model calls and reaches whatever tools its stages name.

## Assign to agents

Same word on the page and on the list, and the same control My tools uses (`uiRenderScopePills`).
The two kinds assign DIFFERENTLY and the pills say so where the switch is, because getting it
backwards is how somebody unplugs something they never touched:

- an agent runs ONE machine, so a pill going on MOVES it, and an agent already running something else
  is labelled with what it would leave (`Busy (runs Other)`);
- an agent holds a LIST of pipelines, so a pill going on ADDS this one and leaves the rest alone.

## What a run costs

Derived, not written: the definition knows which stages call a model and which do not, and a fanout or
a loop turns one line of a stage list into twelve calls. The fanout number comes from
`core.FanoutMaxItems` rather than a second copy of `12` that goes stale the day the cap moves.

## What pipelines deliberately do NOT have

- **No checklist ("What is still missing").** `Validate` REFUSES rather than reports, at every door —
  the stage form, draft, revise, import, and the tool. A stored pipeline therefore has no outstanding
  problems to list. Machines keep imperfect drafts because their checklist is where problems belong.
- **No repair button.** Same reason: there is no stored broken state to mechanically settle.
- **Advice, though, is shared.** `PipelineDef.Advice` reports the one finding whose fix is prose (a
  prompt hand-rolling the JSON its declared fields already produce), using the same sentence machines
  use — see [pipeline-structured-outputs.md](pipeline-structured-outputs.md), which also records a
  second advice rule that was built, tested against the pipelines this repo ships, and removed.

## Files

| File | What |
|---|---|
| `apps/orchestrate/pipeline_page.go` | the extension section, the page, the map, the describe form |
| `apps/orchestrate/pipeline_editor.go` | per-stage form fields, the stages endpoint, Assign, cost |
| `apps/orchestrate/pipeline_revise.go` | describe-a-change, undo, and the drafter |
| `core/pipeline_edit.go` | `RenameStage`, `RemoveStage`, `StageReferences` |
| `core/pipeline_graph.go` | `PipelineDef.Graph` — the adapter for the shared renderer |
| `core/pipeline_advice.go` | the soft findings |
| `apps/orchestrate/pipelines_http.go` | the collection/item routes, `pipelineRow`, duplicate |
