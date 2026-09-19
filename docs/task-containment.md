# Goals, missions and tasks

What the three words actually name, and the one thing that was missing.

## The question this answers

> Goals, missions and tasks should really be somewhat synonymous, the only
> difference being scope or length. Each of them can have multiple objectives or
> sub-tasks. Right now the logic feels hard to follow.

The second sentence is right and nothing in the codebase supported it. The
first needs a correction, and the correction is most of why the logic reads as
hard to follow.

## Two of these words name records; one names a field

| word | what it is |
|---|---|
| **task** | a record that runs: a recurring task, a standing agent, an event monitor |
| **mission** | `StandingAgent.Mission`, what it does each time it runs |
| **goal** | `Until`, the condition under which it should stop existing |

A standing agent carries a mission **and** a goal at the same time. They are not
one thing at two sizes: one is the work, the other is the finish line, and an
agent has both at once no matter how large or small it is.

So "goal" is not a bigger "mission". What makes the page read as confusing is
that **goal was promoted to page-title status while remaining a field on the
other two**, which invites reading it as a third kind of record.

## The axis that was actually missing

Containment. There was no way to say that three schedules are pieces of one
larger piece of work, so somebody who decomposed something got three unrelated
rows and had to hold the connection in their head. That is the real content of
"each of them can have sub-tasks", and it is orthogonal to the mission/goal
distinction rather than a replacement for it.

The model this lands on:

> One kind of thing, which can name a parent. Each one has a **what** (its
> mission) and optionally a **done-when** (its goal). Scope is depth, not a
> different word.

## What is built

A `Parent` field on all three records, holding `"<surface>:<id>"`.

The surface is in the reference because ids are only unique within one: a
monitor and a standing agent may both be called `nightly`. A recurring task is
referenced by its `UID`, not by the scheduler task id its console row is
actioned by, because that id is re-minted on **every fire** (see
`docs/task-notes.md`, which is why the UID exists).

- `taskParentRef` / `parseTaskParent` build and read a reference.
- `taskParentLabel` renders one, including for a parent that has been deleted.
- `taskParentWouldLoop` refuses a cycle.
- `setTaskParent` stores it, scoped to the owner's own records.
- `api/console/scheduler/parent` and its picker source; a **Part of…** action on
  every Scheduler row; a **Part of** line on the Scheduler and Goals rows.

Parents may cross surfaces, because the decomposition people actually do
crosses them: a goal checked by a standing agent, gathered by a recurring task,
triggered by a monitor is one piece of work in three shapes. Offering only
same-kind parents would describe a filing system rather than the work.

## What it deliberately does NOT do

**The link carries no authority.** Not one of these:

- a child is not paused, resumed or retired by its parent
- a parent is not met because its children are met
- nothing inherits: not tools, not permissions, not guardrails

Every one of those is arguable in both directions, and inventing them alongside
the link that needs them is how a link becomes a container. A container record
that does not itself fire is exactly what the objectives build refused, and that
decision is load-bearing:

> an objective is a schedule with a completion check, not a fifth trigger kind
> and not its own table

Nothing here creates a record that does not fire. A parent is an ordinary
schedule that happens to have children. If rollup or inheritance earns its
place, it earns it after somebody has lived with the plain link, and it arrives
as a policy on top rather than as the reason the link exists.

**A deleted parent is not cleared.** The child keeps running and the link keeps
showing, as `<id> (deleted)`. Silently dropping it would lose the only record
that this work was ever part of something, and the broken-dependency posture
everywhere else in this console is to keep the thing, say what is wrong, and let
a person decide.

## Open, and worth having on purpose

- **Rollup.** A parent whose children are all met has no way to notice. Today it
  is judged on its own fires like anything else, which is correct if the parent
  is a real check and wrong if it is a bare heading. Living with it is what
  tells us which.
- **Rollup**, still. See above.

## The tree view (built, v0.6.921)

It got its own page rather than being bolted onto an existing one, which is what
"it wants its own decision" turned out to mean.

Three pages, three questions, three orderings, and no two of them can share:

| page | question | ordered by |
|---|---|---|
| Scheduler | what will happen on its own | when, within kind |
| Goals | what am I still waiting on | what needs you first |
| **Breakdown** | how does this decompose | alphabetically, by tree |

Breakdown sorts siblings by name on purpose. A structural page that reorders
itself by next fire time is one you cannot find the same row in twice.

**It shows only the parts of the tree that ARE a tree.** A schedule with no
parent and no children is already on the Scheduler, and including it would make
this page the Scheduler again with indentation, which answers nothing new.

**A parent that does not resolve leaves its child a ROOT** rather than dropping
it. A page about the shape of the work that silently omits half of it is worse
than one that shows a flat row.

Read-only, like Goals, for the same reason: every row is editable one page over,
and a second set of the same buttons is a second set of rules for one record.
Notes is the exception, and it is the same one Goals makes: reading what a
task's runs worked out is the question these pages ask, not a verb.

### The primitive it needed

`_depth` on a row, honored by the cards layout in `core/ui`. Generic, and it has
to be: a list whose items contain other items is an ordinary shape, so nesting
is a property of a row rather than a second kind of list. The server emits rows
already in order and says how deep each sits; the layout never learns what the
nesting means.

Indent only, no box-drawing connectors. Those need to know whether each ancestor
has more siblings coming, which is a second model of the same data held in the
renderer, and it is wrong the first time a row is filtered out of the middle.
Clamped at both ends, because a negative depth pulls a card out of the list and
an unbounded one pushes it off the right edge, both from a number the server
could get wrong.
