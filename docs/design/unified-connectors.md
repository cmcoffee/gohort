# Unified connectors: how it talks to a provider, what it gives gohort

**Status:** stages 1 to 5 built, 2026-10-30 (`core/service`); streams, stages 6 and 7 proposed. The plain map of
how this sits beside credentials, System Hooks and peers is `../integrations.md`. Related: `connector-templates.md`
(the config surface a template puts over a connector), `templates.md` (recipes),
`core/flows` (System Hooks), `core/apijob` (the job pattern on api tools).

## Problem

Every integration in gohort answers two questions at once: how to talk to the
service (where it is, how to authenticate, what to send, where the answer is),
and what that service is to gohort (an image generator, a transcriber, a source
that wakes an agent). Today each connector kind answers both in one spec shape
of its own, with a Go handler per kind:

| Kind | What it gives gohort | How it talks |
|---|---|---|
| `rest_image` | an image backend | submit, poll, fetch, all in its own spec |
| `rest_poll` | a source that wakes an agent | one call on a credential, on a timer |
| `rest_messaging`, `bot_framework`, `messaging_bridge` | a chat channel | per-kind specs |
| `remote_mcp`, `desktop_mcp` | tools | an MCP server |
| `desktop_command` | one tool | a local command |
| `file_conversion` (System Hooks) | a file conversion job | sandboxed commands |
| `peer_relay` | inbound webhooks | a peer's address |

Three things follow from that.

1. **The same machinery is written several times.** "Submit, get a job id, poll
   until done or failed, fetch the result" exists in `rest_image`, again in
   `core/apijob` for api tools (whose own comment says it is the general form of
   the image engine), and in a smaller form in `rest_poll`. Each has its own
   path grammar and its own gaps.
2. **Capabilities gohort uses itself are still Go per provider.** Image
   generation's built-in Gemini and OpenAI providers, embeddings
   (`core/embeddings.go`), transcription and speech (`core/media`), and the chat
   models (`core/llm_*.go`). A provider that changes its API, or one gohort has
   never heard of, needs a release.
3. **Only System Hooks can repair themselves.** A failing hook has a Fix: the
   worker reads why it fails, tries steps in the sandbox and proposes a flow the
   admin saves. A connector has nothing like it. `rest_image` has no live test
   (its test checks the config and says so), and a generation that fails is
   reported to whoever asked, never recorded on the connector.

The 2026-10-30 lead outage shows the cost. Calls to the lead model were refused
for three days with the provider's own explanation in every error ("use the
top-level 'system' parameter"), the worker took over each time, and nothing
turned that message into a proposed fix. It took a person reading the log.

## Goal

One connector record with two halves:

- **The provider half:** how gohort talks to this service. Per service, written
  as data, and the only half a fix ever edits.
- **The capability half:** what the service gives gohort, as one or more
  capabilities from a fixed list gohort defines. Each capability is a contract
  with inputs, a required output and a check, like a System Hooks job.

Then test, fix, failure records, approval and export are built once, against the
contracts, for every connector.

## The provider half

```json
"provider": {
  "transport": "http",
  "base_url": "https://api.example.test",
  "auth": {"credential": "example_key"},
  "headers": {"Example-Version": "2026-10"},
  "operations": {
    "submit": {"method": "POST", "path": "/v1/jobs", "send": "json",
               "body": {"prompt": "{prompt}", "size": "{width}x{height}"}},
    "status": {"method": "GET", "path": "/v1/jobs/{id}"},
    "models": {"method": "GET", "path": "/v1/models"}
  },
  "limits": {"timeout_secs": 60, "max_bytes": 52428800},
  "health": {"call": "models", "expect": "data", "every_mins": 30}
}
```

- **transport** is one of a small set, each a Go engine:
  - `http`: through the same call path as a SecureAPI credential, so the host
    allowlist, Private mode and the audit apply unchanged.
  - `command`: a sandboxed local command, as System Hooks flows run today
    (`core/flows/run.go`).
  - `mcp`: a remote or desktop MCP server.
  - `peer`: a resource another gohort lends, under the peer-key rule (a peer
    lends resources, never its instance).
- **auth** names a credential and never carries a secret, as today.
  `{"none": true}` is a local, unauthenticated endpoint (a LAN ComfyUI).
- **health** names a free operation that proves the service is up and the
  credential is accepted (a model list, an account or credits endpoint), and
  what its reply must hold. See "Health and tests" below.
- **operations** are named requests. Their templates take `{token}` values,
  inserted into paths URL-escaped and into bodies by the operation's `send`
  (below). The tokens are the capability's inputs plus anything an earlier step
  saved (`{id}`, `{file_id}`).

## The capability half

```json
"provides": [{
  "capability": "image.generate",
  "steps": [
    {"call": "submit", "save": {"id": "job.id"}},
    {"wait": "status", "until": "state == done", "fail": "state == failed",
     "reason": "error.message", "every_secs": 2, "max_secs": 180}
  ],
  "output": {"image": {"path": "outputs[type=image].url", "as": "url"}},
  "defaults": {"width": 1024, "height": 1024},
  "cost": {"per_call": 0.04, "from_reply": "usage.cost_usd"}
}]
```

- **capability** names a contract from the registry below. An unknown name is
  refused at save, not ignored.
- **steps** run in order. A `call` step makes one operation and `save`s values
  from its reply as variables for later steps (`{id}` above). A `wait` step
  polls an operation until its `until` condition holds, stops on `fail` with
  `reason` as the message, and gives up after `max_secs`. A synchronous
  provider has one step.
- **output** fills the contract's outputs from the last step's reply. See
  "Values, encodings and streams" below for what `as` can be.
- **defaults** fill contract inputs a caller left out.
- **cost** says what one call costs. See "Cost" below.
- **One provider, several capabilities.** A service offering images and speech
  is one connector with two entries, sharing its auth and base URL.

## Values, encodings and streams

Most services differ only in how values travel, so that is data too.

**Values.** Every contract input and output has one of these types:

| Type | What it is |
|---|---|
| `text` | a string |
| `number` | a number |
| `json` | any JSON value |
| `file` | bytes with a media type and a name, kept on disk past a size threshold, never wholly in memory |
| `url` | a link gohort fetches into a `file` (see below) |
| `stream` | bytes or events that arrive over time and are passed on as they come |

**Sending.** An operation's `send` says how its request carries values:

| `send` | Request body |
|---|---|
| `json` (default with a `body`) | the `body` template as JSON, `{token}` values typed: a string that is exactly `"{width}"` becomes the number |
| `form` | the `body` template as `application/x-www-form-urlencoded` |
| `multipart` | `parts`: each a value or a file input (`{"file": "{audio}", "purpose": "transcription"}`) |
| `file` | one file input as the whole body, with its media type |
| `none` | no body |

**Reading.** An operation's `read` says what its reply is:

| `read` | Reply |
|---|---|
| `json` (default) | parsed, and read with paths |
| `text` | the body as text |
| `file` | the whole body is a file; its type comes from Content-Type, checked against the bytes |
| `stream:sse`, `stream:ndjson` | events as they arrive; `each` is the path to the piece in each event, `done` the condition that ends it |
| `stream:bytes` | raw bytes as they arrive (speech audio as it is spoken, a video) |

**Output.** Each contract output names where it comes from and `as` what:
`{"path": "...", "as": "base64"}` (decoded into a file), `"url"` (fetched into a
file), `"text"`, `"json"`, or `{"from": "body"}` for the reply itself when the
operation reads `file` or a stream. A `json` output may add `each`, the path
within every element of the list at `path` to the piece kept: OpenAI's
embeddings are `{"path": "data", "each": "embedding"}`.

**Fetching a URL.** A result URL on the provider's own host goes through the
provider half (its credential, the allowlist, the audit). A URL anywhere else (a
CDN, a signed download link) is fetched with no credential, through the client
that refuses non-public addresses after DNS resolution, so a reply cannot steer
gohort into its own network or leak the key to another host.

**Size and time.** `limits` bound every transfer: `max_bytes` for a file
(default 50 MB, written to disk as it arrives), `timeout_secs` for one request,
and for a stream an idle limit and a total limit. A file or stream past its
limit is cut off and the call fails with what was exceeded.

**Streams to gohort.** A contract whose output is a stream hands it on as it
comes: speech starts playing before it finishes, a video reaches the page
through gohort's governed stream proxy (the path `OpenStream` already gives a
credential) with ranges passed through so a player can seek, a model's reply
arrives token by token. A stream into a provider (live audio to transcribe) is
the same in the other direction, `send: stream`.

**What still needs a transport in Go.** A connection that is not
request-and-reply over HTTP: WebSockets (realtime speech services), gRPC
streaming, WebRTC. Each is one engine written once. The operations, steps and
outputs above stay the same shape on top of it (a `send` frame template, frames
`read` like `stream:sse` events), so each service on that transport is still
data.

### Paths and conditions

One grammar for every path, replacing the three in use:

- `a.b.0.c`: fields and list indexes, as today.
- `{id}` and other tokens substituted into a path, as `rest_image` allows.
- `list[field=value]`: the first element whose field equals the value, and
  `list[field]`: the first element that has the field at all. This is what
  fixed indexes cannot express: a reply whose image is not always the same
  element of a list (Gemini's parts, where it is the one with `inlineData`).
- Conditions for `until` and `fail`: `path == value`, `path != value`, and a
  bare `path` meaning present and not empty.

Anything outside the grammar is refused at save, so a typo reads closed.

## The contracts

gohort owns these. Each says what goes in, what must come out, and the check the
output passes before anything uses it. The check is what makes a test and a fix
objective: a fix either makes the contract pass or it does not.

| Capability | In | Out | Check | Today |
|---|---|---|---|---|
| `image.generate` | prompt, width, height, negative?, seed? | image | decodes as PNG, JPEG or WebP, non-zero size | Gemini and OpenAI in Go; `rest_image` |
| `image.edit` | the above plus input images | image | as above | `rest_image` img2img |
| `speech.transcribe` | audio | text | non-empty UTF-8 | `core/media/transcribe.go` |
| `speech.synthesize` | text, voice? | audio | sniffs as audio | `core/media/speech.go` |
| `music.generate` | prompt, lyrics?, style? | audio | sniffs as audio | api tools a person writes |
| `embed.text` | text | vector | the configured length, all finite | `core/embeddings.go` |
| `file.convert` | a file, a job id | the job's output | the job's own check | System Hooks (`core/flows`) |
| `source.watch` | none (on a timer) | a change that wakes an agent | parses as the declared shape | `rest_poll` |
| `messaging` | send and receive | messages | per direction | `rest_messaging` and friends |
| `tool` | named params | text or JSON | size bound | api tools, `remote_mcp`, `desktop_command` |
| `chat.model` | messages, tools | a response | the protocol adapter's | `core/llm_*.go` |

Each contract also carries **test inputs**: the smallest real call that proves it
works (a 256-pixel image of a red circle, one second of tone, one short
sentence). A test is that call through the contract's check.

## What stays in Go, and why

- **Transports** (`http`, `command`, `mcp`, `peer`): where the security lives.
  Credentials, the allowlist, the sandbox and Private mode are enforced here, so
  no spec can route around them.
- **Contracts and their checks.** A spec can never redefine what "an image" is.
- **One wait engine**: `core/apijob`, generalized and used by every connector,
  in place of the three copies.
- **Named helpers** for a step that is genuinely smart (ComfyUI's graph
  auto-wiring), as templates already do.
- **Chat-model protocols.** Streaming, tool calls and caching differ in shape
  between providers, and every turn depends on them. `chat.model` therefore
  picks a named protocol adapter (`anthropic_messages`, `openai_chat`,
  `gemini`, `bedrock`), and the provider half adds what is data: base URL,
  auth, headers and **fixups**. Fixups are a list of small named rewrites
  written in Go and switched on in data: `hoist_leading_system`,
  `drop_param:<name>`, `rename_param:<from>:<to>`, `require_header:<name>`. The
  2026-10-30 fix would have been `hoist_leading_system`, switched on.

Everything that differs between two services offering the same capability is
data.

## Health and tests

Three ways to know a connector works, in order of cost. Only the first runs on
its own.

1. **Health check (free, on a timer).** The provider half's `health` operation:
   a call the provider does not charge for (a model list, an account or credits
   endpoint, a status page) whose reply must hold what `expect` names. It proves
   the service is reachable and the credential is accepted, never that a
   capability produces the right thing. It runs every `every_mins`, only while
   the connector is approved, and never faster than once in five minutes. A
   provider with no free endpoint declares no health check. The connector is
   then known only from tiers 2 and 3, and the page says so rather than showing
   it healthy.
2. **Passive health (free, from real use).** Every real call already proves or
   disproves a capability. Its outcome goes into the connector's record (below),
   so a connector in daily use needs no paid test to be known healthy.
3. **Contract test (paid, on purpose only).** Each contract's test inputs, the
   smallest real call that proves the capability (a 256-pixel image of a red
   circle, one second of tone, one short sentence), run through the contract's
   check. It never runs on a timer. It runs when:
   - an admin clicks Test, shown the expected cost first (from the `cost`
     declaration) when it is not free;
   - a Fix checks its draft, which the admin started;
   - the connector is first approved, once.

   Contract tests draw on a per-connector daily test budget (default 5 calls),
   recorded as test spend so they show apart from real use. Past the budget a
   test is refused with what it would cost, and the admin can raise the budget
   for that connector.

A contract may also declare a **free check** when the provider offers one that
exercises the capability without charging (a dry-run flag, a validation
endpoint, a zero-step render). A test uses it in place of the paid call where it
exists.

## The adapt loop: record, fix

**Record.** Every call a connector makes updates its record, kept per
capability: the last success, the last failure with the provider's message,
counts for the current hour and the one before (as the lead's call counts work
today), the last health check, and what the calls cost. Admin > Connectors shows
"failing since 18:00: <message>", or "healthy, last checked 12 minutes ago".

**Fix.** The assist panel System Hooks already use (`core/assist`), opened on a
failing connector:

- The worker is given the spec, the contract, the recorded failure and a
  sample of the provider's reply.
- Its tools read web pages (the provider's docs) and run the test against a
  *draft* spec. It never sees a secret. The call goes out with the credential
  by name, as any call does.
- It may change how the service is reached (the provider half) and how each
  capability's steps and outputs read the replies. It keeps which
  capabilities the connector provides, their prices and the test budget:
  those are the admin's, and a draft that changes them is refused. For
  `chat.model`, only the fixups and headers.
- It shows its draft passing the contract's test. The admin saves it, and
  saving is a change like any other, asked once.

**Notice.** When a connector fails the same way a few times in an hour, the
admin gets one card: "example-images is failing: <provider's message>. Fix?" The
worker is the model that runs the Fix, so the loop works while the lead is the
thing failing, which is when it matters most.

## Cost

Each capability declares what a call costs, so every connector feeds one cost
total that monitoring can show and caps can read.

```json
"cost": {"per_call": 0.04}
"cost": {"per_unit": 0.006, "unit": "audio_second", "units_from": "input"}
"cost": {"per_call": 0.01, "from_reply": "usage.cost_usd"}
```

- **per_call:** a flat price per successful call, as a credential's
  `CostPerCall` is today.
- **per_unit:** a price per unit the contract measures: images, audio seconds,
  characters, tokens. Each contract names the units it can count (`image.generate`
  counts images, `speech.synthesize` input characters, `speech.transcribe` audio
  seconds), so a price never depends on parsing a provider's own words.
- **from_reply:** a path to the cost the provider reports in its reply, when it
  does. Used when present, with the declared price as the fallback and as the
  estimate before a call.
- **None declared:** the call is counted but unpriced, and the page says
  "unpriced" rather than showing zero.

Charged when the provider did the work, even if what came back then fails the
contract's check (the provider billed for it). A call the provider refused or
failed is counted as a call but costs nothing, unless the provider says
otherwise through `from_reply`.

**Where it goes.** Each call is one ledger entry: connector, capability, who
asked (the person, the agent, the app), whether it was a test, the units and the
cost. That is the per-user API ledger already agreed for cost caps (the
`RecordExternalCostFor` path that credential prices and search use today, given
fields instead of a label), so it feeds:

- the admin cost chart and monitoring's total, alongside the model spend
  `CostRates` already estimates;
- an agent's daily spend cap, which today counts only model rounds;
- an app's daily allowance, which today counts only its agent's asks;
- monthly caps per person and per credential, when those are built.

A cap is checked before a call, using the declared price as the estimate, so an
over-cap call is refused rather than charged and regretted.

## Governance

Unchanged rules, applied once instead of per kind:

- A connector that adds new outside reach is created unapproved, and an admin
  approves it. One that reaches only through a credential already approved for
  that host may go live on create, as `rest_poll` does now.
- An edit to an approved connector, a Fix included, is a change and asks once.
- No secret in a spec, a template, an export or a Fix's context.
- Unknown capability, transport, field or path syntax: refused at save.
- Peer transport lends resources only (`CLAUDE.md`, the peer-key rule).

## Storage, without a flag migration

- New connectors are saved as one new kind, `service`, whose spec is
  `{provider, provides}`.
- Existing kinds stay registered and keep working. Each gets a translator that
  reads its stored spec as the two halves (`rest_image` into an `http` provider
  plus `image.generate`, a hook flow into a `command` provider plus
  `file.convert`). Test, health and Fix then work on old connectors without
  rewriting them.
- Editing an old connector in the new editor saves it as `service`, one
  connector at a time, when a person chooses to. Nothing at boot rewrites
  anything.
- Export and import carry `service` like any kind. An old bundle imports as
  before.
- Templates are how these travel (next section).

## Templates: the shareable form

A template is the whole definition of a service, both halves, with the values
that differ per install left as questions: the address of a LAN service, which
credential holds the key, a model name. Installing it asks those questions and
creates the connector. So:

- **A template is the integration; a connector is one install of it.** "Acme
  images" is a template anyone can import; "acme-images" on this machine is the
  connector it made, with this machine's credential.
- **Templates already work this way** (`core/recipes`): questions plus a bundle
  with `{{question}}` slots, secrets going to the credential store. A template
  whose bundle holds a `service` connector needs no new mechanism.
- **Built-in providers ship as templates** (stages 3 and 5), so gohort's own
  Gemini or OpenAI image support is the same thing a person could write or
  import, and changes the same way.
- **A Fix improves the connector, and can be offered back to the template.**
  When a provider changes its API, the fixed provider half can be saved over
  the template (admin-only, behind the save-template confirmation that exists),
  so the next install starts from the working version.
- **Builder writes templates.** Asked to hook into a service, it drafts the
  two halves and names the questions, and an admin saves it as a template or
  installs it as a connector.

## Stages

1. **The base.** The contract registry, the `http` transport with the `json`,
   `form` and `none` sends and the `json`, `text` and `file` reads, steps with
   saved variables, the generalized wait engine, the path grammar, and
   `image.generate`. Modes not yet built (`multipart`, `file` sends, streams)
   are refused at save with the stage that brings them, never accepted and
   ignored. The
   `rest_image` translator. The free health check, the contract test with its
   budget, the record, and the cost declaration feeding the ledger. Admin's
   image provider picker lists every connector that provides `image.generate`.

   Built as: `core/service` (spec, contracts, transport, run, record, health,
   test); `core/apijob` gained the `list[field=value]` selector, `ValidPath`
   and a `Check` hook so the one wait engine serves both; `SecureAPI.
   DispatchHostScoped` for a keyless service on the LAN; `ConnectorCredentialUser`
   so an agent denied a key is not offered the image backend that spends it.
   Admin > Connectors shows each service's status line with Test (asked first,
   with the expected cost) and Check health; Builder drafts one with
   `connector create kind=service service_spec=...`. The test for an older
   `rest_image` backend runs through its registration with the same contract
   check; the full translator of old kinds comes with Fix in stage 2. Spend is
   recorded per connector and capability in the existing cost ledger (tests as
   their own source); who asked rides on the per-user ledger when it is built.
2. **Fix and the notice card** for connectors, on the System Hooks assist,
   with the translator that reads old kinds as the two halves. `multipart` and
   `file` sends, and `image.edit` on them.

   Built as: Admin > Connectors' Fix (`apps/admin/connector_fix.go`) with three
   tools, `service_check` (free), `service_request` (one operation of a draft
   and its raw reply; free for the health operation, else on the test budget)
   and `service_try` (a draft's contract test); saving an older image backend
   converts it to a service, off until approved again. `FromRestImage`
   translates a synchronous backend, a poll with the result at a path, and a
   result built into a URL (ComfyUI's view), keeping number tokens typed and the
   prompt suffix; a pasted ComfyUI workflow, an upload-first backend and the
   `{images}` token stay `rest_image` and say why. Wait steps save values, and a
   saved value may be empty when its path is there (`apijob.Present`). A
   service failing three times in two hours, more than half its calls, puts one
   notice in each admin's notifications, repeats folded into its count.
   `multipart` carries one file part (the governed upload); `file` sends the
   picture as the whole body through the governed stream. `image.edit` is
   reached by agents through `EditImageWithBackend`, one picture, no mask, via
   `RegisterImageEditBackend`.
3. **Built-in image providers as templates.** Gemini and OpenAI image generation
   become `service` templates, so an API change in either is a spec edit, and
   their Go is deleted.

   Built as: `core/recipes/builtin/gemini-images.json` and `openai-images.json`,
   each a disabled credential (a header key for Gemini, a bearer for OpenAI)
   and an unapproved service connector with a free health check on the model
   list and a declared price. `image.generate` gained an `aspect` input, a
   binding gained `wide` (inputs used when gohort asks for a wide picture: 16:9
   for Gemini, 1792x1024 for OpenAI), and paths gained `list[field]`, the first
   element that has the field (Gemini's picture among its text parts). The
   built-in Go providers are NOT deleted yet: an install whose image provider
   is "gemini" or "openai" uses a key from its image or LLM settings, not a
   stored credential, so removing them would break it until someone sets up a
   template. They stay until the owner decides; deleting them is one commit
   once no install depends on them.

   Templates on the admin page changed with it: the list starts empty and holds
   what was added (imported, saved, or a built-in picked under Add a
   template), and Add offers the built-ins, a template file, or a new one made
   from what was built. The row action that installs a template is Set up.
4. **The command transport.** A service whose provider is a local program:
   operations are `run` lines instead of requests, run by System Hooks' own
   sandboxed runner, so any capability can be served locally with the same
   test, health check, cost and Fix.

   Re-scoped 2026-10-30, with the owner: `file.convert` and a translator for
   adapted flows were dropped. System Hooks already convert files well, with
   their own page, test and Fix; making each adapted flow a service too would
   have given one flow two places to manage and fix it. Hooks stay as they are.

   Built as: `flows.RunCommand` (named file inputs and text values in place of
   a job's {in}, each word quoted, in the sandbox, no network unless the spec
   says so, a text value standing alone as a word refused when it starts with
   "-" so a prompt cannot pose as an option); `transport: "command"` with
   `run`, `read` and `network` (no address, credential or headers); the
   connector's summary names the programs it runs, for the admin approving it.

   With it, `convert_file` became a framework tool: every agent has it while
   any format added to System Hooks is on (one that needs the network, only
   online), hidden from tool pickers, and the per-format Agents switch is gone.
5. **The capabilities gohort uses itself,** one at a time: transcription,
   speech, embeddings, music. Each built-in provider becomes a template. The
   stream reads and sends arrive here, with speech the first to need them,
   and the governed stream proxy for a video a page plays.

   Built as, part 1 (2026-10-30): the contracts `speech.transcribe`,
   `speech.synthesize` (its unit, for a price, is the character) and
   `music.generate`, each tested on its own small sample (a one-second tone,
   a short sentence, a ten-second melody) and checked by what comes back (any text; audio a player recognizes by its
   header). Transcription and speech settings name a service as
   `service:<name>` beside the instance's own and a peer's; `core/media` hands
   those calls to `core/service` through a seam, so media keeps no import of
   it. A music service is a tool, `generate_music_<connector>`, that saves the
   song in the agent's workspace and sends it with the reply. Templates:
   `elevenlabs-speech.json` and `openai-transcription.json`, unpriced, with a
   setup note to declare the price. A request that reads a file now sends its
   JSON or form body (it went out empty); multipart with a file read is
   refused until something needs it.

   Built as, part 2 (same day): `embed.text` (texts in as one list,
   `vectors` out, one per text, all one length; priced per character), and
   `each` on json outputs to read a list piece by piece. Embeddings settings
   name a service beside this instance and the peers; core reaches it
   through a seam in `core/media` (core's export ceiling). Choosing one
   stores the service as the endpoint and the model its spec declares, so
   `EmbedVersion` and the chunk stamp change and vectors from another space
   are skipped until Repair re-makes them; a service that declares no model
   is stamped with its own name, since a blank stamp matches every row.
   Template: `openai-embeddings.json`, unpriced (OpenAI bills by the token).

   Streams stay unbuilt, for want of anything to consume one: read-aloud
   already plays a service's speech within a second, by asking for it a
   sentence at a time, and nothing plays a provider's video yet. They come
   with the first capability that needs them. The built-in
   providers stay in Go beside their templates: retiring them moves keys from
   settings into credentials, which is a migration. Connectors stay
   deployment-wide and admin-approved; the open question below on personal
   connectors is still open.
6. **The rest of the kinds:** `source.watch`, `messaging`, `tool`. The largest
   surface, and it may be enough to share the transport and wait engine with api
   tools rather than merge the two (see open questions).
7. **`chat.model`:** the provider half, the protocol adapters, the fixup list,
   and Fix choosing among fixups.

Each stage ships on its own and leaves every existing connector working.

## Open questions

- **Api tools and the `tool` capability.** An api tool (a TempTool with a job
  spec) and a connector providing `tool` overlap. Merge them, or share only the
  engine? Sharing the engine first is the smaller step.
- **Whose connector.** Connectors are deployment-wide and admin-approved today,
  while credentials are owner-first and per person. A person's own music
  service may want to be personal, like their credential. Not decided by
  stage 5's first part, which kept them deployment-wide.
- **Prices change.** A declared price goes stale when a provider reprices.
  Where a provider reports cost in its reply, `from_reply` keeps it honest; for
  the rest, should a Fix also notice a price that drifts from what the provider
  bills, or is that the admin's to keep current?
- **Free tiers and credits.** A provider with monthly free calls charges nothing
  until it suddenly does. Declare the allowance, or leave it to `from_reply`?
- **A WebSocket transport** for realtime services: when the first capability
  needs it, and whether a page's stream reaches it directly or through gohort.
