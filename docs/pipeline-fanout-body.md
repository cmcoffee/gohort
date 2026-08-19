# Fanout bodies

**Status:** BUILT v0.6.272. The per-branch block mode and the nested-stage
EDITOR remain unbuilt: bodies are authored through the `pipeline` tool, an
import, or a revise, which is the same gap `loop` bodies already had. Related: `core/pipeline_def.go`, `core/pipeline_interp.go`,
`docs/pipeline-structured-outputs.md`, `project_pipeline_loop_stage`,
`project_pipeline_framework_future`.

A fanout runs ONE call per item. A loop runs MANY stages per pass. Nothing runs
many stages per item, so the shape "investigate each of these properly, then
compare what came back" has no expression in a pipeline: you get one prompt per
branch and a joined blob of prose at the end.

This spec adds one thing: **a fanout stage may carry a `Body` of stages, run
once per item, with per-branch scope and a structured collection of results.**

## Why this is the gap that matters

`docs/pipeline-structured-outputs.md` made stages typed, which is what let
`loop`, `branch`, `fan_over NAME.field` and `tool` exist. Composition is the
piece that never landed. Today:

| Want | Today | With this |
|---|---|---|
| One question per sub-question | `fanout` with a prompt | unchanged |
| Search, read, then judge each sub-question | not expressible | `fanout` + 3-stage body |
| Rank what the branches found | prose scoring off a joined blob | a declared list, scored by a normal stage |
| Re-fan over the survivors | not expressible | `fan_over: NAME.items` |

The multi-role debate shape is already expressible (roles are `agent` stages,
rounds are `loop`, a judge panel is `fanout`, the closer is `synthesize`). The
research shape is not, because every branch of a research fan is itself a small
pipeline. This closes that half. It does NOT close recursion; see
"What this does not solve".

## Data model

`PipelineStage.Body` already exists (loop owns it). Fanout starts using it:

```go
// Body, on kind=fanout, is the pipeline run ONCE PER ITEM. Mutually
// exclusive with Agent: an agent branch is a single dispatch, a body
// branch is a composite. Empty Body keeps today's single-prompt fanout.
Body []PipelineStage `json:"body,omitempty"`
```

No new field. The validation at `pipeline_def.go:491` ("body is only valid on
kind=loop") becomes "body is valid on kind=loop and kind=fanout".

## Per-branch scope

This is the whole difficulty, and it is why the change is not a two-line
condition flip.

`pipelineRun.outputs` is one shared map. A loop can write into it because passes
are SEQUENTIAL: pass 2 overwrites pass 1 and the carry threads forward. Fanout
branches run in PARALLEL under `NewLimitGroup(fanoutParallel)`, so a shared map
is both a data race and a semantic collision (branch 3's `analyze` would
clobber branch 1's while branch 1 is still reading it).

So a body branch runs against a CHILD scope:

```go
// forBranch returns a run view whose stage outputs are its own. Seeded
// with a copy of the parent's outputs so a body stage can read anything
// established before the fanout; writes stay local, so two branches
// running the same stage name never see each other.
func (r *pipelineRun) forBranch(item string, idx, total int) *pipelineRun
```

- `outputs`: shallow copy of the parent's map at fan time. Small (one entry per
  completed stage), copied once per branch, and it makes the isolation
  obvious at the type level rather than by convention.
- `vars`: parent vars plus `{item}`, `{branch}`, `{branches}`.
- `blockSeq`: SHARED (it is already an `atomic.Int64` whose comment says "unique
  across parallel fanout branches", so the id space was built for this).
- `sink`, `dispatch`, `inherited`, `app`, `input`: shared.

Body stages are then just `r.forBranch(...).runList(ctx, stage.Body, prev,
label, vars)`, which is the same call the loop makes. The interpreter grows a
scope, not a second execution path.

## What the fanout produces

Two things, one of which is new.

**Text (unchanged).** The same joined block as today, `## Item N: <item>` per
branch, which is what `{prev}` and `{stage:NAME}` read. Back-compatible by
construction: a fanout with no body produces exactly what it produces now.

**Fields.items (new).** When every branch's LAST body stage declared an
`Output`, the fanout also exposes a list:

```json
{"items": [
  {"branch": 1, "item": "how does X fail under load?", "finding": "...", "confidence": 0.8},
  {"branch": 2, "item": "who owns X?",                 "finding": "...", "confidence": 0.3}
]}
```

Each element is the terminal stage's declared fields plus `branch` (1-based) and
`item` (the element it ran on). `stageOutput.Fields` is already
`map[string]any`, so this needs no type change.

That single addition is what makes merge and scoring ordinary work:

```yaml
- name: dig        # fanout, body of 3 stages, terminal stage declares {finding, confidence}
- name: rank       # worker, reads {stage:dig.items}, declares {keep: []string}
- name: deepen     # fanout, fan_over: rank.keep
```

`Output` stays INVALID on the fanout stage itself. The shape is declared in one
place (the body's terminal stage) and inherited, so there is no second contract
to keep in sync, and a body whose last stage declares nothing simply produces
no `items` (text only, exactly like today).

## Templating inside a body

| Variable | Value |
|---|---|
| `{item}` | this branch's element (same as today's single-prompt fanout) |
| `{branch}` / `{branches}` | 1-based index, and the item count. Mirrors `{iteration}` / `{iterations}` |
| `{prev}` | previous BODY stage's output; the first body stage gets the fanout's incoming `{prev}` |
| `{stage:NAME}` | a body stage of this branch, else a stage that ran before the fanout |
| `{input}`, form vars | unchanged, run-scoped |

## Validation

Added to `PipelineDef.Validate`:

1. `body` is valid on `loop` and `fanout` (relaxes the current rule).
2. A fanout may not set both `body` and `agent`. One is a composite branch, the
   other is a single dispatch, and silently preferring one hides the mistake.
3. A body stage may not itself carry a body. **Depth is capped at one.** A
   fanout of loops of fanouts is a cost explosion nobody authored on purpose,
   and one level is what this spec claims to be worth. Relaxing later is a
   validation change, not a redesign.
4. Inside a fanout body, a `branch` stage may skip forward but may not end the
   pipeline. Same rule loop bodies already carry, same reason: half the branches
   deciding to end a run that the other half is still executing is not a
   meaning anyone can hold.
5. Nothing AFTER the fanout may reference a body stage by name. Same rule as
   loop, stronger reason: after a loop such a reference means "whatever the last
   pass left", after a fanout it means "whichever branch won a race".
6. `fan_over` must still resolve, and (unchanged) a fanout may not declare
   `output`.

## Cost, and saying so

A body multiplies. `items` is capped at `fanoutMaxItems` and concurrency at
`fanoutParallel`, both already enforced, but the model-call count is now
`items x len(body)` rather than `items`.

The status line says the multiplication BEFORE it runs, in the same place the
existing one reports branch counts:

```
fanout dig: 8 branches x 3 stages = 24 model calls (parallel 6)
```

`PipelineDef.Advice` gains a matching note when the product exceeds a threshold
(50 is a reasonable first line), phrased as a suggestion, since a deliberate
24-call fan is a legitimate thing to author.

## Transcript

One block per fanout stage, as today. Body stages emit STATUS only.

N branches x K stages of individual blocks would bury a transcript that
currently reads as one entry per stage, and the joined block is the artifact
somebody wants to read. A per-branch block mode (`blocks: "per_branch"`) is an
obvious follow-on and deliberately not in this spec.

## Errors

Unchanged in shape: a failing body stage fails THAT BRANCH, which records
`(branch error: ...)` in its slot and contributes no entry to `items`. The rest
proceed. One bad search should not sink the fan, and a partial fan whose gaps
are visible in the joined text is more useful than an aborted run.

## Compatibility

- A fanout with no `Body` behaves identically. Every existing pipeline is
  unaffected, and `items` simply does not appear.
- Stored defs need no migration: `Body` is already on the struct and already
  round-trips through JSON and the `pipeline` tool.
- `docs/pipeline-surfaces.md` and the `pipeline` tool's help text need the new
  shape described, since the tool is how bodies are authored today.

## Authoring surface

Worth stating plainly: **the pipeline editor cannot author a body at all.**
`applyStageEdit` handles no `body` key, so loop bodies today come from the
`pipeline` tool, an import, or a revise. Fanout bodies would inherit that gap
rather than create it.

A body editor (nested stage rows inside a stage panel) is real work and serves
`loop` equally, so it belongs in its own change. Until it exists, this feature
is reachable to Builder and to import, and invisible on the page. That is an
acceptable first landing, and it should be a stated known-gap rather than a
surprise.

## What this does not solve

Recursion. A research pass that finds a gap, forks a child investigation, and
lets THAT child fork again is a tree of unknown depth. Depth-capped fanout
bodies give one level of composite branching, which covers "investigate each
sub-question properly and merge", and does not cover "keep going until the
question is answered".

The Phase 2 primitive for that is a `pipeline` stage kind: run a NAMED pipeline
as a stage, with a depth counter carried in the context and a hard ceiling.
That is a different change with its own risks (cycles, budget, whose dispatch
ACL applies), and pretending this one covers it would set up exactly the
disappointment worth avoiding.

## Testing

- Two branches whose body stages share a stage name: each reads its own value.
  This is the test the whole scope design exists for.
- A body stage that reads a stage established BEFORE the fanout: resolves.
- A stage after the fanout that names a body stage: rejected by Validate.
- Terminal stage declares output: `items` has one object per surviving branch,
  carrying `branch` and `item`.
- Terminal stage declares nothing: text only, no `items`, no error.
- One branch's middle stage errors: that branch shows the marker, the others
  complete, `items` is short by one.
- `fan_over` a previous fanout's `items` field: re-fans.
- Depth: a body containing a body is rejected at save time.
- Cost line reports `items x len(body)`.
- Back-compat: an existing body-less fanout produces byte-identical output.

## Files

- `core/pipeline_def.go`: validation rules 1 to 6, advice note.
- `core/pipeline_interp.go`: `forBranch`, `runFanoutStage` body path, `items`
  collection.
- `core/pipeline_structured_test.go` (or a new `pipeline_fanout_body_test.go`):
  the list above.
- `apps/orchestrate/pipeline_tools.go`: the `pipeline` tool's schema and help.
- `docs/pipeline-surfaces.md`: the authoring surface note.
