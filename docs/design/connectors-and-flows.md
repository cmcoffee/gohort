# Connectors and flows: one way to think about every integration

**Status:** stages 1 to 5 built, 2026-10-30 (the map, `../integrations.md`;
connectors read inside to outside; gohort's own providers listed as
connectors; saved flows checked by contract; System Hooks' programs and
peers listed as connectors; events start flows that send). Builds on
`unified-connectors.md` (the service spec) and the System Hooks flows
(`core/flows`).

## Why

gohort has grown many ways to reach things: credentials, services, polls,
messaging relays, MCP servers, peers, System Hooks, API tools. Each makes
sense alone. Together they are hard to explain, to an outsider or to the
owner, because each answers the same questions in its own words.

The model was settled with the owner on 2026-10-30, in steps:

- The question is **where does gohort get something, and what does it do
  with it**.
- Flows are deterministic: "the non-deterministic approach will be the agent
  running the deterministic flows."
- A CLI belongs with the connectors, not the flows: gohort owns pandoc no
  more than it owns an API.
- A connection is "basically transports and endpoints".
- Connectors are capabilities with two sides, an inside and an outside, and
  "flows are used to connect connectors". So connectors "end up being more
  gohort wrappers".

## The model

- **Connector**: a wrapper around something gohort does not own.
  - **Inside**: a contract gohort defines (`image.generate`,
    `speech.transcribe`, convert a file, send a message), and events it
    raises (a message arrived, a watched API changed).
  - **Outside**: a transport (HTTP, a program on this machine, MCP, a peer,
    messaging; later WebSocket) and its endpoints (paths, command lines, MCP
    tools), with what reaching them takes: a key, a program, network access,
    approval.
  - **Mapping**: how the inside becomes calls on the outside and the answer
    becomes the contract's output. In a service spec, `provides` is the
    inside and its mapping, `provider` the outside, `operations` the
    endpoints.
- **Flow**: deterministic wiring from connector to connector. The output of
  one is the input of the next.
- **Agent**: decides which connectors to call and which flows to run. The
  only part that is not deterministic.

A credential is not a connector: it is the key a connector's outside uses.
A template is a connector and its credential, packaged with questions.

## Rules

1. **Agents decide; connectors and flows do.** Neither a connector nor a
   flow makes a judgment.
2. **Reach stays on connectors.** Keys, programs, network access and
   approval all sit on a connector's outside. A flow owns nothing it
   reaches; it only calls connectors.
3. **A mapping is not a flow.** A connector's steps (submit, wait on
   status, read) are its mapping, inside one connector. "Flow" means only
   wiring between connectors, so the word means one thing.
4. **The same contract means interchangeable.** gohort talks only to the
   inside, so which connector serves a capability is a setting.
5. **Programs run in the sandbox.** A CLI connector's endpoint gets a folder
   of its own, its words quoted, no network unless the connector has it, and
   limits on time and size.
6. **Flows are checked by contract.** Each step's input is typed by the
   contract it calls, and must be fed by an earlier output of that type, so
   a flow that cannot fit together is refused when it is saved, not when it
   runs.

## What exists, as connectors

| Today | Inside | Outside | Listed as a connector? |
|---|---|---|---|
| Service (`core/service`) | its `provides` contracts | HTTP, or a program | yes |
| Messaging relay | messages in and out | the messaging service | yes |
| Poll (`rest_poll`) | event: the answer changed | HTTP through a credential | yes |
| MCP (`remote_mcp`, `desktop_mcp`) | its tools | MCP | yes |
| Adapted System Hook (`file_conversion`) | a job: convert, read | a program | yes, managed from System Hooks |
| Native System Hook (`core/flows`) | a job | a program | yes, one row per program, marked Program (stage 5) |
| Peer | models, embeddings, speech, transcription | another gohort | yes, marked Peer (stage 6) |
| gohort's own providers (local embedder, built-in image code, local speech and transcription endpoints) | the same contracts | written in Go | yes, marked Built in (stage 3) |

## Stages

1. **The map.** `../integrations.md`. Built.
2. **Connectors read inside to outside.** Built 2026-10-30. A service's
   summary is its wrapper line, `Speech ⇐ speak ⇐ api.elevenlabs.io (key
   elevenlabs_speech)`, or for a program, `Image editing ⇐ edit ⇐ magick on
   this machine (no network)`: what gohort asks, the endpoints that do it,
   what it reaches with what. The Connectors row, the provider choices in
   the capability settings, Builder's connector tool and the Fix form
   ("What it is") show it, and Fix's assistant is told it. Presentation
   over the same spec; no migration.

   A service giving several capabilities names them on one line and counts
   its endpoints; its row's Endpoints lists each endpoint (method and path,
   or a program's command lines) with what it is sent and from where, what
   is read from its reply, and what uses it (a step, the health check, or
   nothing), read from the spec (`Spec.Endpoints`). A declared schema per
   endpoint (its full request and reply) is not built: it would earn its
   place by checking a mapping against it on save, giving Fix the real
   shape, or offering an unused endpoint to agents as a tool.
3. **gohort's own providers listed as connectors.** The local embedder, the
   built-in image providers, and the local speech and transcription
   endpoints appear as built-in connectors: read-only rows with the same
   wrapper line, health and test, read from the settings they already use.
   Each capability's setting then chooses among connectors, built-in or
   added. Nothing stored moves, so no migration, and the built-in Go image
   providers stay rather than being deleted (the question left open by
   `unified-connectors.md` stage 3).

   Built as (2026-10-30): `apps/admin/connector_builtins.go`. Five rows
   after the stored connectors, named `built-in:<what>` (a colon no
   connector name can hold): embeddings, transcription, speech, and the
   Gemini and OpenAI image code. Each reads its settings and gives the
   wrapper line (`Embeddings ⇐ embeddings (model nomic-embed-text) ⇐
   localhost:11434 (no key)`), whether it is in use, off, or replaced by a
   peer or connector (and which), and where it is set up. Test runs that
   settings section's own test on what is stored, and only while the
   built-in is the one in use (a peer or connector choice overwrites the
   local endpoint fields). The Connectors table's rows gained `stored`,
   which gates Approve, Edit spec, Export and Delete, and `state`, the
   badge: Approved, Pending or Built in. Health on a timer is not given to
   built-ins: their settings have no free check to run. Web search, page
   rendering, mail and the LLMs are built-in providers too; web search has
   its contract and row since stage 8, and the rest wait for theirs
   (stages 9 to 12).
4. **Flows checked by contract.** A flow is saved as steps, each naming a
   connector and a contract, with its inputs fed from the flow's own inputs
   or an earlier step's outputs. Saving checks every input is fed, by a
   value of the right type (text, number, json, file). A saved flow is a
   tool an agent can be given, with typed inputs, a test that runs it on
   sample inputs, and a cost that is the sum of its connectors' calls.

   Built as (2026-10-30): `core/service/flow.go` and `flow_tool.go`, and
   Admin > Extensions > Flows (`apps/admin/api_flows.go`, `page_flows.go`).
   A step names a connector, a contract and what feeds each contract input,
   one per line: `{input.<name>}` is a flow input, `{<step id>.<output>}` an
   earlier step's output, anything else the value itself, and in a text
   input a reference can sit inside a sentence. Saving refuses, with every
   problem listed: a reference to nothing or to a later step, a type that
   does not match (a number may feed text), a contract input that is not
   one, a required input nothing feeds (a connector's default counts), and
   a connector that does not give the contract. The last step's outputs are
   the flow's: text goes back to the agent, files are saved in its
   workspace; files an earlier step made are removed. Each flow is the
   agent tool `flow_<name>`, its inputs the tool's parameters (a file input
   is a workspace path), offered only while the flow exists. The row reads
   the flow left to right, `(subject) ⇒ image.generate on acme ⇒ (image)`,
   with what holds it up (a connector not approved yet) or what a run
   costs. Test runs it on sample inputs (a file input left out takes its
   contract's sample) and takes one daily test from each step's connector.
   Flows are the deployment's, made by an admin, and stored beside the
   connectors (`service_flows`); nothing existing became one.
5. **System Hooks' programs as CLI connectors.** Each program a hook uses
   (pandoc, ffmpeg, yt-dlp) is listed as a connector: inside, the jobs it
   does; outside, the program and whether it has the network. The hook's
   steps become that connector's mapping. The downloader's network access
   becomes a setting of its connector. A first step can derive these rows
   from the hooks as they are stored, so nothing moves.

   Built as (2026-10-30), the derived first step:
   `apps/admin/connector_programs.go`. One row per program a job runs,
   named `program:<program>`, marked Program, read from the jobs as they
   are defined: `Reads Word, OpenDocument and RTF attachments; Writes Word
   (.docx); Writes OpenDocument (.odt) ⇐ pandoc on this machine (no
   network)`. A job that has the network says so on its program
   (`yt-dlp … (network for Video downloads)`). The status says where it is
   installed, which jobs it does now and which it stands by for (another
   way is used first), or, when it is missing, what to install and for
   what. Endpoints lists each job's command lines, or "run by gohort,
   driving ffmpeg" for a job gohort runs itself, with what each is given
   and what comes of it. A program not installed is listed only while a
   job it would do has no other way to run, so the list says what to
   install rather than every alternative. Install, adapt and test stay in
   System Hooks, and the network stays a property of the job: making it a
   setting of the program's connector would store hooks as connectors,
   the migration this step avoids.
6. **Peers listed as connectors**, the same way: inside, what the peer
   offers; outside, the peer and its key.

   Built as (2026-10-30): `apps/admin/connector_peers.go`. One row per
   registered peer, named `peer:<name>`, marked Peer: `Inference,
   Embeddings, Speech ⇐ another gohort, gpu-box at gpu.example (its peer
   key)`. The status reads which settings here name the peer (the worker
   or lead LLM, Embeddings, Audio Transcription, Speech, Web Search, page
   rendering, and Image Generation through a renderer the peer added) and
   what it lends that nothing here uses, then its last check. Endpoints
   lists where each capability is served on the peer, what goes there and
   what comes back, with the model or provider the peer named. Test asks
   the peer again what it lends, the same free probe as Re-check under
   Peers. A row describes what is borrowed and what here uses it, nothing
   of the far side's own (a peer key lends resources, never the instance).
   Add, re-key and forget stay under Peers; nothing stored moves.
7. **Events start flows.** A connector's event (a message arrived, a poll's
   change, a schedule) starts a flow directly, and a flow can end by sending
   through a connector: `connector → connector → connector` with no agent in
   between.

   Built as (2026-10-30), with the owner's choices: an admin attaches the
   flow, monitor fires start it, and a daily cap bounds it. A new contract,
   `message.send` (text, an optional file, an optional `to`; a receipt
   `id`), is what a messaging API's connector gives, so a flow can end by
   sending. A flow names the monitor that starts it (`starts`: owner,
   monitor, and the text input the event's text goes into), set from
   Admin > Extensions > Flows ("Starts when"); the link lives on the flow,
   an admin's record, so monitors are untouched and a watch connector
   rebuilding its monitor cannot drop it. Saving refuses an event-started
   flow that has a required input the event cannot feed, or no
   `message.send` step (nothing would see what it made); one flow per
   monitor. On a fire, the orchestrate waker runs the flow first
   (`runEventFlow`): run, the flow is the delivery and no agent is woken;
   not run (past `event_runs_per_day`, default 24, or a step failing), the
   monitor delivers the event its usual way with why in front, so nothing
   is dropped. The run's record reads "Delivered (the flow X ran a, then
   b)". Messages arriving on a messaging relay starting a flow, and agents
   attaching flows, are left for later. A multipart send takes a file every
   time, so a service with separate text and file endpoints is two
   connectors; `message.send`'s contract test sends a sample picture so
   either kind can be tested.

   Templates give `message.send`: Slack (`chat.postMessage`, a bot token)
   and Discord (a bot token in a header), text only; ntfy as JSON, text
   only; and ntfy with a file, a PUT of the file with the message in the
   query. A service whose secret sits in its address (Telegram's bot path,
   a Slack or Discord webhook URL) has none: a spec never holds a secret,
   and a credential is a header, a query value or basic auth.

Each stage ships on its own and leaves everything existing working.

## Every capability through a connector

The owner's goal, 2026-10-30: every capability gohort has can be extended
by a connector. Each one gets the same five parts as embeddings did: a
contract and its check, gohort's own provider as a built-in row, the
settings page offering approved connectors (with a pointer while there is
nothing to choose), templates, and tests. None needs a migration: a
setting gains a `service:<name>` value.

8. **Web search.** Built 2026-10-30. `web.search`: a query (and a count)
   in, `results` out, a list of title, url and snippet, each result needing
   a web address. A spec reads a provider's own result fields into those
   with an output's `fields` (`path "organic", fields {"title": "title",
   "url": "link", "snippet": "snippet"}`); a reply with no list there reads
   as no results, so the contract test (a search for a common phrase) must
   find something, which is what catches a spec reading the wrong place.
   The web_search tool sends a `service:<name>` provider through the
   connector (`service.Search`), which prices its own calls; Web Search
   offers approved ones under Provider, and refuses to save one that is not
   approved. gohort's own search is the `built-in:web-search` row.
   Templates: Brave, Serper, Tavily.

   Source Hooks, 2026-10-30. The API and RAG hooks are listed as
   connectors, read from the hooks (`hook:<name>`, in a Source hooks
   group): what each gives, its query, host and the key it keeps itself,
   what it is used for, and its endpoint. A paywall hook is not listed: it
   is a key sent when fetching certain sites, and belongs with API
   Credentials. A search connector does what an API hook does: its
   `web.search` binding's `research` (topics, or always) puts it among the
   sources research asks on those topics, through core/sourcehooks' seams,
   so the research app is unchanged; its `tool` (a name and a description)
   offers it to agents as a tool of its own, gone from every catalog when
   the connector stops offering it. Every search's results, a hook's,
   a provider's or a connector's, have a Google redirect link marked
   (`service.MarkUnopenable`). Not done: converting a stored hook into a
   connector (one admin click per hook, when wanted), RAG hooks as a
   contract of their own, paywall hooks under API Credentials.
9. **Page rendering.** Built 2026-10-30. `page.render`: an address in,
   the page's readable `text` out (and its `title`, when the service gives
   one), checked for text. Page Rendering (Browser) offers approved
   connectors beside this instance and peers, and refuses an unapproved
   one; core's one routing point (`installBrowseRouting`) sends a
   `service:<name>` source through `media.PageService`, so browse_page,
   the sandbox hook and every fallback from a plain fetch follow it. The
   public-web-only check runs before any renderer, and again in the
   service (`renderVia`), so a flow calling the contract keeps it too.
   The title leads the text unless the text already opens with it. Built-in
   row `built-in:page-rendering`. Templates: Jina Reader and Firecrawl,
   which give readable text; a renderer that gives raw HTML (Browserless)
   would need its HTML read to text first, and is left out.
10. **Mail.** Built 2026-10-30. `mail.send`: to, subject, body, an
    optional from and file; separate from `message.send` because mail has
    a subject and recipients. The mail settings gain Provider ("smtp" or
    `service:<name>`, Send with, offered once a connector is approved; SMTP
    alone shows a pointer to Templates instead). Both senders follow it:
    core's `SendNotification` (signup approvals, reports, alerts) and the
    send_email tool, through `media.MailService`, from the same From an
    SMTP send would use (the web override, then the settings' From; blank,
    the connector's own). SMTP's server and sign-in show only while SMTP
    sends; From and Default Recipient serve both. Built-in row
    `built-in:mail` (off while no server or From is set). Templates:
    Resend, Postmark, Mailgun, text bodies only.
11. **File conversion.** Built 2026-10-30. `file.convert`: a file, its
    format and the format wanted (extensions) in, the converted file out.
    It plugs in where System Hooks already lets an admin replace a job's
    program, the adapted flow: an adapted flow names a connector
    (`service`) in place of steps, and System Hooks' Adapt form offers
    "Done by" (commands on this machine, or an approved converter) once one
    exists. The job's file is sent, the job's own format is asked for (its
    result's default extension), and what comes back is checked by the job
    exactly as a command's result is (`flows.ConvertService`, from
    `core/service`). A connector cannot do a job that starts from a link or
    gives several files. The row reads "the connector X"; Prefer still
    orders it before or after the native flow. No built-in row: the
    programs are already listed (stage 5). Template: ConvertAPI, one
    multipart call with the file back as base64.
12. **LLMs**, last and different in kind: a model call streams, calls tools
    and thinks, and the contract engine has no streams yet. First the
    worker and lead listed as built-in rows and an OpenAI-compatible
    template; then a chat contract with streaming.

    12a built 2026-10-30: `built-in:worker-llm` and `built-in:lead-llm`,
    each reading as its tier (`Worker LLM ⇐ anthropic (model …) ⇐
    api.anthropic.com (key set)`, a model server's address, Bedrock's
    region, or the peer it runs on), whether it is set (an unset lead says
    the worker answers for it), and Test running the tier's own live check.
    12b built 2026-10-30, with the owner's yes to both of its questions. A
    model is not mapped step by step: the `llm.chat` binding names a
    `dialect` gohort's own clients speak (openai at any address; anthropic
    and gemini only at their own hosts), with a default model and context,
    and is priced per million input and output tokens. A tier whose
    provider is `service:<name>` is resolved where a peer is
    (`ResolveModelProvider`, via `media.ModelService`): the dialect's
    client, the connector's address and its credential's key, read fresh on
    every build. That key is the one exception to keys staying in the
    store: a model client streams its own requests, so
    `SecureAPI.ModelKey` gives it, only from an enabled credential of the
    deployment's, of a bearer, header or query kind, for an address the
    credential allows. Each call is counted and priced on the connector's
    row (`retryLLM` notes it), and the connector's token prices replace
    the tier's rates under Costs in every estimate (the form keeps showing
    the stored rates). Model Privacy judges such a tier by the connector's
    own address, and not private when the connector cannot be read (a
    tier's blank endpoint would otherwise read as a local default). Tools
    are native for every dialect. Test is a short real chat. Templates:
    OpenRouter, Groq, Together, Mistral, DeepSeek, any OpenAI-compatible
    API, and Anthropic with its key in a credential. A dialect gohort does
    not speak still needs Go.

## Nothing locked in Go

The owner's direction, 2026-10-30, past "extendable": every provider is a
connector, shipped as a default that can be read, tested, fixed (✨ Fix) and
replaced. Go keeps the contracts and the transports; the providers become
specs. An HTTP provider becomes a spec outright; one that needs Go (SMTP,
the browser, DuckDuckGo's page, Bedrock, Ollama's native client, the LLM
dialects) becomes a connector naming a built-in transport. Two ways to make
one thing are not kept side by side, since then nobody can tell when the
other one breaks: the owner chose a forced migration. One capability at a
time: reach parity in the connector engine, move the stored settings onto a
connector once at startup (marker-guarded, shown under Admin > Migrations,
the same for a fresh install from the defaults), and delete the Go provider
in the same change. The migration code goes in a later release.

1. **Embeddings.** Built 2026-10-30. Parity the engine gained, all generic:
   an output's `or` (other places it may be, tried in turn: data[].embedding,
   embeddings, embedding) and `single` (one item read as a list of one); a
   host that answers 429 is paused until its Retry-After for every call
   (`core/media`'s pause, which the governed dispatch now feeds by keeping
   Retry-After on a 429's reply); and a connector at a loopback or private
   address queues behind the local model, as the built-in embedder did.
   `MigrateEmbeddings` (core/service, run where the credential store comes
   up) writes the settings as connector `embeddings` (`POST
   {endpoint}/embeddings`, the model when there is one), the key as bearer
   credential `embeddings_key`, approves it, and names it in the settings.
   The settings keep their endpoint and model, which name the space the
   vectors are in (EmbedVersion, the chunk stamp), so nothing is
   re-embedded; saving the same connector again keeps them. The HTTP code is
   now the peer transport only, and a config naming neither a connector nor
   a peer is refused. The Embeddings page lost endpoint, model and key: it
   is on/off, "Embed with" (connectors and peers) and the prefixes, with a
   pointer to the templates (Ollama, llama.cpp / vLLM / OpenAI-compatible,
   OpenAI) while nothing embeds. The `built-in:embeddings` row is gone.
2. **Transcription.** Built 2026-10-30. The engine already had parity
   (multipart uploads, a text output read from JSON); what moved was the
   size and time the old transcriber allowed: audio handed to a connector
   may be 100 MB (what the media jobs extract at most) and the migrated
   operation waits five minutes. `MigrateTranscription` writes settings
   that name an endpoint as connector `transcription` (`POST
   {endpoint}/audio/transcriptions` as a multipart form: the audio, the
   model when there is one, the prompt and language when given,
   response_format json), the key as credential `transcription_key`.
   Settings with no endpoint move nothing: there was no default, so
   transcription stays off until a connector or peer is chosen. The
   settings keep only the model, which a peer borrowing transcription is
   told. Serving transcription to a peer now asks whether a connector
   transcribes here, rather than whether an endpoint is stored. The page is
   on/off and "Transcribe with" (connectors and peers); its Test sends a
   second of a tone through the configured path. New template: whisper.cpp /
   OpenAI-compatible (transcription). The `built-in:transcription` row is
   gone.
3. **Speech.** Built 2026-10-31. Parity the engine gained, all generic: a
   new contract, `speech.voices` (a list of ids, or objects naming one
   under id or voice_id with a person's name under name), which fills the
   Voice field's choices where the old speaker read GET /audio/voices; a
   JSON body leaves out a field whose input was not given, as a multipart
   send already did, rather than sending null (a voice nobody chose is the
   server's own); and an ogg reply keeps its extension. `MigrateSpeech`
   writes settings that name an endpoint as connector `speech` (`POST
   {endpoint}/audio/speech` for mp3, the model and voice as defaults when
   set) with the voice list unless the server answers that it has none
   (404, 405, 501: OpenAI's), the key as credential `speech_key`. The
   settings keep the voice (chosen on the page, from the connector's list;
   an agent's own voice is still sent in its place) and the model (what a
   peer is told). Serving speech to a peer asks whether a connector speaks
   here; its voice list goes to the peer as bare ids, or objects with a
   name. The page is on/off, "Speak with" and Voice. New template: Kokoro /
   OpenAI-compatible (speech); ElevenLabs' gains its voice list. The
   `built-in:speech` row is gone.
4. **Images.** Built 2026-10-31. The engine already had parity: the Gemini
   and OpenAI templates make the same requests the Go code did (Gemini's
   picture read from among its reply's parts, a wide picture 16:9; OpenAI's
   1792x1024 or 1024x1024), with two differences. OpenAI's picture comes
   back as pixels rather than an address (b64_json), so a peer can borrow
   it, and each is priced per picture. The OpenAI code's one retry of a
   5xx is not kept: whether to repeat a billed request is the spec's to
   say, and no spec can yet. `MigrateImages` installs the template for the
   provider the settings named (a blank one meant Gemini) as an approved
   connector, its credential holding the key the settings used: Image
   Generation's own, or else the key of an LLM tier on the same provider,
   the worker's first. A setting naming one with no key anywhere made no
   pictures, and is turned off. The setting names the connector and its key
   row is removed. Image profiles (`<name>_provider`) were read and never
   written, and are gone. The page is "Make pictures with" (every approved
   connector that makes pictures: rest_image backends, peers' renderers,
   services) or Disabled, and a save naming anything else is refused. The
   `built-in:gemini-images` and `built-in:openai-images` rows are gone.

   Next, in order: web search, page rendering, mail, the LLM tiers, then the
   built-in transports and peers.

## Migrations

- Stages 1 to 3 and 6 need none: they show what is stored, differently.
- Stage 4 adds saved flows beside everything else; nothing existing
  becomes one. Stage 7 adds fields to a flow, and nothing to a monitor.
- Stage 5 needs none if the connector rows are derived from the stored
  hooks. Storing hooks as connectors instead would be a migration, and is
  not proposed until the derived rows have been lived with.
- The spec's field names stay (`provider`, `provides`, `operations`) even
  though the words are now outside, inside and endpoints: renaming them
  would migrate every stored spec and every template.

## The line between flows and pipelines

gohort already has two structures with steps: pipelines (stages an LLM
runs) and machines (phases an agent moves through). Those are where a model
decides. A flow is where nothing decides. Keeping that line is what stops
this from becoming a second workflow engine: a flow gets no branches beyond
a step's failure, and no model step whose answer it continues with.
Anything that needs judgment is an agent's turn, which may run flows.

## Open questions

- **Does a flow ever call a model?** Transcribing or embedding is a model,
  but its output is checked by a contract. Summarizing is not. The line, as
  built: a step calls a contract, and there is no chat contract, so a flow
  never calls a chat model.
- **Whose flows.** Built deployment-wide, made by an admin, like
  connectors. A person's own flows, promotable to everyone's through the
  same approval as tools, wait until flows have been lived with.
- **Builder.** Builder does not draft flows yet; the form's ✨ helps fill
  one in.
- **Admin's groups.** Capabilities and Extensions today. Once System Hooks,
  peers and built-ins are listed as connectors, a Connectors tab and a
  Flows tab may say it better. Left until stages 3, 5 and 6 have been lived
  with.
