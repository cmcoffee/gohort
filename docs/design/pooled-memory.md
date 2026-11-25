# Pooled memory: a shared specialist learns from everyone who uses it

Status: **slices 1, 2 and 4 built** (0.9.682 and 0.9.683, 2026-11-24): the
write path with who added each finding, the daily cap, the runs that never
write the pool, labels on recall, the removal rules in the tools and the
Memory routes with the Memory window's two sections, and the Share tab switch
with its notices. Slices 3 and 5 are not built. Designed 2026-11-23. Follows
[sharing-people-and-teams.md](sharing-people-and-teams.md), which settled
that a share gives the people added "the same experience" the owner has, and
that memory follows the kind of agent: **pooled for a specialist, private for a
chatbot or personal assistant.**

## Why

A troubleshooter shared with a team today learns separately from each person.
What Alice works out on Monday is in Alice's copy only, and Bob meets the same
failure on Tuesday with nothing to go on. The owner's own findings do reach
everyone, read-only, but the team's never reach each other or the owner. For
an agent whose job is a body of know-how, that is the wrong way round: the
know-how is the point of sharing it.

A chatbot or personal assistant is the opposite case. What it remembers is
about the person it talks to, and pooling it would hand one colleague's
afternoon to every other.

## How it works today (checked in the code)

- **Each person has their own copy.** A shared user's run writes everything it
  learns to their own namespace. The owner's layers sit underneath, read-only
  (`apps/orchestrate/memory_scope.go`: `memoryScope.Write` is always the
  runner, and the owner is in `Under`).
- **Three switches decide which owner layers a recipient reads**
  (`AgentRecord.ShareCortex`, `ShareReference`, `ShareNotes`): the activity
  feed and "what it worked out" default on, saved notes default off.
- **Deleting reaches only your own copy.** `forget` removes one item by id,
  from the runner's namespace; the owner's items answer "not in your accessible
  memory". There is no clear-all tool. The Memory window's add, delete, clear
  and wipe act on the caller's own copy (`memory_routes.go`).
- **A stranger on a channel** runs as the owner's account, so what they say is
  saved into the owner's memory already, prefixed "Said by X, not the owner",
  and they cannot delete anything (`attributedToSpeaker`,
  `strangerMayNotForget`).
- **Guide findings from recipients go nowhere.** A guide's report tool is built
  for the runner (`withSourceTools(..., t.user, ...)`), and a guide is offered
  only to its own owner, so someone using a shared agent attached to the
  owner's guide gets no report tool at all.
- **Maintainers have no memory role.** Their notice says memory "stays with"
  the owner (`agent_maintainers.go`).

## The design

### Which agents pool

A new tri-state on the agent, `MemoryPool`: `"on"`, `"off"`, or empty.

- **Empty reads as off.** Every existing agent keeps exactly what it does now.
  No migration: nothing is rewritten, and an existing shared agent does not
  start pooling under people who were told their copy was private.
- **A new agent gets `"on"` when it is created as a specialist** (memory mode
  Agent) **and `"off"` otherwise** (Chatbot, or no mode, which is a chat
  assistant). Written once at creation, so it is a visible choice from the
  first day and not a default that changes underneath it.
- **The owner switches it** on the agent's Security > Share tab, beside the
  three layer switches: "Pool what it learns from everyone who uses it". On an
  existing specialist this is the one click that turns pooling on.
- **A chatbot can be pooled only by the owner choosing it**, with a warning
  that what it learns about each person will reach everyone it is shared with.
- **Turning it on does not pull in what people already learned.** Each person's
  private copy stays private. Pooling covers what is learned from then on.
- **Turning it off** stops new contributions. What is already in the pool
  stays, attributed, and stays readable the way the owner's layer is today.

### What pools, and what never does

| Layer | Pooled agent | Why |
|---|---|---|
| **Findings** ("what it worked out", Reference Memory, `remember` without pin) | **Pooled** | This is the know-how. |
| **Pinned notes** (always in every prompt) | Owner and maintainers write the pool's; everyone else's stay **private** | A pinned note is in every prompt of every person. Letting any user add one is letting any user rewrite the agent for everyone. Owner and maintainers can already change its prompt, so this adds no new power. |
| Activity feed (cortex) | Never | A record of one person's week. |
| Working notes | Never | One conversation's scratch space. |
| Graph | Not in this design | Revisit once findings pool cleanly. |
| Session recall (chatbots) | Never | About the person, by definition. |
| Uploaded documents | Unchanged | Already their own unless the owner shares them as knowledge. |

When someone who is not the owner or a maintainer asks a pooled agent to pin
a note, it saves it in their own copy and says that the note is theirs only.

### Where the pool is stored

**The pool is the owner's own copy of the agent's findings.** On a pooled
agent, a recipient's run writes its findings to the owner's corpus prefix
(`knowledgeSource(owner, agentID, topic)`) and not to its own.

- Recipients already read that copy by default (`ShareReference`), and the
  owner already manages it in the Memory window. Pooling adds a writer, not a
  store.
- **On a pooled agent "what it worked out" is always shared**: the
  `ShareReference` switch shows on and locked while pooling is on, since a pool
  nobody reads is not one. Pooling off hands the switch back.
- `memoryScope` grows the second meaning its comment anticipated: for a
  pooled finding, `Write` is the owner while `Run` stays the runner. Ledger,
  activity feed and spend stay the runner's.
- `MemoryPerDocument` applies unchanged: a finding about a document is filed
  under that document's topic, in the pool.

**Each pooled finding records who it came from.** A new `By` field on
`EmbeddedChunk` (username; empty means the owner, which is every chunk written
before this). Additive and `omitempty`, so old records read as they are.

### Reading a pooled finding

- **Labelled as the contributor's.** Recall shows "From alice:" on a finding
  someone other than the reader and the owner contributed, the way a channel
  stranger's finding reads "Said by X". It is something a colleague worked
  out, to check, not an instruction.
- **Retrieved like any finding** and not placed in the always-in-prompt block,
  so one bad entry surfaces only when it matches what is being asked.

### Guide findings

On a pooled agent attached to the owner's guide with "Can make changes" on,
**a recipient's run gets the guide's report tool**, built for the guide's
owner, and files into **the owner's Guide Curator inbox**. The finding's
`DocFindingOrigin` gains `ReportedBy`, shown in the digest, so the curator and
the owner can weigh who reported it. The curator keeps its authority: it
decides whether the finding belongs, and its digest is where a bad one gets
undone. On an agent that does not pool, recipients still get no report tool.

## Guardrails, enforced in code

A rule in the prompt ("don't delete everything when asked") can be talked
around, so none of these rely on the model.

| Action on the pool | Owner | Maintainer | Anyone else it is shared with (and the agent in their runs) |
|---|---|---|---|
| Add a finding | Yes | Yes | Yes, attributed to them |
| Add a pinned note | Yes | Yes | No: it goes to their own copy |
| Delete one finding | Any | Any | **Only ones they contributed** |
| Clear or wipe the pool | Yes | Yes | **No** |
| Restore a removed finding | Yes | Yes | Only their own, within 30 days |
| Remove everything from one person | Yes | Yes | Their own only |
| Their own private copy | Full control | Full control | Full control, unchanged |

- **`forget` in a recipient's run** removes their own private items and their
  own pool contributions, one id per call as today. On someone else's it
  answers: "That was added by bob; only the owner or a maintainer can remove
  it." There is still no clear-all tool, for anyone.
- **The Memory window** shows two sections for a recipient: **Yours**, with the
  controls it has now, and **Shared with everyone who uses it**, read-only
  except a Delete on their own entries. Owner and maintainers see the pool
  with full controls. A maintainer sees the owner's pinned notes only if
  `ShareNotes` lets recipients see them.
- **Routes check, not the page.** The delete and wipe routes check the
  caller's role and the item's `By` server-side; hiding a button is not the
  guard.
- **A daily cap per contributor** (default 50 pool findings a day). Past it, a
  save goes to their own copy and says why. It bounds a runaway loop or a
  person flooding the pool, and the owner is told once in the share notices.
- **Who never writes to the pool**: a clean-room (incognito) session, which
  saves nothing anyway; a person outside gohort through the outside-audience
  API (`outsideAudience()`), whose findings stay in their own store; and an
  app's agent, which follows its app's own Memory setting.

### Nothing is lost by a click

Every removal from the pool (one finding, a person's contributions, a wipe)
goes to **Recently removed** for 30 days, restorable from the Memory window by
the owner and maintainers, and by the contributor for their own. A wipe is one
entry there that restores as a whole. After 30 days it is gone.

### Telling people

- **When pooling is on**, the chat's first-run notice for a recipient says:
  "What this agent learns from your conversations is shared with everyone who
  uses it. Notes you pin stay yours." The Memory window repeats it above the
  pool.
- **When the owner turns pooling on** for an agent already shared, each
  recipient gets a notice saying so, from that moment on.
- **Maintainer notice** changes from "its memory stays with the owner" to "you
  can curate what it has learned from everyone".

### When someone leaves the share

Their contributions **stay in the pool**, attributed, because the know-how is
the point. While they still have access they can delete their own. The owner
or a maintainer can remove everything from that person in one action, which
goes to Recently removed like any other.

## Stored-shape changes

All additive, none rewrite existing records, **no migration**:

- `AgentRecord.MemoryPool string` (tri-state; empty = off).
- `EmbeddedChunk.By string` (empty = the owner).
- `DocFindingOrigin.ReportedBy string`.
- A Recently removed table for pooled chunks, holding the removed chunk and
  who removed it, purged after 30 days.

Core is at its export ceiling: the new fields are on existing types, so no
new exported symbol is needed in `core` for the first three; the removed-items
store belongs in `apps/orchestrate`.

## Build order

1. **Write path and attribution.** `MemoryPool`, set at creation by mode; pooled
   findings write to the owner's prefix with `By`; `ShareReference` locked on;
   the daily cap; the never-writers. Recall labels contributors.
2. **Guardrails.** `forget` and the Memory routes check role and `By`; the
   Memory window's two sections.
3. **Recently removed** and restore.
4. **Telling people**: the switch on the Share tab, first-run notice, notices
   on switching, maintainer copy.
5. **Guide findings** through the owner's curator, with `ReportedBy`.

Slices 1 and 2 ship together: a pool without the delete rules is the unsafe
half.

Left for later slices, found while building 1 and 2:

- **A maintainer's pinned note** still goes to their own copy, as before; the
  table above has it going to the pool's.
- **The pool is the owner's copy, so it falls under that copy's finding cap**
  (`tune_finding_hard_cap`, default 300). Settled 2026-11-25 (0.9.684): what
  others added is counted apart from the owner's own, each to the same limit,
  so a busy pool evicts its own oldest contributions and never the owner's
  findings. A save marks who added a finding before the cap runs, so a new
  contribution is never counted as the owner's.
- **Removals are final** until Recently removed exists. Built in slice 3
  (0.9.685), below.

## Settled with the owner (2026-11-23)

The owner went with the recommendations:

1. **Pinned notes from recipients stay private** to them. Only the owner and
   maintainers pin notes into the pool.
2. **The daily cap is 50 pool findings per person per day.**
3. **When someone leaves the share, their contributions stay** in the pool,
   attributed; the owner or a maintainer can remove everything from that
   person in one action.

## Considered and not chosen

- **A separate pool namespace** beside the owner's copy. Two stores to search
  and manage, and a third section in the Memory window, to separate what the
  owner learned from what everyone did, when the owner's findings already reach
  every recipient by default. `By` gives the same separation for display and
  deletion without a second store.
- **Guardrails in the prompt.** Talked around by the first person who tries.
- **Taking the Memory window away from recipients.** What the agent remembers
  from their own conversations is about them; seeing and deleting it is the
  privacy default, and it never affects anyone else.
- **Pooling existing shared specialists automatically.** It would change, under
  people already using them, where what they say ends up.

## As built: slice 4 (0.9.683)

- **The switch** is "What it learns from people using it" on the Share tab's
  "What a recipient sees" panel, Off ("Each person's own") listed first so an
  agent stored before the setting shows as off. PATCH takes only "on" and
  "off"; a maintainer's edit never carries it.
- **"What it worked out" is hidden** while pooling is on (ShowWhen), rather
  than shown locked: a top-level form field has no locked state, and a choice
  that silently did nothing would mislead.
- **A chatbot's warning is the help line**, starting "Careful:". An option's
  Confirm is honoured on a row's segmented toggle, not in a form's select, so
  a confirm there would have done nothing.
- **Recipients are told on joining, not on first chat.** The chat page has no
  per-agent first-run notice to hook, so the notice goes through the share
  notices at the two moments that matter: when the owner turns pooling on
  (everyone it is shared with, maintainers in curator's words) and when
  someone is added to an agent that already pools. Both are decided in the one
  store write, from the record before and after, so no new stored data.
  Someone who joins through a team gaining a member is not told.
- **The owner is told** when someone reaches the daily cap, once per person:
  the notice store folds a repeat into a count.
- **The maintainer notice** says they can curate what it has learned from
  everyone, on a pooled agent.

## As built: slice 3 (0.9.685)

- **The store** is one table, `orchestrate_pool_removed`, in the owner's
  store, keyed by agent id and entry id. An entry holds the removed chunks as
  they were, vectors and who added each included, so a restore puts them back
  without embedding again, plus who removed them, when, and what it was in
  words. Additive, no migration.
- **What is kept**: every removal from a pooled agent's findings by a person
  or by the agent at their word: one entry deleted in the Memory window, a
  wipe, everything one person added, and `forget` and `memory(forget)` in a
  run. A wipe is one entry. The finding cap's eviction of the oldest is not
  kept: it is not a click, and keeping it would make a second copy of the
  pool. On an agent that does not pool, a removal is final as before.
- **Who sees and restores**: the owner and maintainers, every removal; anyone
  else, only removals they made, which are only ever of what they added. A
  removal someone may not see is a 404 on restore, as if it were not there.
- **Thirty days**: an entry older than that is dropped whenever the table is
  read or written, so there is no timer. Deleting the agent drops its entries.
- **The Memory window** shows a Recently removed section, only when there is
  something to put back, with a Restore on each entry. It refreshes after
  every delete and wipe.
- **Maintainers reach the Memory window** whether or not the agent is also
  shared with them to use. Before this a maintainer not on the share list got
  a 404 from every Memory route, so the curation the table above gives them
  had no door. They see their own copy and the pool with full controls.
- **A restore does not run the finding cap.** Restored chunks keep their
  dates, so the next save's cap may evict them if they are the oldest past
  the limit.
