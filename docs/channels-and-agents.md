# Channels attached to Agents

Status: design. This captures the direction of folding phantom's messaging
surfaces into orchestrate Agents, so the decisions are settled before code.

## The idea

Today phantom runs a second agent engine. `processMessage` does its own
persona, memory, knowledge, skills, dispatch, scheduling, and gatekeeping
— all of which an orchestrate Agent already does, in one place. We
maintain two brains.

The direction: a **Channel** is an inbound/outbound messaging surface
that you **attach to an Agent**, the same way you attach tools, skills,
collections, and pipelines today. A message arriving on a channel runs
the bound agent's loop; the reply goes back out the channel. Phantom
stops being an engine and becomes the channel transport layer.

```
  iMessage / Telegram / Slack bridge
        │  POST /hook            ▲  GET /poll
        ▼                        │
  Channel layer (transport: dedup, alias, gatekeep, coalesce, format)
        │  inbound message            ▲ reply
        ▼                             │
  Orchestrate Agent loop  (persona · tools · memory · knowledge · skills · fleet)
```

The Slice 1 multi-service work (the `Service` dimension, the key-scoped
poll, `ServicePolicy`, and `docs/phantom-service-bridge.md`) is the
transport substrate this builds on. See `project_phantom_multichannel`.

## The three surfaces of an agent

An agent can have up to three kinds of conversational surface, and they are
**orthogonal toggles, not agent types**. Any combination is valid.

- **Cortex** (zero or one): the agent's persistent home thread and mind.
  Where the owner directs it, where event-monitor wakes and standing-agent
  reports land, and the cross-channel command center — read across all the
  agent's channels, or message one or all of them. Bounded by rolling-
  summary compaction.
- **Channels** (zero or many): the rooms. Each is a connection from the
  transport (phantom) to the agent representing one place it talks — a
  linked iMessage chat (a person or a group), later a Telegram/Slack room.
  Inbound from a contact runs the agent in that channel's thread. A Channel
  *is* the room; there is no separate "conversation" concept.
- **Sessions** (zero or many): ad-hoc direct web chats — the classic Agency
  conversation where the owner sits down and works with the agent.

Combinations:

| Surfaces | What it is |
|---|---|
| Sessions only | a normal web agent |
| Cortex + Sessions | today's Master Control / Chat |
| Channels + Sessions | a pure messaging bot |
| all three | a full agent: a home-thread brain, rooms it talks in, and a web front door |

In the agent's rail this reads as three groups: the **Cortex** pinned at top
(one), **Channels** (the rooms, labelled by contact/group), and **Sessions**
(web chats). Under the hood all three are session records under the agent,
tagged by origin: the home thread, `chan:<chatID>` rooms, and plain web
sessions.

So the full vocabulary: **Service** is the wire, a **Channel** is a room on
it, the **Cortex** is the mind across the rooms, and **Sessions** are the
front door. Context/compaction controls are not specific to any one surface
— they bound any persistent thread the agent runs (Cortex threads and
Channel threads alike), driven by per-agent settings.

## The attach model

Mirror the existing per-agent attach fields on `AgentRecord`
(`AttachedCollections`, `AttachedPipelines`, `AllowedSkills`). A Channel
is a stored record; attaching it to an agent routes that channel's
inbound to that agent.

- **One channel binds to exactly one agent.** Routing must be
  unambiguous: a message on channel X runs exactly one agent. The
  authoritative binding lives on the channel (`channel.AgentID`); the
  agent editor's "attached channels" list is a view/setter over that
  inverse. (Storing it on the channel keeps the inbound lookup O(1) and
  makes one-channel-one-agent an invariant rather than a convention.)
- **One agent can have many channels.** The same agent answers on
  iMessage and Telegram; each is a separate Channel record bound to it.
- A Channel record carries: `id`, `service` (imessage/telegram/slack —
  the Slice 1 transport id), the binding/address scope (whole service,
  or a specific handle/room), `owner` (gohort user), and `agent_id`.

This makes "give my Support agent a phone number" a first-class action in
the agent editor, alongside "attach a collection."

## Transport vs agent: where the line sits

The split is the load-bearing decision. Channel-layer concerns stay in
phantom; thinking concerns move to the agent.

| Concern | Lives in |
|---|---|
| Inbound dedup (row_id), loop-back detection | Channel layer |
| Alias routing (multiple addresses → one thread) | Channel layer |
| Coalescing rapid messages into one turn | Channel layer |
| Per-service delivery formatting (markdown, chunking, attachment stagger) | Channel layer (`ServicePolicy`) |
| Gatekeeper ("should this even wake the agent") | **Boundary** — see below |
| Persona / system prompt | Agent |
| Memory (per-(user, thread)) | Agent |
| Knowledge / collections | Agent |
| Skills, tools, dispatch, fleet | Agent |
| Proactive / scheduled outreach | Agent (standing agents + watchers) |
| Goal conversations | Agent (dispatch) |

The **gatekeeper** is the interesting boundary. It is a cheap "is this
message worth a full turn" pre-filter (wake-word, rate-limit). It could
stay a channel-layer gate (cheap, runs before the agent spins up) or
become a standard agent-loop gate. Lean: keep it in the channel layer as
a pre-agent filter, configurable per channel, because it protects the
expensive agent run.

## Identity and tenancy

- A messaging **contact** (a handle) is not a gohort user. It is an
  external party the agent talks to.
- A **Channel is owned by one gohort user**; the bound agent runs under
  that owner (the agent's existing per-user memory/knowledge apply).
- Phantom is single-tenant today (the device owner); orchestrate agents
  are already per-user. The binding is therefore `(channel, owner,
  agent)`. Multi-tenant is the same shape with more than one owner — the
  Slice 1 key→service→owner model already carries owner.
- A thread on a channel maps to an agent **session** (orchestrate
  already has per-agent threads). Each contact/room is its own session
  under the bound agent.

## Per-conversation routing (later)

Start with a fixed channel→agent binding. A richer mode is a **router
agent**: one channel bound to a triage agent that reads the contact and
dispatches to specialist agents (the fleet/dispatch machinery already
exists). Defer until the fixed binding works.

## What happens to phantom's bespoke features

Most become "the agent already does this":

- Per-chat memory → the agent's per-(user, thread) memory.
- Goal conversations → agent dispatch.
- Proactive scheduling / reminders → standing agents + watchers.
- Phantom's special tools (read_phantom_chat, notify_me, message_contact)
  → already exposed to orchestrate via the `PhantomLink` core seam; they
  stay, now pointed at the channel layer.

## Naming: resolved — Cortex

`AgentRecord.Channel bool` USED to mean the home thread, which collided
with the messaging-surface "Channel" we want. **Resolved (done):** the
home-thread concept is now **Cortex** — the agent's mind. The rename
landed: `AgentRecord.Cortex` (the JSON/storage key stays `"channel"` so
existing records load without a migration), `cortexSessionID` /
`CortexSessionID` (the stored session-id prefix stays `"channel:"` for the
same reason), and the user-facing "Master Control" label is now "Cortex".

So the vocabulary is settled:
- **Service** — a transport/platform id (imessage / telegram / slack).
- **Cortex** — an agent's persistent home thread (its mind).
- **Channel** — a messaging surface (a Service + binding) attached to an
  agent. The word is now free for exactly this.

(Left as legacy internal keys, invisible to users: the `"channel"` JSON
tag on `Cortex`, the `"channel:"` session prefix, and the `notify="channel"`
monitor enum. A full storage-key migration can clean these later; none
collide with the new Channel records.)

## Migration path

- **Phase 0 (done):** phantom multi-service foundation + bridge contract.
- **Phase 1 — the binding:** a `Channel` record (`service`, address
  scope, `owner`, `agent_id`) + `AttachedChannels` on the agent editor +
  the channel→agent lookup. No behavior change yet; just the data model
  and UI.
- **Phase 2 — route inbound to the agent:** the channel layer, on
  inbound, runs the bound agent's loop instead of `processMessage`'s
  persona path, and delivers the reply through the outbox with the
  service's formatting policy. This is the big one.
- **Phase 3 — retire phantom's parallel engine:** once routing is
  proven, fold phantom's persona/memory/knowledge/skills/scheduling onto
  the agent and delete the duplicates. Phantom is then purely transport.

## Open questions

- Address scope of a Channel: whole-service (every contact on this
  iMessage device → this agent) vs per-contact bindings on one service.
  Start whole-service; per-contact is a routing refinement.
- Does a Channel need its own config (gatekeeper prompt, auto-reply
  policy) or does that move onto the agent? Lean: channel-layer policy
  (auto-reply, gatekeeper) on the Channel record; everything else on the
  agent.
- The desktop bridge (gohort-desktop) exposing multiple services maps to
  multiple Channel records sharing one bridge. Confirm the key→service
  model covers it (it does — one key per service).
