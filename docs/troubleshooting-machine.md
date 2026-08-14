# St4 — a troubleshooting machine, as the test of the phase model

Status: spec, pre-implementation. Replaces the withdrawn St4 in
[agent-machines.md](agent-machines.md).

## Why this, and not a port

The original St4 was "port Builder's intake-to-build flow onto a machine". There was nothing to
port — see the correction in agent-machines.md. But the *purpose* stands, and it is the only stage
that matters for confidence: **something real has to stress the phase model hard enough to send
St1-St3 back for changes.**

A machine authored by the person who designed the phase model is not that test. It will fit, because
it was built to. This one is chosen for three properties the triage machine does not have:

1. **It is a stated need**, not one found by going looking. It came up unprompted while the machine
   work was in flight: *"ask a question, it figures out the best places to go get the information,
   so it may need to branch off in two different directions, or do it sequentially."*
2. **It wants parallelism, which a machine structurally cannot do.** A machine holds exactly one
   position. Fanning out across sources is a pipeline. So this forces the machine-calls-a-pipeline
   composition to be real rather than asserted.
3. **It runs long.** An investigation is ten turns, not three, and every cost assumption in the
   design (the pinned block stays small, the guard is cheap, state is stable) is an assumption about
   short conversations.

Distinct from the existing screenshot-driven troubleshoot app idea (`project_troubleshoot_app`),
which is vision-in / fix-out. This is text, conversational, and open-ended.

## The flow

> Ask a question → work out what is actually being asked and where the answer could live → go and
> get it, sometimes from several places at once → reason about what came back, and keep reasoning
> about it across follow-ups without re-deriving the setup every turn.

## The shape under test

Two candidate designs. **They disagree about one thing, and that disagreement is the experiment.**

### Design A — gathering is a transient phase

```
scope     (transient)  what system, what symptom, what would count as an answer
plan      (transient)  which sources to consult; routes by how many
gather    (transient)  run the probes, write findings to the blackboard
diagnose  (resident)   reason with the user; guarded back to scope on a new subject
```

Clean on paper, and it is what the phase vocabulary invites. It requires two things that do not
exist:

- **Transient phases need real tools.** `chatTurn.machineCatalog` returns nil today, deliberately:
  a transient phase runs during system-prompt assembly, before the turn has built its `ToolSession`,
  and the live catalog is wired to that session. Filling this means moving the phase walk after
  `ToolSession` construction, or moving that construction earlier. **That is an St1 change.**
- **A way to fan out.** `gather` would have to call a pipeline as a tool, which is the same
  requirement in a different hat.

### Design B — the machine holds the frame; the agent does the work

```
scope     (transient)  what system, what symptom, what would count as an answer
plan      (transient)  which sources to consult, and in what shape
work      (resident)   the agent gathers AND reasons, with its full catalog
                       and its attached pipelines, guarded back to scope
```

The machine's job is to hold the investigation frame — what we are looking at, what has been ruled
out, what the plan was — and NOT to do the gathering. Parallelism comes from the agent invoking an
attached pipeline (`run_<pipeline>`), which already works today.

This needs **no St1 changes at all**, which is either the right answer or a suspiciously convenient
one. Finding out which is the point.

**Prediction:** B is correct, and A is the shape the vocabulary tempts you into. A phase is a *mode
of being*, and "go fetch things" is work, not a mode. If B holds, the honest conclusion is that
`machineCatalog` should stay empty and the doc should say so more strongly than it does.

## What would send St1-St3 back

Written before building, so the test can actually fail. Each of these is a specific defect with a
specific consequence.

### 1. The blackboard overwrites instead of accumulating

`walk()` does `cur.State[ph.Name] = PhaseResult{...}`. Re-entering a phase **replaces** its previous
result. An investigation that gathers, learns something, and gathers again loses the first round's
findings entirely. `Keep` does not help — it controls which *phases* survive a re-entry, not whether
a phase's own history does.

This is the failure I most expect. Fixing it means deciding whether a phase result is a value or a
log, and that is a real change to `MachineState`.

### 2. The pinned block grows without bound

`MachineState` has no cap. The whole cache argument — the system prefix stays byte-identical across
a resident run, so cold prefill is paid once — assumes the block is small and stable. A findings list
that grows every time `gather` runs breaks both halves: the prefix changes, and it gets big.

A ten-turn investigation is where this shows up. A three-turn triage never will.

### 3. The guard's per-turn cost stops being negligible

One small call per turn is nothing across three turns. Across a thirty-turn investigation it is
thirty calls, all asking the same question about a conversation that has obviously not changed
subject. Either the guard needs to be cheaper to skip (only run it when the turn looks like a topic
change) or long-running phases want a different mechanism.

### 4. `change_phase` mid-turn leaves tools and tier stale

Documented as acceptable: a phase change takes full effect next turn, and the tool result carries
the new directive. In a short conversation nobody notices. If `work` and `scope` want genuinely
different tool sets, being handed the wrong catalog for the rest of a turn is the kind of thing that
produces a confusing failure rather than an obvious one.

### 5. One resident phase may not be enough

If the user wants to interleave "keep digging" and "explain what you found" as different modes, that
is two resident phases with a human-driven transition, and the only way between them is
`change_phase` — an LLM judgment call on every turn. That may be exactly where a user-facing
"switch phase" affordance turns out to be needed, which is UI work nobody has specced.

## Staging

| Stage | Scope |
|---|---|
| **T1** | Author the Design B machine and attach it to a real agent with real tools. No code changes. Run a genuine investigation of ten-plus turns. |
| **T2** | Record what broke against the five predictions above. Anything not on that list is the more interesting result. |
| **T3** | Fix what T2 found, in core if it is a model problem, in the machine if it is an authoring problem. Telling those apart is the judgement this stage exists to exercise. |
| **T4** | Only if T2 shows Design B genuinely cannot express the flow: attempt Design A, which means filling `machineCatalog` and moving the phase walk. |

T1 requires no engineering, which is the point — the first honest thing to do with a new primitive
is use it, not extend it.

## The success condition

Not "the machine works". The success condition is **a specific, written-down change to St1-St3 that
came from use rather than from design**, or a defensible statement that no change is needed and
why. A run that produces neither means the test was too easy and should be made harder.
