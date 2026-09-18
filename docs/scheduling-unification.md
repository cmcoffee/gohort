# Scheduling unification (stage 3): what to merge, and what turned out not to be the merge

Status: **complete.** Slices 1 and 2 built; 3 and 4 examined and deliberately not built, see below. Stages 0–2 shipped in 0.4.15 (2026-06-11) and built
`core.ScheduledTrigger` (the `{when, gate, action, target}` record) plus the `schedule` tool on
phantom. Stage 3 was deferred with a one-line brief: *fold standing agents and recurring tasks onto
ScheduledTrigger, migrate the console, absorb create_event_monitor, add a reconciler, retire the old
stores after drain.* The plan behind that line lived in a scratch file that no longer exists, which
is most of why this sat still for three months. This is that plan, rebuilt from the code.

## The question that started it

"I find the task scheduler confusing between enabled agents, recurring tasks and event monitors
aren't these just different names for the same thing?"

Nearly. Two of the three are a clock and an agent. The third is a clock and a condition. Only a
webhook monitor is genuinely something else, and it already lives in Bridges.

The surface half of that was answered in 0.6.838–0.6.839: one Scheduler page per scope, the rail
retired. That made the three READ as one thing. It did not make them one thing, and this is the part
that does.

## What the three records actually share

Measured, not guessed. `StandingAgent` (29 fields), the recurring `orchUpdatePayload` (32), and
`ScheduledTrigger` (40).

Standing and recurring share **twelve** fields:

```
AgentID  IntervalSeconds  Surface
Broken  BrokenReason  BrokenCause
Until  MaxAttempts  Attempts  NextAttemptAt  NextAttemptWhy  ConsecutiveFailures
```

Those are not twelve coincidences of naming. They are two whole mechanisms implemented twice:

- **Objectives**: `Until` / `MaxAttempts` / `Attempts` / `NextAttemptAt` / `NextAttemptWhy`. The
  JUDGE was already lifted (`objective_judge.go`, `objective_pacing.go`); the state machine around
  it was not. It lives in `standing_runner.go` and in `scheduled_updates.go`.

  Checked before assuming the worst, and the duplication is BENIGN: both surfaces handle the case a
  resumed objective must not stall on its first fire, by opposite means: standing zeroes a counter
  (`UnmetCount`), recurring moves a base forward (`AttemptsBase`) so the fire count and the history
  survive. Two correct answers to one question. That lowers the urgency of this slice: it is a
  refactor with no defect behind it, and it is only worth doing for what it makes slice 3 and 4
  cheaper.
- **Broken / parking**: `Broken` / `BrokenReason` / `BrokenCause`. Three implementations:
  `core/standing_agent.go` (14 sites), `core/event_monitor.go` (6), `scheduled_updates.go` (5).

## Why the brief as written is the wrong first move

Folding standing and recurring onto `ScheduledTrigger` as it stands means adding roughly twenty-five
fields to it: the random-pattern machinery (`Pattern`, `TimesPerDay`, `MinGapSeconds`,
`MaxGapSeconds`, `HasWindow`, `WindowFromMin`, `WindowToMin`, `MaxFires`, `RemainingToday`), the
objective machinery, the broken machinery, plus `Mission`, `Surface` and the report routing.

The result is a sixty-five-field record that is a UNION of three things rather than a unification of
them, and every reader of it would have to know which third of the fields applied. That is the same
mistake the nav had, moved down a layer: one name over three shapes.

The fields the three records do NOT share are the ones that make them genuinely different, and they
should stay different:

| | standing | recurring | monitor |
|---|---|---|---|
| what fires | a mission, a pipeline or a machine | a prompt into an EXISTING session | a condition over a polled value |
| where it lands | a surface it chooses | the session it was created in | the agent it wakes |
| cadence | cron or interval | interval, or a random pattern inside a daily window | poll interval |

## The order that works

Each slice ships on its own and none moves data while a schedule is armed.

**Slice 1: one objective state machine.** DONE (`settleObjective`). Lift the attempt/stall/stop state out of
`standing_runner.go` and `scheduled_updates.go` into one implementation both call, alongside the
judge that is already shared. Touches no storage. Worth doing for the leverage it gives slices 3 and
4 rather than for a bug it fixes: see the note above; both implementations are currently correct.

**Slice 2: one parking mechanism.** DONE. `core.ParkCauseOf(broken, storedCause)` and
`core.RelinkFixesIt(cause)`; the standing and recurring readers collapsed onto them and the three
row builders stopped spelling the comparison themselves.

Two things it did NOT do, both deliberate:

- **The fields stay where they are.** The tidy version folds `Broken` / `BrokenReason` /
  `BrokenCause` into one embedded struct. gob NESTS an embedded struct, and these records are stored
  flat, so that changes what every armed schedule decodes to: a migration, on records that are
  firing. The shared reader takes the two fields as arguments instead and costs nothing.
- **Monitors keep their own vocabulary.** It is RICHER, not divergent: a monitor separates "the
  thing it needs is gone" (`MonitorStopBroken`, a relink) from "everything resolves and the checks
  keep failing" (`MonitorStopFailing`, not a relink), which standing and recurring do not
  distinguish. `owner` and `met` already share strings across all three by accident; `broken` versus
  `dependency` do not, and reconciling them means rewriting a stored `StopReason` on every monitor.
  Not worth it for one string. The monitor's own answer to the relink question now says so where it
  is asked.

**Slice 3: one cadence type. NOT DOING, and the reason retires slice 4 with it.**

The plan said: `Cron`, `IntervalSeconds`, `StartAt` and the random-pattern fields become a `Cadence`
with one `Next(after)`, because "three files answer when-next and only one knows about windows".
Reading the three, that sentence is true and the conclusion does not follow. Here is all three:

```
nextStandingRun  cron → NextCronOccurrence; else interval, floored by StartAt; else an error
nextPoll         interval, floored by the deployment minimum
computeNextFire  random → two planners; else interval, deferred into the daily window
```

The arithmetic they share is `from.Add(interval * time.Second)`. One line. Everything else is each
surface's own policy (cron and a start date, a minimum poll interval, windows and random planning)
and a `Cadence` holding all of it would carry about eleven fields where `Next()` switches on which
subset applies. That is the same "every reader must know which third is theirs" that this document
rejects for the record fold, rebuilt smaller. A type is not worth having to unify one `Add`.

The other candidate looked better and turned out the same. All three arm identically (compute the
cadence, let a pacing ask override it, consume the ask, schedule) and the override differs on each:

| | an ask wins when | why |
|---|---|---|
| standing | it is in the future | the deployment minimum is the only floor a standing agent has |
| monitor | it is later than the next poll | a snooze is by definition later; core must never poll faster than its owner set it to |
| recurring | (stored as a string, applied in the app) | window and reap ceiling apply too |

Three floors, three reasons, all three written down in `docs/objective-pacing.md` before I looked.
Not drift.

**Slice 4 (the fold), goes with it.** It was contingent on 1–3 leaving standing and recurring
differing "only in what they run and where the answer goes". They do not: they also differ in what a
cadence MEANS, and that is not a field, it is the behaviour. A shared record would have to carry all
three cadence policies and a discriminator saying which applies, which is the union again.

## Where this actually lands

Stage 3 is finished, and the answer is that the merge was the wrong instinct.

Three times this document went looking for divergence between these surfaces: two objective state
machines, three parking readers, three cadences, and three times found deliberate, documented
difference. The duplication that was real (the boilerplate around the objective judge, the parked-
state reader) is gone, in slices 1 and 2. What is left apart is apart on purpose.

The complaint that started this was "aren't these just different names for the same thing", and the
honest answer is no: they are three things that were PRESENTED as though the reader should already
know which was which. That was fixed at the surface in 0.6.838–0.6.839: one Scheduler page, one rail
retired, section headings naming each kind. The confusion was real and the cure was cosmetic, which
is an unsatisfying sentence and appears to be the true one.

If something here is still worth doing later, it is not a merge. It is the vocabulary: monitors say
`broken` where standing and recurring say `dependency` for the same state, and reconciling that
costs a migration on stored `StopReason` values. Worth it only if a third surface ever needs to read
all three, which nothing does today.

## What is NOT in scope

- **Event monitors keep their own record.** A polled monitor is a clock plus a gate, and the gate is
  `ScheduledTrigger`'s whole reason for existing, but monitors are the one surface with a
  push-driven variant (webhook), and they already have a home in Bridges. Fold them last or not at
  all.
- **No data migration.** The decision from stage 0 stands: dual-run, no rewrite of stored records. A
  reconciler reads both stores until one stops being written.
- **The `recurring` tool's API does not change.** Agents author schedules through it; a slice that
  changes what the model calls is a slice that breaks every saved agent.

## The thing to be careful about

These records are firing on a live box. The engagement cycle behind most of this week's debugging
runs every thirty minutes. A slice that strands a schedule is worse than a slice that ships late
which is why every slice above is a refactor with the storage untouched, and why slice 4 is the only
one that moves a record and is deliberately last.
