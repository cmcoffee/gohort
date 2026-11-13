# Threads: conversations an agent has that you did not start

Status: built (2026-11-12). Channel conversations kept their own rail section rather than joining Threads (see below).

## Why

An agent's session list mixes two different things. Sessions are the
conversations the owner started in the web. Threads are conversations the
agent has on its own account: with a contact over a channel, with a client
over MCP, with another agent it handed work to, with a pipeline step that
delegated to it. The owner wants to check on threads, but they should not
crowd out the sessions they are working in, and writing into one from the web
would speak for the agent in a conversation it is having with someone else.

Today some threads are in the session list beside the owner's own sessions
(channel contacts, the MCP thread), some are hidden entirely (hand-offs,
pipeline steps), and MCP turns also land in the agent's Cortex, which is for
arrivals (channels, schedules, wakes), not for the owner's own requests made
from another client.

## What is a thread (verified in code)

Every kind is a stored chat session under its agent; what tells them apart is
how the session id is written, so no stored field and no migration is needed.

| Kind | Session id | Where it shows today |
|---|---|---|
| Channel contact or room | `chan:<address or chat id>` (`ChannelSessionKey`) | the session list |
| Over MCP | `mcp-desktop:<key id>[:<conversation>]` (`mcpThread`, 0.9.328) | the session list, and each turn noted into Cortex |
| Hand-off to another agent | `dispatch:<conversation>:<agent id>` | hidden (`listChatSessions` skips it) |
| Pipeline step's delegate | `machine:<thread>:<step>` | hidden (same) |
| Cortex | `channel:<agent id>` (`cortexSessionID`) | its own view; unchanged here |

Not threads:

- **Scheduled runs.** A fire works under a temporary id (`scheduled:<session>`,
  a tool session that is never stored as a chat), delivers its result into the
  session that set the task up or into Cortex, and leaves a run in the ledger.
  Its history is the Scheduler's View and Fleet Overview, which already show
  each run's whole output. A Threads group for it would repeat those.
- **Outside-audience conversations** (`/v1` outside keys, Teams). They live in
  per-person stores, not the owner's, by design (outside-audience.md).

Found while checking: an agent with Cortex on and exactly one channel runs
every channel inbound inside its Cortex (`effectiveChannelSession`), and MCP
reaches agents through the channel runner, so for that agent an MCP turn runs
in Cortex itself, not only noted there.

## The design

**One list, two views.** The session list gets a Threads button that switches
it between **Sessions** (the default) and **Threads**. Every page load starts
on Sessions, so threads never hide the owner's own conversations until asked
for. On a phone, where the list is a drawer, the same switch sits at its top.

**Threads, grouped by where they came from,** newest first in each group:
Over MCP, Hand-offs, Pipeline steps. Channel conversations are not among
them: the rail already gives them a section of their own, above the
sessions, opened read-only, and the session list already leaves them out. A row shows the thread's title
(a conversation name, a contact or room, the agent handed to), who it is with,
and when it was last active. Search and the unread mark work as they do for
sessions.

**A dot on the button** while the list shows Sessions, when a thread has
something the owner has not seen (the same LastAt/LastSeen unread the session
list uses).

**Read-only.** A thread opens in the main pane like a session, with the
composer replaced by a line saying it is the agent's conversation with X and
cannot be written to. Writing would put words into the agent's conversation
with a contact (and its reply could go out over the channel), into a client's
MCP conversation (which then finds turns it never made), or into a hand-off
the delegating agent re-threads. Acting on a thread is done in a session of
one's own; a "continue in a new session" button can come later if wanted.

**Delete** stays available on a thread, as on a session: it is the owner's
store. Rename does not apply (a thread's title comes from where it came from).
The existing "continue" (copy into a new session) still works on a thread,
which is the way to pick one up and talk about it.

**MCP stops writing to Cortex.** `ask_agent` gets a small runner of its own
that starts or continues its thread as an ordinary session
(`RunAgentSyncContinuingRich` with a kind of its own), instead of the channel
runner, which also notes the turn into Cortex, collapses into Cortex for a
dedicated-Cortex agent, and treats the reply as something to deliver back to
a chat. Approvals behave as in a session nobody is watching: queued for the
owner, never decided on the client's say-so.

## Building it

The list is the agent loop panel's rail (`core/ui`, `AgentLoopPanel`), which
is domain-agnostic, so the switch is a generic feature and the kinds are the
app's:

- `core/ui`: `AgentLoopPanel.ThreadsURL` (a GET returning rows with a group
  field), `ThreadsLabel` (default "Threads") and `ThreadsLockedText`. Set,
  the rail shows the switch; a thread loads through the same LoadURL with the
  composer replaced by the locked note (`applyRecordLock`). Nothing in
  `core/ui` names a kind of thread.
- `apps/orchestrate`: `listChatSessions` leaves thread ids out; a threads
  endpoint lists them by kind with each one's group, title and who it is
  with; the load handler refuses a send into a thread id (the server is the
  gate, the hidden composer is a courtesy).
- `apps/mcpserver` / `apps/orchestrate`: the MCP runner above, through a seam
  in `core/mcp_server_tools.go` (`RunMCPAgent`, `ListMCPThreads`), which
  also backs the `list_conversations` tool.

Existing MCP threads (`mcp-desktop:<key>`) keep their ids and simply move to
Threads; nothing is migrated.

Tests: the session list carries no thread ids; the threads endpoint groups
each kind and carries the unread mark; a send into a thread is refused by the
server; an MCP turn is stored as its own session and writes nothing to Cortex,
for a dedicated-Cortex agent too; the rail switch (JS) starts on Sessions,
shows the dot, and hides the composer on a thread.

## Settled while building

- A group starts folded, its heading saying how many it holds, unless one of
  its threads has something new or is open: a long list of hand-offs would
  otherwise push the rest down.
- Channel conversations keep their own rail section (above), so the question
  of moving them into Threads did not arise.

## Not covered

- App agents' threads (Servitor's investigator and the like) are listed by
  `list_conversations` only when the agent is one `listAgents` returns.
