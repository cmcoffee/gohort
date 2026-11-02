# How gohort reaches things

gohort does a lot itself, and wraps the rest. Two words explain every
integration: **connector** and **agent**.

## Connectors and agents

```
            inside                          outside
gohort ⇄ [ a gohort contract ]  CONNECTOR  [ transport + endpoints ] ⇄ the resource
         make an image, transcribe,         HTTP and a key, a program here,
         convert a file, send a message     MCP, another gohort, a messaging service

AGENT:  decides which connectors to call
```

- **A connector is a wrapper.** It takes something gohort does not own and
  makes it speak gohort's language. Its **inside** speaks a contract gohort
  defines: make an image, transcribe audio, convert a file, send a message,
  or tell gohort that something arrived. Its **outside** reaches the
  resource: a transport and its endpoints, with whatever reaching it takes
  (a key, a program, network access, an admin's approval).
- **An agent decides.** It is the one part that is not deterministic: it
  reads the situation, calls a connector, reads the result,
  and decides again.

A voice note on Telegram, end to end:

```
Telegram connector (a voice note arrives) → transcription connector → agent → Telegram connector (the reply)
```

## What every connector does

The same four jobs, whatever it wraps:

1. **Translate.** Its mapping turns what gohort asks into calls on the
   resource's endpoints, and reads the answer back into the contract.
2. **Govern.** The key and the endpoints it may reach, the sandbox a
   program runs in, whether it has the network, and what reaches it: a
   services connector is reached through the pages that name it (Web
   Search, Image Generation, a Research Source, ...), the others once an
   admin approves them. What gohort can reach, and who allowed it, all sits on
   connectors.
3. **Measure.** A free health check on a timer where the resource offers
   one; a test, one small real call checked against the contract (tests can
   cost money, so they run only on a click, during a Fix, or on first
   approval, within a daily budget); and the cost of every call, in the cost
   ledger.
4. **Repair.** When the resource changes, ✨ Fix reads the failure, asks the
   resource, tries a corrected mapping and proposes it. It can change the
   outside and the mapping, never the contract or the price.

Because gohort only ever talks to the inside, **connectors with the same
contract are interchangeable**: hosted or local, one provider or another,
is a setting.

## The inside: contracts

What gohort asks of a connector: what each contract is, what it takes and
gives, what its check requires of an answer, and where gohort uses it. A
connector's editor (Edit on its row under Admin > Extensions > Connectors)
shows the same for each job it does.

<!-- contracts: made from core/service's registry; UPDATE_DOCS=1 go test ./core/service -run TestTheDocsListTheContracts rewrites it -->
| Contract | What it is | Takes | Gives | Checked for | Where it plugs in |
|---|---|---|---|---|---|
| `embed.text` | Vectors for texts, for semantic search: texts is a list of strings, and vectors a list of lists of numbers, one for each text. | texts (json, required), model (text) | vectors (json) | At least one vector came back, none of them empty, all the same length. The test embeds two texts. | Admin > Capabilities > Embeddings: "Embed with" |
| `file.convert` | A file turned into another format: from is its format and to the one wanted, each as an extension (docx, pdf, png). | file (file, required), to (text, required), from (text) | file (file) | A file that is not empty came back; the job that asked checks it is the format it wanted. | Admin > Capabilities > System Hooks: a job's Adapt, "Done by" |
| `image.edit` | A picture changed as a prompt asks. | prompt (text, required), image (file, required), negative (text), width (number), height (number), seed (number), steps (number), model (text) | image (file) | image is a picture gohort can show: PNG, JPEG, GIF or WebP, with a size. | Admin > Capabilities > Image Generation, and agents' image tools |
| `image.generate` | A picture made from a prompt. | prompt (text, required), negative (text), width (number), height (number), seed (number), steps (number), model (text), aspect (text) | image (file) | image is a picture gohort can show: PNG, JPEG, GIF or WebP, with a size. | Admin > Capabilities > Image Generation, and agents' image tools |
| `image.search` | Pictures already on the web. results is a list of objects: image_url (the picture itself), title, source (the site it is on) and link (the page it is on). | query (text, required), count (number) | results (json) | results is a list, and every image_url in it is an http or https address. The test, a search for a common subject, must find at least one. | agents' find_image, through the connector Web Search is set to |
| `mail.send` | An email: to, subject and body, from the connector's own sender unless one is given, with a file when one is given; id is the service's name for the message. | to (text, required), subject (text, required), body (text, required), from (text), file (file) | id (text) | Any answer: a reply at all means the service took the message. | Admin > System > Mail (SMTP): "Send with" |
| `message.fetch` | New messages from a messaging API: since is where the last fetch left off (the cursor it gave), and chat the conversation, for a service that fetches one at a time. messages is a list of objects: id, chat, from, from_name, text and at; cursor is where the next fetch starts. | since (text), chat (text) | messages (json), cursor (text) | messages is a list, and every message in it has an id. A message that does not say its chat is in the chat asked for. The test fetches without a cursor, as a link's first fetch does. | Bridges > Linked services: a linked service's messages reach its channels |
| `message.send` | A message sent through a messaging API, with a file when one is given; to is whom to send to, when the service needs one and the connector does not set it, and id is the service's name for the message. | text (text, required), file (file), to (text) | id (text) | Any answer: a reply at all means the service took the message. | Bridges > Linked services: the replies to what the same connector fetches |
| `music.generate` | A piece of music made from a prompt, with lyrics, a style, a title and a length when given. | prompt (text, required), lyrics (text), style (text), title (text), duration (number), model (text) | audio (file) | audio is sound gohort can play: MP3, WAV, OGG, FLAC, AAC or MP4 audio, judged by its first bytes. | an agent tool of its own, named generate_music_ and the connector's name |
| `page.render` | A page read the way a browser shows it: the readable text of the page at url, and its title when the service gives one. | url (text, required) | text (text), title (text) | text is not empty. | Admin > Capabilities > Page Rendering (Browser): "Render pages on" |
| `speech.synthesize` | A text spoken aloud, in a voice when one is given. | text (text, required), voice (text), model (text) | audio (file) | audio is sound gohort can play: MP3, WAV, OGG, FLAC, AAC or MP4 audio, judged by its first bytes. | Admin > Capabilities > Speech (TTS): "Speak with" |
| `speech.transcribe` | The words spoken in a sound file. | audio (file, required), prompt (text), language (text), model (text) | text (text) | Any answer the spec can read, empty text included: the test's sound is a tone with no words. | Admin > Capabilities > Audio Transcription: "Transcribe with" |
| `speech.voices` | The voices a speech service offers. voices is a list, each a bare id or an object naming the voice under id, voice_id or name (name is what people read, when there is an id too). | nothing | voices (json) | voices is a list, and each one names a voice. | the Speech (TTS) Voice field's choices, from the connector that speaks |
| `web.search` | A search of the web. results is a list of objects: title, url and snippet, and published when the engine says when a page was published. | query (text, required), count (number) | results (json) | results is a list, and every url in it is an http or https address. The test, a search for a common phrase, must find at least one. | Admin > Capabilities > Web Search: "Search with" |
<!-- end contracts -->

The inside faces both ways. gohort asks things of a connector (make, convert,
send), and a connector tells gohort things (a message arrived, a watched API
changed). The second is what starts work with no one asking: a message wakes
an agent.

## The outside: transports and endpoints

| Transport | Its endpoints | What reaching it takes |
|---|---|---|
| HTTP | paths and methods | a credential (the key) and the endpoints it allows |
| A program on this machine (a CLI) | command lines | the program, and whether it may use the network |
| MCP | the server's tools | the server's address and sign-in |
| Peer | another gohort's models, embedder, speech | a peer key; it lends resources, never its data |
| Messaging | send, and what arrives | the service's bot or account |

A **credential** is not a connector. It is the key a connector's outside
uses, stored encrypted and attached on the server at call time, so no model
ever sees it. An AWS credential signs each request instead (Signature
Version 4), with a stored key pair or, made by an administrator, as this
machine's own AWS identity.

A connection keeps its own **settings** and **key**, in a space of its own.
Its spec declares what it needs, and what kind of value each is (an
address, a model, a choice, a number, a key), and uses each as
`{config.name}`; the page that uses it shows them under the choice, and its
row's Settings shows them all. A key is sent only to the address it was
given for: move the connection and it asks for the key again. These keys
are not API Credentials, which keep what has no page of its own.

## Reading a connector

A connector's row reads inside to outside:

```
Speech ⇐ speak ⇐ api.elevenlabs.io (its ElevenLabs API key)
Image Generation ⇐ submit, then wait on status ⇐ api.example.com (its API key)
Image editing ⇐ edit ⇐ magick on this machine (no network)
```

What gohort asks, the endpoints that do it, and what it reaches with what.
The same line heads its ✨ Fix form. A connector giving several things keeps
to one line by naming them and counting its endpoints:

```
Speech, Transcription, Image Generation ⇐ api.example.com (its API key), 7 endpoints
```

**Endpoints** on the row lists each endpoint once, with what it is sent and
where each value comes from, what is read from its reply, and what uses it:

```
POST /v1/images      Image Generation, step 1
                     json: prompt ← {prompt}; size ← "{width}x{height}"    reads: job.id → id
GET /v1/jobs/{id}    Image Generation, step 2
                     reads: until state == done; result.url → image (a file, fetched)
GET /v1/models       health check
DELETE /v1/files/{id}  not used
```

`{prompt}` is what gohort asked for, the contract's side; the field names
are the endpoint's; the arrows are the mapping. It is read from the
connector's spec, so it shows the part of each endpoint gohort uses, not the
resource's whole schema.

## Everything gohort has, as connectors

| You know it as | Inside | Outside |
|---|---|---|
| A service (Admin > Extensions > Connectors) | the contracts it provides | HTTP and a key, or a program here |
| A System Hook (Admin > Capabilities > System Hooks) | convert or read a file, cut audio | a program here: pandoc, ffmpeg, yt-dlp |
| An MCP server | its tools, for agents | MCP |
| A peer (Admin > Capabilities > Peers) | models, embeddings, speech, transcription | another gohort |
| A messaging relay | messages in and out | Telegram, Teams, others |
| A poll (made by an agent's bridge tool) | "the answer changed" | HTTP and a key |
| A research source (Admin > Extensions > Research Sources) | a search connector put to use for research and agents | the connector's |
| gohort's own providers (DuckDuckGo search, the browser, SMTP mail and the local model servers) | the same contracts | written in Go where Go is the protocol or the server is on this machine; every hosted provider moved to a connector (embeddings, transcription, speech, images, the other search engines, the hosted LLMs and Bedrock) |

Admin > Extensions > Connectors lists what has a spec, the part that can be
edited and fixed without a new build: services, messaging relays, polls, MCP
connections, adapted System Hooks and peer relays, grouped by what each does.
What gohort does in Go has no spec and is not listed there: DuckDuckGo and
the browser are set up under Admin > Capabilities, SMTP under Admin > System >
Mail, a local model server under Admin > LLMs, peers under Admin >
Capabilities > Peers, and the programs System Hooks run under System Hooks.

## Where to find things

There are two consoles. **The Extensions app** is each person's own: keys are
per person and never shared between accounts unless the owner lends one.
**Admin** is the deployment's.

| What | Where |
|---|---|
| Connectors | Admin > Extensions > Connectors (polls also under Admin > Extensions > Bridges) |
| Which connector serves a capability | Admin > Capabilities (Embeddings, Speech, Transcription, Image Generation, Web Search) |
| Credentials | yours: Extensions app > APIs; everyone's: Admin > Extensions > API Credentials |
| System Hooks | Admin > Capabilities > System Hooks |
| Peers | Admin > Capabilities > Peers |
| MCP servers | yours: Extensions app > MCP servers; the deployment's: Admin > Extensions > MCP Servers |
| Agent tools | yours: Extensions app > Tools; everyone's: Admin > Extensions > Global Tools |
| Templates (a connector and its credential, packaged to reuse or share) | Admin > Extensions > Templates |

## Which one do I want?

| You want… | Use |
|---|---|
| a hosted provider to do something gohort already does | a connector from a template, then pick it under Admin > Capabilities |
| an agent to call an API | a credential, plus a tool (Builder drafts both) |
| a file type handled by a program here | System Hooks |
| to be told when something outside changes | a poll (an agent's bridge tool makes one) |
| an agent on a messaging service | a messaging relay |
| another gohort's GPU or models | a peer |
| to hand your setup to someone else | a template |

## Sending messages

Templates under Admin > Extensions > Templates add a `message.send`
connector: *Slack (send messages)* and *Discord (send messages)* through a
bot, *ntfy (send notifications)*, and *ntfy (send files)*. Channels, built
later, send through them; until then nothing calls them but their Test.
Telegram, and Slack or Discord webhooks, keep their secret in the address,
which a connector never holds, so they have no template.

## An example from start to finish: hosted embeddings

The connector: inside `embed.text`, mapping one request, outside OpenAI with
its key.

1. **Admin > Capabilities > Embeddings:** in "Embed with", choose
   **+ Add another…**, then **Add…** on *OpenAI (embeddings)* and give it a
   name. This creates the connection, shown as *OpenAI (embeddings)*, and
   the page reloads with it in the list. (The same templates are under
   Admin > Extensions > Templates, by category.)
2. **Still on Embeddings:** set "Embed with" to it. Its model and its API
   key show under the choice. Test embeds two sentences and checks for two
   vectors of the same length. Save: the page now references it, and keeps
   the model and key with the connection; its row then says *Referenced by
   Embeddings*.
3. **Admin > Maintenance > Vector Index:** run *Repair the vector index*.
   Vectors made by the old embedder are in a different space, so search
   skips them until they are made again, one embed call per stored piece.

From then on the connector's row shows its calls, failures and cost. If
OpenAI changes something, ✨ Fix is on the same row.

## Not there yet

The model is where gohort is going; some pieces still bend it.

- **A program's network access belongs to the job.** A video downloader's
  program is listed with the network, but the setting is the job's, on
  System Hooks, not the connector's.
- **An adapted System Hook is stored as a connector** already, and is set up
  and fixed from System Hooks.
- **Older image backends** (`rest_image`) are not run any more: A1111,
  ComfyUI and peers' renderers are connections. A ComfyUI one is moved by
  setting it up again from its row; the rest wait for the migration.

The plan for closing these is
[design/integration-model.md](design/integration-model.md).
