# Archetype: Investigator sub-agent

A read-only sub-agent that goes and LOOKS when its parent needs to know
something it cannot answer from memory — probing whatever it is attached to,
reporting what it found and what it could not determine, and keeping what it
learns so the next question about the same subject starts warm.

Build this when the user asks for an agent that can "investigate", "look into",
"go and check", "figure out what's actually going on with X", or when an
existing agent keeps guessing about a system, folder, repo or service it has no
grounded picture of. Also the right answer to "can my agent do what Servitor
does" for subjects Servitor does not cover.

## Why a SUB-agent and not tools on the parent

Three reasons, and the third is the one that bites later.

- **Its transcript stays in its own session.** An investigation is dozens of
  probe results; put the probe tools in the parent's OWN catalog and every one
  of them lands in its persisted history and is replayed into its prompt forever
  after. On a standing thread that is unrecoverable — the parent gets slower
  every day and nothing in its settings explains why. Dispatched, the parent
  receives the ANSWER and nothing else. (A machine phase escapes this too, and
  is the "built in" variant at the end of this doc: a transient phase runs on a
  session of its own, before the turn's loop exists.)
- **Read-only is a property of the sub-agent, not a promise in a prompt.** See
  reach, below.
- **It accumulates its own picture.** The investigator's memory is about the
  subject, not about the parent's conversations. Ask it twice and the second
  answer is better; put the same tools on the parent and every investigation
  starts from nothing.

## Composition (create_agent)

- **owned_by**: the parent agent's ID. Ownership IS the dispatch link — the
  parent may dispatch without an `allowed_dispatch_targets` entry, and deleting
  the parent takes the investigator with it. Pair with **hidden: true** so it
  stays out of the global fleet menu; nobody chats to an investigator directly.
- **allowed_tools**: only what LOOKS at the subject. Attach the subject as a
  Source (`attached_sources`) rather than naming tools where you can — a folder,
  an MCP server, a system — because a source contributes its own per-item tools
  and keeps working when the catalog moves underneath.
- **Memory ON**, and this is the point of the archetype rather than a default
  worth keeping. Facts are what the investigator knows about the subject —
  versions, paths, which service actually serves which port. Working notes are
  what it is mid-way through; give each subject its own section
  (`update_notes(section: "<subject>", …)`) so a note about one is not rewritten
  by a finding about another.
- **think**: `"on"`. An investigation is judgement — what to probe next, whether
  an answer actually settles the question — not a lookup. (Sub-agents default to
  `"off"`; override it here.)
- **max_worker_rounds** ~14. Probing is iterative: a first look usually only
  tells you what to look at next.
- **gap_check** ON — it re-checks that the answer covers the question, which is
  exactly the failure an investigation makes: answering the part that was easy
  to find out.

## The read-only gate

Do not rely on the persona for this. Give the investigator a machine with one
transient phase, or a pipeline stage, carrying **`reach: "read"`** — the coarse
tool scope that keeps only what reads, dropping anything that writes, runs, or
reaches the network.

A capability class rather than a list of tool names, because a name list is
written against one deployment and describes the next one badly: an MCP server
publishes tools when it connects, a credential mints its own per session, an
attachment mints more per agent. "This step may look, not act" means the same
thing everywhere.

It fails CLOSED, which is worth telling the user: a tool that only reads but
declares an execute capability is dropped too. An investigator that cannot
reach something it safely could is a smaller problem than one that changes a
system nobody asked it to touch.

## Orchestrator prompt — the shape

The persona is an investigator whose every answer is either grounded or openly
incomplete. Cover these beats:

1. **Check what you already know first.** `recall` / `knowledge_search` the
   subject before probing. A fact recorded last week that still answers the
   question is a better answer than a fresh probe, and it is instant.
2. **Probe narrowly, then widen.** One well-aimed look, read the result, decide
   the next one. Do not fire five probes in parallel hoping one lands — the
   answer to the second usually depends on the first.
3. **Say what you did not determine.** An investigation that reports only its
   findings reads as complete when it is not. Every answer ends with what
   remains unknown and what would settle it — that sentence is the difference
   between evidence and a guess, and the parent is going to act on this.
4. **Never state a value you did not see.** No inferred version numbers, no
   assumed paths, no plausible-sounding port. If the probe did not return it,
   it is unknown; say so.
5. **Record what is durable.** `remember` the facts that will still be true next
   week — versions, layouts, which service owns which path. Not the transient
   readings that prompted this question; those are the answer, not the memory.
6. **Answer the question that was asked.** The parent asked something specific
   because it is about to do something with it. A survey of the whole subject is
   not a better answer, it is a slower one.

**Reporting shape**: findings first, in plain sentences, each traceable to what
produced it. Then, when anything is missing, a short `Not determined:` line. No
preamble about having investigated — the parent knows.

## Wiring the parent

The parent needs one thing: to know when to hand over. Add a beat to ITS prompt:

> When a question turns on the CURRENT state of <subject> rather than on what
> you know about it, dispatch to <investigator> and answer from what it returns.
> Do not guess at live state, and do not go and look yourself.

That last clause matters. A parent holding both the investigator and the
subject's own tools will use the tools — they are closer — and the transcript
lands in its thread, which is the thing this archetype exists to avoid.

## Built into one agent, with no second record

Everything above gives the parent an investigator it dispatches to. The other
shape puts the investigation INSIDE the agent, as a transient machine phase, and
it is the one most people actually want: one agent, one record, and the looking
happens before the answer rather than because the model remembered to ask for it.

It escapes the transcript problem for the same reason a delegate does. A
transient phase runs during system-prompt assembly, before the turn's own loop
exists, on a session of its own — its probe calls never enter the thread. What
survives is the fields it declares, which arrive in the persona as
`{state:<phase>.<field>}`.

### The phase shape

Two phases: a transient one that looks, and the resident one that answers.

```
machine_def(action: "create", name: "grounded_answers",
  start: "look",
  attach_to_agents: ["<the agent>"],
  phases: [
    { "name": "look", "desc": "Find out what is actually true right now.",
      "reach": "read",
      "think": "on",
      "prompt": "The user asked: {input}\n\nFind out what is actually the case. Probe narrowly, read the result, then decide the next look. Record only what you SAW — no inferred versions, no assumed paths. Name what you could not determine.",
      "output": [
        {"name": "findings",     "type": "string", "desc": "What you established, each traceable to what produced it", "required": true},
        {"name": "undetermined", "type": "string", "desc": "What you could not settle, and what would settle it"}
      ],
      "next": "answer" },

    { "name": "answer", "desc": "Answer from what was found.", "resident": true,
      "guard": "the user is asking about the CURRENT state of something rather than about what you already established",
      "guard_to": "look",
      "prompt": "Answer from these findings:\n\n{state:look.findings}\n\nNot established: {state:look.undetermined}\n\nDo not state a value that is not above. If the answer needs something not established, say so." }
  ])
```

`attach_to_agents` in the same call: an unattached machine does nothing at all.

### The three settings that carry it

- **`reach: "read"`** — the gate, and the reason this is safe to run unattended.
  A capability class, not a tool list, so it survives a catalog that moves. It
  fails closed; see above.
- **`think: "on"`** — transient phases default to thinking OFF, which is right
  for a router and wrong here. Deciding what to probe next, and whether an
  answer actually settles the question, is judgement.
- **`guard` + `guard_to`** — what stops it investigating on every turn. The
  machine starts at `look`, so the FIRST turn establishes the ground; after that
  the cursor rests on `answer` and only returns to `look` when the guard fires.
  Without the guard you pay a full investigation on "thanks, that's great".

### What you give up

**A memory of its own.** The separate investigator accumulates facts about the
SUBJECT in its own namespace, which is what makes its second answer better than
its first. A phase writes into the parent's memory, where findings about the
subject mix with everything else that agent knows — or into phase state, which
goes when the session does. Mitigate it by having the `look` phase `remember`
what is durable and keep a working-notes SECTION per subject
(`update_notes(section: "<subject>", …)`), so the subject's running picture stays
separate from the rest of the parent's notes.

**The choice of when.** The machine decides, not the model. That is the trade in
both directions: a phase cannot forget to look, and it cannot decide that this
particular turn did not need to.

### Choosing between them

- The agent's job IS the subject — a support agent whose every question is about
  one system — build it in. Always look, then answer.
- Investigating is occasional, or several agents want the same investigator, or
  the subject deserves a memory that outlives one agent's sessions — use the
  sub-agent. The second record earns itself.

## What to tell the user

"I gave <parent> an investigator — when you ask something it can only answer by
going and looking, it hands off to <name>, which probes read-only and reports
back. It remembers what it learns, so the second question about the same thing
is faster and better than the first. It can look; it cannot change anything."
