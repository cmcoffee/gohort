# Pipelines and machines

Two ways to give an agent a workflow you design, instead of leaving it to work
out its approach every time.

| | A pipeline | A machine |
|---|---|---|
| **Is** | A run that goes start to finish and returns a result | A set of steps a conversation moves through and stays in |
| **The unit** | One run | One turn of a conversation |
| **Reached by** | An agent calling it as a tool, `run_<name>`; a schedule; an app's page | An agent that carries it into its conversations |
| **Per agent** | Several | One |
| **Good for** | Research, reports, reviews, checks: work with an end | Intake, triage, investigation: a conversation with a shape |

Both are edited on the same kind of page, with the workflow drawn at the top
(each box links to its part), a way to try it before you use it, an estimate
of what it costs, and ✨ **Get assistance** to make a change by describing it.
Both export as a portable recipe with no account or deployment in it.

## Pipelines

A pipeline is a list of **stages** that run in order. Each stage's output is
kept under its name, and a later stage reads it in its instructions:

- `{input}`: what the run was asked
- `{prev}`: the stage just before
- `{stage:plan}`: a named earlier stage, or `{stage:plan.focus}` for one
  field of its output

### The kinds of stage

| Stage | What it does |
|---|---|
| **Worker** | One model call: instructions in, text out. The cheap one, for summaries, extraction and rewrites |
| **Agent** | Hands the stage to one of your agents, with its tools, knowledge and memory |
| **Fanout** | Runs once per item of an earlier stage's list, in parallel: breadth |
| **Loop** | Repeats a body of stages, each pass seeing the last, up to a count or until a condition holds: depth |
| **Branch** | Reads a yes/no an earlier stage produced, and stops the run or skips ahead. No model call |
| **Tool** | Calls a tool directly, with arguments you wrote. No model call: for arithmetic, lookups, a specific API call |
| **Gather** | Searches the web for each query, reads the best pages, and numbers them as sources for a later stage to cite |
| **Verify** | Checks what an earlier stage wrote against the sources the run read: every citation names a source, every figure appears in what was read |
| **Panel** | Several voices on one question in parallel, for up to four rounds, each round reading the last. A voice is one of your agents, or a role ("the skeptic") the model plays |
| **Machine** | Runs a whole machine as one stage, such as once per item of a fanout |

**Add a stage** offers the first eight. Panel and Machine stages are written
through Builder or ✨ Get assistance.

A stage can declare **typed output fields** (a list, a number, a yes/no), so
the next stage, a branch or a fanout reads a value rather than parsing prose.
Each stage also chooses how far it reaches into tools: everything the caller
has, only what reads, or nothing.

### Making one

Your pipelines are on the **Extensions** page, under **Pipelines**. Three ways
to start:

- **New pipeline**: a small working pipeline to change into yours.
- **Describe one...**: say what the work is, start to finish, and **✨ Draft
  it** writes one.
- **Import...**: a `.pipeline.json` recipe somebody exported.

Or ask Builder in chat: "make a pipeline that researches a topic, then has a
skeptic review the draft".

### The pipeline's page

- **The pipeline**: its name and what it is for, with **Version history** and
  **Duplicate** (a copy to experiment on).
- **One section per stage**, with only the settings that kind of stage has,
  and **Add a stage**.
- **Run it**: a real run, with each stage streaming in as it finishes. A
  finished run can offer follow-ups: further pipelines run from its result.
- **What a run costs**: worked out from the definition before you pay for a
  run.
- **What it will do** and **Worth a look**: what the definition says, and
  what it looks like it might not have meant. Neither refuses a save.
- **Assign to agents**: tick the agents that get it as `run_<name>`. An
  agent can hold several pipelines. One assigned to nothing does nothing.
- **Who may run this pipeline**: share it (see [Sharing](sharing.md)). A
  person you share it with runs it against their own agents, tools and
  credentials, not yours; **Can edit** lets them change it too.

On an agent's own page, **Configure › Pipelines** attaches pipelines from the
agent's side.

## Machines

A machine is a set of **steps**. A new conversation starts at the first step
and walks forward until it reaches a step it **waits in**; it answers from
there, and later messages come back to that step. So an agent can work out
what is being asked once, pick an approach once, and then answer in that
frame, instead of re-deciding on every message.

Earlier steps leave **state, not transcript**: what a step worked out is kept
as fields (`{state:triage.severity}`), so the eighth message does not re-read
the first one's reasoning.

### What a step can do

- **Think and hand on**: run its instructions, record its output fields, and
  move to the next step.
- **Choose where to go**: pick the next step from a list, at run time. This is
  how a router routes.
- **Wait for the person**: the step the conversation stays in.
- **Leave when the job does**: a **guard** is a sentence ("the person has
  moved on to a different problem") checked each turn; when it holds, the
  conversation goes back to the start, or to the step you name.
- **Narrow its tools**: everything, only what reads, nothing, a named list, or
  everything except a named few.
- **Run something instead of thinking**: hand the step to another agent, run a
  pipeline or another machine, or call one tool directly with arguments you
  wrote.
- **Reply with a template**: a waiting step can answer with text built from
  earlier steps' fields, with no model call, for a step whose job is passing
  a result along unchanged.
- **Pick the model and reasoning** for turns spent in it.

A machine can also be set to **route each message**, so every new message
starts at the first step again (for a machine whose job is sending each
message to whatever should answer it), or to **run unattended**, start to
finish with no step that waits, like a pipeline.

### Making one

Your machines are on the **Extensions** page, under **Machines**, with the same
three ways in as pipelines (a working starter, **Describe one**, and
**Import**), or ask Builder. `extras/investigation.machine.json` is a worked
example: it triages, commits to one hypothesis and tests it against
independent evidence.

### The machine's page

- **The machine**: its name, and where a new conversation begins.
- **One section per step**, and **Add a step**.
- **Try it**: a rehearsal conversation. Send a message and watch which step it
  lands in; keep sending to see a guard fire or a handoff happen. It shows the
  path, not the answer.
- **What a turn costs**, **Worth a look** and **What is still missing**.
- **Assign to agents**: an agent runs one machine at a time, so ticking an
  agent that runs another moves it to this one.
- **Who may run this machine**: share it, as for a pipeline.

On an agent's page, **Configure › Machines** sets the agent's machine.

### In a conversation

The chat toolbar shows the step the conversation is in. Hover it for what the
step is for.

A machine is fixed for a conversation when the conversation starts. To use a
changed or different machine, start a new conversation. Deleting a machine
leaves conversations already in it alone: their next turn runs as an ordinary
agent turn.

## Changing one by describing it

**✨ Get assistance**, on both pages, talks a change through with you: it
reviews the draft against what is there, applies it, and keeps the version
before it. **Last change** shows what was applied, with **Undo the
revision** to put the previous version back.

## Choosing between them

- The work has an end and a result you want back: **a pipeline**. Attach it to
  an agent so it can call it, or have the Scheduler run it.
- The work is a conversation that should keep a shape: **a machine**.
- A conversation that needs a big piece of work done partway through: a
  machine whose step runs a pipeline.
- A pipeline that needs a small conversation-shaped job done per item: a
  fanout whose stage is a machine.
