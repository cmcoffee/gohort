# Integrations: connectors, webhooks, channels and one key store

**Status:** design, for the owner's review. Nothing here is built. Written
2026-11-01 from a conversation with the owner. It follows
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

**In use** has one meaning: a page or an agent names the connector. Approve
and Unapprove go away for connectors.

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
- **Desktop programs** (`desktop_command`): with the desktop's own page.
- **Peers:** their own page. A peer's lent renderer becomes a connector.
- **System Hooks programs** (`file_conversion`): already their own page.

## DLP

The model makes data loss prevention a matter of a few control points, not a
scan of everything.

**The rule a DLP enforces:** sensitive data only flows to destinations
trusted for it.

**A trust level on every connection** (connector, webhook, channel, LLM
provider, MCP server). Proposed values:

- `internal`: this machine or the LAN;
- `vendor`: a service under an agreement (a DPA, an enterprise contract);
- `public`: anyone else.

A new connection starts `public`, so nothing sensitive goes to it until
someone says it may. That is fail-closed, as gohort's other safety defaults
are. Trust is a stored field, so it is decided in this document, not later
while building.

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

Building a DLP is not part of this plan. Two things are: the trust field and
the egress check interface with its hooks, so that a DLP can plug in later
without reopening the four concepts.

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
- "In use" as a page or an agent naming it (tie-ins).
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

- **A read-only report:** `gohort --config-report` prints what the old
  settings were, and only that they had a key, never the key:
  - each LLM tier's provider, address and model;
  - the web search provider, the Research Sources;
  - the image backend (ComfyUI's address; its workflow and node map,
    exportable to paste back), the default, the agents list;
  - transcription, speech, mail;
  - per-user access and agent scopes;
  - agents naming anything that no longer exists.

  It reads the database and changes nothing. It refuses while gohort runs,
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
| `remote_mcp`, `desktop_mcp` | MCP Servers page | later step; until then, as is |
| `desktop_command` | the desktop's page | later step |
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
| a page's choice of connector | **open (Q7):** plain names (`serper-search`), or per-job prefixes (`search:serper-search`) |
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
| trust | field `trust`: `internal`, `vendor`, `public` (default `public`) |
| the egress check | `EgressCheck`, `Egress`, `Decision` |
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
6. The Connectors page shows connectors: one editor per row, Approve gone for
   connectors, the other kinds listed on their own pages.
7. The cleanup button.
8. The embeddings check against a copy of the live database, the security
   acceptance tests, and the private repo's branch.

**After the rework,** each its own branch, with no migration:

9. The trust field and `EgressCheck`, hooked into the dispatch, then the LLM
   request path, web reach, the sandbox relay and MCP. First after the rework,
   so few connections exist unclassified.
10. Webhooks: the record, `/hook/<name>`, the verifiers, the consumers;
    `rest_messaging`'s webhook side, Bot Framework's inbound and `peer_relay`
    move onto them.
11. Channels on connectors: `message.fetch`; Slack, Discord and Teams
    templates.
12. Watches on connectors: `rest_poll` becomes a watch over a connector.
13. MCP servers, desktop programs and peers on their own pages with the key
    store's rules.
14. The vault and the key store (Q3).

## Open questions for the owner

1. **Approve for connectors.** Removing it means a connector a non-admin
   drafts (Builder) is held only by the page that names it. Today that page's
   save is the approval (`MarkChanged`), so nothing is lost, but confirm.
2. **Trust defaults.** Should a connection on a LAN address start `internal`,
   or should every connection start `public` until raised by hand?
3. **The key store's shape.** Do connection spaces absorb the vault, or does
   the vault take on the rules? The second keeps API Credentials as the page
   people know. The first keeps one record per connection. Not needed for the
   rework.
4. **Webhooks without a channel.** Is "wake an agent" (a watch) or "start a
   flow" enough of a consumer for the first version, or does the first
   version need channels too?
5. **Desktop programs.** Their own page, or System Hooks (programs on this
   machine and on a desktop, together)?
6. **The deploy sitting.** Is a window where gohort has no LLM until it is
   reconfigured acceptable, or should the report also print the exact values
   to paste, tier by tier, to keep the window to minutes?
7. **Choice values.** Plain names, or per-job prefixes? Prefixes only earn
   their place if one value can mean different things on one page.
