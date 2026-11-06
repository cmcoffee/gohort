# External access: who can reach this deployment from outside

Status: in discussion (2026-11-05). Built: C (Feature Access takes teams, Everyone is explicit, an emptied list is nobody, a feature can be Closed) and B (Model access, closed, with per-user requests-a-minute and at-once limits; the daily token or spend cap is still to come).

## Why

Gohort has grown many doors a caller outside the web UI can come through, and
each grew its own gate. Some are the administrator's to open, some are any
user's, and one (serving people outside gohort) had no administrator in it at
all. A key that lets a stranger in, or that hands out raw compute, is a
deployment decision, not a personal one.

## The doors today (verified in code)

| Door | Reaches | Gate today | Gap |
|---|---|---|---|
| Model API `/v1`, agent or channel | an agent: its tools, memory, documents | admin grants the Model API per user; the user mints and scopes keys | a key that ticks the user's own agent reaches it without "Reachable over MCP" approval |
| Outside-audience keys (`/v1`) | an agent, for strangers, each in their own store | the agent's switch, a plain save | no administrator involved |
| Model API `/v1`, `worker`/`lead` | raw models | same grant as above (`openai`) | raw compute, no cap |
| Ollama proxy | raw worker model | admin turns it on and binds it (loopback by default); off-machine needs a key with the `openai` grant | raw compute, no cap; a loopback request needs no key |
| MCP | the user's agents (`ask_agent`) | admin grants MCP per user; key scope | the same self-grant bypass as `/v1` |
| Desktop bridge | lends the user's computer to their agents | admin grants it per user | none |
| Teams bots | an agent, for a tenant's people | governance: a new bot waits for approval | an approved bot's agent can be switched to outside without approval |
| Channels answering "anyone" | an agent running as its owner | whoever can use Bridges | no administrator policy |
| Personal inbound webhooks | wake an agent | whoever can use the app | no administrator policy |
| Published agents (`/agents/`) | an agent, for every signed-in user | administrator approval | none |

Feature Access itself has two faults: it takes users only, not teams, and an
empty list means everyone, so removing the last person opens a feature to the
whole deployment.

## The rule

**What a key reaches decides how it is opened, not which protocol it speaks.**

| Reaches | Opened by |
|---|---|
| An agent or a channel, for anyone but its owner's own client | the owner requests; an administrator approves and **issues** a key bound to that one target |
| Raw models (Ollama proxy, `/v1` tiers) | an administrator adds the person or team to **Model access**; closed by default; per-user limits |
| The owner's own client to their own agents (MCP, desktop bridge) | a personal key, under the existing per-user grants |

## Proposal

### A. Published doors and issued keys (`/v1`)

- The Model API stops being a personal key feature. Every `/v1` key that
  reaches an agent or a channel is issued by an administrator and bound to
  exactly one target.
- The owner asks from the agent's (or channel's) page: "Connect a voice line"
  or "Publish to a chat platform". The request shows what the agent lends
  (tools, credentials) and its limits.
- The administrator approves and issues the key in the admin UI. Each issued
  key has:
  - its **target**: one agent, or one channel;
  - **who calls**: *just the owner* (turns run as the owner, as a voice line
    does today) or *people outside gohort* (each person in their own store,
    docs/design/outside-audience.md);
  - a **platform name**, an optional **allowed IP list**, and the limits.
- The key is recorded under the target's owner (their spend, their limits);
  only an administrator creates or revokes it. The owner sees that it exists
  and its use, never its secret.
- The administrator sees each issued key's last use and the people who came
  through it, and revokes it.
- Gone: the "For people outside gohort" mark on a personal key, and the
  self-grant that let a personal key reach an agent without approval.
- Teams fits the same shape: the bot is an approved door already, and its
  agent serving people outside needs the same approval.

### B. Model access (raw compute)

- A new grant, **Model access**, covers the Ollama proxy and `/v1`
  `worker`/`lead`. It no longer rides the Model API grant.
- **Closed by default**: nobody until an administrator adds people or teams.
  Administrators always have it.
- Someone not granted does not see it: their key editor does not offer it.
- **Per-user limits**: requests at once, requests per minute, and a daily
  token or spend cap; administrator defaults, overridable per person. Over a
  cap, a 429 naming the limit.
- The admin page shows each user's model-access use.

### C. Feature Access takes teams, and "Everyone" is explicit

- Every feature's list takes people and teams; a team covers its members, as
  App access does.
- "Everyone" is an entry, shown and removable. An empty list means nobody.
- A feature whose policy was never saved keeps today's meaning (everyone),
  marked by one additive field on the policy record; once saved, empty is
  nobody. Model access is new and starts closed.

### D. Governance for the rest

New entries beside API credentials, MCP servers and Teams bots, each Allowed /
Needs approval / Admin only, governing what is turned on from now:

- **Serving people outside gohort** (an agent's switch): Needs approval.
- **Channels that answer anyone**: Allowed by default (unchanged until an
  administrator tightens it).
- **Personal inbound webhooks**: Allowed by default.

### E. One External access page

For administrators: every live door in one table, each with an off switch.
Issued keys (owner, target, who calls, last use), personal keys with what
they reach, agents reachable from outside or serving people outside, Teams
bots, channels answering anyone, webhooks, Model access grants and use. And a
deployment-wide switch that stops all outside audiences at once.

## Changeover (no migration)

- Personal keys can no longer tick the Model API for an agent or channel.
  Existing ones keep working until they expire (the administrator's key
  lifetime), listed on the External access page as "personal: issue a
  replacement".
- Model access starts closed: anyone but an administrator who uses the Ollama
  proxy or raw `/v1` today loses it until added. Intended, and named here
  because it is a change in behaviour.
- Feature Access lists already saved keep their meaning; "empty means nobody"
  applies from the next save.

## Order of work

1. Feature Access: teams and an explicit Everyone (C). Everything below uses it.
2. Model access, closed, with limits (B).
3. Issued keys and the publish request (A); retire personal Model API keys.
4. Governance entries (D) and the External access page (E).

## Decisions (owner, 2026-11-05)

1. Outside keys are **issued by an administrator** for a published agent or
   channel, not minted by users. Voice lines go the same way.
2. The Ollama proxy and raw model tiers are **Model access**: closed by
   default, granted to people **and teams**.
3. **Administrators always have** Model access and the Model API, and grant
   them; a list with nobody on it means administrators only. The **Model API
   (`/v1`) is Closed too**, until issued keys (A) replace personal ones.

## Open

1. A loopback request to the Ollama proxy needs no key today. Keep that for
   administrators (there is no user to attribute local traffic to), or require
   a key even from the same machine?
2. Default per-user Model access limits.
3. Whether a change to an approved agent's lent tools or credentials is
   requested again.
