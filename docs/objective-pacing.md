# Objective pacing: an attempt that can say when to come back

Status: **built** (v0.6.758, 2026-09-14), on all three scheduling surfaces. The one piece
deliberately NOT built is stage 3's optional `pace: "self"` backoff — see the end of **Stages**. Written after the
question "should we have a Task registry for agentic tasks", where the answer was no (objectives
already are that registry, `docs/loop-objectives.md`) but one of the two gaps behind the question is
real.

Stage 1 landed as: `core.RescheduleTaskAt`, the `core/pacing` package (the ask and the tool),
`NextAttemptAt`/`NextAttemptWhy` on `orchUpdatePayload`, and `apps/orchestrate/objective_pacing.go`
(the bounds, the window deferral, and the move). Tests: `core/pacing/pacing_test.go`,
`apps/orchestrate/objective_pacing_test.go`, `TestRescheduleTaskAt` in
`core/scheduler_update_test.go`.

**One deviation from the design below, forced by the build.** The spec put the tool in core as
`core.NextAttemptTool`. Five new exported names tripped `TestCoreStaysUnderItsCeiling`, so the ask
and the tool went to a subpackage instead: `pacing.Tool`, `pacing.ToolSpec`, `pacing.Ask`,
`pacing.ToolName`. Only `RescheduleTaskAt` stayed in core, where it has to be (it touches the
scheduler's own store). This is the better shape anyway and the documented answer to that pressure:
a host imports `core/pacing` directly rather than reaching it through an alias.

Read `docs/loop-objectives.md` first. This changes exactly one thing about an objective: *when* its
next attempt happens.

## The gap

An objective is a schedule with a completion check, and that is still the right shape. But every
surface it rides fires on a clock: `interval_minutes`, `times_per_day`, the random `min_gap`/
`max_gap` window, cron, a poll interval. The cadence is decided once, at creation, by whoever wrote
the task, and nothing that happens afterward can change it.

So an attempt that knows exactly why it fell short cannot act on it. The judge writes "the build was
still running when the check ran", `appendObjectiveAttempt` stores that sentence, the next fire is
told it in `objectiveAttemptsBlock`, and then the next fire happens an hour later because an hour is
what the task was created with. The information is complete and inert.

Two costs, and the second is the sharper one:

1. **Attempts are spent by the clock rather than by trying.** `MaxAttempts` counts fires. An
   objective waiting on something external burns its whole allowance reporting that it is still
   waiting, and stalls having never had a real second try.
2. **A goal with no natural rhythm has to invent one.** "Get the release notes published once the
   build goes green" is not an hourly task, it is one task with an unknown wait in the middle. Today
   the author picks an interval that is a guess, and the guess is the schedule forever.

## What this adds

One ask: **the attempt may name when the next attempt should happen.** Nothing else about the
objective changes. Not a new record, not a new trigger kind, not a schedule edit.

### The fields

On the schedule's own record, beside `Until` / `MaxAttempts` / `Attempts`:

| field | meaning |
|---|---|
| `NextAttemptAt string` | RFC3339 UTC. The time this attempt asked for. Consumed when applied, then cleared, so it can never be read as a standing preference. |
| `NextAttemptWhy string` | one line, the attempt's own words. Shown on the card and in the console, and it is the whole accountability story: a fire that moved itself has to say why. |

Flat fields, not an embedded struct, for the reason `objectiveRun` already documents: kvlite stores
these records with gob and gob nests an embedded struct, which would silently change the shape of
every payload already written.

### Who decides, and why it is not the judge

**The attempt decides.** The judge (`judgeObjective`) answers one binary question from evidence and
writes one sentence of reason. It is a small worker call, deliberately, and it does not know what
the attempt is waiting for: it sees the actions and the report, not the state of the build. Handing
it a schedule lever would mix two jobs in one call and give the cheapest model in the stack a say
over how often the deployment does work.

The attempt is where the knowledge is. It is the turn that just ran the tools, read the 401, saw the
queue depth. It should be able to say so in the same breath.

### The tool

A framework-level tool, injected by the **fire host** rather than by any surface's tool group:

```go
// core/pacing, following the core.WorkPlanTools(spec) pattern:
// the package owns the state and the tool, the host wires the seam.
func Tool(spec ToolSpec) core.AgentToolDef
```

Name: `set_next_attempt` (verb_noun, per the tool-name collision rule). Parameters: `minutes`
(relative, the common case) or `at` (RFC3339, for "after the 09:00 standup"), and `why` (required).

**It must not be an action on `recurring`.** That is the mistake v0.6.621 already paid for: a Fleet
agent is never given the `recurring` tool, so scoping objectives to it made them unreachable for
exactly the agents most likely to be handed a goal. The pacing tool is mounted wherever an objective
fires, on the same condition every time: `Until != ""` and the fire is the scheduled one (`reArm`).

**Excluded from the objective's evidence.** `objectiveToolLabels` renders the tool trace for the
judge, where the rule is that the actions are the evidence and the report is a claim.
`set_next_attempt` is bookkeeping, not work toward the goal, and an attempt that ran nothing but the
pacing call must still read to the judge as an attempt that ran nothing. Filter it out at
`objectiveToolLabels`, with the reason in a comment, or the first objective to pace itself will look
busier than it was.

### Applying it: the successor is already armed

`preArmNextFire` persists the next occurrence **before** the fire runs, so a process death mid-fire
cannot end the chain. That is why a met objective has to *cancel* its successor rather than simply
not schedule one, and it is why pacing has to *move* an entry that already exists.

Core needs one new call, a sibling of `UpdateScheduledTaskPayload` with the same lock and the same
contract:

```go
// RescheduleTaskAt moves a STILL-QUEUED task to a new time, returning false when
// it has already fired or been unscheduled. Callers must NOT re-create the task
// on false: re-adding an entry that already fired is how a recurring chain
// duplicates. Exists for the same reason the payload updater does, one step on:
// the pre-arm pattern arms the successor before the fire, so anything the fire
// learns about WHEN the next one should run arrives after the entry exists.
func RescheduleTaskAt(id string, runAt time.Time) bool
```

In `fireOrchestrateUpdate` the pacing step sits in the objective block, after the verdict and after
the stop/park decisions, and runs only when `reArm && armedID != "" && !objStopped`. A stopped
objective has no successor to move; a stalled one is parked and must stay parked, and a pacing ask
on a stalled fire is dropped with a log line rather than silently honoured.

Order within the fire: judge, record the attempt onto the successor's payload, decide stop / park,
then pace. The existing `UpdateScheduledTaskPayload(armedID, armed)` at the tail of the fire keeps
doing its job unchanged; pacing adds one call beside it.

## Guardrails

- **Floor.** Never sooner than `orchUpdateMinInterval()`. An attempt asking for 30 seconds gets the
  deployment minimum, and the clamp is logged.
- **Ceiling, and it is not just a number.** Never later than 7 days, **and never past the task's own
  idle-reap horizon** (`orchUpdateIdleDays`). A fire armed beyond the reap window would be reaped
  before it ever ran, which reads to the owner as a task that vanished. Clamp to the earlier of the
  two, log which one bound it, and say so on the card.
- **The window still rules.** A paced time outside `active_from`/`active_to` defers to the next open
  through the existing `nextWindowOpen`. Pacing does not buy a 3am wake-up.
- **One occurrence, not the schedule.** The cadence is untouched. The fire after the paced one is
  back on the normal rhythm. An agent that genuinely wants a different cadence already has
  `recurring(action="schedule")` with the same name, which edits in place; pacing is a deferral of
  the next attempt and nothing more. This is the containment that keeps the feature from being a
  second, undeclared way to rewrite a schedule.
- **Objectives only.** A task with no `until` cannot pace itself. Without a done check, "come back
  later" has no way to ever stop, and for a plain recurring task the cadence *is* the contract.
- **One ask per fire.** Last call wins, and the override is logged. Nothing is gained by letting a
  turn negotiate with itself.
- **Never on Run now.** `RunOrchestrateUpdateNow` deliberately does not touch the schedule; that is
  the whole contract of the manual path, and the one way an owner retries a stalled objective.
- **It still counts as an attempt.** Stage 1 does not exempt a deferred fire from `MaxAttempts`.
  The alternative (a `blocked: true` ask that does not count) is defensible and probably right
  eventually, but it needs its own cap or an objective can defer forever and never spend anything.
  Named here so the next person does not think it was overlooked; see **Later** below.
- **Pacing grants nothing.** The paced fire runs under the same pre-authorised tool set and the same
  queue as any scheduled fire. `set_next_attempt` changes a time, not a permission.

## Where it shows

No new page, no new rail, following the loop-objectives precedent exactly.

- **The report card** detail line, beside the cadence and fire number: `· next attempt 14:00, waiting
  on the build`.
- **The console row** (`console_recurring.go`): the State cell gains the reason, so an objective that
  has moved itself twice says so. The Next run cell already renders the armed time, so it reports the
  moved time with no change.
- **The run ledger** row keeps the verdict prefix it already has. Pacing is a fact about the next
  run, not about this one.

## Stages

**Stage 1, recurring tasks. BUILT.** `RescheduleTaskAt` in core, the two fields on
`orchUpdatePayload`, the tool and its seam, the guardrails, the card and console lines. This closes
the gap on its own: it is the surface an agent's own `recurring` tool creates, and the one with the
card per fire.

Two things the build settled that the spec left open. The ceiling is measured from the moment of
pacing and the move renews `LastActive` in the same pass, so the fire that waits cannot be reaped
for having waited. And an ask made on a fire that exits early (failed, guardrail-stopped, empty
reply, preamble-only) is dropped rather than applied: those paths return before the objective is
judged at all, and a schedule moved by a fire nobody judged would be the one kind of pacing with no
verdict beside it on the card.

**Stage 2, standing agents. BUILT.** The same tool at the standing runner's fire, the same two
fields on `StandingAgent`, and `ObjectiveAttempt.NextAt` so `objectiveAttemptsBlock` renders
`3. 2026-09-06 09:00 — not yet: the build was still running (asked to resume 13:00)`. Without that
line the next attempt knows it waited but not that it *chose* to.

Three things the build settled:

- **Nothing is moved on this path, and that is simpler rather than harder.** A standing agent's
  successor does not exist while the fire runs: the re-arm is DEFERRED and re-reads the record
  afterwards. So the ask is only written down, and `ScheduleStandingAgent` honours it, consumes it,
  and goes back to the cadence for the occurrence after. `RescheduleTaskAt` is not involved.
  The reason outlives the ask by exactly one occurrence, because it explains the run now armed.
- **The tool needed a seam.** A standing fire builds its tools inside the dispatch path, where the
  caller cannot reach. `runAgentSyncExtra` takes tools the CALLER supplies for this run only, and
  `runAgentSyncConfirm` is now a one-line wrapper over it. Appended last, so a run-scoped tool
  cannot be dropped by a filter meant for the agent's own catalog. The tool belongs to the fire, not
  to the agent: the same agent dispatched from a conversation has no next run to move.
- **It is mounted on a manual run too**, unlike the recurring path. The runner closure is not told
  the trigger, and the objective block had already made this call for the bigger hammer: a goal that
  is met is met however the fire that met it was started. A fire that is blocked is blocked the same
  way.

No window and no reap ceiling here: a standing agent has neither. The floor is the same deployment
minimum, because it answers the same question and is the only floor there is.

Where it shows: the standing row's detail strip (`console_recurring.go`) gains `· waiting: <why>`
beside the cadence, and the run's summary carries the pacing line after the verdict. Tests:
`TestScheduleStandingAgentHonoursAPacedAttempt` / `TestAPacedTimeInThePastIsIgnored` in
`core/standing_rearm_test.go`, and the standing cases in `apps/orchestrate/objective_pacing_test.go`.

**Stage 3, event monitors. BUILT.** Same two fields on `EventMonitor`, honoured and consumed by
`ScheduleEventMonitor`, which arms the next poll exactly the way `ScheduleStandingAgent` arms the
next run.

What is different here is WHO asks. A monitor's own check is a poll with no turn in it, so the asker
is the agent the check WOKE: it reads "PR #12 has a new review comment" and can answer "still in
review, don't look again until tomorrow". That makes this a snooze, and it arrives through
`AgentSyncRun.AppTools`, which already existed for caller-injected per-run tools. Two consequences
worth stating:

- A monitor delivering only by `text` or `direct` never paces. Nothing ran that could have an
  opinion, and that is the correct answer rather than a gap.
- A webhook monitor never gets the tool. It has no timer to move, so offering it would be offering a
  button that does nothing.

The floor here is the monitor's OWN interval, not a deployment minimum: a snooze is by definition
later than the next check would have been, and asking for sooner is asking for nothing. Core enforces
the same rule at arming (`NextAttemptAt` wins only if it is after `nextPoll`), so a paced check can
never poll something faster than its owner set it to. A met goal drops the ask instead of leaving a
reason on a monitor that has stopped.

**`pace: "self"` — NOT built, and the recommendation is to leave it.** The spec called it optional
and droppable, and building the rest made the case clearer:

- It adds a MODE to three records and backoff arithmetic to three arming sites, for behaviour nobody
  has asked for.
- The gap this document opens with is closed by the tool. Backoff is the fallback for attempts that
  DON'T ask, and an attempt that does not ask is one that either did its work or has no opinion.
  Doubling its interval is a guess made by the framework, on behalf of something that knew better and
  said nothing.
- It would give one schedule two things moving it, and every surface that explains the next run would
  have to say which of them chose it.

**Backoff on FAILURE, the narrower feature, IS built** (v0.6.759,
`apps/orchestrate/failure_backoff.go`). Different trigger, different information: pacing is an
attempt saying what it is waiting for, backoff is the framework noticing that nothing is working.
They meet only at the end, where both move the next occurrence through the same two fields.

- **Recurring and standing only.** Event monitors already had this bound: `ConsecutiveFailures` plus
  `monitorFailureThreshold` parks them after three failed polls. Backing off there would only make
  the owner wait longer to learn it is broken. The other two had no bound at all — a fire that
  errored was recorded and re-armed on the same cadence, forever.
- **The curve is relative to the schedule's own cadence** (2x, 4x, 8x, 16x, then stop climbing):
  five minutes is a long wait for a five-minute task and no wait at all for a daily one. Capped by
  the same ceiling pacing uses, so a backed-off fire still cannot be armed past the idle reap.
- **An explicit ask beats the curve.** A fire that failed but still said when to come back knows
  something a counter does not. The streak counts either way, so failures that each ask for a minute
  do not escape the bound.
- **The first run that works clears it.** An empty or suppressed fire leaves the streak where it
  was, which is the honest reading: neither proves the thing is fixed.
- The row says which of the two moved its next run, whether or not the task carries an objective.

## Tests

- `core/scheduler_test.go`: `RescheduleTaskAt` moves a queued task, returns false for a fired or
  unscheduled one, and does not resurrect it.
- `apps/orchestrate/objective_test.go`: a pacing ask moves the armed successor; a met objective
  ignores one; a stalled objective ignores one; a sub-minimum ask is floored; an ask past the reap
  horizon is clamped to it; an ask outside the window defers to the next open; the pacing call is
  absent from the judge's evidence labels.
- The tool is absent from a fire with no `until`, and from a manual Run now.

## What this is not

Not a new registry, and not a new noun. The objective record already holds the goal, the bound, the
attempt history and the terminal states; this adds a time to it. Not a schedule edit: one occurrence
moves, the cadence does not. Not a way out of the attempt bound: a deferred fire still spent one.
