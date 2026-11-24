# Scheduler

The **Scheduler** answers one question: what is this agent going to do on its
own? It lists everything that runs without you starting it: work on a clock,
and watches that wait for something to change. Each one says when it runs
next and how its last run went.

Open it from **Scheduler** in an agent's sidebar for that agent and its
sub-agents, or from the **Fleet** menu for every agent at once, where each
card is led by the agent's name.

## The three kinds

| Kind | Runs | Use it for |
|---|---|---|
| **Scheduled agents** | On a clock, handing the agent a **mission** each time | Work that reports whatever it finds: "every morning, summarize the overnight alerts" |
| **Recurring tasks** | On a clock, giving the agent a **directive** each time | A repeated chore for one agent: "check the build, post if it is red" |
| **Event monitors** | When a condition comes true, and stay silent until then | Waiting for something: a status page changes, a value crosses a line |

A scheduled agent can also run a pipeline, or a machine that is set to run
with nobody there, instead of an agent.

The easiest way to make any of them is to ask the agent: *"every weekday at
8, check the release notes and tell me what changed"*. It checks for a
schedule that already does the job before making a second one. You can also
press **New recurring task** or **New machine run** at the top of an agent's
Scheduler.

What a schedule finds lands in the agent's **Cortex**, its home thread, as a
card naming what ran and when, not in the conversation where it was made. So
a good directive stands on its own: name the repository, the build or the
page, not "this".

## When it runs

A schedule's form has two halves, **What it does** and **When it runs**. No
cron string is ever typed.

- **How often**: **Once a day**, **At set times**, **Every...** (minutes,
  hours or days), **At random, a number of times a day**, **At random gaps**,
  **Once a month** or **Once a year**, with the days of the week it may run
  on.
- **Runs**: **Indefinitely**, or **Until...** any of:
  - **A date is reached**
  - **It has run a number of times**
  - **A goal is achieved**: what must be true for it to be done ("the PR is
    merged"), with an optional limit on tries. The goal is judged after
    each run, which costs one model call per run.

  It ends as soon as any one of them happens.
- **Who does it**: the agent, or one of its sub-agents. A sub-agent's work
  still wakes the parent, which hands it on and reports what comes back.

An event monitor checks **On an interval** or **Daily at set times**, and
**When it checks** can be **Continuously** or **During specified time
periods**.

## Event monitors

A monitor wakes the agent with its **Brief handed to the agent** when its
condition matches. There are a few kinds:

- **A question**: **Check (asked every interval)** is answered by a checker
  agent, and the monitor fires when the answer contains a word you choose
  (YES by default).
- **A URL**: **URL fetched each interval**, a value pulled out of it by JSON
  path or regex, and a comparison (less than, greater than, contains and the
  like) against a value you set.
- **A watched tool's output**: fires when the output changes, with an
  optional **Format script** to say how the change is described.
- **Pushed**: fires when something posts to it, so it has no interval.

**Test** runs the check once now, and fires if it matches. **View** shows
the recent checks, including ones that would have fired, because a monitor
that never fires otherwise looks just like a broken one.

## Working with the list

Each card shows the next run and a badge for the last one: **OK**,
**ATTENTION**, **FAILED**, **RUNNING** or **NOT RUN YET**. Within each section
the soonest run comes first.

The buttons on a card depend on its kind:

- **View**: how it has been going (its last runs, or a monitor's checks),
  and its **notes**: what its runs have left for the next one. **Edit notes**
  corrects what later runs believe.
- **Edit** changes what it does and when.
- **Run now** runs it once as a test. Its schedule does not change, and a
  recurring task's run does not count against its limit.
- **Pause** and **Resume**.
- **Delete**.

Above the list, **Kind** narrows it to **Agents**, **Tasks**, **Monitors** or
**Done**. **Show** narrows it to **Needs attention**, **At rest** or **With a
goal**. Each chip carries its count, so **Needs attention 0** answers the
question without a click.

## When something goes wrong

A card is marked, and counted on the Scheduler's badge, when it needs you:

- It failed twice or more in a row.
- A guardrail stopped it twice or more in a row. Once is usually the
  guardrail doing its job.
- It is **parked**, because the agent, pipeline, machine or tool it runs was
  deleted.

**✨ Need help?**, in **View**, works out with the assistant what keeps going
wrong in its runs, and changes what it does or says what to change.

A parked schedule keeps its mission, timing, goal and notes. **Relink** moves
it onto another agent with all of that kept. A parked schedule nobody relinks
is deleted after 30 days.

### Runs with nobody watching

A scheduled run has nobody there to answer. A tool the agent has may run on a
timer just as in chat, with two exceptions:

- A tool you have marked never to run unattended is refused.
- A call through a credential set to ask before every call is refused for
  this run and waits for you, in the agent's **Permissions** page and its
  Cortex. Approving it lets the next run go ahead.

## Link groups

**Link** puts schedules on one shared time: when it comes, they run in order,
each starting when the one before it finishes. One failing does not stop the
rest. A group can link scheduled agents and recurring tasks from any of your
agents; event monitors are not linkable, since they don't run on a clock.

A group's card leads the list, with **Edit time**, **Run now** (every member,
one after another), **Pause** and **Resume**. On each member, **↑** and **↓**
change the order, and **Unlink** takes it out and puts it back on its own
time, which was kept while it was linked.

## Done

A schedule that finished moves to **Done**: a monitor whose condition came
true or that used up its fires, or a scheduled agent that reached its goal or
its end. Done schedules are deleted after 30 days, unless you press **Keep**.
A recurring task that finishes removes itself.

## Tips

- Ask the agent to change a schedule rather than retyping it: it can edit
  one passage of a long mission and leave the rest.
- To run something once, don't schedule it: ask the agent, or use **Run
  now** on a schedule that exists.
- Use a monitor, not a frequent schedule, for "tell me when it changes": it
  stays quiet until there is something to say.
- Check **Needs attention** now and then. A schedule that works is one you
  stop reading, so the Scheduler tells you which ones have stopped working.
- What an agent remembers between runs is in its notes and its memory; see
  [Memory](memory.md). To give a run a fixed set of steps, schedule a
  pipeline or machine; see [Pipelines and machines](pipelines-and-machines.md).
