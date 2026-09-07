# Loop objectives — a recurring task that knows when it is done

Status: **stages 1-2 built** (v0.6.616, 2026-09-07). Stage 3 unbuilt. Decision locked by the
build: an objective is a recurring task with a completion check, not a fifth trigger kind.

Landed in stage 1 (v0.6.615): `apps/orchestrate/objective_judge.go` (the check, its evidence, and
the outcome rule), `Until` / `MaxAttempts` on `orchUpdatePayload` and `RecurringSpec`, and the
judge step, card line, ledger prefix and stand-down in `fireOrchestrateUpdate`.

Landed in stage 2 (v0.6.616): `Attempts` on the payload, `noteObjectiveAttempt`,
`objectiveAttemptsBlock`, and the block's injection into the fire's prompt. Stage 2 also fixed a
stage-1 mistake — see **Stop or continue** below: a stalled objective was cancelled, which made
the task VANISH from the console rather than stand there saying it had stopped.

Tests: `apps/orchestrate/objective_test.go`. Nothing authors an objective yet — that is stage 3 —
so today it is set by a caller of `ScheduleOrchestrateUpdate`.

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

`recurring(action="schedule")` with the same name and an `until` edits the task in place, the
way it already does for timing. `recurring(action="list")` shows the attempt count and last
verdict beside the cadence. The Builder-facing help text gains one paragraph: *when the user
wants something done rather than something run, set `until`.*

## Where it shows in the UI

The **Recurring tasks** card on the chat page's console rail, and the same rows in the console's
recurring view (`console_recurring.go`). An objective is a recurring row with two more cells:

| cell | today | objective |
|---|---|---|
| Name | prompt's first line | same |
| Cadence | `recurring · every 1440m · 09:00–09:30` | same |
| Fires | `3 / 10` | `3 / 10 · 0 met` |
| State | blank, or `⚠ needs relink` | `not yet — <last reason>`, `done`, or `⚠ stalled — <last reason>` |
| Next run | RFC3339 | same; blank once done or stalled |

Row actions stay Delete and Run now. **Open for stage 3:** a parked objective renders through the
existing broken-row path, which is Delete-only, so the retry-after-fixing story needs either Run
now on a parked row or a Resume action that clears the park the way a relink does. The history
survives either way — it is on the payload the park carries.

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
3. **Authoring and console.** `until`/`max_attempts` on the `recurring` tool, the help paragraph,
   the two console cells and the state strings. Test: `recurring(action="list")` shows the count
   and verdict; the console row for a stalled objective keeps Run now and loses Next run.

Each stage is shippable alone. Stage 1 without stage 2 already stops a met goal from re-firing,
which is most of the value; stage 2 is what makes the unmet ones improve.

## Out of scope

- Objectives on pipelines and machines. A pipeline loop stage already has `until` as a bool
  field; a machine's phases already have exits. If a goal needs those, author it there.
- A judge that plans the next attempt. The ledger tells the next fire what failed; deciding what
  to do about it is the fire's job, with the tools it already has.
- Cross-agent objectives. One task, one agent, one thread, like every recurring task today.
