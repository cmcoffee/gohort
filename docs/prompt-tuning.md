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

## Map the framework first: the Friction report

Tuning fits wording to what the platform lets the builder do. A tool that
cannot do what was asked, a misleading error or a wrong check is not fixed
by wording, and tuning around it fits the wording to a bug. So before
tuning, the platform is mapped, by a private admin app of its own,
**Friction report** (`/friction`), which changes nothing and grounds each
answer in something stronger than a model's opinion:

1. **Known-good builds** are played first: a passing build's tool calls,
   recorded, played back in a fresh sandbox by a script instead of a model,
   through Builder's real loop, tools and checks. One that fails is the
   platform broken, proven, by the call that failed or the grader.
2. Every task is built **twice on the worker and twice on the lead** with
   the wording as it is live, and the lead **reviews every build**, passing
   ones too (a pass that worked around a tool error is still friction).
3. A passing build that passes again when played becomes that task's
   known-good build.
4. Each task comes out **broken**, **not shown possible** (nothing passed on
   either model: the strongest sign), **worker struggles**, or **ok**.
   Problems group by component as **proven**, **blocking** (on a task
   nothing could do) or **friction** (on tasks shown doable), with the
   models each was seen on: friction both hit is the platform's.

Each report is dated and versioned and compared with the one before (new,
gone, still). **Copy report** hands it to whoever fixes the platform;
**Re-check** on a component plays its known-good builds again or rebuilds
its tasks, and says whether it is gone. The wording's and the model's
problems are kept apart, for Optimize, whose section shows the latest
report. Private: not part of the release.

## Using it: one click per model

A model's prompts are part of the model: optimizing one lives with it, on
the admin **LLMs** tab, under **Optimize**. Each model (the worker, and the
lead when there is a separate one) has two rows: **quick** (two passes at
most, about four hours, for trying it or after a model change) and
**extended** (the overnight run, as far as the settings on Details allow).
Both work the same way:

- **Probe first.** Before a task is built, Builder is given its request and
  its real turn is stopped at its first authoring call, without making it
  (`core.WithToolProbe`, `/sandbox/probe`): which tool it reached for, with
  which settings. Each probe is asked twice. A task's probes are its own
  (`probes` in task.json), or one from its request and kind: a tool wants
  `tool_def`, an app `app_def`, an unattended machine `machine` with
  `unattended: true`. A wrong reach is fixed against the probes, seconds a
  check, before a build spends minutes finding it out. What is checked is
  what Builder did first, not what it says it would do.
- **Fix as it goes.** Each train task is built once. A failure is read by
  the lead as the framework's (a tool that cannot express what was asked, a
  broken platform check, the harness) or the wording's. The framework's is
  reported at once and the task set aside, since no prompt can fix it; the
  run's result lists them under **Set aside**. **Copy findings** on the
  run's row puts them on the clipboard, each with the build behind it (the
  request, the checks that failed, what Builder said and did), for whoever
  fixes the code. **Re-check** on a finding builds that task again on what
  is live now, on the run's model, and says whether it passes now, still
  fails as the framework's, or now reads as the wording's; one at a time,
  and not while Optimize runs, since they build in the same place. The
  wording's gets one edit,
  and that task alone is built again with it, twice at most; the edit is
  kept if the task now passes.
- **Confirm end to end.** After a pass, the whole suite runs, held out
  included, on what the pass kept. It stays only if more train builds pass
  and neither split got worse; if not, the pass's edits are taken back one
  at a time to find the one that hurt, and failing that the pass is dropped.

When the run ends it applies what it kept as that model's own wording, each
change a revision. The older way, the whole suite around every single edit,
is still on Details as "rounds". **Undo** takes the
whole run back. The wording remembers the model it was fitted to; when the
model behind a tier changes, its row says so. While it runs, the row's
progress and "What it is doing" say where it is, and the run sits in the
live indicator and on the Monitor page like any other work using the
models.

The **Prompts** tab is for hand edits to something specific: **Prompt
overrides** is the editor. Its **Read it back** asks the worker and the
lead what the open block tells them to do (for a tool's description, when
they would reach for it first; for a parameter's, what they would put in
it), each reading the wording it would be sent, and shows the two readings
side by side. It is the quick look while editing: a reading is what a model
says it understood, a probe is what it does. Its **Probe** runs a choice probe
there and then: Builder is given a request (your own, or one of the suite's,
those that want the open block's tool first) in a sandbox holding live's
wording with the open block as it is on screen, and stopped at the first
thing it reaches to build, for the worker and the lead side by side.

A run gives way to people. While someone else is using the model it tunes
(a call out, or one in the last 30 seconds), its calls wait at the shared LLM
handle's gate (`core.SetYieldGate`, `core.ModelInUse`) and its sandboxes are
told to pause; it picks up where it was when they are done. A call already
out runs to its end. Paused time does not count against a build's budget, a
probe's, or the session's hours, and the row says it is paused. A Probe from
the editor does not pause: someone is waiting on it.

A restart does not lose a run. Ninety seconds after the server starts, the
newest run, if a restart cut it off in the last day, is picked up where it
was: from its last confirmed point, the pass it was in done again from its
start, its best's suite run reused, its set-aside tasks still set aside,
and its spend carried over. The time it lay stopped does not count against
its hours. A run of rounds is finished instead, and what it had confirmed is
applied. One that cannot be picked up (another run going, the model behind
its tier changed) says why, and its row offers Resume. Everything below the
button is behind **Details** (`/tuning/details`): the settings, every run with its rounds,
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

The harness lives in the live server: the Optimize section on the admin
LLMs tab (a row per model, and the latest friction report), Details behind
it, the Friction report app at `/friction`, and Read it back
and Probe in the Prompt overrides editor on the Prompts tab. It starts the
sandbox and drives it over HTTP: Builder is given each
task through the ordinary chat endpoint, the harness answering its
confirmation cards as a person would, and the tier is pinned by the
sandbox's LLM settings. Each task runs N times
(default 3 on the worker, 1-2 on the lead) because one run of a
non-deterministic model is an anecdote.

A suite run is long (a dozen tasks, three repeats, minutes each), so it
follows the house rule for anything long: a moving indicator, "14 of 36 runs -
9 passed - 41m", the run's own ending reported, a page that arrives mid-run
rejoining it, the run on a context the request cannot cancel, and a Stop
button. `core.ReportMaintenanceProgress`, `ui.ActionList.ProgressSource` and
`ui.ActionList.CancelTo` are the pieces.

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

Built since, on top of those: tool and parameter descriptions as blocks;
one-click Optimize per model with Undo; the focused mode (fix as it goes,
confirm on the whole suite) in quick and extended runs; choice probes, and
Read it back and Probe in the editor; findings set aside with Copy and
Re-check; giving way to people and picking up after a restart; and the
friction report, which comes first.

The suite and harness also serve the self-fine-tune plan: the eval app that
plan called "oracle" is this harness pointed at a LoRA adapter instead of a
prompt variant.

## Live run plan

Everything is built. Optimize first ran against the live deployment on
2026-10-03 (see "What the first live runs showed" below); no friction report
has run yet. The plan is a ladder: each run proves the harness can be trusted with the next, and
each says what to look at before climbing. Every run starts from the
Optimize section on the admin LLMs tab. Runs give way to people using the
model they build on, so they can run while the deployment is in use, only
slower; overnight is still the natural time.

### Before the first run

1. **Setup.** The deployment builds with the tuning app registered (the
   blank import in the machine-local `private.go`), a lead model that is not
   the worker (the judge and the friction report's reviewer are the lead), the lead's
   price set under Prices, and room on the disk beside the workspaces for
   `tuning-sandboxes` (one copy of the binary and a fresh data directory per
   run; anything older than a day is swept).
2. **The suite.** Twenty tasks, fifteen train and five held out, aimed at
   the friction of the framework and authoring tools. See the suite's
   README. The visibility gaps found writing the first plan (a silent
   progress line, rounds invisible until they ended, no scorecard) are
   closed.

### Run 0: a friction report

**Write report** in the Friction report app, before any tuning.

Proves the sandbox starts, takes the LLM settings and the live prompts,
that Builder can be driven on both models and played from a record, that
the checks grade, and that a long run pauses for people, survives leaving
the page, and stops on Stop. What it finds is the point: proven and
blocking components first, then friction both models hit. Copy report to
whoever fixes the platform; after each fix, rebuild and restart, then
Re-check that component until it reads gone. Write another report once the
proven and blocking components are gone, and tune only when a report turns
up nothing a fix should handle. Look at, as well: whether the reviewer's
framework and wording calls hold up against the evidence it quotes, and
that Spent matches what Cost History gained (a lead run's sandbox books
the lead's model as its worker; the harness reads it back as the lead's).

### Run 1: a quick Optimize

Worker, **Optimize** (quick: two passes, four hours, the cap from Details).

The first real proposals. Look at: probes running before each build and
their fixes; failures set aside as the framework's (each one is a gap the
friction report missed, so Copy findings and Re-check them); each kept
edit, and whether it names a general rule or the task it saw; the result
applied as the worker's own wording, each block a revision in Prompt
overrides; Undo putting live back; restarting the server mid-run and the
run picking up where it was.

### Run 2: noise

From Details: rounds mode, 5 builds per task, 1 round.

The starting measurement is the point. Read the spread per task off the
scorecard. It decides how many builds per task a confirmation needs and
whether the whole-suite check can call a change better on a suite this
size. A task that passes 0 of 5 or 5 of 5 every time tells the tuner
nothing and is a candidate to rewrite; one that flips between runs of the
same prompts is the noise floor.

### Run 3: overnight

Worker, **Optimize overnight**, with the builds per confirmation from run 2.
A kept edit that holds on held-out tasks is the first one worth keeping for
good. Pick pairs in the judge's review so its agreement line means
something.

### Run 4: the lead

Lead, quick, one build per task, a cap of about three measurements' worth at
the lead's price. No judge (the judge is the lead), so the checks and the
owner's picks carry the comparison. Watch for the cap stopping a round
before it starts rather than in the middle, and for Spent agreeing with the
provider's own bill.

### What the first live runs showed (2026-10-03)

Optimize ran for an evening on the worker, then on the lead, before the
friction report existed. Read from the server's debug log, where each
sandbox's own log is mirrored under `[tuning sandbox-N]`:

- **Restarts were survived.** Two rebuilds cut the worker run off; it picked
  itself up each time (and once by hand) where it was.
- **Framework problems turned up before any wording did.** Two tasks were
  set aside as the framework's (a check expecting a different invoice than
  the fixture served; a verification check rejecting a correct tool chain),
  and other failures pointed at the platform too (a URL template rejected
  by `tool_def`, a machine failing inside a step). This is what the friction
  report exists to sort out first.
- **The lead never answered the lead's work.** Gemini rejected every call
  that briefs by a system message ("Role 'system' is not supported"), so the
  proposer, the diagnosis, the judge and the reviewer all fell back to the
  worker without anyone choosing it. Fixed in the core Gemini client: system
  messages go to its system instruction. Any run before v0.7.300 tuned with
  the worker proposing.
- **A lead run's spend read as the worker's.** Pinned to the lead, a sandbox
  is given the lead's model as its worker, so it books every call as worker
  tokens ("Worker tokens", tier=worker in its log, for calls that went to
  the lead's model). The harness took that as it read, so the cap could not
  stop a lead run and Cost History filed its spend as the worker's. The
  harness now reads a lead-pinned sandbox's usage as the lead's.
- **Tool schemas are most of Builder's prompt:** about 35k tokens, some 82%
  of every call, the largest single schemas being the agent and tool
  authoring tools. Friction no wording fixes; a candidate for the first
  report's review.

### Stop and look again if

- a report keeps finding a component after its fix (the fix missed, or the
  reviewer is reading the wording's failures as the framework's);
- a starting measurement and an unchanged rerun disagree by more than the
  spread from run 2 (the sandbox is not reproducible);
- builds fail for the harness's reasons more than the occasional once;
- probes pass but the builds after them fail at the same choice (the probe
  watches the wrong call);
- the proposer keeps choosing one block, or keeps writing the task back into
  its rule;
- the judge's agreement with the owner's picks is near a coin toss;
- Spent and Cost History disagree.

## Open questions

- **How many repeats are enough?** Decided by measurement: run 2.
- **How far to trust the reviewer.** It is the lead, uncalibrated; the
  known-good builds and the both-model builds now carry the weight, and its
  calls sit beside them. Owner picks on its calls, as the judge has, if it
  misreads often.
- **Whether the friction report becomes a periodic check.** It is run by
  hand while the platform is being got right. If reports keep finding real
  issues after the first round of fixes, a scheduled report after each
  release would catch regressions before tuning does.
- **Where a task's expected output comes from for machines.** Hand-written for
  now; a machine whose job is judgement may only be gradable by the judge.
- **Sandbox resources.** A second gohort plus headless Chrome for app checks
  on the same box as the live server. Runs give way to people on the model
  they use, but the CPU and memory of the sandbox are not yielded.
