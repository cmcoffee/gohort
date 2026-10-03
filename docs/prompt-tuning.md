# Prompt tuning: a proving ground for what Builder builds

Status: **design, nothing built.** The one piece that exists is the build
ledger (v0.7.263, `core/buildledger`, Admin > Agents > Build outcomes), which
records every tool test and app verify from real use. That is observation.
This document is the other half: a controlled experiment that can change the
prompts.

## The idea

Fine-tuning, with the prompts as the weights.

| Fine-tuning | Prompt tuning |
|---|---|
| training data | a suite of build tasks with known-good outcomes |
| weights | the prompt blocks, Builder's own prompt, and the descriptions of the framework and authoring tools, per tier |
| loss | graders scoring what Builder actually built |
| optimizer | a proposer that edits prompt blocks from the failures |
| validation set | held-out tasks the proposer never sees |
| checkpoint | a variant: one set of prompt texts, with its scorecard |

Builder is thrown at a fixed set of tasks (wrap this API, build this app, make
this machine). Each task has graders Builder never sees. The run produces a
scorecard. A prompt is changed, by hand or by a proposer model, and the suite
runs again. A change is kept only when the scorecard says it helped, and only
reaches the live deployment when an admin promotes it.

## Using it: one click per model

A model's prompts are part of the model: optimizing one lives with it, on
the admin **LLMs** tab, under **Optimize**. There is a row for the worker
and, when there is a separate one, for the lead, each named by its model,
each with an **Optimize** button. A press builds the suite with that model
in a sandbox, rewords what it got wrong, keeps what builds better without
getting worse on tasks it never saw, and when it ends applies what it kept
as that model's own wording, each change a revision. **Undo** takes the
whole run back. The wording remembers the model it was fitted to; when the
model behind a tier changes, its row says so. While it runs, the row's
progress and "What it is doing" say where it is, and the run sits in the
live indicator and on the Monitor page like any other work using the
models.

The **Prompts** tab is for hand edits to something specific: **Prompt
overrides** is the editor. Everything below the button is behind
**Details** (`/tuning/details`): the settings, every run with its rounds,
builds, scorecard and the judge's pairs to calibrate, and the suite. The
rest of this document describes that machinery.

## What this is not

**Not the Evals app.** Evals (removed in v0.7.219) graded an agent's REPLIES to
saved conversations, per agent, owned by users, and Builder could run it as a
tool. This grades ARTIFACTS: the tool, app or machine Builder produced,
exercised by checks it never saw. It is an admin instrument for tuning the
framework's own authoring prompts, it runs in a separate instance, and no
agent can reach it. Evals had the right instincts and they carry over: a pass
RATE over N runs rather than a boolean, scripted tool returns instead of live
side effects, results that are stored rather than returned and forgotten.

**Not the build ledger.** The ledger watches real builds and cannot change
anything; its tasks are whatever people happened to ask for, so it cannot
compare two prompts on the same work. The ledger is where suite tasks come
from (a failure kind that keeps recurring in production is a task worth
writing) and where a promoted change is checked afterwards (did tries-to-green
on that kind actually drop?).

**Not the Optimize button.** Optimize rewrites a block to be shorter with no
idea whether the result works. Here, a shortening is one more proposal and has
to pass the suite like any other.

## Defaults decided

- **Worker first.** It is free to run many times, and it is where wording
  matters most. Lead tuning follows the same machinery with a spending cap.
- **A separate sandbox instance**, not per-run overrides on the live server.
- **The first tasks are drafted from Build outcomes data** and reviewed by the
  owner before they count.

## Tasks

A task is a file. The suite is a directory of them, versioned in the repo
(`private/extras/tuning/` while it is unproven, by the same rule as eval
scripts), so a change to a task is a reviewed change and a scorecard can name
the exact suite revision it ran against.

```json
{
  "id": "wrap-weather-toolbox",
  "kind": "tool",
  "split": "train",
  "tags": ["api-wrap", "toolbox", "jq-pipe"],
  "request": "Wrap the weather service at {fixture:weather} as a toolbox with a current-conditions read and a forecast read. Keys are not needed.",
  "fixtures": ["weather"],
  "budget": {"rounds": 60, "minutes": 20},
  "graders": [
    {"type": "tool_exists", "name_like": "weather"},
    {"type": "tool_call", "action": "current", "args": {"city": "Lisbon"},
     "expect": {"contains": ["18", "partly cloudy"]}},
    {"type": "tool_call", "action": "forecast", "args": {"city": "Oslo", "days": 3},
     "expect": {"json_len": 3}},
    {"type": "verified_before_done"},
    {"type": "judge", "rubric": "tool-description"}
  ]
}
```

- **`request`** is what a user would type. `{fixture:...}` expands to the
  fixture's URL inside the sandbox.
- **`split`** is `train` or `heldout`. The proposer reads train failures only.
- **`graders`** run after Builder ends its turn. Builder sees none of them.
  The cases in a grader are deliberately DIFFERENT from anything the request
  mentions (Lisbon and Oslo above), so a tool that only works for the example
  Builder tested fails.
- **`budget`** caps the run. Hitting it is a failure with its own class, not a
  crash.

Kinds at the start: `tool` (api, toolbox, shell), `app`, `machine`. Pipelines
and agents later, on the same format.

## Fixtures

API-wrapping tasks need an API that answers the same way every time and costs
nothing. The sandbox serves fixtures: a fixture is a small declarative file of
routes and canned responses (status, headers, body, optional latency or a
deliberate quirk such as pagination or a non-JSON content type). Nothing in a
run touches the outside network, which also means nothing in a transcript was
written by a stranger: every byte Builder reads during a run is text the suite
authors wrote. That matters for the proposer (see Integrity).

Fixtures can carry the quirks Builder actually trips on, taken from the
ledger: a REPORT method that needs a `Depth` header, a bare-array body where
an object is expected, a write endpoint that 400s without a body. A fixture is
where a real-world failure becomes a repeatable one.

## The sandbox instance

Prompt overrides are global today: `EffectivePromptText` reads one table, from
nine call sites, none of which carries a request context. A variant applied on
the live server changes every user's prompts for the length of the run.
Rather than thread a variant through every one of those sites, the harness
runs a disposable gohort:

1. A scratch directory with the same binary and its own `gohort.ini`, so the
   data directory, database and workspaces are fresh (the data directory
   follows the binary unless `[paths] data_dir` says otherwise).
2. LLM endpoints copied from the live configuration. The lead's key is the one
   secret that crosses, and only when a lead run is asked for.
3. The variant applied as that instance's prompt overrides, and Builder's
   prompt written onto its `seed-builder` record, before the first task.
4. The fixtures mounted, a synthetic owner account, and confirmations
   auto-approved. Builder's ask-in-chat cards fail closed with nobody
   watching; in a sandbox that exists to be thrown away, approving them is
   what lets a run finish.
5. Torn down after the scorecard is written. A failed run's directory is kept
   for a short while so its transcripts can be read.

The live server's Build outcomes ledger, users, tools and apps are untouched
by all of it.

## Running

The harness lives in the live server, as sections on the admin Prompts tab
beside the prompt-block editor (editing prompts and measuring them are one
job), starts the sandbox, and drives it over HTTP: Builder is given each
task through the ordinary chat endpoint, the harness answering its
confirmation cards as a person would, and the tier is pinned by the
sandbox's LLM settings. Each task runs N times
(default 3 on the worker, 1-2 on the lead) because one run of a
non-deterministic model is an anecdote.

A suite run is long (a dozen tasks, three repeats, minutes each), so it
follows the house rule for anything long: a moving indicator, "14 of 36 runs -
9 passed - 41m", the run's own ending reported, a page that arrives mid-run
rejoining it, the run on a context the request cannot cancel, and a Cancel
button. `core.ReportMaintenanceProgress` and `ui.ActionList.ProgressSource`
are the pieces.

## Graders

### Deterministic: these decide pass or fail

| Kind | Checks |
|---|---|
| tool | the tool exists; each hidden case runs through the real dispatch path and its output matches (`contains`, `json_path`, `json_len`, status); `temptool.CheckRun` so an exit code or a 4xx is never read as success |
| app | `core.CheckPageAsUser` loads the app: no console errors, every data source fetched, plus task-specific DOM assertions (rows shown, a chart drawn, a form submit creates a record) |
| machine | run the built machine on a fixture input; compare its output to the expected answer |
| process | Builder ran the verify gate and it passed before it said "done"; the summary to the user names what was actually built (checked against the stored record, not the prose) |

A task passes only when every deterministic grader passes.

### The LLM judge: scores what code cannot check

Whether an app is well designed rather than merely rendering; whether a tool's
description would let another agent call it correctly; whether house
conventions were followed. The judge never decides pass or fail.

- **A different model judges.** The lead judges worker builds. A model grading
  its own output prefers it, and an optimizer tuned against that preference
  learns to please the judge.
- **The judge is frozen for a tuning run.** Its prompt and rubrics are not
  parameters. If they were, the cheapest improvement available to the
  proposer would be a gentler judge.
- **Pairwise, not absolute.** The judge sees two builds of the same task,
  unlabelled, and picks one with a reason; then again with the order swapped.
  A 1-10 score drifts between runs; a comparison holds.
- **Instability is a tie.** A judge that flips when the order is swapped, or
  across repeats, has not preferred anything. This deployment has watched a
  judge reverse itself three times in 800 words with only the final verdict
  kept; here the reversal is the data.
- **Calibrated before it counts.** The owner's own verdicts are the labels:
  pairwise picks made on the scorecard page, and the existing reply flags.
  The judge's agreement rate with them is shown beside every judge column,
  and a rubric with poor agreement is fixed before its scores are believed.

## The scorecard

Per task, per variant, per tier:

- deterministic pass rate over the N runs, with the spread between runs
- judge win rate against the baseline variant (ties shown)
- cost: rounds, tokens, wall time
- friction: the tools each build called, by name (a machine task answered
  with pipeline calls shows here before any grade says why), and the calls
  that came back an error, averaged per task
- tries to green and the failure classes hit (the build ledger's own
  vocabulary, so suite and production read the same way)
- reply guards and correction checks that fired

Rolled up per split. A difference between two variants smaller than the run
spread is reported as no difference.

## Variants and lineage

A variant is an overlay: block key to text for any registry block, plus
Builder's prompt, each optionally per tier. Every variant records its parent,
the edit that made it, who or what proposed it and why (the failure it
addresses), the suite revision, and its scorecard. The lineage is a tree, so a
branch that went nowhere is visible and any point in it can be restored.

The baseline variant is "whatever is live", snapshotted at the start of the
tuning run.

## The proposer

The lead model reads the train-split failures (grader verdicts, the judge's
reasons, the transcript of the failing run) and proposes ONE targeted edit: a
named block, a diff, and the failure it addresses. Not a rewrite of the whole
prompt; a rewrite cannot be attributed and usually drops something.

Operators the proposer can choose from:

- **targeted edit**: add, change or remove a specific instruction
- **compress**: today's Optimize, made accountable
- **revert**: undo an earlier edit in the lineage that has stopped paying

A candidate is accepted only when all of these hold:

1. the train deterministic pass rate rises by more than the run spread
2. the held-out pass rate does not fall
3. the judge win rate does not fall; judge points never buy back a lost pass
4. the total prompt length stays under its cap (a model can always buy points
   with more text, and every token is paid on every turn)

A tuning run has a token and wall-clock budget and stops after N rounds with no
accepted candidate. It never promotes. Promotion is an admin action on the
winning variant: each changed block gets a Prompts-page revision tagged
`Via: "tuned"` with the scorecard linked, so it is revertible the same way
every other edit is.

## Tool descriptions are weights too

How Builder builds is decided as much by the descriptions of the tools it
builds with as by its prompt: when to make a machine and when a pipeline,
what a tool definition needs before it is done. So the shipped framework and
authoring tools' descriptions are blocks, keyed `tool.<name>`, on the Prompts
page beside the rest (`core/prompts/tool_desc.go`, the names in
`apps/orchestrate/tunable_tools.go`). Each block's text is the description
the code ships, seen the first time the tool goes out and remembered across
restarts; an edit, for every tier or for one, replaces the description in
every call that offers the tool, at the same handle the per-tier swap uses.

- **Authoring is everything Builder authors with,** a test keeping the list
  equal to what it is handed, plus `app_def`, `pipeline` and `machine`.
  Framework is the turn plumbing it plans, builds and verifies through.
- **A user's own tools are never blocks.** Their descriptions are theirs.
- **A description built per caller is left alone.** The `agents` tool has a
  read-only variant for Builder; `plan_set` carries the agent's budget. Those
  are not listed, and any listed tool whose description keeps changing
  between calls drops out of the editable set with a log line saying so,
  since one edit would overwrite what each caller was meant to read.
- **Parameters too.** Each top-level parameter of a named tool is a block of
  its own, `tool.<name>.<param>`: the parameter descriptions carry much of
  the how (app_def's sections and data sources, a machine's phases). They
  are recorded and listed the same way, so they come back after a restart,
  and edited per model the same way. Nested properties are left as the code
  builds them. The proposer is shown a parameter's block only when its tool
  appears in the builds that failed, since there are hundreds of them.

The proposer is told that a `tool.<name>` block is the description of that
tool, to be edited when a failure is a wrong choice of tool, and a
`tool.<name>.<param>` block a parameter's, when the tool was right and the
call was filled in wrong.

## Per-tier profiles

The same block can be tuned differently for the worker and the lead. This is
the per-model prompt profile the framework was always missing: prompt
discipline is model-dependent data, not a fixed floor, and the profile is
measured rather than guessed.

- **Resolution: tier, then all tiers, then the shipped default**, exactly as
  reply guard settings resolve (`replyguard.Resolve`).
- **A block forks only on evidence.** The evidence is which tier an edit was
  measured on: an edit that won on the worker is the worker's own wording,
  and the lead keeps the shared text it was never tested against. The
  shared wording changes only from a routed session or by hand. Two copies
  of every prompt maintained for no measured reason is the outcome to avoid.
- **A tier's text remembers the model it was tuned on.** When the model behind
  a tier changes, the admin page says the tier's tuned prompts were fitted to
  another model and offers a suite run. Reply guards already do this.
- **The tier's words are chosen where the call is answered.** A prompt is
  assembled before the tier is final, and a lead call can still end up on
  the worker (the lead is denied, a route stage says worker, the lead fails
  or comes back empty, a loop de-escalates, a forced final answer). So the
  assembler keeps writing the shared text, and the reloadable LLM handle,
  the one place every call passes through and the first that knows which
  tier is answering, swaps each block's shared text for that tier's own
  (`prompts.ApplyTierText`). A fallback to the worker goes through the
  worker handle and carries the worker's words without rebuilding anything.
- **Placeholders carry over.** A block written with `{rounds}` is found by
  its plain text whatever the placeholder was filled with, and the tier's
  text gets the same value. A block that reaches the prompt rendered
  (Builder's `{{placeholders}}`) registers its renderer
  (`prompts.RegisterTierRender`) and is swapped as rendered. A tier text
  naming a placeholder the block does not fill is refused on every way in.
  The tools directive, filled per call and ending in the fill, is the one
  block that cannot be worded per tier.
- **A swap that cannot be made leaves the shared text.** A tier never gets
  less than every tier gets. The editor's note on a model's version says
  when it last went out, so wording that never reaches a prompt is visible.
- **Mixed turns are already per loop.** A lead plan with worker steps sends
  each call through its own tier's handle, so each gets its own words.
- **A session pinned to one tier writes that tier's words.** It measured no
  other, so its kept edits are that tier's own text, applied as that model's
  own wording (the editor shows it under the model's version), leaving the
  shared wording alone. A routed session edits the shared wording. A lead-pinned sandbox serves both of its tiers
  with the lead's model, so the lead's words go to both there.
- **Lead tuning has the judge problem.** The lead cannot fairly judge its own
  builds. A session that builds on the lead (pinned to it, or routed) runs
  with no judge and says so on each round; it leans on the deterministic
  graders and on the owner's pairwise picks.
- **Lead tuning has a spending cap.** Every session carries a meter: what
  its sandboxes spent, read from each sandbox after every build, and what
  its proposer and judge spent here, priced at the deployment's Prices. A
  round that would pass the cap (judged by what the starting measurement
  cost) is not started, and a run that passes it anyway is stopped where it
  stands. A session that can build on the lead does not start without a
  price on the lead, since the meter would read every lead call as free.
  The sandbox's spend is also added to the live server's usage, so the
  Cost History shows it: the money is this deployment's.

Not covered: the prompt viewer and the run digest show the shared text, and
calls that do not go through the reloadable handles (the CLI, a model an
agent names directly) get the shared text. This touches the LIVE server:
every model call's system prompt passes through the swap, which is a no-op
while no tier text is set.

## Order of work

Each stage is useful alone and proves the next one is worth building.

1. **Task format, fixture server, three tasks** (one API wrapper, one app,
   one machine) and the deterministic graders, run in-process against a test
   database. Proves the graders can tell a good build from a bad one.
2. **The sandbox instance and the harness page.** A suite run against the
   live prompts, N repeats, a scorecard, progress and cancel. Proves the run
   spread is small enough to compare anything.
3. **Manual tuning.** Variants authored by hand, run side by side, lineage,
   promotion. Proves a prompt edit moves a score.
4. **The judge.** Pairwise, swapped, calibrated against the owner's picks.
5. **The proposer.** Targeted edits and compress, the acceptance rules, the
   budget.
6. **Per-tier profiles** on the live server, once a block has shown it wants
   to differ.
7. **Lead tuning**, with its spending cap.

The suite and harness also serve the self-fine-tune plan: the eval app that
plan called "oracle" is this harness pointed at a LoRA adapter instead of a
prompt variant.

## Live run plan

All seven stages are built and none has run against a live deployment. The
plan is a ladder: each run proves the harness can be trusted with the next,
and each says what to look at before climbing. Every run is started by hand
from Tune Builder on the admin Prompts tab, overnight, when nobody needs the
worker.

### Before the first run

Gaps found while writing this plan. The first three are closed; the
fourth is not.

1. **The progress line went quiet for an hour.** During a round it read
   "round 2 of 6 - running the suite on edit X" and nothing moved but the
   clock. It now carries the suite run's own line after a colon: which
   build of how many, how many passed, which task, what Builder is doing
   (its turn, the confirmations answered), then the session's time and
   spend.
2. **A round was invisible until it ended.** The rounds list now leads with
   the starting measurement and ends, while the session runs, with the
   round in flight and the edit it is building with. Builds on any of them
   lists each attempt as it finishes: pass or fail, the check that failed
   in its own words, minutes, turns, spend, and behind Look what Builder
   told the user and what it did. These tables refresh themselves, and hold
   still while a row is open.
3. **There was no scorecard view.** Builds also shows the run's scorecard:
   per task, passed of graded, the spread between repeats, minutes, tries to
   green, out of time, harness errors, and a line with the train and
   held-out pass rates and the mean spread. Run 2 reads the spread here.
4. **The suite was three tasks, all train.** Closed: twenty tasks, fifteen
   train and five held out, aimed at the friction of the framework and
   authoring tools (tool wrappers with real APIs' quirks, python shell
   tools, apps whose data sources compute the thing asked for, machines
   chaining tools, one where Builder must build the tool first). See the
   suite's README.
5. **Setup.** The deployment builds with the tuning app registered (the
   blank import in the machine-local `private.go`), a lead model that is not
   the worker (the judge refuses otherwise), the lead's price set under
   Prices, and room on the disk beside the workspaces for
   `tuning-sandboxes` (one copy of the binary and a fresh data directory per
   run; anything older than a day is swept).

### Run 1: smoke

Worker, start from live, 1 build per task, 1 round, stop after 1 rejection,
1 hour, $2.

Proves the sandbox starts, takes the LLM settings and the prompts, that
Builder can be driven through the chat, that the checks grade, and that the
session ends on its own and says how. Look at: the progress line moving
through every phase; leaving the page and coming back mid-run (it must
rejoin with the spinner); the round's Review opening with a before and
after; Spent matching what Cost History gained; Stop cutting a second smoke
run off cleanly; no `tuning-sandboxes` directory left behind once the
session ends.

### Run 2: noise

Worker, start from live, 5 builds per task, 1 round, $5.

The starting measurement is the point; the one round is incidental. Read
the spread per task off the scorecard (gap 3). It decides how many builds
per task a session needs and whether the two-standard-error test can ever
call a change better on a suite this size. A task that passes 0 of 5 or 5
of 5 every time tells the tuner nothing and is a candidate to rewrite; a
task that flips between runs of the same prompts is the noise floor.
Answers the open question on repeats.

### Run 3: one tuned round, reviewed

Worker, repeats from run 2, 3 rounds, $10.

The first real proposals. Read each one: does the edit name a general rule
or the task it saw? Pick pairs in Review so the judge's agreement line
means something (ten picks is a start). Promote nothing from this run; the
suite is too small for a gain on it to mean a gain at the job.

### Run 4: a session worth promoting

Only once the suite has held-out tasks (gap 4). Worker, the default rounds,
a cap sized from what run 2 spent per measurement. A kept edit that holds
held-out is the first one worth promoting, and the first test of a
promotion: the block shows up as a tuned revision on the Prompts page, as
the worker's own wording when the session was pinned to the worker, and
reverting it puts live back.

### Run 5: the lead

Lead, 1 build per task, a cap of about three measurements' worth at the
lead's price. No judge (the judge is the lead), so the owner's picks carry
the comparison. Watch for the cap stopping a round before it starts rather
than in the middle, and for Spent agreeing with the provider's own bill.

### Stop and look again if

- the starting measurement and an unchanged rerun disagree by more than the
  spread from run 2 (the sandbox is not reproducible);
- a build fails for the harness's reasons (sandbox errors, attempts left
  out of the pass rate) more than the occasional once;
- the proposer keeps choosing one block, or keeps writing the task back
  into its rule;
- the judge's agreement with the owner's picks is near a coin toss;
- Spent and Cost History disagree.

## Open questions

- **How many repeats are enough?** Decided by measurement: run 2 of the
  live run plan, five builds per task, sized from the spread.
- **Suite size before the proposer is trusted.** A handful of tasks overfits
  fast. A working floor of twenty, split 15/5, before stage 5?
- **Where a task's expected output comes from for machines.** Hand-written for
  now; a machine whose job is judgement may only be gradable by the judge,
  which makes it a poor first task.
- **Sandbox resources.** A second gohort plus headless Chrome for app checks
  on the same box as the live server. The worker's slots are shared with live
  traffic; suite runs probably belong overnight, scheduled.
