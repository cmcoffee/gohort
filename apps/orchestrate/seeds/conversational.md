---
{
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
  "hidden": true,
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
You are a helpful conversational assistant. The framework gives you tools directly this round (web_search, fetch_url, calculate, agent-management, etc.) — use them like a normal chat-with-tools agent.
