---
{
  "summary": "A sub-agent that catalogues one subject (a GitLab instance, a set of repositories, a document estate) by walking it with a plan and recording where everything is and how it connects, keeps that catalogue current on a schedule, and is asked FIRST: its parent reads the catalogue before searching for anything itself.",
  "aliases": [
    "catalogue",
    "catalog",
    "cartographer",
    "mapper",
    "directory"
  ],
  "match": [
    "map out",
    "where the code lives",
    "where things live",
    "where everything lives",
    "where everything is",
    "which repo",
    "which repository",
    "keep a map",
    "map of our",
    "catalogue of",
    "catalog of",
    "catalogue our",
    "catalog our",
    "index our",
    "look up where",
    "knows where"
  ],
  "asks": [
    "What should it catalogue: a GitLab instance, a set of repositories, a document estate, some systems?",
    "How does it reach it: an API credential, an MCP server, a file store, a Servitor system?",
    "Which agent should ask it first, or will several agents share it?"
  ]
}
---
# Archetype: Librarian sub-agent

A sub-agent that catalogues one subject by walking it with a plan, records where
everything is and how it connects, keeps that catalogue current, and is the one
its parent asks FIRST: before searching for code, a document or a system, the
parent looks up where it lives.

Build this when the user asks for an agent that will "map out where our repos
are", "knows where the code lives", or will "keep a catalogue of our GitLab
projects"; or when an agent keeps searching from scratch for things that do not
move ("look up which repo owns a service before searching"). It is Servitor's
method without a Servitor system: plan the walk, walk it with the agent's own
tools, record each finding, say what could not be reached, and answer from what
was recorded before going live.

## How it differs from the investigator

Read `investigator` first: the Librarian keeps everything it says about the
sub-agent shape, the read-only gate (and its trap with remote tools), and
recording only what was seen. The difference is the job.

- The investigator answers ONE question by going and looking, and keeps what it
  learns as a side effect.
- The Librarian's job IS the record. It walks the whole subject on purpose, on a
  schedule, and is consulted for what it already holds. Going live is the
  exception, for what the catalogue lacks.

## Composition (create_agent)

- **owned_by**: the parent agent's ID, with **hidden: true**. The parent always
  reaches its own sub-agent, nothing else can, and nobody chats to it directly.
  When a second agent needs the same catalogue, make it a top-level agent
  instead (no `owned_by`, not hidden) and attach it to each.
- **What it reads the subject with** is the person's answer to the second ask,
  never assumed. Prefer attaching the subject as a Source (`attached_sources`):
  a Servitor system or a file store contributes its own read tools and keeps
  working when the catalog moves underneath. An API is a credential the person
  already has (`fetch_url_<credential>`) or an MCP server's read tools. Only
  tools that LOOK; a Librarian never needs one that writes.
- **Memory ON**, all of it: the catalogue IS its memory. `memory_mode: "agent"`.
- **work_plan: true.** The walk is a tracked plan (`set_plan`,
  `record_step_findings`, `report_gaps`), so a long walk keeps its place and an
  unreachable area is reported rather than quietly skipped.
- **think**: `"on"`. Deciding what to walk next and what is worth recording is
  judgement. (Sub-agents default to `"off"`; override it.)
- **max_worker_rounds** ~24. A walk visits many areas; a lookup uses few.
- **gap_check** ON.
- **force_private: true.** A catalogue of an organisation's systems is as
  sensitive as what Servitor gathers, and Servitor's investigator never reaches
  a hosted model unless every model is declared private. Say so to the person;
  they can turn it off on the agent.
- **rules vs. persona**: `rules` renders above memory and above the persona and
  wins every conflict, so the two constraints a model drops under pressure go
  there: "never record a location you did not see in a tool result" and "when
  something is not in your catalogue, say so before you go and look". How to walk
  and how to report stay in the persona.

## What it records

Three kinds, each where it is best found again.

- **Graph links** (`link_entities`) for the structure: repository `in_group`
  group, service `lives_in` repository, team `owns` repository, document
  `describes` service. This is what makes "where does X live" an answer rather
  than a search, and what the parent's `recall_<librarian>_about` reads.
- **Findings** (`remember`, unpinned, one `topic` per area of the subject) for
  what each area holds, in a sentence or a paragraph: what the repository does,
  its main language, where its entry points are.
- **Pinned notes** (`remember` with `pin: true`, `origin: "told"` or
  `"concluded"`) only for the subject's conventions that shape every lookup:
  "services are named <team>-<service>", "archived projects live under the
  attic group".

Record what is durable. A commit count, a pipeline's last status, who pushed
last: that is live state, not a catalogue.

## The first walk

Run it once, right after creating the Librarian, by dispatching to it, so it
starts with a catalogue rather than an empty memory. The brief:

> Walk <subject> and catalogue it. Set a plan first: discovery (list what is
> there at the top level), then one step per area, in depth. For each area,
> link what you find into the graph, save a finding for what it holds, and note
> any convention that will help find things later. Record only what a tool
> returned. Before you finish, report the gaps: what you could not reach, and
> why.

Tell the person what the walk found and what it could not reach.

## Keeping it current

A weekly re-walk, through the `schedule` tool on the PARENT (`when: "every"`,
`time: "weekly MON 06:00"` or what the person prefers), running the Librarian
with:

> Walk <subject> again. Add what is new, update what changed, and retire what is
> gone (forget the finding, unlink the graph entry). Report what changed since
> the last walk.

Between walks the catalogue still grows: a lookup that had to go live records
what it found.

## How it answers

1. **The catalogue first.** `recall_about` for a named thing, `recall` for a
   question. A recorded location that still answers is the best answer, and it
   is instant.
2. **Say when it is not there.** "Not in the catalogue" is a finding the parent
   needs: it means go and look, not that the thing does not exist.
3. **Then look, narrowly**, and record what the look established.
4. **Answer with where, not with a survey.** The parent asked so it can go
   there.

## Wiring the parent

Two things, both on the parent:

- **Attach the Librarian under the parent's Sources** (`attached_sources:
  ["agent:<librarian id>"]`). The parent then gets
  `search_<librarian>_memory` and `recall_<librarian>_about`, which read the
  catalogue at once without running the Librarian, and `consult_<librarian>`
  for what the catalogue does not hold. A sub-agent is offered in its own
  parent's Sources and nowhere else.
- **Add a beat to its prompt:**

  > Before you search for code, a document or a system in <subject>, look it up
  > with search_<librarian>_memory or recall_<librarian>_about, and search where
  > it says. If it is not catalogued, consult <librarian>, then search.

## What to tell the user

"I gave <parent> a librarian for <subject>: it walked <subject> and recorded
where things are and how they connect, and <parent> looks there first before
searching. It walks it again every week, and adds to the catalogue whenever it
has to go and look. It reads only; it cannot change anything."
