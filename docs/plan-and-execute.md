# Plan and execute

Status: shipped v0.6.336. `extras/plan_and_execute.machine.json` +
`extras/plan_steps.pipeline.json`. Import the pipeline first, then the machine.

This is the answer to "should we add a built-in planning and execution pipeline". No new
primitive was added. The recipe is DATA — a machine and a pipeline, both authored in the
vocabulary that already existed — which was the test the exercise was really for: if the
planner could not be said in that vocabulary, the missing piece would have been worth more
than the feature.

## The shape

```
decompose  (transient, thinks, reach none)  Goal in one sentence, then 3-8 INDEPENDENT steps.
execute    (runs the pipeline)              All steps at once. Findings + blocked accumulate.
review     (transient, thinks, reach none)  Is this enough, or is one more pass worth it?
fill_gaps  (runs the pipeline)              The steps the results turned out to need.
report     (terminal, thinks, reach none)   Answers the goal; states what was not established.
```

The pipeline is three stages: read the plan as a list, fan out one worker per step, collect
what came back into findings / empty / blocked.

## The two decisions that matter

**The steps run in parallel, so the plan cannot be a sequence.** Every step has to stand on
its own and be answerable now. A step reading "then, using what step 2 found" comes back
empty, and the decompose prompt says so at length because it is the one way to write this
plan wrong. Work that genuinely has a second layer gets it from `review` → `fill_gaps`,
which is exactly one pass: the plan is allowed to be shallow because the pass exists, and
the pass is bounded because otherwise a machine with a re-entrant step will happily spend an
afternoon deepening a plan nobody is waiting on.

**What could not be done survives to the report.** `blocked` accumulates alongside
`findings`, and the report step is told to end with what was NOT established, plainly
labelled. An answer that quietly omits its gaps reads exactly like one that had none — the
same reason the work-plan tool group in core has a gap report at all (see `core/plan.go`).

## Why a machine AND a pipeline

They are not interchangeable here, and the split is the clearest statement of what each
primitive is for:

- A **pipeline** is dataflow. It can fan one stage over a list and run the branches at once,
  which is the whole muscle of this recipe, and it cannot hold a working set.
- A **machine** holds the working set. `accumulates` merges both passes' findings into one
  list under one name, and the report reads `{state:findings}` without knowing which pass
  produced what.

So the machine is the spine and the pipeline is the muscle — `docs/machine-as-spine.md`
argued for exactly this, and this is the first shipped recipe that uses it.

## What it needs to be useful

Tools. The planning and reporting steps deliberately reach for nothing; all the looking
happens inside the pipeline's fanout, which inherits whatever the caller carries. Run it from
an agent, a schedule or a dispatch with a real tool pool, or every step reports NOTHING
honestly and at length.

If the pipeline is missing, the `execute` step does not fail — the broken-dependency posture
runs it inline and leaves a breadcrumb. That is right for a recipe carried between
deployments and wrong here: the run would work every step in one prompt, in sequence, and
read like it worked. `TestShippedMachinesNameAShippedPipeline` is the guard on our side;
importing the pair together is the guard on yours.

## Not done yet

The live checklist card in a turn-free run. A conversation with the Tracked-plan toggle draws
its plan as it moves (`apps/orchestrate/work_plan.go`); a schedule or dispatch has no SSE, and
`PipelineEvent` carries only text, so the run panel cannot draw one until that event gains a
data field. The run tracks its plan correctly either way.
