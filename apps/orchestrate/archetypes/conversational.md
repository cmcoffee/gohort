---
{
  "summary": "A helpful chat-with-tools agent: replies directly for casual turns, plans and uses tools when the turn needs them, remembers user preferences, and can optionally conduct a fleet (schedule other agents, run monitors, delegate).",
  "aliases": [
    "chat",
    "assistant",
    "general",
    "conversation"
  ],
  "match": [
    "general assistant",
    "personal assistant",
    "chat agent",
    "everyday helper",
    "assistant that can also do things",
    "someone to talk to",
    "day to day help",
    "general purpose assistant",
    "day to day assistant",
    "talk to about"
  ],
  "asks": [
    "What should it help you with most days?",
    "How should it talk to you?"
  ],
  "record": {
    "id": "seed-chat",
    "name": "Chat",
    "description": "Default conversational agent. Replies directly for casual turns, plans + uses tools when needed, and can manage your other agents on request.",
    "channel": true,
    "fleet": true,
    "pre_mortem": true,
    "max_plan_steps": 6,
    "max_worker_rounds": 18,
    "memory_mode": "chatbot",
    "allow_private_mode": true,
    "hidden": true
  },
  "notes": {
    "channel_and_fleet": "Chat is the primary channel agent, the Operator folded into it. channel (Cortex) gives it a persistent home thread where monitor wakes and standing-agent reports land, alongside its ordinary sessions, plus the management sidebar. fleet grants the delegation, standing-agent and event-monitor toolset. The two are independent; both are on here.",
    "pre_mortem": "Chat is the orchestrator, so it plans and executes real goals. Plan-first plus pre-mortem discipline makes it lay out a plan, flag the risks, and await deferred-feedback steps (a reply, a call, a job) instead of blocking or faking them. It self-scopes to goals, so ordinary chat is unaffected.",
    "allowed_tools": "Left empty on purpose: the runner reads empty as \"use the default pool\" (every non-blocked chat tool with a Read or Network capability, plus the unannotated agent-CRUD tools). That matches the standalone Chat app's everything-available surface, so Chat-in-orchestrate feels equivalent to Chat-the-app.",
    "max_plan_steps": "Headroom for multi-tool agent authoring: a pipeline plus an agent that uses it is 2 steps, \"agent with 3 custom tools\" is 4, and a final orchestrator verification step pushes it up. 6 covers the common authoring patterns; truly large designs still get the user-visible build plan card alongside, which is the cleaner surface for breadth.",
    "max_worker_rounds": "Higher than the framework default of 5, because Chat-style turns iterate inline (the orchestrator calls tools across rounds instead of via plan_set), so \"compare these three products\" easily wants 6 to 10 rounds before the final reply. 18 covers the common case and leaves room for agent-creation flows that need research, design and execution in one turn without squeezing out the create_agent call.",
    "allow_explorer": "Off. The original use case, heavy authoring flows, moved to Builder, and 18 rounds covers normal multi-tool conversational work with headroom. Power-user agents (research, investigation) can opt in; Chat does not need it.",
    "hidden": "Seeds default to hidden so they do not surface in other agents' fleet dispatch lists. They are user-facing entry points, run directly from the Agency picker, not workhorses to be chained into other agents' workflows. A user can flip this per non-Builder seed if they actually want fleet dispatch.",
    "allow_private_mode": "Surfaces the per-turn Private toggle. Chat is the general-purpose conversational agent, and sometimes the user wants a network-only-when-they-say-so answer (personal notes, local-doc Q and A, offline-friendly turns). Opting in here means the toggle is visible without an admin flipping it on every install; users who never use it just leave it off.",
    "memory_mode": "Chat is the canonical chatbot-mode agent, where Explicit Memory is the broader catch-all: user preferences, conversation-coherence notes and generalized lessons are all welcome."
  }
}
---
# Archetype: Conversational / general-purpose agent

A helpful chat-with-tools agent: replies directly for casual turns, plans and
uses tools when the turn needs them, remembers user preferences, and can
optionally conduct a fleet (schedule other agents, run monitors, delegate).

Build this when the user asks for "a general assistant", "a chat agent", "an
everyday helper", or "a personal assistant that can also do things." This is the
default shape when no more specific archetype fits.

## Composition (create_agent)

- **allowed_tools**: leave EMPTY to grant the default pool (every non-blocked
  Read/Network chat tool plus the agent-management tools), which is what makes it
  feel like a full assistant. Only set an allowlist if the user wants it narrowed
  to a purpose.
- **memory_mode**: `"chatbot"` — the broad catch-all where user prefs,
  conversation-coherence notes, and generalized lessons all belong. Explicit
  memory ON.
- **max_worker_rounds** ~18 — inline multi-tool turns ("compare these three
  products") iterate across rounds before producing the reply.
- **max_plan_steps** ~6 — covers common multi-step work; large designs still get
  the visible build-plan card.
- **allow_private_mode** ON — surface the per-turn Private toggle for
  network-only-when-asked turns (personal notes, local-doc Q&A).
- **rules vs. persona** — `rules` renders ABOVE memory and above the persona and
  is framed as non-negotiable: when anything in the prompt conflicts with a rule,
  the rule wins. So a constraint that must hold on every turn whatever else is
  going on belongs there, not buried in a paragraph of voice. For a general
  assistant that is usually about reach and disclosure — "never send a message on
  my behalf without showing me the text first". Voice, approach and when to reach
  for what stay in the persona, where the model can weigh them against the turn.

### The conductor variant

If the user wants it to also MANAGE other agents — schedule them, wake them on
events, delegate — turn on:
- **cortex** (`channel`): a persistent home thread where monitor wakes and
  standing-agent reports land, plus the management sidebar.
- **fleet**: the conductor toolset (the `delegate` tool, standing-agent
  scheduling, event-monitors, run-ledger, history-recall).
- **pre_mortem** ON for goal-driven turns: lay out a plan, flag risks, await
  deferred-feedback steps instead of blocking or faking them.

These two are independent of each other and of ordinary dispatch — an agent
calls peers via `agents(action="run")` governed by its Dispatch policy whether or
not the conductor tools are on. Set Dispatch policy to "Allow none" to fully
ground a conversational agent that should never delegate.

## Orchestrator prompt — the shape

Short and open: "You are a helpful conversational assistant. The framework gives
you tools directly this round (web_search, fetch_url, calculate,
agent-management, etc.) — use them like a normal chat-with-tools agent." The
value of this archetype is breadth, so the prompt stays light and the tool pool
does the work. Add persona/domain flavor per the user's ask, but keep the
tool-use posture permissive.

## What to tell the user

"I built your assistant — chat with it at /chat/<name>. It answers directly,
reaches for tools when a question needs them, and remembers what you tell it."
For the conductor variant: "…and it can schedule and delegate to your other
agents when you ask."

## Persona

You are a helpful conversational assistant. The framework gives you tools directly this round (web_search, fetch_url, calculate, agent-management, etc.) — use them like a normal chat-with-tools agent.
