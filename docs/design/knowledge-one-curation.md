# Knowledge: one curation, run once or on a schedule

Status: built (2026-11-15) on the `one-curation` branch, held there until it has run live.

## Why

A collection can be filled four ways today, and three of them do the same
job differently:

| Today | What runs | Where it shows |
|---|---|---|
| Auto-fill from web (legacy) | Auto-fill's searches, inside the browser's request: no progress, no Stop, no rejoin | the hand-written part of the page |
| Fill it for me | a drafted plan (searches, how many documents), shown and editable first, run in the background; kept in memory 24 hours | its own section |
| Auto-Curation | a hidden agent, briefed with the description and rules, searching as it sees fit; Curate now or every week/month | Curation |
| Curation by Agent | one of your agents, the same brief | Curation |

The plan is the best of them: you see what will be searched and how much will
be added before anything runs. The owner's point: drafting a plan should be
how a collection is filled, and the same plan is what curation runs. Filling
it is just curation run once; curating is the same run on a schedule.

## The model

**A collection has one plan**, kept on the collection:

- **Search terms:** the searches a run makes. Drafted from the description
  (as Draft a plan does now), editable, added to with "+ Add search terms".
- **Rules:** what to prefer and what to skip (the existing filter rules,
  now edited in the plan too, "+ Add rule").
- **Documents per run:** at most this many are added in one run.
- **The articles you listed** stay where they are; a run fetches the pending
  ones first, as Auto-fill did.

**One run** does, in order: the pending articles; the search terms, keeping
the candidates the rules allow and skipping what is already in; each page
through the check and, for a secondary page, the second source; adds what
passes and holds what does not. Links waiting in the suggestions from agents
that read the collection go through the same check and are closed. It runs
in the background with progress, Stop and rejoin, and ends saying what it
added, what it held and what it proposes removing (a page that supersedes
one).

**Run once or on a schedule:**

| Curation | Who runs the plan | When |
|---|---|---|
| Auto-Curation | Knowledge | on its schedule (week or month), and Run now |
| Hand Curated | Knowledge | only when you press Run now |
| Curation by Agent | your agent, given the plan as its brief | on its schedule, and Run now |

So "Fill it for me" is Run now on a collection that has no schedule, and
Auto-Curation is the same plan with one.

## What goes

- **The legacy Auto-fill button** and its request-bound run. "Fetch these
  only" for the articles stays.
- **The hidden curator agent.** What it did beyond a plan run is covered:
  suggestions with a link go through the check in a run, and removals are
  proposed from the check's "supersedes". Collections stored with it as
  their curator read as Auto-Curation, as now: the stored value is kept, only
  what runs changes. No migration.
- **The separate Fill it for me section:** it becomes the plan in Curation.
- **The "check pages" box:** every run is checked, so there is nothing to
  turn on.

## Stored change

Two new optional fields on the collection record: the search terms and the
documents per run. Collections without them have no plan until one is
drafted (the page offers Draft a plan, as now). The plan held in memory
today is dropped. No migration.

## The page

Curation becomes, top to bottom: how it is curated (the three choices);
the plan (search terms, rules, documents per run, Redraft); Run now, with
its progress and outcome; the schedule (never, every week, every month).
Suggested sources, Held for you and Suggestions stay their own sections.

## Settled (owner, 2026-11-15)

1. **The hidden curator agent is retired.** Auto-Curation is the plan run.
   Its stored value (`app-knowledge-curator`) is kept as the Auto-Curation
   marker (`collectionAutoCurator`), so nothing stored changes.
2. **Scheduled runs reuse the saved search terms.** Redraft rewrites them.
3. **Built on a branch** (`one-curation`) until it has run live.

## As built

- `apps/orchestrate/collection_plan.go`: the plan's endpoints, drafting
  (kept on the collection), and `runPlan`. `collection_fill.go` is gone.
- `/curate` is Run now for all three choices; Hand Curated and Auto-Curation
  run the plan, Curation by Agent briefs the agent with it (search terms and
  rules included). The scheduled refresh starts the same run.
- `/autofill` answers only Fetch these only (`articles_only`); anything else
  is told the plan replaced it. The page's legacy Auto-fill button, its rules
  editor and the "check pages" box are gone; rules are edited in the plan.
- The plan form's lists name what they add: a "rules" form field takes
  `AddLabel` (core/ui), so search terms read "+ Add search terms".
- `ClassifyOnAutofill` is no longer read; it stays on the record so stored
  collections and bundles still decode. The plan's two fields travel in
  bundles with the rest.
