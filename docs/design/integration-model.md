# Integrations: connectors, webhooks, channels and one key store

**Status:** design, for the owner's review. Nothing here is built. Written
2026-11-01 from a conversation with the owner; the owner's answers to its
questions are under Decisions. It follows
`connectors-and-flows.md` (one way to think about every integration),
narrows what a connector is, and plans a rework that reaches this design
without migrations. `channels-and-agents.md` is where the word channel comes
from.

## Goal

**Adapt to tomorrow's requirements without a Go change.** That was the point
of connectors from the start, and every decision below is checked against it.

"Tomorrow's requirement" is not one thing. Each kind has its own place where
it is met without Go, or a reason it cannot be:

| Tomorrow's requirement | Without Go? | Where |
|---|---|---|
| A provider changes: a field renamed, a new endpoint, a different auth header | yes | edit its connector's spec, Test |
| A new provider for a job gohort already does: another search, image or speech API | yes | a connector, from a template or written |
| A new OpenAI- or Anthropic-compatible LLM | yes | a built-in compatible-endpoint provider: a form |
| A new thing for agents to do: call a CRM, look up stock | yes | an API tool or a saved flow |
| A new service pushing events in | yes, once webhooks exist | a webhook |
| A new job gohort itself runs, or a new protocol (streaming, graph surgery) | no | Go, kept small: a `builtin` behind a configurable connector |

Each place is small, pure data, and something the Builder can draft for the
owner to approve. That last part is the strongest lever: a new requirement
becomes a draft an agent writes, not code anyone writes.

The missteps so far came from asking one concept, the connector, to absorb
every row of that table, including the last one: built-in providers were
turned into specs, and kinds that are not REST calls were called connectors.

## Why

Connectors grew by adding kinds. There are eleven now: `service`,
`rest_image`, `remote_mcp`, `desktop_mcp`, `desktop_command`, `rest_poll`,
`messaging_bridge`, `rest_messaging`, `bot_framework`, `peer_relay` and
`file_conversion` (System Hooks' adapted flows). Together they made the
Connectors page hard to read:

1. **Connector and connection overlap.** The page says connector, a row's
   settings say connection, and only one kind (`service`) has a connection's
   own key and settings. Nothing on screen says which a row is.
2. **The kinds overlap.** `service` and `rest_image` both make pictures. Three
   kinds do messaging, two kinds run things on a desktop.
3. **"In use" means two things.** A `service` is in use while a page names
   it. Every other kind is switched on with Approve. Both are stored in the
   same `Approved` field.
4. **Keys live in two places.** A `service` keeps its key itself, bound to
   its address, shown as saved, self-checked, limited to users and agents.
   The other kinds name a vault credential, where none of that applies.
5. **Five ways to edit a row:** Settings, Configure, Edit spec, Fix, Revert.
   Which one applies depends on how the row was made, and the row does not
   say.
6. **Two template systems:** the JSON recipes (`core/recipes/builtin`) and
   the Go setup forms (`ConnectorTemplate`: ComfyUI, A1111). ComfyUI is added
   by one and configured by the other.
7. **Provided connections are a third state.** The LLMs and local servers
   gohort installs are locked connections with Revert, Changed and
   Outdated/Successor. That machinery exists so a Go client can wear a
   spec.
8. **A connection's settings show in two places:** under the choice on the
   page that uses it, and under Settings on its row.

And the way there cost more than the result: plan B (2026-10-30, a6d845ac
onward) turned every built-in provider into a connector, with a forced
migration and the Go provider deleted in the same change. Decisions made
while building were undone with more migrations (the `service:` prefix, then
per-job names; provider name case; the two image kinds). Since plan B, 22
commits changed 332 files and added six startup migrations.

## The model

Four concepts, each with one job and clear edges.

| Concept | Direction | What it is |
|---|---|---|
| Connector | out | the configuration of REST calls to one service, for one gohort job |
| Webhook | in | an address gohort exposes, a check of who is calling, how the payload reads, and what it feeds |
| Channel | both | a conversation surface attached to an agent; its transport is a connector, a webhook, or both |
| Key store | none | addresses, keys and who may use them, kept the same way for everything that has a key |

Some things are none of these, and stay built in: LLM providers, MCP
servers, programs on a desktop, peers, and System Hooks' programs. They use
the key store for their keys and keep their own pages.

### Connector

**Definition (the owner's, 2026-11-01): a connector is the basic
configuration of REST calls.** Concretely, for one outside service:

- its address and its key (held in the key store);
- the requests it makes (operations);
- how each reply is read (outputs);
- which gohort job it fills (the contract: `web.search`, `image.generate`,
  `message.send`, ...).

That is today's `service` kind. The spec engine's home ground is one shape:
send a request, maybe wait on a job, read fields out of the reply into a
contract.

**What a connector is not:**

- a protocol gohort speaks in Go (chat streaming, MCP);
- a schedule (a watch);
- a conversation (a channel);
- an inbound address (a webhook);
- a program on a machine.

**The escape hatch.** Some services are REST but need Go to shape their
calls: PubMed, OpenAlex, EDGAR (built-in searches), and ComfyUI (its graph
wiring). Such a service stays a connector, because its address, key and
contract are connector things. Its operation names a Go builtin
(`"builtin": "comfyui"`). The rule that keeps the engine from becoming a
second programming language:

- **one provider needs it:** write a builtin in Go;
- **several providers share the shape:** extend the engine.

**References, not "in use"** (decided 2026-11-01). A connector has no on/off
state. What it has are references: the pages that name it (Web Search, Image
Generation and its list offered to agents, Research Sources, a flow's step,
...), each named as the list it is in. References do two things:

- **guard deletion:** a referenced connector cannot be deleted, and says what
  references it, so the reference is changed first;
- **show what depends on it:** its row reads "Referenced by Web Search,
  Research Sources", or "Not referenced", muted.

The call gate goes: today a connector no page names cannot be called, but a
call only ever reaches a connector through something referencing it, so the
gate adds little. With it go Approve and Unapprove, the `Approved` meaning for
connectors, the Settle machinery that sets it, and the in use badge. Before
the gate is removed, every path that calls a connector by name is checked
(the Builder's own test action, flows, agents' tools): each counts as a
reference or goes through the admin's Test preview, and one that does neither
is a gap to close.

**The Changed hold stays.** An edit the Builder or a non-admin makes waits
until an admin accepts it, by saving a page that references the connector.
That is a gate on edits, not on references, and its marker stays on the row.

**One editor per connector:**

- **Settings:** its values and its key. The page that chooses a connector
  shows this same form, not a second copy.
- **Edit how it runs:** the spec, or, for one made by a Go setup form (ComfyUI),
  that form.

Fix becomes how Edit opens on a spec that does not validate. Configure
becomes the Go form's Edit. Revert goes with provided connections.

### Key store

What `service` connections already have, made the rule for every key a
connector, webhook, channel or built-in provider holds:

- **bound to addresses:** a key is sent only to the addresses it was given
  for; moving the address means giving the key again;
- **who may use it:** per user, and per agent through the credential scope;
- **seen safely:** the saved-key line (`ends in …WXYZ, given <date>`), never
  the key;
- **checked:** the startup self-check, and on its row;
- **deleted with its owner:** deleting a connector, webhook or channel
  deletes its keys.

The vault (API Credentials) stays as it is in the rework. API tools
(`call_<cred>`), OAuth, credentials lent between users and the credential
audit all stand on it. Whether it and the key store become one is a later,
separate decision (Q3).

### Webhook

The mirror of a connector: a service calls gohort.

- **An address:** `/hook/<name>` on this instance. An instance behind NAT is
  reached through a peer's relay, so `peer_relay` becomes how a webhook is
  reached, not a kind.
- **A check of who is calling,** one of:
  - `none`;
  - `token`: a shared token in a named header;
  - `hmac`: a signature over the body, with a named header and algorithm;
  - a **named verifier** in Go for a provider's own scheme: `slack`
    (request signing), `graph` (Microsoft Graph's validation handshake),
    `botframework` (signed tokens).

  The first three are data. A named verifier follows the escape hatch rule.
  Its secret lives in the key store.
- **How the payload reads:** dot-paths into an event, the way a connector
  reads a reply: `id`, `chat`, `from`, `text`, `kind`.
- **What it feeds**, one of:
  - a channel (a message arrived);
  - a watch (wake an agent);
  - a flow (start it).

The pieces exist today, scattered across kinds:

- the row's Webhook… button;
- `rest_messaging`'s `webhook_provider` (`generic`, `graph`, `slack`);
- Bot Framework's verifier;
- `peer_relay`'s inbound address;
- the trigger and monitor plumbing.

### Channel

As in `channels-and-agents.md`: a messaging surface attached to an agent.
The channel layer keeps what it already owns: dedup, aliases, coalescing,
the gatekeeper and delivery formatting. This document adds only **where a
channel's transport comes from:**

- **a connector** that gives both `message.send` (exists) and a new
  `message.fetch` (messages since a cursor). The channel polls one and replies
  through the other. A polling service is then pure configuration: Slack,
  Discord, Teams through Graph, Mattermost or Matrix become templates that map
  two REST calls;
- **a webhook** for services that push (Slack events, Graph change
  notifications), with replies still sent through the connector;
- **a channel type of its own** for what is not REST:
  - Bot Framework (gohort as a Teams bot under its own identity);
  - the iMessage relay (a device relay).

`message.fetch` (proposed):

- **inputs:** `since` (the cursor, optional), `chat` (optional);
- **output:** `messages`, a list of `{id, chat, from, text, at, files}`, and
  the next `cursor`;
- **check:** the reply is a list, and every message has an `id` and a `chat`.

### Built in, not connectors

- **LLM providers.** A Go client streams, runs tools and handles thinking,
  so a spec adds nothing but a costume. They are built-in providers:
  - the named ones: Anthropic, OpenAI, Gemini, Bedrock, and the local
    servers Ollama, llama.cpp and vLLM;
  - **OpenAI-compatible endpoint**, as many as wanted, each with a name, an
    address, a key (in the key store), a model (its List reads the
    endpoint's `/models`) and optional prices. A new provider tomorrow
    (Groq, OpenRouter, a vLLM box) is a form, with no Go, spec or rebuild.
    The edges where "compatible" varies (the max-tokens field, where
    reasoning comes back, usage while streaming, tool-call details) are a
    few named switches on the endpoint, taken from what the client already
    special-cases;
  - **Anthropic-compatible endpoint**, the same for gateways that speak
    Anthropic's API (today the anthropic dialect is fixed to Anthropic's own
    host).

  Each tier picks one. The privacy check keeps judging a tier by its
  address (a cloud address is not private), failing closed, and cost rates
  stay per provider.
- **MCP servers** (remote and desktop): their own page.
- **Desktop programs** (`desktop_command`, and desktop MCP): a per-user
  Desktop page, since each person sets up what runs on their own machine.
- **Peers:** their own page. A peer's lent renderer becomes a connector.
- **System Hooks programs** (`file_conversion`): already their own page.

## DLP (later)

**Decided 2026-11-01: DLP is revisited after everything else** (see
Decisions). What follows are notes for then, not part of the plan. One rule
holds until then: the rework adds no new way for data to leave gohort outside
the control points below, so a DLP still has only these to hook.

The model makes data loss prevention a matter of a few control points, not a
scan of everything.

**The rule a DLP enforces:** sensitive data only flows to destinations
trusted for it.

**Whether connections carry a trust level is open (Q2).** The owner does not
want trust drawn from internal versus external: an address on the LAN can
forward to the cloud. Either a trust level is set by hand, with names that
are not about network location, or there is none and the check judges what
is sent rather than where it goes.

**One egress check interface, called at every control point.** The DLP
itself (pattern rules, a classifier, a vendor's engine) plugs in behind it
later:

```
EgressCheck(ctx, Egress{
    Destination string   // the connection's name
    Trust       string   // internal, vendor, public
    Contract    string   // web.search, llm.chat, message.send, ...
    Field       string   // the contract input it carries, when known: query, prompt, text
    Body        []byte   // what leaves
}) (Decision, error)     // allow, redact (with the new body), or block (with why)
```

Knowing the contract lets a policy be precise ("a `web.search` query must
not carry a customer's name"), not a scan of opaque bytes.

**The control points:**

| Path | Passes through | Status |
|---|---|---|
| Connector requests, `call_<cred>` tools, channel sends | the governed dispatch (`DispatchConnection`, `DispatchHostScoped`), which already does allow-lists, Private mode and the audit | one hook covers all |
| LLM prompts to cloud models | the Go clients' own requests | **gap, and the largest**: most sensitive data leaves here. Hook in the request path. The privacy check already knows local from cloud |
| fetch_url, the browser (URLs, form fills) | their own clients | gap: hook each |
| The sandbox's network | the per-run relay | gap: hook the relay |
| Remote MCP tool calls | the MCP client | gap: hook the client |
| Inbound (webhooks, tool results) | the webhook's one path; the existing tool-result scan | inbound counterpart, for injection and for logging |

**Where data leaks, and how:**

| Path | What can leak | How it typically happens |
|---|---|---|
| The lead model (and any cloud tier) | the most, by volume: the conversation, tool results, files, gathered facts | every escalation sends the context |
| web_search | little, but pointed: what is in the query | an agent searches a customer's name, an internal hostname, an error with an address in it; the query sits in the provider's logs |
| fetch_url, the browser | what is in a URL, or typed into a page's form | an identifier in a query string; the browser filling a form |
| Message sends (mail, Slack, Discord) | what the message carries | a report to the wrong channel, or one that says too much |
| Connector calls (transcription, image APIs) | the file or text processed | a recording or document sent to a cloud service |

**The first case is the lead model:** what is gohort willing to send to it?
For each escalation, a DLP would choose one of:

- send as is (today's behaviour);
- redact, then send: strip what a policy matches (keys and tokens, customer
  identifiers, facts gathered from the owner's systems), so the lead sees the
  problem without the specifics;
- do not escalate: keep a sensitive turn on the local worker, even if the
  answer is weaker.

**Search and fetch come next.** They carry less, but an agent chooses on its
own what goes into a query or a URL, to a third party.

**This leans Q2 toward a policy per destination, set by hand:** what am I
willing to send to this model, this search provider, this cloud API. It is
never inferred from where the destination sits on the network.

When DLP is taken up: the egress check interface and its hooks first (the
LLM request path, then search and fetch), the DLP itself behind it.

## The rework: reaching this design without migrations

**Approach.** History stays as it is (the owner declined rewriting it). On
one branch, the code is reshaped into what would have been built the first
time. Settings the new code does not recognise are not converted: they read
as not set, and the owner reconfigures them by hand once, after deploying.
No startup migration is added, and the six that exist are deleted.

### What is taken out

- Provided connections: `ProvideConnectors`, `MarkConnectorProvided`, spec
  hashes, Revert, Changed, `Outdated`/`Successor`.
- LLM tiers routed through connections (`dialect` bindings, `ModelKey` for
  connections, `media.ModelService`).
- The `service:` legacy reads (`legacyChoice`).
- Every startup migration since plan B: `connections:v2`, `connections:v3`,
  `source_hooks_to_connectors:v1`, `search_research_to_sources:v1`,
  `connectors_in_use:v1` (its `Settle` call stays), `images:v1`.
- `--migrate-dry-run`, which existed for those migrations, and `core/bbolt`,
  which existed for the dry run.
- The `rest_image` kind, once A1111 and peers' renderers are connectors.
- The Contracts page (Admin > Extensions > Contracts) and `/api/contracts`
  (decided 2026-11-01). Contracts stay as the idea under connectors, and
  their text stays where it is read: the connector editor shows what the job
  it fills takes and gives, Fix and Add tell the assistant, and the table in
  `docs/integrations.md` stays.

### What is brought back or built

- **LLM providers as built-ins** (above), with a plain form per tier. Pre-plan-B
  code is the reference: `git show a6d845ac^:<path>`.
- **A1111 and peers' renderers as connectors**, A1111's img2img through a
  builtin if the engine cannot express its inline pictures.

### What is kept

- Connectors for REST jobs: search (the 13 templates), images (OpenAI,
  Gemini, ComfyUI as a builtin), mail, transcription, speech, embeddings,
  conversions, message sends.
- Templates as data, contracts, Fix and Test without a rebuild.
- Tie-ins, as references: which pages name a connector (see References, not
  "in use").
- The key rules: address-bound keys, per-user and per-agent scope, the
  saved-key line, the self-check (including other kinds).
- Settings travelling with exports; display names.
- Everything since plan B that is not connector work: Wiwee's background
  awareness (a060d238), the transcription WAV fix (25b65e68).

### Rules the branch must follow

1. **The embeddings space is not lost.** If the embeddings setting no longer
   resolves to the same vector space, gohort re-embeds every collection, slow
   and paid. The embeddings record keeps its shape and its space identity: it
   is the one setting not reconfigured by hand. This is checked against a copy
   of the live database before deploying.
2. **Stale settings fail visibly.** A value the new code does not recognise
   (`llm:anthropic`, `search:serper-search`) reads as not set, with a notice
   on its page. It never falls back to another provider. For the LLM tiers
   that means gohort runs without a model until reconfigured, so the deploy
   and the reconfiguring happen in one sitting.
3. **Names agents use do not change:** granted tools (`generate_image_<name>`,
   search tools, `flow_<name>`), `query_source` sources, and credential
   scopes (`connection:<name>`). Anything an agent names that no longer exists
   is listed in the report.
4. **Every security property since plan B survives,** as acceptance tests the
   branch passes before merging:
   - a key goes only to its addresses;
   - agent and user scope on keys, at the call and in tool lists;
   - the privacy check judges a tier by its address, failing closed;
   - the self-check.
5. **The vault is not touched** (see Key store).
6. **The peer protocol between instances does not change.** Instances that
   lend to each other upgrade at different times.

### Coordination

- **The private repo moves in step.** Research, debate, blogger, servitor and
  the other private apps call `service.Call`, the choice values and model
  resolution. Its branch is reviewed and merged with this one.
- **`main` keeps moving.** Other sessions commit while this branch is open.
  The branch is rebased often, and connector-area work on `main` pauses
  until it merges.
- **One deploy.** The rework lands as one merge and one deploy. Intermediate
  states would make the owner reconfigure twice.

### Helping the owner reconfigure

- **A read-only report:** `gohort --config-report` prints the old settings as
  the exact values to type into the new forms, tier by tier, and only that a
  key was kept, never the key (decided, Q6):
  - each LLM tier's provider, address and model;
  - the web search provider, the Research Sources;
  - the image backend (ComfyUI's address; its workflow and node map,
    exportable to paste back), the default, the agents list;
  - transcription, speech, mail;
  - per-user access and agent scopes;
  - agents naming anything that no longer exists.

  The owner gathers the keys beforehand (Anthropic, OpenAI, Gemini, Serper,
  ...), runs the report on the old build, deploys, and fills in the LLM tiers
  first, so the window without a model is minutes. It reads the database and
  changes nothing. It refuses while gohort runs,
  as the dry run did, and reads only the tables it names, never an app's
  data. It is deleted a release later.
- **A cleanup button:** Admin > Maintenance > "Clear old connection records",
  pressed after reconfiguring. It removes provided connections, orphaned
  connection spaces and their keys, and finished migration markers. It is
  not run at start.

### Other tasks on the branch

- **Tests:** hundreds encode plan-B shapes. They are rewritten or deleted,
  and the new code gets the deliberate-break checks every change gets.
- **Docs and memory:** `docs/integrations.md`, `connectors-and-flows.md` and
  `unified-connectors.md` are updated or marked superseded; so are the memory
  notes that describe plan B.
- **Past exports:** bundles already exported with connectors import, or fail
  with a clear reason.
- **Cost history:** past costs keep their `service:<name>/…` labels; reports
  show old and new labels side by side.
- **Both build modes** (module and GOPATH) build on every push.

### Before deploying

Stay on the current build until the rework is ready. Rebuilding from today's
`main` runs `images:v1`, which is harmless (the rework reads ComfyUI as a
connector), but nothing is gained by it.

## What moves where

| Today (kind) | Becomes | Old config |
|---|---|---|
| `service` | Connector | kept as is |
| `rest_image` | Connector (ComfyUI is already; A1111 and peers' renderers in the rework) | reconfigure A1111; peers re-offer theirs |
| provided LLM connections | built-in LLM providers | reconfigure each tier |
| `remote_mcp` | MCP Servers page | later step; until then, as is |
| `desktop_mcp`, `desktop_command` | the per-user Desktop page | later step |
| `rest_poll` | a watch over a connector's `fetch` | later step |
| `rest_messaging` | a connector (`message.send` + `message.fetch`) + a channel, + a webhook if it pushed | later step |
| `bot_framework` | a channel type + a webhook with the `botframework` verifier | later step |
| `messaging_bridge` | a channel type (device relay) | later step |
| `peer_relay` | how a webhook is reached | later step |
| `file_conversion` | System Hooks (already) | as is |

"Later step" kinds keep working as today through the rework. Each moves on
its own branch, without a migration, and is reconfigured when it moves.

## Names to agree before any code

Every name below is a proposal. Each is stored or shown, so each is settled
here, per the rule that names are agreed first.

| Thing | Proposed name |
|---|---|
| the four concepts, in the UI | Connectors, Webhooks, Channels, Keys |
| a page's choice of connector | plain names (`serper-search`); built-in names (`duckduckgo`, `smtp`, ...) reserved so no connector can take one; `peer:<name>` kept (decided, Q7) |
| the compatible LLM providers | `openai_compatible`, `anthropic_compatible` (shown as "OpenAI-compatible endpoint", "Anthropic-compatible endpoint") |
| their switches | `max_tokens_field`, `reasoning_field`, `stream_usage`, plus any the client already special-cases |
| the reconfigure report | `gohort --config-report` |
| the cleanup | Admin > Maintenance > "Clear old connection records" |
| a webhook's address | `/hook/<name>` |
| a webhook's check | `verify`: `none`, `token`, `hmac`, `slack`, `graph`, `botframework` |
| a webhook's payload map | `map`: `id`, `chat`, `from`, `text`, `kind` |
| a webhook's consumer | `feeds`: `channel:<name>`, `watch:<agent>`, `flow:<name>` |
| a webhook's reach | `reach`: `direct`, `peer:<name>` |
| fetching messages | contract `message.fetch` |
| trust | open (Q2), with DLP |
| the egress check | `EgressCheck`, `Egress`, `Decision` (with DLP) |
| one-provider Go in a connector | `builtin` (exists) |

## Order of work

**The rework** is one branch, reviewed commit by commit, merged and deployed
once:

1. This document, agreed. No code.
2. The reconfigure report, written first, against today's shapes.
3. LLM providers as built-ins, including the compatible endpoints.
4. Provided connections, the legacy reads, the migrations, the dry run and
   `core/bbolt` taken out.
5. A1111 and peers' renderers as connectors; `rest_image` deleted.
6. The Connectors page shows connectors: one editor per row, each reading
   what references it; the in-use gate, Approve and the Settle machinery go
   for connectors, after the call paths are checked; the Changed hold stays;
   the other kinds are listed on their own pages; the Contracts page goes, its
   text shown in the connector editor.
7. The cleanup button.
8. The embeddings check against a copy of the live database, the security
   acceptance tests, and the private repo's branch.

**After the rework,** each its own branch, with no migration:

9. Webhooks, first version: the record, `/hook/<name>`, the verifiers, and
   two consumers, watches and flows (decided, Q4). `peer_relay` becomes a
   webhook's reach. `rest_messaging`'s push side and Bot Framework's inbound
   stay as they are until channels.
10. Channels on connectors: `message.fetch`; Slack, Discord and Teams
    templates; the channel consumer for webhooks; `rest_messaging` and Bot
    Framework move, once.
11. Watches on connectors: `rest_poll` becomes a watch over a connector.
12. Desktop programs (`desktop_command`, `desktop_mcp`) on a per-user Desktop
    page (decided, Q5); MCP servers and peers on their own pages with the key
    store's rules.
13. The vault and the key store (Q3).
14. DLP: the egress check and its hooks, then the DLP behind it; trust (Q2).

## Decisions (2026-11-01)

1. **Approve is dropped for connectors.** A non-admin's or Builder's draft is
   held as Changed until an admin saves a page naming it, so the gate moves to
   the page (see decision 7 for what replaces "in use").
2. **Trust: not drawn from internal versus external.** DLP as a whole is
   revisited after everything else, and Q2 with it.
3. (Q4) **Webhooks' first version feeds watches and flows.** Messaging's push
   side and Bot Framework move once, with channels.
4. (Q5) **Desktop programs get their own per-user page,** desktop MCP with
   them.
5. (Q6) **The report prints the exact values to type back in,** tier by tier,
   and the owner has the keys ready, so the window without an LLM is minutes.
   No "restore from old settings" button: that would be migration code under
   another name.
6. (Q7) **Pages store plain names.** Built-in names are reserved, and
   `peer:<name>` stays.
7. **References replace "in use".** A connector has no on/off state: its
   references guard its deletion and show on its row. The call gate goes once
   every call path is checked; the Changed hold on edits stays.

## Still open

- **Q2. Trust,** with DLP: leaning toward a policy per destination, set by
  hand (what is gohort willing to send to this model, this search provider),
  never inferred from the address.
- **Q3. The key store's shape:** the vault absorbs connection keys, connection
  spaces absorb the vault, or two stores with the same rules. Not needed for
  the rework.
