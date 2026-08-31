# Evals — a suite you can attach to anything, with results that survive

Status: **design / target** (not built).

gohort authors itself. Builder writes agents, tools, pipelines and apps; a
prompt gets edited, a stage gets a tier, a tool description gets rewritten.
Every one of those is a claim that something got better, and the only
instrument for checking is somebody noticing later.

There is already an eval feature and it is better than it looks. What is
missing is not the grading — it is that grading is welded to one primitive and
throws its answers away.

## What exists, and what is actually wrong with it

`EvalCase` grades far more than text. It has substring assertions
(`must_include` / `must_not_include`), **tool-call trace** assertions
(`must_call_tools` / `must_not_call_tools`, read from the actual trace so
"narrated it but never called the tool" is caught), an LLM judge criterion, and
`stub_results` for scripting what each tool RETURNS so a multi-step scenario
behaves like production without the side effect. `EvalResult` carries a pass
RATE over N runs rather than a boolean, which is the right shape for a
non-deterministic model.

None of that needs redesigning. Three things around it do.

**1. It is a field on `AgentRecord`.** `Evals []EvalCase` exists on exactly one
type. A pipeline, a tool, a machine, an app and a skill cannot be graded at
all — and a pipeline is the primitive most likely to regress silently, because
its output is five prompts deep.

**2. Results are never stored.** `RunAgentEvals` returns `[]EvalResult` to
`handleAgentEval`, which writes them to the HTTP response. Nothing persists.
So "did that edit help?" is not a hard question, it is an unanswerable one:
there is no before.

**3. There is no run surface.** It is a POST that returns JSON. No history, no
transcript, nothing to reopen.

## The shape

A suite becomes its own record, owned like every other authored thing, naming
what it grades:

```go
// EvalSuite is a saved set of cases plus the thing they grade.
type EvalSuite struct {
	ID      string
	Owner   string
	Name    string
	Desc    string
	// Target is what gets run. Kind decides how a case's input is delivered
	// and what "the output" means; ID resolves within the owner's records.
	//
	// The kind is stored rather than inferred from the ID, because ids are
	// unique per KIND and a suite that guesses would grade whatever it found
	// first the day somebody names a pipeline after an agent.
	TargetKind string // "agent" | "pipeline" | "tool" | "machine"
	TargetID   string
	Cases      []EvalCase
	// Runs is how many times each case runs. The pass RATE is the signal; a
	// single pass on a non-deterministic model is an anecdote.
	Runs int
	// Stub scripts tool RETURNS instead of executing them. Default TRUE, and
	// the default is the whole safety story: an eval that runs for real sends
	// the emails, files the tickets and spends the money, every time anybody
	// clicks Run. Turning it off is a deliberate act on a suite that needs it.
	Stub bool
	Created, Updated time.Time
}
```

### Targets, and what a case means for each

The case vocabulary is unchanged. What changes per kind is how the input is
delivered and what gets graded:

| kind | input | graded output | tool trace |
|---|---|---|---|
| agent | `prompt` as a user turn | the reply | the turn's calls |
| pipeline | `prompt` as `{input}`, plus form vars | the final stage's text | every stage's calls |
| tool | `prompt` parsed as the tool's args | the tool result | n/a |
| machine | `prompt` as the machine's input | the machine's result | every phase's calls |

Pipelines get one thing agents cannot have: a case may assert on a **declared
stage field** (`{stage:judge.winner} == "for"`), which is a far sharper signal
than substring-matching the prose. That is only possible because structured
outputs already exist, and it is the single strongest argument for extending
evals beyond agents at all.

## Results that survive

Each execution writes an `EvalRun`: when, which suite, the per-case
`EvalResult`s, the aggregate pass rate, and — the field that makes the whole
thing worth building — **a fingerprint of the target as it was**.

```go
type EvalRun struct {
	ID, SuiteID, Owner string
	Started, Finished  time.Time
	Results            []EvalResult
	Passed, Total      int
	// TargetHash identifies the version graded: a hash over the fields that
	// change behaviour (an agent's prompt + tools + model tier; a pipeline's
	// stages). Two runs with the same hash are the same thing measured twice
	// and their difference is noise; two with different hashes are a change
	// and their difference is the answer somebody wanted.
	TargetHash string
	Note       string // optional: what was being tried
}
```

Without `TargetHash` a score history is a list of numbers that drift for
unknown reasons. With it, the history answers the question that motivated the
whole primitive: this edit moved it from 24/30 to 29/30, or it did not.

## The run surface it already has

An eval run is a run: it produces a transcript and a result, it takes minutes,
and somebody wants to watch it and come back to it. `core/pipeline_runs.go`
already serves exactly that for anything satisfying `RunWork`, and it recently
grew the three things an eval run needs most:

- **detached execution** — a thirty-case suite outlives the tab that started it
- **reconnect** — come back to one in progress
- **cancel** — stop a suite that is obviously failing rather than paying for
  the rest of it

So the suite mounts as a `RunSurface`: one block per case, streamed as it
finishes, red or green with its reasons. `session_meta` promotes the pass rate
onto the sidebar row, which makes the run history a **score history** with no
extra work — the list of past runs IS the graph, in the panel that already
draws it.

It also lands on the activity ribbon, which matters more here than elsewhere:
an eval suite is the thing most likely to be started and forgotten.

## What this deliberately does not do

**It does not gate authoring.** There is a verify gate for apps and tools
already, and extending it to "a failing suite blocks the save" is a different
feature with a different failure mode — the one where somebody cannot ship a
fix because an unrelated case is flaky. Evals report; a gate is a later,
separate decision, and one that wants this running first so there is evidence
about flakiness before anything is blocked on it.

**It does not auto-generate cases.** Builder could draft them, and should, but
that is a tool on top of the primitive rather than part of it. A generated
suite nobody read is a green light nobody earned.

**It does not replace `AgentRecord.Evals`.** That field keeps working and keeps
being editable where it is. A suite can be created FROM it in one action, which
is the migration: nothing breaks, and the agent form's JSON textarea stops
being the only place evals can exist rather than being deleted out from under
somebody.

## Tests

- a suite against each target kind runs and grades: agent, pipeline, tool,
  machine
- a pipeline case asserting on a declared stage field passes and fails for the
  right reasons, and a reference to a field the pipeline does not declare is a
  SAVE-time error, not a mid-run one
- stub mode is the default, and a suite with `Stub: false` says so where it is
  started rather than only in the record
- a consequential tool is not executed under stub mode — asserted by the tool
  not being called, not by the absence of its output
- two runs of an unchanged target carry the same TargetHash; editing the
  agent's prompt changes it
- the pass rate reaches the sidebar row, so the history reads as a score
  history
- cancelling a suite mid-run records the cases that finished rather than
  discarding the run

## Rollout

1. `EvalSuite` + `EvalRun` records and storage. Nothing runs them yet.
2. The runner generalized: lift `RunAgentEvals` to take a target rather than an
   `AgentRecord`, agent kind first, behaviour identical.
3. Mount as a `RunSurface`, with the pass rate promoted via `session_meta`.
4. Pipeline targets, including stage-field assertions.
5. Tool and machine targets.
6. "Create a suite from this agent's evals" — the migration, offered where the
   field is edited.

Bump `version.txt` on every commit (no trailing newline).
