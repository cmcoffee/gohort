# Agent shapes: two kinds, and Builder's shaper

Status: **the shaper is built** (`apps/orchestrate/builder_shaper.go`). The recipe library it
replaces is gone, and the only shape left is the Assistant. Written 2026-10-17, replacing two
earlier drafts ("Agent Types", then a library of archetype recipes).

## The decision

An agent is made by describing it to Builder, and Builder builds what was described. There are
two kinds of agent, and they are a description, not a type anybody picks or an agent follows:

- **Assistant.** People talk with it (the owner, a group chat, a contact). It keeps a standing
  mind and remembers who it talks to. `memory_mode: "chatbot"`.
- **Specialist.** One job, used directly or handed work by another agent. It remembers lessons,
  not people. `memory_mode: "agent"`. A research agent, a knowledge base, a watcher, a digest, an
  analyst: all specialists, differing in what they reach.

The New Agent wizard already asks exactly this (Assistant or Specialist). Everything else about a
specialist is the build's to decide, and what goes wrong in a build is the SHAPE: how many agents,
which kind each is, what each can reach, and why any part is split off. Settings are tuned after,
by whoever uses the agent.

## Why not a library of recipes

Builder had one: a recipe per kind of specialist (investigator, librarian, correspondent,
reviewer, analyst, digest, scheduled watcher, research, knowledge base), read through the
`archetype` tool, which is gone too. Over four days
of logs (2026-10-08 to 12) Builder never called that tool once, which is why it had already been
moved behind `load_tool`. And the recipes, written side by side, showed the slicing was wrong:

- half of each was the same advice (owned and hidden, the three reasons to split, rules vs.
  persona), and the rest was a framework fact found by reading the code;
- the split between recipes followed how people word requests, not real differences, which is
  why the Investigator and the Librarian blurred into each other;
- what decides a shape is reach and context control, which no recipe owned.

## The shaper

One call with a brief about nothing but the shape, made before Builder creates or changes an
agent (`shape_agent`).

- **Forced by the intake, not by a prompt.** A Builder session whose intake said Create or Change
  and Agent cannot run `create_agent` or `update_agent` until `shape_agent` has run in it
  (`shapeGateRefusal`). A new intake in the same session is shaped again. A session with no
  intake (another agent sent it, or a one-line request) is not held, and Builder may still call
  the tool when a request turns out to change what an agent reaches.
- **On a change** it sees the agent as it stands, with its sub-agents, and most of the time says
  "No shape change." so Builder goes straight ahead.
- **What it answers:** the shape in a sentence; each agent with its kind, its job, what it
  reaches and, for a sub-agent, why it was split off; the wiring (delegation, a Source, a
  schedule, a board, a guardrail); and only the questions the person must decide. Builder shows
  it to the person before building.
- **Its own route stage** (`app.orchestrate.builder_shaper`, lead by default) and its own prompt
  block (`agent.builder.shaper`), so an operator can move it to the worker or reword it. A
  session that may not reach the lead uses the worker. A shaper that cannot answer does not hold
  the build up: the shape is advice.

### The split test

A part becomes its own sub-agent (`owned_by` the parent, hidden) only when splitting it off buys
one of three things, and the shaper names which:

1. **Reach** that should live on it alone: the parent should not hold that tool at all (sending
   messages to people, running code, changing a system).
2. **A heavy transcript** kept out of the parent's conversation: many tool calls, and the parent
   needs only the result.
3. **A memory of its own** that should not mix with the parent's: a catalogue, a dataset's
   layout, which drafts break which rule.

No reason, no split. Looking something up before answering every message is a machine phase on
the agent, which keeps the transcript out without a second agent.

### Reach is the person's

The shaper names what an agent NEEDS ("a way to read the sales exports") and asks the person what
fills it: tools, Sources, credentials, channels, spend and the lead model are theirs to choose,
never assumed. It may make an agent stricter (only tools that look, a guardrail on a parent).

### The facts it carries

The ones builds got wrong, kept in its brief rather than in a recipe nobody read: messaging lives
on one agent with a guardrail on its parent; a sub-agent sends through its parent's channels and
is scheduled by its parent; a read-only step drops remote reads too; an unwatched run refuses a
tool that needs approval and queues it; a bulletin board's limits (one poster, a sub-agent posting
under its parent's right, 600 characters, the post going out of date after `ttl_hours`); a
schedule for reporting every time versus a monitor for staying silent until something changes.

## What is left of the library

`apps/orchestrate/archetypes/` keeps one shape: `conversational`, the Assistant the wizard
builds. Agents made from it follow it: fields the owner never changed are read from the shape at
load (`ShapeID`, `agent_overlay.go`).

Research and knowledge base stopped being shapes in 0.8.381 (no recipe, no wizard template, no
draft from the matcher).

- **Knowledge base is gone.** Nothing ran it; it was a template to clone. A new one is a
  specialist like any other, and the shaper's brief carries what makes it one: documents
  attached, Private, no memory of its own, and the answer-only-from-documents rule.
- **Research is an engine.** Its record moved to `builtin/` because four features run it by id:
  Collections research and audit (the Knowledge app's Research button too), Scribe's audit and
  its research tool. It stays hidden. It is not an app agent because the app-agent spec carries
  no rules, plan guidance, gap check or round budgets. Builder may clone it as a starting point.

Agents made from either shape before stop following it and keep their settings as of their last
save. No data migration.

## Where it leads: an assistant as liaison

The composition this aims at: the agent a person talks to is an assistant, and it holds
specialist sub-agents, one that knows where things are, one that goes and looks at live state, one
that holds the messaging. It decides which to ask, reads what they have recorded before running
any of them, and puts their answers together. Reading a sub-agent's memory without running it is
built (`agent_catalog.go`): attached as a Source, an agent gives `search_<agent>_memory` and
`recall_<agent>_about` beside `consult_<agent>`.
