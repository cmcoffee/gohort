# Prompt tuning: a proving ground for what Builder builds

Status: **built, first live runs and friction report done** (v0.7.362). The harness and the
friction report are private (`private/tuning`, not in the release); the
pieces they stand on (tool probes, the yield gate, per-tier wording, the
stage tracker) are in core. The build ledger (v0.7.263, `core/buildledger`,
Admin > Agents > Build outcomes) records every tool test and app verify from
real use; that is observation. This is the other half: a controlled
experiment that can change the prompts.

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
scorecard. A prompt is changed by a proposer model, and the suite runs
again. A change is kept only when the scorecard says it helped; when the run
ends, what it kept is applied as the one wording both models read, and Undo
takes the run back.

## Map the framework first: the Friction report

Two sources feed it (2026-10-04): a report written on purpose, and every
Optimize run, whose failures read as the platform's are listed under
**Seen while optimizing** as they happen. A run sees less (failed builds
on the worker only, no known-good replay, on wording it was changing), so
a report is still the check after a batch of fixes and the only one on the
lead; Optimize's list is friction found as a by-product of runs done anyway.

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

The loop the report is for:

1. The operator writes a report (**Write report**) and watches its stages.
2. **Copy report** goes to whoever fixes the platform; in practice it is
   pasted into a coding session, where the text carries everything needed
   without access to the deployment: each component with every problem, the
   quoted evidence, the requests behind it, and the task verdicts.
3. The proven and blocking components are fixed first, then friction both
   models hit; the operator rebuilds and restarts.
4. **Re-check** on each fixed component until it reads gone.
5. The next report, compared with the last, confirms the fixes held and
   nothing new crept in. Tuning starts when a report turns up nothing a fix
   should handle.

## Using it: one Optimize, aimed at the worker

Optimizing lives with the models, on the admin **LLMs** tab, under
**Optimize**: one row, **Builder's wording**, one button (decided
2026-10-04; it was a row per model). The page explains the process, not
the models: what Optimize is for (improving the wording Builder works from
by building real things with it, keeping only changes that make more
builds pass), a run's six steps (tighten, build, read, fix, confirm, apply), and
its limits, the spend cap at the admin Prices among them. Which model does
what gets one sentence there; the detail is here. A run builds on the **worker** and the **lead helps**: it proposes
the edits, reads the failures and reviews the builds. What it keeps is one
wording that both models read. Why (the owner's reasoning): the worker is
local and cheap, so its builds cost nothing but time, and it is the likely
cause of most friction; the lead is generally a larger model that costs
money per call, and wording that helps the worker will likely already
translate to it. Tuning the lead apart spent its paid builds on the model
that needed it least. A run is
quick by default (two passes at most, about four hours); the **Extended
run** switch under the button makes it take the limits on Details as they
are (named for what it is, not when to start it: it is the run to leave
overnight). Details is linked under it, so its settings are reachable
before the first run. A run works one
way:

- **Tighten first.** Before anything is fixed, the 6 longest blocks are
  said in fewer words by the lead and the whole suite runs once on all the
  cuts together; they stay only if no build is lost (see "Tool descriptions
  are weights too"). First, so every build after runs on the shorter
  wording, each one cheaper, and the fixes are written into the short text
  instead of being cut back out of it by a trim that follows. Once per
  run, quick or extended, with no take-backs. On by default; off on Details
  with **Tighten first**; skipped, and said, when the budget or hours left
  would not cover the run.
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
  set aside, since no prompt can fix it, and listed with why on the run's
  row and on the friction page under **Seen while optimizing**, whose Copy
  hands it over (and a report's Copy includes what was set aside after
  it). Re-checking it is the friction report's. The
  wording's gets one edit, and that task alone is built again with it,
  twice at most; the edit is kept if the task now passes.
- **Confirm end to end.** After a pass, the whole suite runs, held out
  included, on what the pass kept. It stays only if more train builds pass
  and neither split got worse; if not, the pass's edits are taken back one
  at a time to find the one that hurt, and failing that the pass is dropped.

When the run ends it applies what it kept as the shared text, and **Undo**
takes the whole run back; **Reset to shipped** takes back every run. The
risk is an edit that helps the worker and hurts the lead, which a run no
longer measures: the friction report still builds every task on both
models, so a task the lead used to pass and no longer does shows there. While it runs, its stages show above "What it is doing"
(each pass, and in the pass running each task as it goes), and it sits in
the live indicator and on the Monitor page like any other work using the
models.

A prompt changes in one of two ways: in the source, where the friction
report's fixes land, or by Optimize, which measures what it keeps. There is
no hand editor (v0.7.317): it measured nothing and froze each block it
touched against every later change to what ships. **Settle prompt wording
(once)** (Housekeeping, v0.7.339) clears what it saved, makes the worker's
tuned per-model wording from earlier runs the shared text and drops the
lead's, each text logged; it records that it ran and does nothing again,
since after a one-wording run the shared text is Optimize's.

**A borrowed worker takes its wording from where it is tuned.** When a
machine's worker is a peer's model (an LLM tier set to `peer:<name>`), it
reads that peer's tuned wording: the serving machine publishes its shared
prompt-block overrides to peers holding a models key, and the borrowing
machine fetches them every few minutes, keeps the last copy if the peer is
down, and reads them over its own. Only registered prompt blocks travel
(tool descriptions included), never the admin's Style rules. Its Optimize
row says the worker is tuned on the peer, and runs there. Built in
v0.7.341: `GET /api/peer/v1/prompts` on the serving machine (models key
and grant, advertised in the manifest), the peer layer in core/prompts
read first by every override lookup including per-tier resolution, and
`LocalPromptOverride` for what changes this machine's own text. The
borrowing machine's Worker LLM section shows it (v0.7.345): how many blocks
of which peer's wording, tuned for which model, fetched how long ago.

**Ask Builder**, on Details, gives Builder a request (your own, or one of
the suite's) in a sandbox holding live's wording and stops it at the first
thing it reaches to build, for the worker and the lead side by side (the
lead's column is how a change aimed at the worker is checked on it): a quick
look at what Builder does, before or after a run.

A run gives way to people. While someone else is using the model it tunes
(a call out, or one in the last 30 seconds), its calls wait at the shared LLM
handle's gate (`core.SetYieldGate`, `core.ModelInUse`) and its sandboxes are
told to pause; it picks up where it was when they are done. A call already
out runs to its end. Paused time does not count against a build's budget, a
probe's, or the session's hours, and the row says it is paused. A probe from
Ask Builder does not pause: someone is waiting on it.

A restart does not lose a run. Ninety seconds after the server starts, the
newest run, if a restart cut it off in the last day, is picked up where it
was: from its last confirmed point, the pass it was in done again from its
start, its best's suite run reused, its set-aside tasks still set aside,
and its spend carried over. The time it lay stopped does not count against
its hours. One that cannot be picked up (another run going, the model
behind its tier changed) says why, and its row offers Resume.

**Details** (`/tuning/details`) holds the settings, every session's steps
(each edit with why, before and after, and its builds and scorecard), and
the suite. The rest of this document describes that machinery.

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

**Not a rewrite for length alone.** The per-block Optimize (later Tighten)
button in the old prompt editor shortened a block with no idea whether the
result still worked, and went with the editor. Here a shortening has to hold
up on the whole suite like any other change.

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
    {"type": "verified_before_done"}
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
LLMs tab (one Optimize row, and the latest friction report), Details behind
it with Ask Builder, and the Friction report app at `/friction`. It starts the
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
`ui.ActionList.CancelTo` are the pieces. Optimize and the friction report
also show their stages with `ui.StageTracker`: every stage in order, done,
running, pending, failed or skipped with a count; the running pass's tasks
one by one (probed, built, passed, set aside, fixed); what it is doing now
and the last thing worth noticing. Re-checks show theirs under the list they
were started from. A single progress line said what was happening but not
where in the whole that was.

## Graders

### Deterministic: these decide pass or fail

| Kind | Checks |
|---|---|
| tool | the tool exists; each hidden case runs through the real dispatch path and its output matches (`contains`, `json_path`, `json_len`, status); `temptool.CheckRun` so an exit code or a 4xx is never read as success |
| app | `core.CheckPageAsUser` loads the app: no console errors, every data source fetched, plus task-specific DOM assertions (rows shown, a chart drawn, a form submit creates a record) |
| machine | run the built machine on a fixture input; compare its output to the expected answer |
| process | Builder ran the verify gate and it passed before it said "done"; the summary to the user names what was actually built (checked against the stored record, not the prose) |

A task passes only when every deterministic grader passes.

### No LLM judge

An earlier version had a frozen, pairwise, calibrated LLM judge scoring what
code cannot check (design, description quality), never deciding pass or
fail. It only ever served the old rounds mode, and is gone with it: a
focused run keeps or drops a pass on the deterministic checks across the
whole suite, and the friction report's reviewer reads every build for what
got in the way. Every grader a task declares decides.

## The scorecard

Per task, per variant, per tier:

- deterministic pass rate over the N runs, with the spread between runs
- cost: tokens, wall time
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

The lead model reads the train-split failures (grader verdicts and the
transcript of the failing run) and proposes ONE targeted edit: a
named block, a diff, and the failure it addresses. Not a rewrite of the whole
prompt; a rewrite cannot be attributed and usually drops something.

Operators the proposer can choose from:

- **targeted edit**: add, change or remove a specific instruction
- **compress**: today's Optimize, made accountable
- **revert**: undo an earlier edit in the lineage that has stopped paying

A candidate is accepted only when all of these hold:

1. the task it was made for passes with it (a probe fix: Builder reaches for
   the right thing first)
2. on the whole suite at the pass's end, more train builds pass and neither
   split got worse
3. the total prompt length stays under its cap (a model can always buy points
   with more text, and every token is paid on every turn)

A tuning run has a token and wall-clock budget and stops after N passes in a
row with no edit that held. When it ends it applies what it kept: each
changed block's shared text, which both models read, and Undo takes the
whole run back at once from the run's own record of what it replaced.

## Tool descriptions are weights too

How Builder builds is decided as much by the descriptions of the tools it
builds with as by its prompt: when to make a machine and when a pipeline,
what a tool definition needs before it is done. So the shipped framework and
authoring tools' descriptions are blocks, keyed `tool.<name>`, in Prompt
overrides beside the rest (`core/prompts/tool_desc.go`, the names in
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
- **Only tools the suite measures for everyone who reads them.** A tool
  other agents use for their own work (scheduling, run inspection, sessions,
  fetching) is left out even when Builder misuses it during builds: the
  suite measures Builder alone, so an edit fitted to "Builder should not
  reach for this to try something" could change how every other agent uses
  it, with nothing watching. Builder's misuse is fixed where only Builder
  reads it, in its own prompt block, which Optimize already edits. Such a
  tool joins the list once its own users have a suite (decided 2026-10-04,
  after the first friction report showed create_standing_agent, list_runs,
  inspect_run, open_session and fetch_url misused in builds).
- **Parameters too.** Each top-level parameter of a named tool is a block of
  its own, `tool.<name>.<param>`: the parameter descriptions carry much of
  the how (app_def's sections and data sources, a machine's phases). They
  are recorded and listed the same way, so they come back after a restart,
  and tuned the same way. Nested properties are left as the code
  builds them. The proposer is shown a parameter's block only when its tool
  appears in the builds that failed, since there are hundreds of them.

The proposer is told that a `tool.<name>` block is the description of that
tool, to be edited when a failure is a wrong choice of tool, and a
`tool.<name>.<param>` block a parameter's, when the tool was right and the
call was filled in wrong.

**Shorter is rewarded, not just allowed.** Tool schemas and prompt blocks
are about 44k tokens, some 82% of every Builder call, so a shorter block
makes every turn of every agent faster and cheaper. The growth cap only
stops prompts getting longer, and `compress` was only ever chosen to fix a
failure. So a run tightens first: before any fix it shortens the longest
blocks, builds the whole suite once on the cuts, and keeps them only if no
build is lost (no worse, rather than better). A batch that loses a build
is dropped whole. "No worse" (`noWorse`) means no split's verdict worse and
no split passing fewer builds, so "no builds lost" is literal, at the price
of a good cut sometimes dropped by one unlucky build.

History (private `trim.go`): built 2026-10-04 as a trim after each
confirmed pass, the 3 longest blocks, taking cuts back one at a time when a
batch lost a build. Moved first on 2026-10-05 (`a20ccdb`): the 6 longest,
before the first pass, so the fixes land in the short wording. Made the
only trim, with no take-backs, the same day (`1543bb8`), after the first
extended run showed what whole suites cost (see "The first extended run").
The start is measured only when there are cuts to judge, and the budget is
checked again once it is, since that is the first suite whose cost is
known. A restart that cut the tightening off before its suite tries those
blocks again (`tightenedFirst`). Setting **Tighten first** on Details,
default on.

## Per-tier profiles

**Superseded for Optimize (2026-10-04):** a run now writes one shared
wording aimed at the worker (see "Using it"). The per-tier machinery below
stays in core (a tier's own text, swapped in at the LLM handle) but nothing
writes it now; Undo of an older run, Reset to shipped and Settle prompt
wording clear what earlier runs left there. Kept as the record of what was
built and why it was set aside.

The same block can be tuned differently for the worker and the lead. This is
the per-model prompt profile the framework was always missing: prompt
discipline is model-dependent data, not a fixed floor, and the profile is
measured rather than guessed.

- **Resolution: tier, then all tiers, then the shipped default**, exactly as
  reply guard settings resolve (`replyguard.Resolve`).
- **A block forks only on evidence.** The evidence is which tier an edit was
  measured on: an edit that won on the worker is the worker's own wording,
  and the lead keeps the shared text it was never tested against. The
  shared wording changes only from a routed session or in the source. Two copies
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
  less than every tier gets. Each swap made is recorded per block
  (`TierTextApplied`), so wording that never reaches a prompt can be found.
- **Mixed turns are already per loop.** A lead plan with worker steps sends
  each call through its own tier's handle, so each gets its own words.
- **A session pinned to one tier writes that tier's words.** It measured no
  other, so its kept edits are that tier's own text, applied as that model's
  own wording (What changed on the run shows it), leaving the
  shared wording alone. A routed session edits the shared wording. A lead-pinned sandbox serves both of its tiers
  with the lead's model, so the lead's words go to both there.
- **Lead tuning has a spending cap.** Every session carries a meter: what
  its sandboxes spent, read from each sandbox after every build, and what
  its proposer and diagnosis spent here, priced at the deployment's Prices.
  A run that passes the cap is stopped where it stands. A session that can build on the lead does not start without a
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
4. **The judge.** Pairwise, swapped, calibrated against the owner's picks
   (since removed: it served only the rounds mode, also removed).
5. **The proposer.** Targeted edits and compress, the acceptance rules, the
   budget.
6. **Per-tier profiles** on the live server, once a block has shown it wants
   to differ.
7. **Lead tuning**, with its spending cap.

Built since, on top of those: tool and parameter descriptions as blocks;
one-click Optimize with Undo (one run aimed at the worker since
2026-10-04, per model before); the focused mode (fix as it goes,
confirm on the whole suite), now the only mode; choice probes, and Ask
Builder on Details; giving way to people and picking up after a restart; stages on
every long run; and the friction report, which comes first.

The suite and harness also serve the self-fine-tune plan: the eval app that
plan called "oracle" is this harness pointed at a LoRA adapter instead of a
prompt variant.

## Live run plan

Everything is built. Optimize first ran against the live deployment on
2026-10-03 (see "What the first live runs showed" below); the first friction
report ran the next night, and its fixes are in. The plan is a ladder: each run proves the harness can be trusted with the next, and
each says what to look at before climbing. Every run starts from the
Optimize section on the admin LLMs tab. Runs give way to people using the
model they build on, so they can run while the deployment is in use, only
slower; overnight is still the natural time.

### Before the first run

1. **Setup.** The deployment builds with the tuning app registered (the
   blank import in the machine-local `private.go`), a lead model that is not
   the worker (the friction report's reviewer and the run's diagnosis are the lead), the lead's
   price set under Prices, and room on the disk beside the workspaces for
   `tuning-sandboxes` (one copy of the binary and a fresh data directory per
   run; anything older than a day is swept).
2. **The suite.** Twenty tasks, fifteen train and five held out, aimed at
   the friction of the framework and authoring tools. See the suite's
   README. The visibility gaps found writing the first plan (a silent
   progress line, rounds invisible until they ended, no scorecard) are
   closed.
3. **Live on what ships.** Anything saved in the old hand editor still
   applies, and it would be measured as if it were the shipped text. Run
   **Settle prompt wording (once)** (Admin > Maintenance > Housekeeping)
   after upgrading past v0.7.317; it logs each text it clears.

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

Worker, **Optimize** (quick by default: two passes, four hours, the cap from
Details).

The first real proposals. Look at: probes running before each build and
their fixes; failures set aside as the framework's (each one is a gap the
friction report missed, so write another report); each kept
edit, and whether it names a general rule or the task it saw; the result
applied as the shared wording; Undo and Reset to shipped putting live
back; restarting the server mid-run and the
run picking up where it was.

### Run 2: noise

Extended run on, and from Details: 5 builds per task on the whole suite, 1
pass.

The whole-suite measurement of where it starts is the point. Read the spread per task off the
scorecard. It decides how many builds per task a confirmation needs and
whether the whole-suite check can call a change better on a suite this
size. A task that passes 0 of 5 or 5 of 5 every time tells the tuner
nothing and is a candidate to rewrite; one that flips between runs of the
same prompts is the noise floor.

### Run 3: extended

**Optimize** with Extended run on, and the builds per confirmation
from run 2. A kept edit that holds on held-out tasks is the first one worth
keeping for good.

### Run 4: the lead

No longer a run of its own: the lead helps every run and is not tuned apart.
Check it instead in the friction report after a run (tasks the lead passed
before and fails now) and with Ask Builder's lead column. Watch Spent
against the provider's bill on any run, since the lead's help is paid for.

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

### The first friction report (2026-10-04)

Report a1dcb6f6 on v0.7.320: 0 proven, 8 blocking, 15 friction; the worker
passed 18 of 40 builds, the lead 11; six tasks nobody passed. Each item was
checked in the code before anything changed, and the larger share of the
blocking ones were the harness's, not the platform's:

- **The graders, mostly.** A tool check called the built tool with its own
  argument and action names, which no request tells the builder, so
  city vs city_name, from vs from_currency, current vs get_current missed,
  and the error was thrown away. App checks seeded number fields as text.
  word_stats never named its JSON keys; c_to_f checked one formatting.
  Fixed in the harness: arguments bind to the builder's names, numbers
  seed as numbers, failures carry the error. The reviewer's most repeated
  complaint, "verified: pass beside a failing check", was a misread: that
  check is the builder's own last verification, and now says so.
- **The platform, the rest** (v0.7.323-327): the never-worked guard told
  the model a built-in tool was broken after five of its own validation
  errors; toolbox top-level params, method and headers were ignored; a
  param's default was dropped; a write fired by hand never counted; there
  was no way to run an unattended machine from the turn, and a pipeline
  run had no machine runner; a run ending on a skipped step returned the
  step before it as the result; app verify said OK on an empty store; the
  html JS check ran node 10 and refused modern syntax; a list argument
  sent as a JSON string wiped what was stored; a credential refused its own
  base URL.
- **Builds leaked into later builds.** With no way to run a machine once,
  Builder made standing agents on 30 and 60 second intervals to get one
  run, and they fired for the rest of the run on the models every later
  build was measured on. The sandbox now retires an earlier build's
  standing agents and monitors when the next build begins (logged), and
  create_standing_agent says it is not for one run (v0.7.330).
- **Left as found:** the machine "prompt asks for JSON" warnings (working as
  meant), delegation to one's own agent needing approval (intended; machine
  run is the way to try one now), the fx fixture's 422 for pairs it does not
  know (sandbox only), and one verification report missing an endpoint
  (the reviewer's own misread: each tool result in its transcript was clipped
  to 600 characters, and tool_def test's output opens with a 360-character
  untrusted-content fence, so it saw one endpoint of two. The reviewer's copy
  now tags the fence in a few words and keeps 1200 characters a result).

Next: rebuild and restart, run Settle prompt wording (once), then a third
report to confirm the second's fixes (toolbox wrapping on the worker above
all), then the first Optimize run.

### The second friction report (2026-10-04)

Report 55151b1b on v0.7.353: the worker passed 23 of 40 builds (18 in the
first report), the lead 30 of 40 (11), with no blocking component and no
task that nothing could do (8 and 6 before). The lead nearly tripling
with no tuning confirms its first-report failures were the harness; it
also shows the one shared wording has not hurt it. The shell tasks went
from none to all on the worker (the grader fixes) and every machine task
passes at least once (machine run). What is left on the worker is mostly
wrapping an API as a toolbox: books, fx, library, todo and translate
passed none there, while the lead passed most of them.

- **Harness, again** (private a25334c): an errored build kept no
  transcript, so its tool errors arrived with none of the calls; the
  grader matched actions by name only, so "book" never found get, lookup
  or get_by_id (it now takes the one action that can take its arguments);
  two fields sharing a word made a field look missing; the tide task
  checked for a tool its request never asked for; the jobs fixture blamed
  the header for a missing q.
- **Platform** (v0.7.356): a step's tool sent as an object was saved as
  nonsense; naming no app or machine read as "not found"; "NO action
  buttons" was read as no edit buttons; tool_def help buried the toolbox
  shape under the shell notes.
- **tool_def** (v0.7.360): a write fired by hand reached the session's
  verify record but never the build ledger, so translate and todo kept
  failing "verified" after doing it right; path params read as not
  required for several shapes a builder meant as required (14 times);
  test_args was ignored and create ignored cases; a passing read showed no
  data; method and body slipped into url_template unchecked; actions were
  dropped outside toolbox mode; a failed probe did not show what was sent.
- **From the lead's half** (v0.7.358): a pipeline tool stage decoded the
  untrusted fence's "[" as a JSON array, failing tools that answered with
  exactly the declared object; reply_with on an unattended machine was
  refused in conversation terms, so builds kept putting it back; a sample
  that did not parse was run as no sample and reported OK. Four more
  tool_def items went in with the rest in v0.7.360 (a read that passed
  going back to unproven on a re-test, a RESULT line that led with
  "passed" while a write still waited, a response_pipe yielding nothing
  passing, the toolbox's missing top-level description). Every confirmed
  item from the second report is now fixed.
- **Misreads:** the "wrong book" was the try-it ISBN's own (Dune), the
  read_output ids and the literal "..." and "N" arguments were the
  builder's own invention, the library tool had already been deleted.

### The first extended run (2026-10-05)

Started 2026-10-04 21:56 on the 21:50 build (trim after each pass, with
take-backs), and it never got past pass 1. Not a hang: an extended whole
suite is 20 tasks at 3 builds each, 60 builds, and took 1.5 to 2.3 hours
each time. Pass 1's one-task builds took 1.5 hours (22:05 to 23:31); then,
from the log:

| Whole suite | What it was | Passed |
|---|---|---|
| 23:32 | measuring the start | 39 of 60 |
| 01:05 | confirming pass 1's fixes, rejected | 41 of 60 |
| 02:55 | one fix taken back, rejected | 39 of 60 |
| 04:26 | another fix taken back, kept | 47 of 60 |
| 05:59 | the trim after the pass, rejected | 38 of 60 |
| 08:17 | one cut taken back | |

A pass could spend seven whole suites, more than 12 hours holds, and the
tighten-first stage as first built would have added up to four more ahead
of it. Fixed in `1543bb8`: tightening once, with no take-backs, and no trim
after a pass, so the most an extended run spends before pass 2 is five
suites (start, cuts, confirm, two fix take-backs). Fix take-backs stayed:
the kept one at 04:26 was the run's best result.

The lesson for anything added to a run: count the whole suites a pass can
spend against the hours. On the extended run each is roughly two hours.

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
- Spent and Cost History disagree.

## Open questions

- **How many repeats are enough?** Decided by measurement: run 2.
- **How far to trust the reviewer.** It is the lead, uncalibrated; the
  known-good builds and the both-model builds now carry the weight, and its
  calls sit beside them. Owner picks on its calls if it misreads often. On
  the first report it was right that its "grader" group was broken, wrong
  about why: it blamed the platform's verification, and the fault was the
  harness's own checks. A component it files under grader or page check is
  read as the harness's first. And a misread can be the harness's too: it
  judges a transcript the harness clipped, so before believing "X is missing
  from Y", check that Y reached it whole.
- **Decided: a write fired by hand is remembered in memory only.** A
  restart forgets it, which costs one more direct call or test, and only
  when the restart lands inside one build's verify step: a verified tool
  stays verified (stored per session), a call made after the restart is
  recorded fresh, and a tuning sandbox never restarts mid-build. Storing it
  would let a saved "this worked" outlive the service it was about, for one
  call saved in a rare case. Records and waiting writes alike are dropped
  after 24 hours (v0.7.337).
- **Closed from the first report.** fetch_url's private-host refusal names
  the credential on that host whose base path the URL left off (v0.7.335):
  the builder had written the URL from the host alone.
- **Whether the friction report becomes a periodic check.** It is run by
  hand while the platform is being got right. If reports keep finding real
  issues after the first round of fixes, a scheduled report after each
  release would catch regressions before tuning does.
- **Where a task's expected output comes from for machines.** Hand-written for
  now; a machine whose job is judgement may only be gradable by reading it,
  which makes it a poor task.
- **Sandbox resources.** A second gohort plus headless Chrome for app checks
  on the same box as the live server. Runs give way to people on the model
  they use, but the CPU and memory of the sandbox are not yielded.
