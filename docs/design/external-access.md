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

## An app as its own MCP server (2026-11-19)

An app that offers something beyond its page (actions, data sources or its
pipeline marked expose) is an MCP server of its own at `/mcp/app/<slug>/`.
The handshake names the server after the app and gives its description as
instructions, to a key that reaches it; `tools/list` holds that app's tools
alone (no agents, no built-ins, no other app); a call runs as the owner's own
click would. Owner decisions:

1. A per-app address, not only keys narrowed on `/mcp/`: a client lists
   servers by name, and the app's own name and instructions are what make it
   read as the app.
2. Keys: an MCP key may hold `apptools:<slug>` targets (core.AppToolsTarget;
   not `app:`, which names an app's agents). A key naming apps reaches those
   apps and no other; one naming apps and no agent reaches nothing else, no
   agent and no built-in tool; one naming no app reaches every app its owner
   has, as before. Made from the app's Settings ("Use from an MCP client":
   address, what it offers and leaves out, Make a key, the keys that name it,
   Revoke) or ticked under "Your apps" on the Account page's MCP key. Stored
   in the key's existing targets: no new field, no migration.
3. No outside door to the app's agent: asking the app in words is left out.
4. Actions that ask to confirm first: at the app's own address only, MCP
   elicitation carries the app's question (apps/mcpserver/app_ask.go). A
   client that says it can ask, on a key whose owner set "Ask in the client"
   (off by default; customapps stores it by key id, no key field), gets a
   session, and only a session lists them. Each call asks before it runs;
   accept runs it, anything else (decline, cancel, error, 5 minutes) does
   not. "Don't ask again" keeps a yes for that key and action alone, not a
   gohort grant; turning asking off or revoking forgets it. Sessions are in
   memory: after a restart a client is one that cannot be asked. The general
   /mcp/ address still leaves them out.
5. Owner only at first. Then (same day) the people an app is shared with,
   behind the owner's switch: AppSpec.OfferMCPToShared, "Offer to people
   it's shared with", off by default, deployment-local (cleared on export
   and import, and not a change to what the app serves). Each person finds
   the section on their own Settings and makes a key of their own, pinned
   "apptools:<owner>/<slug>" so it never comes to mean an app of their own
   at the same slug; a call runs as their click would, on their own records,
   checked at the moment (the switch, the share, their access to the app,
   MCP open to them). The address finds their own app first, then the shared
   one. The owner sees whose keys name the app and when each was last used,
   not what they did. A shared app's tools are not offered to the person's
   agents. No call limits: those belong with per-user API cost caps.
   AppToolsTarget lives in apps/customapps (the account page reads the apps
   host's list), so core's export ceiling is back to 2447.

## An app's calls as jobs, and the app over plain HTTP (2026-11-26)

Found in a durability pass on a throwaway instance: a call of an app's tool
over MCP ran inside its request, so a client that gave up waiting (Claude
Code waits 60 seconds for a first byte) or dropped its connection stopped the
run it had asked for. Owner decisions:

1. **Every app call over MCP is a job** (apps/mcpserver/app_jobs.go), on the
   app's own address, the general one, and a confirm-first action once
   answered. It runs on a context of its own; the request waits 20 seconds.
   A call that ends in time answers as before. One that does not answers
   with its job, its time so far and its latest output line.
2. **`<slug>_result(job, stop)`** collects a job (waiting 20 seconds each
   call) or, with `stop: true`, stops it. With no job it lists the key's
   calls of the app from the last hour, for a client that went away before
   it was told the job. It is named `<slug>_app_result` if the app has a tool
   called `<slug>_result`. A key may call it whenever it may call any of the
   app's tools, as `agent_result` follows `ask_agent`.
3. As `ask_agent`'s jobs: a job belongs to the key that started it, its
   answer is kept for an hour, it is in memory and a restart drops it. No
   stored field, no migration.
4. **A key has at most 8 calls running at once**; the next is refused with
   the running jobs named.
5. **The same app over plain HTTP, below the same address and with the same
   keys** (apps/mcpserver/app_http.go): what a key reaches decides how it is
   opened, not which protocol it speaks.
   - `GET tools`: the tools with their input schemas.
   - `POST tools/<name>`: the body is the arguments, a JSON object; waits up
     to 20 seconds (`?wait=0..20`). 200 with the result (the tool's own JSON
     as JSON), or 202 with the job (`Location` is its URL).
   - `GET jobs`, `GET jobs/<id>?wait=0..20`, `DELETE jobs/<id>` (stop).
   - 422 a tool that ran and failed, 409 a stopped job or an action that asks
     to confirm first (nobody is asked over plain HTTP), 429 over the cap,
     401/403/404 as the key's reach. The key goes in `X-API-Key`; a sign-in
     cookie alone is refused, and there is no CORS: it is for scripts and
     servers, not another site's page.
   - The app's Settings shows a curl example beside the MCP config when a key
     is made.
6. A stopped action says it was stopped, on its page as over MCP and HTTP: a
   script killed part way through used to read as one that printed no JSON.
