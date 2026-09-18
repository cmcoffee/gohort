# Scheduling unification (stage 3): what to merge, and what turned out not to be the merge

Status: **slices 1 and 2 done; 3 next.** Stages 0–2 shipped in 0.4.15 (2026-06-11) and built
`core.ScheduledTrigger` — the `{when, gate, action, target}` record — plus the `schedule` tool on
phantom. Stage 3 was deferred with a one-line brief: *fold standing agents and recurring tasks onto
ScheduledTrigger, migrate the console, absorb create_event_monitor, add a reconciler, retire the old
stores after drain.* The plan behind that line lived in a scratch file that no longer exists, which
is most of why this sat still for three months. This is that plan, rebuilt from the code.

## The question that started it

"I find the task scheduler confusing between enabled agents, recurring tasks and event monitors —
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

- **Objectives** — `Until` / `MaxAttempts` / `Attempts` / `NextAttemptAt` / `NextAttemptWhy`. The
  JUDGE was already lifted (`objective_judge.go`, `objective_pacing.go`); the state machine around
  it was not. It lives in `standing_runner.go` and in `scheduled_updates.go`.

  Checked before assuming the worst, and the duplication is BENIGN: both surfaces handle the case a
  resumed objective must not stall on its first fire, by opposite means — standing zeroes a counter
  (`UnmetCount`), recurring moves a base forward (`AttemptsBase`) so the fire count and the history
  survive. Two correct answers to one question. That lowers the urgency of this slice: it is a
  refactor with no defect behind it, and it is only worth doing for what it makes slice 3 and 4
  cheaper.
- **Broken / parking** — `Broken` / `BrokenReason` / `BrokenCause`. Three implementations:
  `core/standing_agent.go` (14 sites), `core/event_monitor.go` (6), `scheduled_updates.go` (5).

## Why the brief as written is the wrong first move

Folding standing and recurring onto `ScheduledTrigger` as it stands means adding roughly twenty-five
fields to it: the random-pattern machinery (`Pattern`, `TimesPerDay`, `MinGapSeconds`,
`MaxGapSeconds`, `HasWindow`, `WindowFromMin`, `WindowToMin`, `MaxFires`, `RemainingToday`), the
objective machinery, the broken machinery, plus `Mission`, `Surface` and the report routing.

The result is a sixty-five-field record that is a UNION of three things rather than a unification of
them — and every reader of it would have to know which third of the fields applied. That is the same
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

**Slice 1 — one objective state machine.** DONE (`settleObjective`). Lift the attempt/stall/stop state out of
`standing_runner.go` and `scheduled_updates.go` into one implementation both call, alongside the
judge that is already shared. Touches no storage. Worth doing for the leverage it gives slices 3 and
4 rather than for a bug it fixes — see the note above; both implementations are currently correct.

**Slice 2 — one parking mechanism.** DONE. `core.ParkCauseOf(broken, storedCause)` and
`core.RelinkFixesIt(cause)`; the standing and recurring readers collapsed onto them and the three
row builders stopped spelling the comparison themselves.

Two things it did NOT do, both deliberate:

- **The fields stay where they are.** The tidy version folds `Broken` / `BrokenReason` /
  `BrokenCause` into one embedded struct. gob NESTS an embedded struct, and these records are stored
  flat, so that changes what every armed schedule decodes to — a migration, on records that are
  firing. The shared reader takes the two fields as arguments instead and costs nothing.
- **Monitors keep their own vocabulary.** It is RICHER, not divergent: a monitor separates "the
  thing it needs is gone" (`MonitorStopBroken`, a relink) from "everything resolves and the checks
  keep failing" (`MonitorStopFailing`, not a relink), which standing and recurring do not
  distinguish. `owner` and `met` already share strings across all three by accident; `broken` versus
  `dependency` do not, and reconciling them means rewriting a stored `StopReason` on every monitor.
  Not worth it for one string. The monitor's own answer to the relink question now says so where it
  is asked.

**Slice 3 — one cadence type.** `Cron`, `IntervalSeconds`, `StartAt`, and the random-pattern fields
become a `Cadence` value with one `Next(after time.Time)`. Today three files answer "when next" and
only one of them knows about windows. This is what makes a later record fold mechanical rather than
a rewrite.

**Slice 4 — the fold, if it is still worth it.** With 1–3 done, standing and recurring differ only
in what they run and where the answer goes. That is a two-field difference and a shared record
becomes obvious — or obviously unnecessary, which is an acceptable outcome. Decide with the code in
front of you, not now.

**Slice 5 — retire what is drained.** Only after 4, and only for a store nothing writes.

## What is NOT in scope

- **Event monitors keep their own record.** A polled monitor is a clock plus a gate, and the gate is
  `ScheduledTrigger`'s whole reason for existing — but monitors are the one surface with a
  push-driven variant (webhook), and they already have a home in Bridges. Fold them last or not at
  all.
- **No data migration.** The decision from stage 0 stands: dual-run, no rewrite of stored records. A
  reconciler reads both stores until one stops being written.
- **The `recurring` tool's API does not change.** Agents author schedules through it; a slice that
  changes what the model calls is a slice that breaks every saved agent.

## The thing to be careful about

These records are firing on a live box. The engagement cycle behind most of this week's debugging
runs every thirty minutes. A slice that strands a schedule is worse than a slice that ships late —
which is why every slice above is a refactor with the storage untouched, and why slice 4 is the only
one that moves a record and is deliberately last.
