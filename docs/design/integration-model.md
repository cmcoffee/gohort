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
| A new thing for agents to do: call a CRM, look up stock | yes | an API tool |
| A new service pushing events in | yes, once webhooks exist | a webhook |
| A new API whose data has a shape gohort does not handle yet (files inline as base64, a streamed reply, paged results) | once | Go once: an adapter (below); every API of that shape after it is data |
| A new job gohort itself runs, or one provider's own logic (graph surgery) | no | Go, kept small: a `builtin` behind a configurable connector |

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
- **several providers share the shape:** write an adapter (below), and
  every provider of that shape is data from then on.

**References, not "in use"** (decided 2026-11-01). A connector has no on/off
state. What it has are references: the pages that name it (Web Search, Image
Generation and its list offered to agents, Research Sources,
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

Built in step 6a (2026-11-01). The audit found two gaps, both closed: a
backend's `generate_image_<name>` tool reaches only a connection Image
Generation offers (its default or the ones offered to agents), since a tool
grant alone is no reference; and the free health check runs on its own only
for a referenced connector, so a draft makes no calls. A service connection
is served from its save and at start (`Connector.Served`); the stored
`Approved` field stays for the other kinds and is ignored for connections.
What is left of the gate is the hold below, checked at every call.

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

**Research sources are agents' tools** (2026-11-01). A research source
offered to agents is a tool of its own, by the tool name given under
Research Sources: listed in the agent editor under "Research sources", in
the default set, and taken off an agent like any tool; a call shows as the
source's own tool. The one `query_source` dispatcher, added to every turn
unseen, and its "Available sources" prompt block are gone. A tool name
another tool or source has is refused. Web Search keeps research sources
out of its engines: "Use a research source…" and the list under it choose
one.

**Adding one** (2026-11-01). A template is a recipe, never installed: Add…
on it asks its questions and makes the connector, and the template stays
for next time. Admin > Extensions > Templates lists them all by category.
Every page's list of connectors ends with **+ Add another…**, which opens
the templates for that kind (those filed under its category whose
connector does its job) and, once one is added, reloads the page with it
in the list. The list of templates "added to the system" before they
could be set up is gone. Connector groups start folded, each heading
saying how many it holds and how many have an error.

### Adapters

Decided 2026-11-01 (decision 10). **An adapter is a small Go primitive for
one shape of data or transport**: how a request carries its values, how a
reply is read, how a job is waited on, how a key is sent, how results are
paged. It is written once, named, documented and tested on its own, and a
connector's spec composes adapters by name. A new API whose shape gohort
already handles is pure data; Go is written only when a new shape appears,
and every API of that shape after it is data too.

Three concepts, each standing on its own:

- **adapter:** Go, a shape (inline base64, multipart, a streamed reply, a
  poll, a signature);
- **connector:** data, one configured service: adapters composed into its
  calls, with its address, key and job;
- **template:** data, a starting point that makes a connector.

**Named in the spec, where it applies.** An adapter shapes one part of a
call, so the spec names it in that part, never in a list apart and never as
a stored record a connector points to. An adapter is code: all there is to
keep is which one and its settings for this call, and those belong to the
call. A stored adapter would need an attach step, versions, and an answer
for changing under every connector at once: the provided-connections
problem again.

| Part of a call | Where the spec names it | Adapters today |
|---|---|---|
| how the request carries values | the operation's `send` | `json`, `form`, `multipart`, `file`, `none` |
| how the reply is read | the operation's `read` | `json`, `text`, `file` |
| how a value is decoded | the output's `as` (with `each`, `fields`) | `base64`, `url`, `json` |
| how a job is waited on | a step's `wait` (`until`, `fail`, `every_secs`, `max_secs`) | the poll |
| how the key goes | the secret's `send` | `bearer`, `header:<name>`, `query:<name>`, `basic`, `aws_sigv4` |
| how results are paged | the operation's `page`, when needed | none yet |

A plain name covers most; one that takes settings uses the shape builtins
already use, the name and its `options`. Today's names stay the adapters'
names, so formalising them changes nothing stored.

**What lives outside the spec:** each adapter's Go, its test, and its line
in the table above. For now the names a spec may use are what its
validation checks; once there are enough to be worth it, a registry lists
them (name, what it does, its options) for the connector editor, the Fix
assistant and the Builder, as the contracts are listed. A spec naming an adapter this build
does not have fails validation, and its self-check says so. An adapter's
name is a contract: it never changes meaning, and a new behaviour gets a new
name.

**Builtin versus adapter.** A builtin replaces a whole operation with one
provider's Go (ComfyUI's graph wiring, PubMed's searches); an adapter
changes one aspect of an operation and serves any provider. Over time most
builtins become an ordinary operation and an adapter or two: A1111's inline
base64 images, now inside the A1111 code, are a `base64-inline` request
adapter; PubMed's XML replies, an `xml` read adapter.

**The guardrail.** Adapters handle the shape of data and its transport,
never control flow or a provider's business logic: that is what keeps the
engine from becoming a programming language. A new adapter is added when an
API in hand needs it, with a fake server test, not ahead of one.

**Candidates,** each when a real API needs it:

- inline base64 in a request body (A1111, most image-editing APIs);
- streamed replies, SSE and NDJSON (anything LLM-like);
- pages: a next cursor or a next link;
- upload, then reference: post the file, keep its id, send the id;
- XML and RSS replies (PubMed, feeds).

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
  - a watch (wake an agent).

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

## The rework: reaching this design, with one migration written last

**Approach.** History stays as it is (the owner declined rewriting it). On
one branch, the code is reshaped into what would have been built the first
time. The six startup migrations that exist are deleted.

**One migration, written last** (decided 2026-11-01). Once the design and the
code are final, one migration is written that takes an older install into the
final shape, from either starting point:

- **before plan B** (before 30 October 2026): providers built into gohort,
  settings on each page, keys in the tier tables and the vault;
- **plan B** (what the owner's install runs now): provided connections,
  `llm:` and `search:` style names, connection spaces.

It covers the LLM tiers, the pages' choices, and the local servers'
connections (`local-embeddings`, `local-transcription`, `local-speech`,
`ollama-embeddings`), which become ordinary connections under the same names,
so the embeddings space is kept by construction. It is tested from each
starting point and run against a copy of the live database before deploying,
and it is deleted a release later. Until it exists, and for anything it does
not cover, a setting that no longer reads fails visibly and is reconfigured
by hand, as below.

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
- **A1111 and peers' renderers as connectors** (built in step 5). A service
  operation runs the `a1111` builtin, `options.mode` `txt2img` or `img2img`,
  beside `comfyui`; the image machinery (RestImageSpec) runs both. A plain
  A1111 stores only its mode and what differs from it; its `--api-auth` login
  is the connection's key, sent as basic. A peer's renderer is an `a1111`
  connection under its old name with its own path and body, the peer key
  kept as its secret for the peer's address and rewritten in place when the
  key changes; the next sync rewrites one stored as `rest_image`. A peer's
  renderer is put to use where it is chosen (Image Generation), not on
  arrival. Builder writes one as a plain `create` of a service.

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
   and paid. The embeddings record keeps its shape and its space identity,
   and the migration keeps its connection's name. This is checked against a
   copy of the live database before deploying.
2. **Stale settings fail visibly.** A value the new code does not recognise
   (`llm:anthropic`, `search:serper-search`) reads as not set, with a notice
   on its page. It never falls back to another provider. For the LLM tiers
   that means gohort runs without a model until reconfigured, so the deploy
   and the reconfiguring happen in one sitting.
3. **Names agents use do not change:** granted tools (`generate_image_<name>`,
   search tools, a research source's own tool), and credential
   scopes (`connection:<name>`). An agent naming something that no longer
   exists is listed in the admin notice, as a setting needing it is.
4. **Every security property since plan B survives,** as acceptance tests the
   branch passes before merging:
   - a key goes only to its addresses;
   - agent and user scope on keys, at the call and in tool lists;
   - the privacy check judges a tier by its address, failing closed;
   - the self-check.
   Checked in step 8 (2026-11-01). The tests that prove each, on the
   branch:
   - a key goes only to its addresses: a connection's
     (TestAConnectionCarriesItsSecret), an image connection's
     (TestAnImageConnectionKeepsItsKeyAndItsScope), a tier's
     (TestATiersKeyGoesOnlyWhereItWasGiven), a peer renderer's
     (TestPeerKeyNeverLandsInTheConnectorSpec, TestRotationReachesTheRenderersKey);
     a key given for another address refuses the call, nothing sent;
   - agent and user scope, at the call and in tool lists: images
     (TestAnAgentBarredFromAConnectionIsNeitherOfferedNorServed,
     TestADeniedConnectionHidesTheBackend, and a backend's own tool in
     TestAnImageConnectionKeepsItsKeyAndItsScope), music and research sources
     (their tool-list tests in core/service), the connection's own
     check (TestAConnectionBarsAnAgentOrAUser);
   - the privacy check judges a tier by its address, failing closed:
     TestATierStillNamingAConnectionFailsClosed (an old `llm:` value, an
     unknown name, a hosted provider at a private address),
     TestHostedProvidersAreNeverJudgedPrivate;
   - the self-check: core/service/selfcheck_test.go, including a key given
     for another address and a reserved name.
   The rework's own: a held connection is reached by no path
   (TestAChangeIsHeldUntilAnAdministratorAccepts and each path's test), and
   a backend's tool needs Image Generation to offer it
   (TestAComfyUIConnectionDraws). The private apps' tests pass on the
   branch (`go test ./...` does not follow the private symlink: run
   `go test ./private/...` too).
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

### Reconfiguring, highlighted by gohort

Decided 2026-11-01: a reconfigure is fine where one is needed, and gohort says
where. No report: the highlight sits where the fix is made.

- **On each page:** a setting that no longer reads shows "Needs setting
  again" and reads as not set (the LLM tiers do already, step 3). Every page
  whose choice can go stale does the same.
- **Across admin:** one notice listing every setting that needs it ("3
  settings need setting again: Worker LLM, Web Search, Image Generation"),
  each a link to its page. It goes once they are set.
- **In the log:** the same list at start, once.

Built in step 6b (2026-11-01). A setting needs setting again when it names a
connection the old way (`<job>:<name>`, `service:<name>`) or names one that
is not there; an LLM tier only the old way, since a plain name there is an
unknown provider its page refuses. The pages that choose a connector (Web
Search, Image Generation and its list offered to agents, Embeddings,
Transcription, Speech, Page Rendering, Mail, Music) read such a value as not
set, with "Needs setting again" saying why; a disabled embedder, transcriber
or speaker is not flagged. The admin notice is one dashboard card naming
each page, its button opening the first; `service.StaleSettings` is the one
list behind the card, the pages and the log. The reserved names are `none`,
`default`, `local`, `commands`, `duckduckgo`, `searxng`, `smtp` and the
built-in LLM providers; a new connector cannot take one, and one already
saved under such a name says so in its self-check. The embeddings' space
survives the rename: choosing the connection a value named the old way keeps
the endpoint it was stored with.

**Before deploying, a checklist** (no code):

1. Export all connectors (Admin > Extensions > Connectors > Export all): their
   specs and settings, never keys, to import back if anything is lost.
2. Note each LLM tier's provider, address and model, and have the keys ready
   (Anthropic, OpenAI, Gemini, Serper, ...): no key is ever shown again.
3. Keep the embeddings connection's name exactly (`local-embeddings` or the
   one in use): it names the vector space, and another name embeds every
   collection again. (Moot once the migration has run.)
4. Look over agents granted image or search tools, and the credential
   opt-outs naming a connection.

5. Run the migration's check against a copy of the database (step 9, under
   Order of work) and read its list.

Deploy: the migration runs at start, so the tiers keep a model. Then set
whatever the admin notice still lists.

- **A cleanup button:** Admin > Maintenance > "Clear old connection records",
  pressed after reconfiguring. It removes provided connections, orphaned
  connection spaces and their keys, and finished migration markers. It is
  not run at start.

  Built in step 7 (2026-11-01), under Admin > Maintenance > Reclaim space as
  a dry run and the delete it lists. It removes a connection gohort once
  provided only when its spec no longer validates and no page uses it (plan
  B's LLM providers), with its space and key; a provided one that still
  validates (a local server's) stays as the ordinary connection it is now.
  It removes `connection:<id>` spaces no connector owns, the markers of
  the seven migrations the rework removed, and saved flows (decision 9). It leaves `rest_image` records
  (the migration's) and the vault (rule 5). The delete refuses while any
  setting needs setting again, since setting it replaces the keys those
  records hold; and until the migration (step 9) has run. Since step 9 it
  also removes a model connection (one providing `llm.chat`) no page uses:
  the migration moved what its tier needed onto the tier.
  Found while building it: a peer's token rotation was still rewriting the
  vault credential its renderers sent before step 5; it now rewrites the
  key each renderer connection keeps.

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
| the highlight | "Needs setting again" on a page; the admin notice "N settings need setting again" |
| the cleanup | Admin > Maintenance > "Clear old connection records" |
| a data shape gohort handles in Go | adapter; named in a spec's `send`, `read`, `as`, a step's `wait`, a secret's `send`, and `page`; today's values are its first names (decision 10) |
| a webhook's address | `/hook/<name>` |
| a webhook's check | `verify`: `none`, `token`, `hmac`, `slack`, `graph`, `botframework` |
| a webhook's payload map | `map`: `id`, `chat`, `from`, `text`, `kind` |
| a webhook's consumer | `feeds`: `channel:<name>`, `watch:<agent>` |
| a webhook's reach | `reach`: `direct`, `peer:<name>` |
| fetching messages | contract `message.fetch` |
| trust | open (Q2), with DLP |
| the egress check | `EgressCheck`, `Egress`, `Decision` (with DLP) |
| one-provider Go in a connector | `builtin` (exists) |

## Admin flows: getting each thing working (2026-11-01)

What an administrator clicks today, after the changes on this branch, for
each common goal; where it is clumsy; and a proposed path. Proposals only:
each is decided before it is built. None of them changes a stored shape, so
none blocks the migration (step 9); the one new kind is marked.

### Search the web with Serper

Today:

1. Admin > Capabilities > Web Search: "Search with" > **+ Add another…**.
2. Add… on *Serper (web search)*: it asks a name; Add; Back to the list.
3. The page reloads with Serper in the list, not chosen: choose it.
4. Its key field appears under the choice: give the key, Test, Save.

Clumsy: step 3. The page was opened to add Serper, and after adding it the
admin has to find and choose it before its key field shows.

Proposed: adding from a page leaves the page with the new connector
chosen (not yet saved), its key and settings showing, so steps 3 and 4 are
one: give the key, Save. Needs one generic `core/ui` piece: a form value
set on arrival (the page told which value to choose as it reloads).

### Make pictures (Gemini, OpenAI, ComfyUI, A1111)

Today, two ways in:

- "Make pictures with" > **+ Add another…**: the Gemini, OpenAI, ComfyUI
  and Automatic1111 templates, then as Web Search above;
- the section's own toolbar: **Add image backend…** (ComfyUI and A1111's
  setup forms) and **Configure backend…**.

Clumsy: two ways to add ComfyUI or A1111, and Configure backend… does what
the connector's row Edit already does.

Proposed: one way. The toolbar goes: ComfyUI and A1111 come through
+ Add another… like the rest (their templates exist), and Edit on the
connector's row opens their setup form. Plus the arrival choice above.

### Embed with a hosted or local model

Today:

1. Admin > Capabilities > Embeddings: "Embed with" > + Add another… >
   Add… on *OpenAI (embeddings)* (or Ollama, llama.cpp/vLLM).
2. Back to the list, choose it, give its model and key, Test, Save.
3. Admin > Maintenance > Vector Index: Repair, so stored documents are
   embedded again in the new space.

Clumsy: step 3 lives on another tab, and nothing on Embeddings says it is
needed; until it runs, search skips stored documents.

Proposed: the arrival choice; and after a save that changes the space,
Embeddings says so and offers **Re-embed now** in place, running the
repair with its progress (the spinner rule).

### Transcribe and speak

Today: Audio Transcription and Speech, each as Embeddings' steps 1 and 2
(Speech also picks a voice from the connector's list).

Clumsy: only the arrival choice.

### Send mail through an API (Resend, Postmark, Mailgun)

Today: Admin > **System** > Mail (SMTP): "Send with" > + Add another… >
Add… > back, choose, key, Save.

Clumsy: mail is the one capability on the System tab, so it is not where
the other "what does gohort use for X" pages are.

Proposed: Mail moves to the Capabilities tab, titled "Mail". Plus the
arrival choice.

### Add a research source (PubMed)

Today, two pages and two records:

1. Admin > Extensions > Research Sources > Add: "Search connector" >
   + Add another… > Add… on *PubMed (research)* > back.
2. Choose PubMed, set its topics or Always, and to offer it to agents a
   tool name and description; Save.
3. Its tool then shows in each agent's tools under Research sources.

Clumsy: Research Sources sits under Extensions while Web Search, its
nearest neighbour, is under Capabilities; and the template makes only the
connector, so the source is a second form after it.

Proposed: Research Sources moves to the Capabilities tab, beside Web
Search; and a research template asks the source's questions too (topics or
always, offer to agents, tool name) and makes both. That needs a bundle
artifact for a research source: **a new kind**, additive (bundles without
one import as now; no migration). Today only the legacy Source Hook
artifact makes both, by an older shape.

### Borrow a peer's model, search or GPU

Today: Admin > Capabilities > Peers: add the peer's address and key; what
it lends then appears on each page's list ("Peer: gpu-box"), and its
renderers as connections under Image Generation.

Clumsy: nothing found that this rework should change.

### Set the LLM tiers

Today: Admin > LLMs > Worker LLM and Lead LLM: provider, address (blank for
the provider's own), key, model; Test; Save.

Clumsy: nothing found that this rework should change.

### Summary

| Proposal | Touches | Stored shape |
|---|---|---|
| Arrival choice: a page reloads with what was just added chosen | `core/ui` (a form value set on arrival), each page's + Add another… | none |
| One way to add an image backend: the toolbar goes | Image Generation | none |
| Re-embed now on Embeddings after the space changes | Embeddings, the repair's progress | none |
| Mail moves to Capabilities | the admin page's tabs | none |
| Research Sources moves to Capabilities | the admin page's tabs | none |
| A research template makes the source too | a research source bundle artifact (new kind), the research templates | additive |

Suggested order: the arrival choice first (every goal above has it), then
the two moves and the image toolbar (small, page-only), then Re-embed now,
then the research source artifact (a new kind, agreed before it is built).

## Order of work

**The rework** is one branch, reviewed commit by commit, merged and deployed
once:

1. This document, agreed. No code.
2. (Dropped: a reconfigure report, built in 29bc896c and taken out; gohort
   highlights what needs setting again instead, built with each step.)
3. LLM providers as built-ins, including the compatible endpoints.
4. Provided connections, the legacy reads, the migrations, the dry run and
   `core/bbolt` taken out.
5. A1111 and peers' renderers as connectors; `rest_image` deleted.
6. (6a references, 6b plain names, 6c the page.) The Connectors page shows connectors: one editor per row, each reading
   what references it; the in-use gate, Approve and the Settle machinery go
   for connectors, after the call paths are checked; the Changed hold stays;
   the other kinds are listed on their own pages; the Contracts page goes, its
   text shown in the connector editor; and pages store their choices as plain
   names (Q7), the stale highlight catching the prefixed ones until the
   migration rewrites them.
   Built in 6c (2026-11-01): connector rows open one editor, Edit (the spec
   beside the assistant with each capability's contract, or the setup form
   a template made it with), shown as Fix when the spec does not validate;
   Settings, Access, Endpoints, Test and Delete stay. The other kinds sit in
   "Other integrations" below, still approved there, until their own steps.
7. The cleanup button.
8. The security acceptance tests and the private repo's branch.
9. The one migration, from both starting points, written now that the rest is
   final, and its check against a copy of the live database (the embeddings
   space above all). For a stored `rest_image` connector:
   `RestImageSpec.AsService` makes the connection for a ComfyUI or an
   A1111-shaped one, its vault credential's send carried (`credentialSend`);
   other shapes (a hosted API polled for its result) had a general
   translation, `FromRestImage`, taken out in step 5 with the Fix that used
   it: `git show 0a7e63ec:core/service/translate.go`. Peers' renderers need
   nothing: the next sync rewrites them.

   Built (2026-11-01): `core/service/integration_migrate.go`, run once at
   start under the marker `integration_model:v1`, after the credential store
   is wired. Finding is read only and applying writes, so `gohort
   --migrate-dry-run` lists exactly what it does. In order:

   - a connector keyed by a vault credential keeps its key itself (made
     again from its template's spec, or with the credential made a secret
     setting sent as the credential sent it); a credential nothing else
     names is then deleted;
   - a `rest_image` backend becomes a `comfyui` or `a1111` connection under
     its name, its key moved; a peer's renderer is left to its sync; a shape
     no builtin runs is left (`a1111Service` now refuses a backend read any
     other way than A1111's `images.0`, so it is never turned into one that
     cannot work), and the page naming it needs setting again;
   - research topics and a tool written into a spec become its Research
     Sources entry; a Source Hook becomes its search connector
     (`HookConnector`), on when the hook was, and Web Search's engine when it
     was; a paywall hook is left;
   - an LLM tier on a plan B connection (provided or a model connector) runs
     on that connection's dialect directly, with its address (blank when it
     is the provider's own), model and key; the key moves only when it was
     given for the address the tier now sends to. A Bedrock key is not moved.
     A tier from before plan B has its key bound to the address it was sent
     to;
   - a page naming a connection `<job>:<name>` or `service:<name>` names it
     plainly; embeddings, transcription and speech records keep their
     endpoint and model, so the embeddings' space (`EmbedVersion`) is the
     same;
   - Brave or Serper as Web Search's own engine, and OpenAI or Gemini as
     Image Generation's own, become connectors from their templates with the
     keys they had; Google, or an engine with no key, is left;
   - embeddings, transcription and speech at this instance's own server
     become connectors from the local templates at that address (Ollama when
     the address is Ollama's), the embeddings' space kept. A key they had is
     not carried, since those templates take none; the log says so.

   `query_source` was never stored in a grant (it was added to every turn
   unseen), so nothing rewrites it. The model connections a tier ran on are
   cleared by Clear old connection records, which now also waits for the
   marker. What it leaves is listed as needing setting again: a tier key
   with no address, a record set up before its kind was a connector, an
   image provider gohort no longer runs itself, and a `rest_image` backend.

   **Checking it against a copy of the live database** (by the owner: the
   database holds sysprobe data, which these sessions do not open):

   1. Stop gohort and copy the data directory somewhere else, with the
      `gohort.ini` pointed at the copy.
   2. With this branch's build, run `gohort --migrate-dry-run` against the
      copy. It lists each change; nothing it covers is written.
   3. Check that the tiers name the providers and addresses expected, that
      Embeddings keeps its connection's name, and that nothing listed is a
      surprise.
   4. Start this build on the copy (not the live directory) and look at
      Admin: no "needs setting again" card for anything the list covered,
      the Embeddings page naming the same connection, and a test chat on
      each tier.

**After the rework,** each its own branch, with no migration:

10. Webhooks, first version: the record, `/hook/<name>`, the verifiers, and
   one consumer, watches (decided, Q4; flows were its second until they
   were retired, decision 9). `peer_relay` becomes a
   webhook's reach. `rest_messaging`'s push side and Bot Framework's inbound
   stay as they are until channels.

    Built (2026-11-01, branch `webhooks`): `core/webhooks`, with the section
    under Extensions > Webhooks. A webhook is its owner's, as a watch is,
    and wakes only one of their watches woken by a post (`feeds`
    `watch:<monitor>`). Its address, `/hook/<name>`, is unique on the
    instance and made from its label; one that checks nothing gets a long
    random name, since the address is then its only secret. Checks:
    `none`, `token` (a named header, or a bearer in Authorization) and
    `hmac` (sha256 or sha1, an optional prefix, hex in either case or
    base64); the named verifiers (`slack`, `graph`, `botframework`) move
    with channels. The secret is kept encrypted and never read back: the
    Add form opens with a fresh one to copy to the service, or the
    service's own is pasted over it; New secret replaces it the same way.
    How it checks is fixed when it is added. `map` reads `text`, `kind`,
    `id` and `from` by dotted path or `header:<Name>`; `kinds` limits
    which pass on; an `id` seen lately is passed over, claimed in one
    step so two retries together are not both passed on, and released
    when the watch's budget refuses it so the retry is. `reach` is
    `direct` or `peer:<relay connection>`, whose public address the row
    shows while it is connected (`PeerRelayAddress`). Bounds: 1 MB a
    post, 120 posts a minute a webhook, 20 refusals a minute an address,
    and the watch's own 12 wakes a minute, now kept in core
    (`EventMonitorFires`) and spent by both doors. Every call is kept on
    its row (the last 20), refusals saying why. A watch's own token URL is
    unchanged.
11. Channels on connectors: `message.fetch`; Slack, Discord and Teams
    templates; the channel consumer for webhooks; `rest_messaging` and Bot
    Framework move, once.

    11a built (2026-11-01, branch `channels`): the `message.fetch` contract
    (since, chat; messages of id, chat, from, from_name, text, at, and a
    cursor; every message has an id, and one that does not say its chat is
    in the chat asked for). A person links a service under Bridges >
    Linked services (`apps/bridges/links.go`): a connector giving both
    `message.fetch` and `message.send`, called as them (its Access), under
    a service name channels bind to, polling named chats or the whole
    service. One link a service a person, refused where another transport
    carries it. The first fetch of each chat marks what is there as seen;
    later ones pass on only what is not seen yet, so a service asked for its
    latest (Discord) is not counted twice; replies go out through
    `message.send` to the chat they came from, kept for the next poll when
    sending fails. Templates: Slack (messages), with a cursor, and Discord
    (messages), latest 50 a poll (after=0 would walk its whole history).
    Mattermost was not added: its reply is a map of posts keyed by id,
    which the spec cannot read as a list, so it waits for an adapter
    (decision 10). Nor can a secret's send add a prefix yet: Discord's
    token is pasted as "Bot <token>". The send-only templates have no
    caller now that flows are retired; their text says so.

    11b built (2026-11-02, branch `channels`): a webhook feeds a channel,
    `feeds` `channel:<service>`, one of its owner's linked services (the
    bridges app answers `webhooks.LinkedServices` and takes
    `webhooks.ToChannel`, which goes in through `ingestInbound` as a
    link's own fetch would). `map` gains `chat`, needed to feed a channel;
    a post saying no chat, no sender or (with a text path) no text is
    passed over, which is what keeps a bot's own replies, posted back as
    events, from coming round again; an id is claimed once. The `slack`
    check is Slack's request signing (`v0:<timestamp>:<body>`, the
    timestamp within five minutes either way) and answers its URL check
    with the challenge. A link a webhook feeds fetches nothing and sends
    its agents' replies every three seconds. The Extensions page's Webhooks
    section says how to set Slack up; Graph and Bot Framework stay with
    11c and 11d.

    11d built (2026-11-02, branch `channels`), as personal bots: a person
    adds a Teams bot they registered in Azure under Bridges (`core/teamsbots`
    keeps it: app id, tenant, the client secret encrypted with the bot,
    reply host, conversation types, reply in thread). Adding one is the
    administrator's policy (`governance.TeamsBots`, needs approval by
    default; Admin > Governance lists every bot to approve, switch off or
    delete). The bot is reached through a webhook made and removed with it,
    checked `botframework`: webhooks gain channel types' own checks
    (`RegisterChannelCheck`), which check and read their posts themselves.
    The token's audience is the bot's app id and it must fit the activity
    (its serviceurl, the key's endorsement); a bot waiting for approval, or
    an activity from another tenant, passes nothing on. Replies are signed
    with a token minted from the bot's own id and secret (its tenant, or
    botframework.com for a multi-tenant bot), kept until near expiry, and
    go only under its reply host, to a conversation it heard. One reply
    loop serves every approved bot. Under Edit, its messaging endpoint
    (with Copy) and its Teams app package (manifest and both icons, made
    for that address; never the owner's name). The `bot_framework`
    connector kind is deleted, with Builder's create and update for it.
    A shared bot with Teams users mapped to gohort users is not built.
12. Watches on connectors: `rest_poll` becomes a watch over a connector.
13. Desktop programs (`desktop_command`, `desktop_mcp`) on a per-user Desktop
    page (decided, Q5); MCP servers and peers on their own pages with the key
    store's rules.
14. The vault and the key store (Q3).
15. DLP: the egress check and its hooks, then the DLP behind it; trust (Q2).
16. Adapters (decision 10): no project of their own. Each new adapter
    lands with the API that needs it, inside the existing engine, with its
    fake-server test and a line in the Adapters table. The "registry" stays
    what the spec's validation already checks until there are enough to be
    worth listing in the editor and Fix. Folding the RestImageSpec image
    machinery into adapters is a separate decision, made later on its own
    merits.

## Decisions (2026-11-01)

1. **Approve is dropped for connectors.** A non-admin's or Builder's draft is
   held as Changed until an admin saves a page naming it, so the gate moves to
   the page (see decision 7 for what replaces "in use").
2. **Trust: not drawn from internal versus external.** DLP as a whole is
   revisited after everything else, and Q2 with it.
3. (Q4) **Webhooks' first version feeds watches.** Messaging's push side
   and Bot Framework move once, with channels. (Revised 2026-11-01: it fed
   flows too, until they were retired, decision 9.)
4. (Q5) **Desktop programs get their own per-user page,** desktop MCP with
   them.
5. (Q6) **Reconfiguring is highlighted where it is needed,** on each page and
   in one admin notice, with a checklist before deploying; the owner has the
   keys ready and sets the LLM tiers first. No report (built, then dropped),
   and no "restore from old settings" button: that would be migration code
   under another name.
6. (Q7) **Pages store plain names.** Built-in names are reserved, and
   `peer:<name>` stays.
7. **References replace "in use".** A connector has no on/off state: its
   references guard its deletion and show on its row. The call gate goes once
   every call path is checked; the Changed hold on edits stays.
8. **One migration, written last,** from both starting points (before plan B,
   and plan B) into the final shape, once the design and code are final.
   Reconfiguring, highlighted, is the fallback for anything it does not cover.

9. **Flows are retired** (2026-11-01). The connector chains under Admin >
   Extensions > Flows, their `flow_<name>` tools and an event starting one
   are removed: nothing in a week of logs ran one, and agents chain calls
   themselves. A flow's steps no longer count as references. Saved flows
   (the `service_flows` table) are inert, and Clear old connection records
   removes them. The code is in git history should a webhook-to-flow need
   appear. System Hooks' own flows (`core/flows`) are a different thing and
   stay. `message.send` has no caller until channels; its four templates
   (Slack, Discord, ntfy, ntfy with a file) stay for them.

10. **Adapters** (2026-11-01, see Adapters). Go primitives for shapes of
   data and transport, named in a connector's spec where each applies, never
   stored apart; a builtin stays for one provider's own logic. Today's
   `send`, `read`, `as`, wait and key names are the first adapters. A rule
   for where Go goes, not a framework: nothing is built ahead of an API that
   needs it, and the word stays with spec authors, the Builder and Fix,
   never a thing an admin manages on a page.

## Still open

- **Q2. Trust,** with DLP: leaning toward a policy per destination, set by
  hand (what is gohort willing to send to this model, this search provider),
  never inferred from the address.
- **Q3. The key store's shape:** the vault absorbs connection keys, connection
  spaces absorb the vault, or two stores with the same rules. Not needed for
  the rework.
