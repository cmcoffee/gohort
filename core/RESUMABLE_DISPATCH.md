# Resumable dispatch — design

Status: design (not yet built). Author note: distilled from a working session;
the goal is that a dispatched sub-agent can **ask its caller a question mid-task
and resume where it left off**, instead of running to completion or failing.

## The problem

Today dispatch is synchronous one-shot: a caller runs a sub-agent
(`OrchestrateApp.RunAgentSync`), the sub-agent runs to completion, and a final
string comes back. There is no "pause partway and ask the caller a question."

That forces a bad choice on any sub-agent that hits genuine ambiguity: guess, or
fail. The motivating case is authoring — Chat delegates "build a tool that reads
the wiwee chat" to Builder; if the brief is under-specified Builder can only
guess. The general case is every sub-agent in the fleet: it should be able to
ask rather than guess.

We do NOT want a "headless vs. interactive" split. We want **one delegation
model that is conversational only when it needs to be**: runs straight through on
a complete brief, pulls a human in (through whichever ancestor is user-facing)
when it genuinely can't decide.

## The key realization

Resumption here is **message-history-based, not call-stack-based.** A suspended
sub-agent is not a frozen goroutine to freeze and thaw. It is:

- a lifecycle record marked `idle`, and
- its normal `ChatSession` (full message history, including the question it just
  asked).

"Resume" = run a **brand-new turn** over that persisted history with the answer
appended as the latest user message. The sub-agent continues by re-reading its
own context, not by resuming a stack frame. It cannot tell the difference — and
neither can a turn that asked three questions across an hour.

This is what makes the whole thing tractable: nothing exotic persists, and there
is no parked goroutine to keep alive between question and answer.

## The primitive already exists

`core/sub_session.go` defines the `autonomous` sub-session kind. Its own doc
describes exactly this shape:

> a long-lived task that drives its OWN runner when a message arrives … spends
> most of its life idle waiting on an external party … exempt from the
> promotion-window / turn-cap auto-retirement.

Lifecycle: `active → idle → (re-driven) → active → … → retired`. The record
carries only lifecycle metadata; the message history stays in the per-app
`ChatSession`. That separation is the foundation.

The **proven instance** of the full loop is the phantom *goal conversation*
(`apps/phantom/tool_goal_conversation.go`): an autonomous sub-session that sends
an opener, goes idle, is re-driven by `RouteGoal → runGoalTurn` when the contact
replies, and retires via `finishGoalConversation`. It is a resumable dispatch
where the party it waits on is a phone contact. Swap "contact" for "the user, via
the channel agent" and it is the same machine.

### What maps to what

| Need | Already there |
|---|---|
| A suspended task record | `SubSession{Kind: autonomous, Status: idle}` (core/sub_session.go) |
| Resume a sub-agent with a new message | `RunAgentSyncContinuing(…, subSessionID, message, …)` (apps/orchestrate/agent_dispatch.go) |
| Re-drive on an inbound message | `RouteGoal → runGoalTurn` (apps/phantom/tool_goal_conversation.go) |
| Deliver a wake/question into a thread | the channel waker (apps/orchestrate/operator_wake.go) |
| Distinguish "parked, will re-drive" from "worker died" | `SubSessionLivenessChecker` + `RetireOrphanedActiveSubSessions` |

## The three moments

**Where it suspends.** The sub-agent calls an `ask_caller` tool. Instead of
blocking, that ends its turn: the dispatch run returns a **SUSPENDED** result
carrying `{question, subSessionID}`, and flips the record `active → idle`. No
goroutine is left parked — an idle sub-session is free.

**What persists.** Two things, both already durable: the `SubSession` record (now
`idle`) and the sub-agent's `ChatSession` (its full history, including the
question). No stack capture, no serialized continuation.

**How resume re-enters.** The answer routes to the idle sub-session and calls
`RunAgentSyncContinuing(subSessionID, answer)`, which runs a fresh turn over the
persisted history with the answer as the new user message.

## Propagation up the chain

A SUSPENDED result bubbles up. Each caller that receives "child needs input"
either answers it itself or — if it is user-facing — surfaces it. Chat is
user-facing, so it renders the question in the **channel** and routes the reply
back down by sub-session id. Depth-N works because every level just forwards a
SUSPENDED with the id attached. `Builder ↔ Chat ↔ user` is the 2-level case.

## What is missing (the build)

1. **An `ask_caller` tool + a SUSPENDED dispatch-result type.** Today a dispatch
   result is only "done" or "errored"; add a third state that carries the
   question and the sub-session id.
2. **Async dispatch in orchestrate.** Per `sub_session.go`'s header,
   orchestrate's `agents(run)` is *sync*; phantom already has the async path.
   This is mostly a **lift**: extract phantom's goal-conversation runner into a
   shared core/orchestrate "resumable dispatch," parameterized on *who it waits
   on* (a contact vs. the channel/user).
3. **An answer-router in orchestrate** — the `RouteGoal` analog: an inbound
   message finds the right idle autonomous sub-session and re-drives it.
4. **Host handling in the channel agent** — render a child's question in the
   channel, and route the user's reply to the waiting sub-session instead of
   treating it as a normal chat turn.

## Sharp edges

- **Liveness.** An idle sub-session has no worker — that is the *normal* state,
  not an error. The liveness-checker + orphan-retirement machinery already
  exists to tell "parked, will re-drive" from "goroutine died."
- **Runaway asking.** `SubSession.TurnCount` already caps follow-ups; reuse it so
  a child cannot ask forever.
- **Compaction.** The channel's running summary must not bury an open question —
  a pending-question marker must be pinned (same class as monitor-wake
  de-pollution in `apps/orchestrate/operator_compaction.go`).
- **Multiple parked children.** The id-on-the-question handles routing; the
  channel UI just needs to show which question belongs to which child.

## Sequencing

The foundation is NOT "Chat → Builder authoring." It is **lift the
goal-conversation loop out of phantom into a generic resumable dispatch.** That
one move yields: Builder-authoring-with-questions, the watcher/tool-approval
flow, and every sub-agent gaining "ask instead of guess." Builder is just the
first rider.

Step 1: extract `runGoalTurn` / `RouteGoal` / the IDLE-mint into a shared
resumable-dispatch primitive in core (+ an orchestrate host adapter), keeping
phantom's goal conversations working on top of it unchanged. Then add the
`ask_caller` tool and the channel host handling.

## Related work in the tree

- `core/sub_session.go` — the autonomous sub-session lifecycle index (the spine).
- `apps/phantom/tool_goal_conversation.go` — the proven resume loop to lift.
- `apps/orchestrate/agent_dispatch.go` — `RunAgentSync` (sync) and
  `RunAgentSyncContinuing` (the resume entry point).
- `apps/orchestrate/operator_wake.go` — the channel waker (question delivery).
- `apps/orchestrate/operator_compaction.go` — where the pinned-question marker
  fix lands.
