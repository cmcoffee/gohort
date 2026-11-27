# Checks that adapt to the model answering them

Status: built (2026-11-27). Where the build differs from the plan below:

- **No effort adaptation for checks.** The two checks that think already set
  their own effort (the turn judge low, the guardrail warden low or medium),
  so trying medium on them would change nothing. Effort is Measure's, for
  agent work: settings at medium and low (low is what a project set to Quick
  runs at on vLLM), so a project's Reasoning is chosen from the results.
- **Forms are tried only for checks the worker answers** (its full answer),
  on the worker's identity with the served weights in it. A forms run is
  kept apart from the last run the page shows (`last_forms`).
- **The map pass does it**: after mapping, it asks the checks the worker
  got wrong as written in the other forms, up to two runs; the 15-minute
  tick starts the pass when forms are missing too. Builds on the judge map (core/checktest) and
the model identity that now includes the served weights (core/decide
served.go).

## Why

A check is asked one way. When the model behind it changes, the check either
still works or it does not, and the only thing that adapts is whether the
check is offloaded (the judge map). On 2026-11-27 new weights behind the
same served name failed two Channel gatekeeper cases the old ones always
passed. Nothing could try asking it another way, or thinking harder about
it, to get those cases back on the new model.

## 1. How a check is asked: earned per model

Each check can be asked in a few ways (its **forms**), and the check tests
find the one the current model answers right.

Forms come from the Verdict itself, so no check is rewritten to have them:

| form | what changes |
|---|---|
| `reasons` (today's) | what to weigh first, then the answer |
| `answer` | the answer alone (the shape today's refusal fallback uses) |
| `quoted` | the input it judges is fenced and labelled as quoted, untrusted text, line by line (aimed at what the gatekeeper fell for: a forged turn or an owner claim inside a message) |

A check may add an authored form of its own later; none does at first.

- **The check tests** ask each case in each form the tier has not earned yet,
  on the current model identity. A form is earned the way a mapping is: every
  case right, two runs in a row.
- **The map** keeps, per check and tier, the form earned: `reasons` when it
  passes (no change from today), else the first other form that does.
  Stored as one more field on the tier's result (`form`, empty meaning
  `reasons`). Nothing to migrate: today's entries read as `reasons`.
- **Live**, the check asks in the form the map names for its model, and in
  `reasons` when none is earned. The refusal fallback stays as it is.
- **Cost**: a run asks more cases only for checks whose `reasons` form
  missed: the ones that pass are not asked again in other forms.

The fingerprint stays per check (its cases and prompts): a form is a
different rendering of the same prompts, recorded alongside.

## 2. How hard it thinks

Most checks call the model with thinking off, where effort does nothing; two
think (the turn judge, the guardrail warden). Effort matters most for agent
work: Anvil's rounds on the worker are mostly thinking.

- **Checks that think**: the check tests also try `medium` effort, and the
  map keeps the lowest effort that earns every case right twice (`effort`
  field, empty meaning the check's own). A check that loses a case at
  `medium` stays as it is.
- **Agent work (Anvil, Builder)**: Measure gets settings at `low` and
  `medium` effort beside today's, so a run says what each effort costs in
  pass rate, rounds and time on the current model. Changing an app's effort
  stays the owner's choice (a setting per app), not automatic: one bad
  switch costs a whole build, not one check.

## Stored changes

Two optional fields on the judge map's per-tier result (`form`, `effort`).
No new records, no migration.

## Not now

- Authored forms per check.
- System One question forms (its question is already asked both ways round).
- Choosing an app's effort automatically from Measure.

## Settled (2026-11-27)

1. **Forms**: `reasons` (today's), `answer` and `quoted`.
2. **Effort**: automatic for checks that think, from the check tests; the
   owner's choice for agent work, from Measure's new effort settings.
3. **Every model starts fresh**: a form earned on one model is not carried
   to the next.
