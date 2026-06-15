# Channel / Thread model — shared-log unification

Status: design, pre-implementation. Decision locked: **shared log** (see Decisions).

## Why

Today there are two half-reconciled concepts:

- **Sessions** — ordinary per-agent conversations (`orchestrate_sessions:<agentID>`, keyed by
  session id, messages inline).
- **The "channel" home thread** — a synthetic per-agent thread `channel:<agentID>` where
  background wakes (monitor fires, standing-agent reports, goal-conversation completions,
  phantom pushes) are *supposed* to land, surfaced through a separate Channel/History nav.

The split leaks. In practice standing agents capture `ReportSessionID` = the ordinary session
they were created in (`standing_runner.go`), so their reports land in a normal UUID session,
not `channel:<agent>`. The synthetic thread is mostly the unused fallback. The History nav
reads `channel:<agent>` and finds nothing, the Channel row routing has been fragile, and users
can't tell whether they're "in the channel" or "in a session."

## The model

Collapse to **one primitive: a Thread** (we keep calling the shared-membership case a
"channel").

> A Thread = a message log + a set of participants (humans, agents) + the producers
> (monitors, standing agents, dispatches, watches) attached to it.

- **Private chat** = a Thread with participants `{owner, oneAgent}`. This is today's "session."
- **Shared channel** = a Thread with more participants `{owner, agentA, agentB, monitorX}`.
- **"Sessions communicating"** = participants of a shared Thread all read and post to the same
  log; producers post into it; participant agents can be woken to react.

A session is therefore just the 2-participant special case of a channel. There is no separate
"home thread" — the per-agent default is simply the Thread you land on, pinned to the top of
the list, openable and scrubbable like any other.

## Who the human talks to (addressing)

The human is a participant, but **a human post is always directed at an agent, never at the
channel itself.** Reading is broadcast; writing is addressed.

- Every channel has a **lead/host agent** that fields human input. In a 1:1 session the lead is
  the only agent, so it is implicit — that is just normal chat.
- In a **shared** channel a human post goes to the lead (or an explicitly @-addressed
  participant). Other agents/producers see it as context but do not all reply — no reply storm,
  no "which agent answers?" ambiguity.
- The human **reads** the full shared log of any channel they are in.
- A pure **agent↔agent** channel with no human lead is **observe-only** for the human: they
  watch it, and to act they go talk to their agent.

This preserves the unification (a session is a channel whose lead is its single agent) and
matches the real loop: async result lands in a channel → human reads it → human reacts *to their
agent* → the agent does the cross-agent / cross-contact work. It does not affect Stage 1 (one
agent per channel ⇒ the lead is trivially that agent); it defines **addressing in Stage 2**.

## Decisions

1. **Delivery: shared log (LOCKED).** A channel owns ONE message log. Participants read the
   same log; a post appends exactly once. No per-subscriber fan-out copies (which would drift).
   Consequence: the log cannot live inside a single agent's storage bucket — it is owned by the
   channel, user-scoped, agent-agnostic.

2. **Reacting to a post — reuse existing notify semantics, per participant/subscription:**
   - `channel` — wake the agent: it runs a turn with the channel log as context, its reply
     appends to the same log (today's reasoning-summary behavior).
   - `direct` — the post just appears in the log, no LLM.
   - `text` — pushed to the owner's phone, no LLM.

3. **One primitive, migrated behind today's session storage.** Build toward Thread==Channel,
   but migrate lazily so nothing breaks. Existing session ids and `ReportSessionID` values keep
   pointing at the same threads (now Channels).

## Storage shape

New, user-scoped (NOT agent-keyed), so a channel's log is independent of any one participant.

```
channels                     key = channelID    -> Channel (metadata + participants)
channel_msgs:<channelID>     key = seq          -> ChatMessage          (Stage 2; Stage 1 inline)
channel_read:<channelID>     key = participant  -> last-read seq/time   (per-viewer unread)
```

```go
type Participant struct {
    Kind string // "human" | "agent"
    ID   string
}

type Channel struct {
    ID           string
    Title        string
    Owner        string        // user
    Participants []Participant
    Messages     []ChatMessage // inline in Stage 1 (mirrors ChatSession); split out in Stage 2
    Created      time.Time
    LastAt       time.Time
}
```

- **Unread** moves from "bump `LastAt`, list reads it" (global) to a per-participant read
  cursor in `channel_read:<channelID>`. Stage 1 has a single human owner, so one cursor; the
  shape already supports per-agent cursors for Stage 3.
- **Producers** keep their target on their own record: `StandingAgent.ReportSessionID`,
  event-monitor target, dispatch continuity — these become channel ids (Stage 1 keeps the
  field name; values are unchanged because session ids == channel ids after migration).

## Migration (lazy, verify-before-drain)

Mirror the phantom-memory migration pattern (load into new store, confirm, then leave the old
row; never blind-drain):

- `channel:<agentID>` (synthetic home thread) -> Channel `{Participants: {owner, agentID}}`,
  **same id string** so `channelSessionID` fallbacks keep resolving.
- Each `orchestrate_sessions:<agentID>` row -> Channel `{Participants: {owner, agentID}}`, id =
  the existing session id (UUID or `channel:<agent>`).
- `ReportSessionID` / monitor targets need no rewrite — the ids they hold are now channel ids.

## Stage 1 — the cut that lays the model (no storage move, no shared membership yet)

**Storage stays put.** A channel's log only needs to leave the per-agent bucket once a channel
has more than one agent (shared membership), which is Stage 2. For Stage 1 every channel has
exactly one agent, so today's `orchestrate_sessions:<agentID>` keyed by channel id is correct
and carries ZERO migration risk. The user-scoped `channels` table moves to Stage 2, where
multi-agent actually forces it. (Lesson from the phantom-memory migration: do not relocate live
conversation data until something requires it.)

Stage 1 is the **model + UX unification** on the existing storage:

1. **Model groundwork (safe, additive):** add `Participants` / lead-agent to the session record,
   populated from the existing single agent id. No behavior change; sets up Stage 2 addressing.
2. **One list:** stop filtering the per-agent home thread out of the session rail; pin it to the
   top as the default thread. Everything the user has is one list of threads.
3. **Retire the Channel/History nav split:** the home thread is just the pinned row; "Scrub a bad
   turn" becomes a per-message delete in the open thread (replaces the History nav row-delete).
   The fleet-management nav items (Enabled agents / Event monitors / Authorizations / grants /
   Clear / Decommission) stay — they are not part of the split.
4. Producers (standing agents / monitors / dispatch) already target a session id; unchanged.

Exit criterion: every conversation shows in one list with the home thread pinned; the separate
Channel/History nav is gone and turn-scrub still works per-message.

Sequencing note: step 1 is pure groundwork and lands first. Steps 2-3 touch the (intricate)
runtime session-list + alt-nav code, so they land only after the channel-nav ReferenceError fix
is confirmed working on a redeploy — building UI changes on verified ground, not on sand.

## Stage 2 — turn on sharing

- Multiple named channels; explicit create + subscribe.
- `post_to_channel` tool; per-subscription notify (`channel`/`direct`/`text`).
- Split messages to `channel_msgs:<channelID>` for long shared logs; per-participant read
  cursors drive each viewer's unread.
- This is the natural `target` for the unified trigger engine (a trigger's action posts to a
  channel).

## Stage 3 — agent ↔ agent over channels

- Agents as first-class participants that post and get woken; the fleet/Operator coordination
  substrate. Multi-participant `{owner, agentA, agentB}` threads with per-agent read cursors and
  react/observe modes.

## Open questions

- Origin-less wakes (phantom push with no source thread): land in a per-agent **Inbox** channel
  (a normal pinned channel, not a magic thread) vs the most-recent channel. Leaning Inbox.
- Whether `Channel` (agent-level capability flag) fully dissolves into "this thread has
  producers attached," leaving only **Fleet** (tool access) at the agent level. Likely yes.
