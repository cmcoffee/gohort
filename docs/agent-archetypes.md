# Agent archetypes: Builder's starting ideas

Status: **the Librarian is built** (v0.8.364 its framework piece, `agent_catalog.go`; v0.8.367
its recipe, `archetypes/librarian.md`). Next: build one with Builder against a real subject.
Written 2026-10-17, replacing an earlier draft of "Agent Types".

## The decision

An agent is made by describing it to Builder, and Builder builds what was described. There is no
official notion of an agent type: nothing a person picks, makes, shares or publishes, and nothing
an agent "is" after it is built. What helps Builder build well is a library of **archetypes**:
built-in recipes for the kinds of agent gohort is good at, which Builder reviews when it is asked
for an agent, choosing one, combining several, or composing from scratch.

So the work is growing that library until it covers the areas gohort covers, and making sure each
recipe can actually be built: a recipe that asks for something the framework cannot do is a
recipe Builder will fake.

Dropped from the earlier draft: an Agent Types section in Extensions, types made and shared by
people, publishing types through the admin gate, the type question in the New Agent wizard, and
agents that follow their type as it changes.

## What exists

- **The library.** `apps/orchestrate/archetypes/*.md`, one file per shape, embedded at build
  (`archetypes/README.md` is the format). Each has a one-line `summary`, the `aliases` a model
  types for it, `match` phrases scored against a request (`shape_match.go`), and `asks`: the two
  or three questions whose answers change the build. Above `## Persona` is the recipe, written to
  Builder about construction; below it, when the shape ships an agent, the prompt that agent
  wears. Today: conversational, research, knowledge base, investigator, scheduled watcher.
- **Builder reads it** through the `archetype` tool (list, read), and a creation dialog opens
  with a draft from the best match and then asks that shape's questions.
- **A shape that ships a record** (`record` in its frontmatter) can be cloned from the wizard's
  "Start from a template" row, and an agent cloned from it follows it: fields the owner never
  changed are read from the shape at load (`ShapeID`, `agent_overlay.go`), so a fix to the recipe
  reaches agents made from it. A shape whose prompt has to name its subject (investigator,
  watcher) ships no record; Builder composes from the recipe instead.

## How Builder uses the library

1. Asked for an agent, it lists the shapes and reads the ones whose summary or match fits.
2. It picks one, or combines them. Combining is common: a conversational agent with an
   investigator sub-agent, a scheduled watcher that posts to a bulletin board.
3. It asks the chosen shapes' `asks`, in the conversation, and nothing the request already
   answered.
4. It composes the agent from the recipe and the answers, under the rule below, and tests it as
   it tests anything it builds.

The library only has to be good enough that step 1 usually finds the right recipe. A request it
does not cover is composed from scratch, as now.

### Where it leads: a conversational agent as liaison

The composition this is aiming at: the agent a person talks to is conversational, and it holds
sub-agents built from other recipes, a Librarian for where things are, an Investigator for live
state, a Writer for the documents. It decides which to ask, reads what they have recorded before
running any of them, and puts their answers together. Each sub-agent keeps its own memory and
stays private to it. Sub-agents, their dispatch and their ownership exist; reading a sub-agent's
memory without running it is the Librarian's piece below, built generically for this reason.

## The rule for writing a recipe

A recipe says how an agent works. It may make the agent stricter. It never decides what the agent
can reach or who it answers to, and holds nothing the agent remembers.

- **How it works** (the recipe's to prescribe): instructions, a machine, plan guidance, work-plan,
  gap-check and pre-mortem, lead plans or review, thinking and effort, step and round caps,
  context depth, memory mode and recall, notes, Cortex, an intake form.
- **Stricter** (the recipe may add): rules, guardrails, never-unattended marks, tighter tool-result
  scanning, Private.
- **What it reaches and who it serves** (the person's, asked about, never assumed): tools, Sources,
  collections, pipelines, skills, credentials, dispatch, network, sharing and exposure, spend and
  the lead model. A recipe names what the agent NEEDS ("a way to reach the subject") and Builder
  asks the person what fills it.

## Recipes to add

A sub-agent earns its own record when splitting it off buys one of three things: a memory
that should not mix with its parent's, permissions that should live on it alone, or a heavy
transcript kept out of the parent's conversation. Already in the library and usable as
sub-agents: investigator, research, knowledge base, and the ones below marked built.

| Recipe | What it is for |
|---|---|
| **Librarian** (built) | Catalogues one subject (a GitLab instance, a document estate) and is the one other agents ask first |
| **Correspondent** (built) | The only agent in the family that holds the messaging tools; writes and sends to people on the owner's behalf |
| **Analyst** (built) | Works on data in a file store or the sandbox; scripts and large outputs stay in its run |
| **Reviewer** (built) | Checks a draft, a plan or a change against rules and requirements; a second judgement that did not write it |
| Case worker | Works cases in a file store: intake, an evidence folder per case, timelines, a write-up per case |
| System operator | Looks after Servitor systems: health checks, investigating, asking before any change |
| Coordinator | Hands work to other agents and keeps track of it: fleet, delegation, monitors |
| Front desk | Answers people on a channel or a public link, with guardrails, handing off to the owner |
| Writer | Drafts and keeps a guide or document, with what agents learn reported into it |
| Panel | Puts a question to several voices and synthesizes, with the pipeline panel stage |
| **Digest** (built) | Gathers from sources on a schedule and posts a summary or a bulletin |

Each lands as one file in `archetypes/`, plus whatever the framework is missing for it to be built
properly, which is the part to find before writing the recipe.

## The Librarian

### What it is

The investigator archetype with a different job. The investigator answers one question by going
and looking. The Librarian catalogues a whole subject, keeps that catalogue current, and is asked
first: other agents look up where something lives before they go searching for it. Servitor's
method without a Servitor system: plan the walk (discovery first, then depth), walk it with the
agent's own tools, record each finding, report what it could not reach, and answer from what it
recorded before going live.

Example: an agent that writes code against an organisation's repositories has a Librarian of its
GitLab, reached through a GitLab credential or MCP server. The catalogue says which group holds
which service, where the shared libraries live, who owns what. Before searching any code, the
agent asks the Librarian where to look; the repositories that matter are attached as Servitor Git
repo systems for the code itself.

### The recipe

Grows from `investigator.md` and keeps what it says about the sub-agent shape, the read-only gate
(and its trap with remote tools), and recording only what was seen. What it adds:

- **The subject and how to reach it** are the two asks: "What should it catalogue?" and "How
  does it reach it: an API credential, an MCP server, a file store, a Servitor system?".
- **A sub-agent by default** (`owned_by` the agent that needs it, `hidden`), private to that
  parent. When a second agent needs the same catalogue it becomes a top-level agent that both
  attach under Sources.
- **The first walk** is one run with a required plan: discovery first, then depth, one step per
  area, each finding recorded, `report_gaps` before it ends. Builder runs it once when the
  Librarian is made, so it starts with a catalogue rather than an empty memory.
- **A re-walk on a schedule**, weekly by default, through the `schedule` tool: walk again, update
  what changed, retire what is gone, say what changed. The schedule belongs to the parent.
- **What it records:** findings by topic (one topic per area of the subject), facts for exact
  values (paths, owners, versions), and graph links for the structure (repository to group,
  service to repository, owner to service). The graph is what makes "where does X live" an
  answer rather than a search.
- **Answering:** from the catalogue first; go live only for what it lacks, and record what that
  finds, so the catalogue grows from use as well as from the walks.
- **Private on** (`force_private`): a catalogue of an organisation's systems is as sensitive as
  what Servitor gathers, and Servitor's investigator never reaches a hosted model unless every
  model is declared private.
- **The parent's beat:** "Before you search for code or documents in <subject>, ask
  <librarian> where they are, and search there."

### What the framework is missing for it

1. **Reading another agent's catalogue without running it. BUILT, v0.8.364**
   (`apps/orchestrate/agent_catalog.go`). Attached as a Source, an agent gave only
   `consult_<agent>`, which runs it. It now also gives `search_<agent>_memory` (its graph links,
   pinned notes, saved findings and documents for a query, at once, with no model call, described
   as the place to look first) and `recall_<agent>_about` (one named thing, its links and the
   notes that name it, or with no name the things it has mapped). Named apart from a Servitor
   system's `search_<x>_knowledge` so an agent and a system of the same name cannot clash.
   Read-only (`CapRead`), never its conversations, and for the user's own agents only: a shared
   agent stays consult-only until a read path honours its share settings.
2. Nothing else. The first walk and the re-walk are a plan and a schedule, which exist; recording
   is the memory tools it already has; the subject-neutral instructions are the recipe's to
   write.

### Order

1. The read-first Source tools (missing piece 1). Built, with tests: they find what the agent
   recorded, they are read-only, a miss points at consult, and another user's agent offers none.
2. `archetypes/librarian.md`, the recipe, with its `match` phrases and `asks`. Built. An agent's
   own sub-agents are now offered in its Sources picker (they are hidden from the fleet, so the
   shared list left them out), which is how a Librarian sub-agent is attached to its parent.
3. Build one with Builder against a real subject and read what it does, before writing the next
   recipe.

## Open decisions

- **The catalogue's memory framing.** Today's modes name the pinned block "Lessons learned",
  "Saved notes" or "Shortcuts". Start with the investigator's settings and add a "catalogue"
  framing only if the Librarian's notes read wrong under them.
- **How far a shared agent's catalogue may be read.** Proposed: as far as its share settings
  already allow its memory to be read, since asking it would reveal the same.
- **Whether the wizard's "Start from a template" row stays** now that Builder is the way agents
  are made. It is the only place a shape's record is cloned and followed; it can stay until the
  wizard itself is revisited.
