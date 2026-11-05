# Outside audiences: gohort behind someone else's chat

Status: design agreed (2026-11-05); see Decisions. Step 1 for /v1 is built (outside keys, per-person stores, the agent switch).

## Why

Two ways people who are not gohort users end up talking to a gohort agent:

1. **A chat widget on another platform** (a customer portal such as Magentrix, a
   help widget, a voice line). The owner tunes the agent here (persona, tools,
   guardrails, knowledge) and the platform only carries the conversation.
2. **A Teams bot** added to a tenant, where colleagues DM it and add it to
   group chats and channels.

Both work today for the owner's own use. Neither is safe for an *audience*:
many people the owner does not know, talking to one agent at once.

## What exists (verified in code)

**The /v1 API (`apps/openaiapi`).** OpenAI's chat completions shape, the de
facto standard a "bring your own model" widget speaks.

- `model: "agent:<id>"` runs a full agent turn (persona, tools, guardrails,
  memory); `worker`/`lead` are the raw tiers.
- An agent is reachable only with "Reachable over MCP" on, a model-API scoped
  key, and the feature allowed for the key's owner.
- Threads are per caller: `X-Session-Id`, a call id, or the `user` field, all
  namespaced under the API key that made the call (`sessionKey`), so one
  integration cannot name its way into another's threads.
- The turn runs **as the key's owner**.

**Teams bots (`core/teamsbots`, `apps/bridges/teams.go`).**

- Bot Framework push: every activity carries a bearer token Microsoft signs
  for the bot's app id, and the single-tenant check refuses other tenants.
- The speaker is identified by Entra object id (`From.AadObjectID`), signed
  by Microsoft. That is a real identity, unlike a widget's `user` string.
- Each conversation is remembered (`botConv`) with its type:
  `personal` (a DM), `groupChat`, or `channel`. `Bot.Accept` limits which
  types it answers.
- After the front door it is ordinary channel machinery: one whole-service
  channel bound to an agent, a thread per conversation, the channel's
  gatekeeper and Senders policy, dedup, the loop guard.
- `conversationUpdate` (installed, someone joined) is ignored today.

## The gaps

1. **Memory is the agent's, shared by everyone.** An agent's facts live under
   `agent:<id>` (`apps/orchestrate/facts.go`). Every outside caller writes to
   and reads from the same memory: one customer's details can surface for
   another, and anybody can try to plant "facts" for everyone after them.
2. **Who is asking is only as good as the caller.** A widget's `user` field is
   whatever the integration sends. Fine from the platform's server; worthless
   from a browser, where anyone can send any name.
3. **The agent's reach comes with it.** Outside turns run as the owner, with
   the owner's tools, credentials and connected accounts. Approvals cannot be
   answered by a customer, so an "ask first" tool either blocks the turn or,
   worse, is configured to run unattended.
4. **No per-person limits.** One busy or abusive end user can spend the
   owner's model budget for everyone.
5. **Teams treats every conversation alike.** The gatekeeper and Senders policy
   are per *channel*, and one channel carries the bot's DMs, group chats and
   channels together. A DM wants an answer every time; a group wants one only
   when the bot is addressed. A DM's private details must never surface in a
   group.
6. **Teams setup is manual.** The bot answers on its one channel, but nothing
   happens when it is added to a team or a group; per-type behaviour has to be
   arranged by hand, if it can be at all.

## Proposal

### A. An agent can be set to serve an outside audience

A setting on the agent, beside "Reachable over MCP": **Audience: just me /
people outside**. Off by default. Turning it on changes how every *outside*
turn of that agent runs (A through E below). The owner's own chats with the
same agent are unchanged.

What counts as an outside turn: a /v1 call whose key is marked for outside use
(C), and any Teams activity whose speaker is not the bot's owner.

### B. Memory per person, never shared

- Each outside person runs in a store of their own, `outside:<source>:<id>`
  (`agenthost.OutsideScope`), where source is the API key or the Teams bot,
  and id is the asserted user or the Entra object id. It is the path a shared
  agent's recipient and a servitor appliance already take: notes, findings,
  history, past sessions and threads are all theirs, with no layer learning a
  new namespace. (Decided 2026-11-05 over a namespace inside the owner's store,
  which would have needed a carve-out per memory layer.)
- `remember` / `recall` in an outside turn read and write only that store.
- What reaches them from underneath is fixed, whatever the share switches say:
  the agent's saved notes, read-only; never the owner's cortex or the
  agent's findings; curated knowledge as today.
- The agent's own memory (what the owner taught it) is **read-only** context
  for outside turns: the agent knows its job, but nothing an outsider says
  writes into it.
- In a Teams group or channel, memory is per **conversation**, not per person:
  what the group said stays with the group, and a DM's memory is never loaded
  into a group turn (or the reverse).
- Knowledge collections attached to the agent are read as today: they are the
  owner's published material, not anyone's private data.

### C. Identity the agent can trust

- **/v1:** an API key gets an "outside audience" mark. Such a key must be used
  server to server: the platform's backend names the end user in `user` (or an
  `X-End-User` header) and the key is never put in a browser. The key's page
  says so plainly. Optional hardening: the platform signs the end-user id with
  a per-key secret (HMAC), and an unsigned or wrongly signed id is refused.
- **Teams:** the Entra object id from the signed activity. No extra work.
- A turn with no end-user id is refused for an outside-marked key: there is no
  safe default bucket to put an anonymous person's memory in.

### D. A smaller reach for outside turns

- Outside turns see only tools the owner marks **"safe for outside use"** on
  the agent's tool list. Default: none.
- Anything that would ask the owner first is not offered at all in an outside
  turn (there is nobody to approve it), rather than queuing or running.
- Credentials and connected accounts follow the same mark: only those the
  owner lends to outside use. Never the owner's personal accounts by default.
- Guardrails apply as today; an outside turn is also never "unattended work"
  that can schedule, message other people, or change the agent.

### E. Per-person limits

- A per-person rate (turns per minute and per day) and a per-person daily
  spend cap, set on the agent's audience settings. Over the limit, the person
  gets a polite fixed reply; the owner gets one notice a day, not one per hit.

### F. Teams: group versus DM, set up automatically

Per conversation type, set once on the bot:

| | DM (`personal`) | Group chat | Channel |
|---|---|---|---|
| Answers | every message | only when @mentioned | only when @mentioned |
| Memory | the person's (B) | the conversation's | the conversation's |
| Default | on | on | off (owner turns on) |

- "When @mentioned" uses the activity's mention entities, which the bridge
  already strips from the text; it keeps a flag that the bot was named
  instead of discarding that fact.
- **Automatic setup:** `conversationUpdate` (bot added to a group or team) is
  no longer ignored. It records the conversation with its type and that
  type's defaults, so the first message in it behaves correctly without the
  owner doing anything. The first DM from a new person does the same for a
  `personal` conversation. Nothing new to approve per conversation: the owner
  approved the bot, and the per-type table is the policy.
- The owner can still override one conversation (mute the bot in a noisy
  channel, or let it answer everything in a small team), on the bot's page.

## Stored data

All additive, no migration:

- Agent: an audience setting with its limits (A, E) and per-tool and
  per-credential "outside use" marks (D).
- API key: an "outside audience" mark, and an optional signing secret (C).
- Teams bot: the per-type table (F). `botConv` gains whether the last message
  named the bot.
- Memory: new namespaces under the agent (B). Existing `agent:<id>` memory is
  untouched and becomes the read-only layer for outside turns.

Nothing existing changes meaning: an agent with Audience "just me" behaves
exactly as today.

## Order of work

1. Memory per person (B) and identity (C): the two that make it safe at all.
2. Reach (D), limits (E).
3. Teams per-type rules and automatic setup (F).
4. Magentrix specifically: confirm its chat can point at an OpenAI-compatible
   endpoint with a server-side key. If it cannot, a small relay on the
   platform's side is needed, and that is a separate piece.

## Decisions (owner, 2026-11-05)

1. Outside turns see the owner's agent memory **read-only** (B as written).
2. Per-person memory **does not expire**.
3. Teams **channels default off** (the owner turns a channel on); group chats
   on with @mention, DMs on.
4. **Signing the end-user id comes later**: outside-marked keys are used
   server to server and name the end user; HMAC signing is a follow-up.
