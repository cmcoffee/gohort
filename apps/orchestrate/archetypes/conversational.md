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
