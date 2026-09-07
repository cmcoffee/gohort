# Loop objectives — a recurring task that knows when it is done

Status: **built** (v0.6.621, 2026-09-07). All three stages, on BOTH scheduling surfaces.
Decision locked by the build: an objective is a schedule with a completion check, not a new
trigger kind.

**Standing agents too (v0.6.621).** The original spec scoped this to recurring tasks and put
standing agents in Out of scope. That was wrong, and the reason is structural rather than a
matter of taste: a Fleet agent is not given the `recurring` tool at all — it schedules through
`create_standing_agent` — so scoping objectives to `recurring` made them unreachable for exactly
the agents most likely to be handed a goal. Found by asking one for an objective and watching it
do the work inline instead, because it had nothing else to reach for.

Landed in stage 1 (v0.6.615): `apps/orchestrate/objective_judge.go` (the check, its evidence, and
the outcome rule), `Until` / `MaxAttempts` on `orchUpdatePayload` and `RecurringSpec`, and the
judge step, card line, ledger prefix and stand-down in `fireOrchestrateUpdate`.

Landed in stage 2 (v0.6.616): `Attempts` on the payload, `noteObjectiveAttempt`,
`objectiveAttemptsBlock`, and the block's injection into the fire's prompt. Stage 2 also fixed a
stage-1 mistake — see **Stop or continue** below: a stalled objective was cancelled, which made
the task VANISH from the console rather than stand there saying it had stopped.

Landed in stage 3 (v0.6.617): `until` / `max_attempts` on the `recurring` tool and its listing,
the objective state on the console row, and **Resume** for a parked task
(`handleConsoleRecurringResume`) with `AttemptsBase` so a resumed objective gets a fresh
allowance instead of stalling on its first fire.

Tests: `apps/orchestrate/objective_test.go`.

## The gap

gohort already loops four ways. A recurring task re-fires on its cadence until its `max_fires`
cap. A standing agent runs on cron. An event monitor polls until its condition matches and wakes
an agent. A pipeline loop stage repeats until a bool field flips. The machinery for "do it again"
is not the problem.

None of them knows whether the goal was reached. A recurring task told to "get the blog post
published and linked from the thread" fires every day until its cap, whether or not the post
went out on day one, and every fire starts from compacted history rather than from a record of
what the earlier fires tried and why they fell short. The person reading the cards sees the same
attempt narrated five times; the model making the attempt has no idea it is the fifth.

An objective is the smallest addition that closes that gap: a done check judged after each fire,
an attempt ledger the next fire reads, and a stop that says what happened.

## What an objective is

One `orchUpdatePayload` (a recurring task) with three new fields:

| field | meaning |
|---|---|
| `Until string` | the completion check, in plain language: "the post is published and its URL was posted to the thread" |
| `MaxAttempts int` | how many fires may end without the check passing before the task escalates; 0 = `max_fires` governs |
| `Attempts []objectiveAttempt` | `{at, met, reason}` per earlier fire, oldest first, capped at twelve. Carried on the PAYLOAD, which is what survives into the next fire — the same reason `RemainingToday` and `LastActive` live there. |

**On `Attempts` and the run ledger.** The spec first proposed this field, then dropped it on the
grounds that the ledger already records a row per fire. Building stage 2 showed that was half
right and settled it the other way: the ledger holds the *fire*, in prose written for a person to
read in Activity, while the next attempt needs the *verdict*, structured. Recovering reasons by
parsing a display string would make a wire format out of a sentence written to be read. Two
records, two jobs, and the bound counts neither — `MaxAttempts` is measured against `FireCount`.

Everything else is the recurring task it already is: prompt, cadence (`interval_minutes`,
`times_per_day`, the random `min_gap`/`max_gap` window, `active_from`/`active_to`), surface,
`max_fires`, the pre-armed successor, the run ledger row per fire, the report card in the thread.
No new scheduler kind, no new console rail, no new storage table.

Why not a new primitive: the four loops exist because each has a different *trigger*. An objective
does not change what triggers a run, it changes what the run is *for*. Grafting it onto the one
loop that already posts a card per fire and already records a run per fire means the ledger, the
card, and the cap come for free.

## The fire, with an objective

`fireOrchestrateUpdate` runs the fire exactly as today. After the reply is in hand and before the
card is appended:

1. **Judge.** *(built)* If `Until` is set, ask the worker tier one question against the fire's reply and
   its tool trace: *did this attempt satisfy `Until`?* Answer shape is the turn-claim judge's
   (`judgeTurnClaim` in `apps/orchestrate/turn_judge.go`): a verdict and a one-line reason,
   nothing else. The evidence is the same `TurnClaimEvidence` the reply judge already assembles,
   so an attempt cannot pass by narrating a success its tool calls do not show.
2. **Record.** *(built)* The run ledger row for this fire (`RecordRun`, already written with
   `Task`, `Brief`, `Summary`, `Steps`) gets the verdict as its `Summary` prefix, so the Activity
   feed shows it without a schema change and the history is queryable by task name. A stalled
   objective also flips that row to `RunAttention`.
3. **Card.** *(built)* The report card (`ReportFrom`/`ReportKind: cortexKindScheduled`) carries the
   verdict in its detail line, beside the cadence and fire number: `· objective met — <reason>`,
   `· objective not yet — <reason>`, or `· objective STALLED after N attempt(s) — <reason>`. That
   is the one visible change per fire.
4. **Stop or continue.** *(built)* On a manual Run now the verdict is judged and shown, but the
   schedule is deliberately untouched — that path's contract — so an owner can retry a stalled
   objective after fixing what the reason named.
   - Verdict passed: the pre-armed successor is cancelled (`CancelOrchestrateUpdate`), the card's
     verdict line reads *done*, and the task is retired the way the fire cap retires it today
     (`recurring-retired` diag, final-fire wording on the card).
   - Verdict failed and attempts remain: the successor fires as scheduled.
   - Verdict failed and `MaxAttempts` is reached: the card says *STALLED* with the last reason,
     this fire's own ledger row flips to `RunAttention`, and an `objective-stalled` diag lands on
     the ⚠ trail. The stall rides the fire's existing row rather than calling
     `recordScheduledDrop`, which would file a second run for a fire that did post.

     The successor is cancelled and the task is then **parked** (`parkRecurringBroken`), carrying
     its reason and its attempt history. Stage 1 only cancelled, and that was wrong: the fired
     occurrence is already off the queue before the handler runs, so cancelling the successor too
     removed the last trace of the task — an owner who was never going to get their objective
     would also never see that it had stopped trying. A parked task stays listed, stops firing,
     and can be resumed. A MET objective still retires outright, the way any capped task does.

## The next attempt reads the earlier ones

*(built)* The reason a fifth attempt is not a first attempt: the fire's prompt gets one block,
built from `Attempts` and placed last, in the volatile tail beside the time context — recency is
where it belongs, and that tail never caches, so the block costs no prefix reuse:

```
[Objective: <Until>. Attempts so far: 3 of 5.
 1. 2026-09-04 09:00 — not yet: the post was drafted but never published (create_post returned 401).
 2. 2026-09-05 09:00 — not yet: published, but the thread reply carried the title, not the URL.
 3. 2026-09-06 09:00 — not yet: URL posted to the wrong thread.
 Do not repeat an attempt that already failed for the same reason.]
```

Short, structured, newest last, the same reason strings the judge wrote. This is the failure
memory the agent loop keeps for repeated tool errors (`loadFailureMemory` in
`core/agent_loop_failures.go`), lifted one level: not "this call failed" but "this goal was not
reached, and here is why each time".

## Authoring

Through the existing `recurring` tool, two optional parameters:

- `until` — the completion check. Setting it makes the task an objective.
- `max_attempts` — fires that may end unmet before the task stalls.

*(built)* `recurring(action="schedule")` with the same name and an `until` edits the task in
place, the way it already does for timing. `recurring(action="list")` returns `objective`,
`objective_state` and `parked` beside the cadence, so the model that scheduled one can see where
it stands and that it stopped, without being told to read a console. The tool's own description
carries the one-line rule: give it `until` when the user wants something DONE, omit it when they
want something RUN.

## Where it shows in the UI

The **Recurring tasks** card on the chat page's console rail, and the same rows in the console's
recurring view (`console_recurring.go`). An objective is a recurring row with two more cells:

| cell | today | objective |
|---|---|---|
| Name | prompt's first line | same |
| Cadence | `recurring · every 1440m · 09:00–09:30` | same |
| Fires | `3 / 10 fired` | same — the fire count is not the attempt count once a Resume has moved the allowance |
| State | blank, or the broken label | `objective — no attempts yet`, or `objective — not yet (3 attempt(s)): <last reason>`; a stalled one shows the broken label, which already carries the stall reason |
| Next run | RFC3339 | same; blank once parked |

**Built, differing from the sketch above:** there is no `0 met` cell. A met objective retires, so
the count would read `0` for the whole life of every objective and `1` for none of them. The
State cell carries the verdict instead, which is the thing worth reading.

*(built)* Row actions: Run now, Move to…, Delete on a live row; **Relink** and **Resume** on a
parked one. Run now stays hidden on a parked task because a parked payload short-circuits at the
top of the fire, so the button would do nothing.

**Resume** is the answer to "I fixed what the stall named". It clears the park, puts the task back
on its real cadence, and moves `AttemptsBase` to the current fire count so the allowance restarts
— without that the resumed task stalls again on its first fire, which is the whole reason the
attempt number is measured against the allowance rather than the lifetime fire count. History is
kept: `Attempts`, `FireCount` and the ledger are untouched. Offered on any parked row, since "the
cause is fixed" is the same request whatever parked it; a task parked for a deleted agent simply
re-parks with the same message.

The thread the task reports to already shows one card per fire; the verdict line is the addition.
The Activity feed (`handleConsoleActivity`, the run ledger) shows the verdict in each run's
summary. No new page, no new tab.

Not the Monitor page: that is the live view of what is running now. An objective is a standing
intention, and its home is with the other standing things.

## Guardrails

- **The check must be legible.** `Until` is shown on the card and in the console verbatim, and
  every verdict carries its reason. A judge nobody can read is a judge nobody can correct.
- **The cap is hard.** `MaxAttempts` is not raised by the fire; only the owner raises it. The
  daily spend cap and the fire cap still apply underneath.
- **Same authorisation as any scheduled fire.** An objective runs unattended, so it runs under
  the pre-authorised tool set and the queue the scheduler already enforces
  (`project_scheduled_autonomous_tool_gating`); `until` grants nothing.
- **A failed judge is not a pass.** If the judge call errors, the attempt is recorded as
  *unjudged* with the error as its reason, counts toward `MaxAttempts`, and the successor fires.
  Failing open would let a broken worker LLM turn every objective into an endless loop.
- **No self-grading.** The judge is a fresh worker call with the evidence, not the agent that made
  the attempt. The fire's own reply text is an input to the judge, never the verdict.

## Stages

1. **Fields, judge, ledger, card.** — **BUILT (v0.6.615).** `Until`, `MaxAttempts`, `Attempts` on the payload; the judge
   step and the verdict line in `fireOrchestrateUpdate`; retire on pass; stall on cap. Tests: a
   fire whose judge passes cancels its successor and posts *done*; a fire whose judge fails leaves
   the successor armed and the ledger one row longer; the cap stalls with an attention drop; a
   judge error records *unjudged* and counts.
2. **The attempts block.** — **BUILT (v0.6.616).** Built from `Attempts` into the fire prompt.
   Tests: the block names every prior reason oldest-first, is absent on the first attempt and on
   a task that is not an objective, keeps the newest twelve, and recording on the pre-armed
   successor never reaches back into the firing payload's slice.
3. **Authoring and console.** — **BUILT (v0.6.617).** `until` / `max_attempts` on the `recurring`
   tool and its listing, the objective state on the console row, and Resume for a parked task.
   Tests: the tool declares both parameters; the state label reads correctly at each stage; a
   resumed objective starts a fresh allowance instead of stalling immediately.

Each stage was shippable alone, and each was shipped alone. Stage 1 stopped a met goal from
re-firing; stage 2 made the unmet ones improve; stage 3 made the whole thing reachable from a
conversation.

**What the build changed about the spec**, in order: the `Attempts` field was dropped in stage 1
and restored in stage 2 (the ledger holds the fire for a person, the payload holds the verdict for
the next attempt); the stall was cancelled in stage 1 and parked in stage 2 (cancelling made the
task vanish); the attempts block lost its `of N` denominator in stage 3 (a Resume makes it false,
and it is pressure to overclaim); and `AttemptsBase` was added in stage 3 because a bound measured
against the lifetime fire count cannot be resumed.

## The standing-agent half

Same judge, same outcome rule, same attempts block. What differs is only what the two records
can offer:

| | recurring task | standing agent |
|---|---|---|
| authored with | `recurring(action="schedule", until=…)` | `create_standing_agent(until=…)` |
| attempt number | `FireCount + 1 - AttemptsBase` | `UnmetCount + 1` — there is no lifetime fire count to subtract from |
| met | cancels the pre-armed successor, task retires | sets `Paused`, schedule stays listed and can be started again |
| stalled | parks via `parkRecurringBroken` | `MarkStandingAgentBroken`, which pauses and unschedules |
| resumed by | console **Resume**, moving `AttemptsBase` | `ClearStandingAgentBroken`, zeroing `UnmetCount` |

Stopping needed no new core plumbing on either side. The standing scheduler re-arms in a
`defer` that re-reads the record and skips a paused one, and its own comment already said that
re-read exists to honour a pause or edit that happened during the run.

One deliberate asymmetry: the recurring path judges a manual Run now but leaves the schedule
alone, because "Run now does not touch the schedule" is that path's documented contract. The
standing runner acts on any fire, because the runner closure is not told the trigger, and a goal
that is met is met however the fire that met it was started.

`core.ObjectiveAttempt` is the one type both records store, and it is the only objective code in
core — the judging and the outcome rules stay with the runner in `apps/orchestrate`. The fields
are FLAT on both records rather than shared through an embedded struct, because kvlite stores
them with gob and gob nests an embedded struct, which would change the shape of everything
already written.

## Out of scope

- Objectives on pipelines and machines. A pipeline loop stage already has `until` as a bool
  field; a machine's phases already have exits. If a goal needs those, author it there.
  (Standing agents WERE listed here and have since been built — see the status note.)
- A judge that plans the next attempt. The ledger tells the next fire what failed; deciding what
  to do about it is the fire's job, with the tools it already has.
- Cross-agent objectives. One task, one agent, one thread, like every recurring task today.
