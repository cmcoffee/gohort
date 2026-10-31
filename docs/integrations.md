# How gohort reaches things

gohort does a lot itself, and wraps the rest. Three words explain every
integration: **connector**, **flow** and **agent**.

## Connectors, flows, agents

```
            inside                          outside
gohort ⇄ [ a gohort contract ]  CONNECTOR  [ transport + endpoints ] ⇄ the resource
         make an image, transcribe,         HTTP and a key, a program here,
         convert a file, send a message     MCP, another gohort, a messaging service

FLOW:   connector → connector → connector    deterministic wiring between connectors
AGENT:  decides which connectors to call and which flows to run
```

- **A connector is a wrapper.** It takes something gohort does not own and
  makes it speak gohort's language. Its **inside** speaks a contract gohort
  defines: make an image, transcribe audio, convert a file, send a message,
  or tell gohort that something arrived. Its **outside** reaches the
  resource: a transport and its endpoints, with whatever reaching it takes
  (a key, a program, network access, an admin's approval).
- **A flow connects connectors.** The output of one becomes the input of the
  next. A flow is deterministic: the same input takes the same steps, every
  time, so it can be tested, priced and fixed.
- **An agent decides.** It is the one part that is not deterministic: it
  reads the situation, calls a connector or runs a flow, reads the result,
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
   program runs in, whether it has the network, and the admin's approval.
   What gohort can reach, and who allowed it, all sits on connectors.
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

What gohort asks of a connector, and where it plugs in:

| Contract | Where it plugs in |
|---|---|
| `image.generate`, `image.edit` | Admin > Capabilities > Image Generation, and agents' image tools |
| `speech.synthesize` | Admin > Capabilities > Speech (TTS): "Speak with" |
| `speech.transcribe` | Admin > Capabilities > Audio Transcription: "Transcribe on" |
| `embed.text` | Admin > Capabilities > Embeddings: "Embed on" |
| `web.search` | Admin > Capabilities > Web Search: "Provider" |
| `page.render` | Admin > Capabilities > Page Rendering (Browser): "Render pages on" |
| `mail.send` | Admin > System > Mail (SMTP): "Send with" |
| `file.convert` | Admin > Capabilities > System Hooks: a job's Adapt, "Done by" |
| `llm.chat` | Admin > LLMs > Worker LLM and Lead LLM: "Provider" |
| `music.generate` | an agent tool, `generate_music_<connector>` |
| `message.send` | a flow's last step: send what it made through a messaging API |

The inside faces both ways. gohort asks things of a connector (make, convert,
send), and a connector tells gohort things (a message arrived, a watched API
changed). The second is what starts work with no one asking: a message wakes
an agent, and later a flow.

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
ever sees it.

## Reading a connector

A connector's row reads inside to outside:

```
Speech ⇐ speak ⇐ api.elevenlabs.io (key elevenlabs_speech)
Image Generation ⇐ submit, then wait on status ⇐ api.example.com (key acme)
Image editing ⇐ edit ⇐ magick on this machine (no network)
```

What gohort asks, the endpoints that do it, and what it reaches with what.
The same line heads its ✨ Fix form. A connector giving several things keeps
to one line by naming them and counting its endpoints:

```
Speech, Transcription, Image Generation ⇐ api.example.com (key acme), 7 endpoints
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
| gohort's own providers (the local embedder, the built-in image code, web search, the browser, SMTP mail, the LLM tiers) | the same contracts | written in Go |

Services, messaging relays, polls, MCP connections, gohort's own providers
and the programs System Hooks run are listed under Admin > Extensions >
Connectors, in groups by what they are. The built-in ones are marked *Built in*: they are set up under
Admin > Capabilities, and Test there runs the same check as their settings
do. The programs are marked *Program*: each says what it does now, and its
command lines are under Endpoints; they are installed, adapted and tested
from System Hooks. Peers are marked *Peer*: each says what it lends, what
here uses it and what nothing uses, and Test asks it again what it lends;
they are added and re-keyed under Admin > Capabilities > Peers.

## Where to find things

There are two consoles. **The Extensions app** is each person's own: keys are
per person and never shared between accounts unless the owner lends one.
**Admin** is the deployment's.

| What | Where |
|---|---|
| Connectors | Admin > Extensions > Connectors (polls also under Admin > Extensions > Bridges) |
| Flows | Admin > Extensions > Flows; each is the agent tool `flow_<name>` |
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
| connectors chained the same way every time, as one agent tool | a flow |
| a file type handled by a program here | System Hooks |
| to be told when something outside changes | a poll (an agent's bridge tool makes one) |
| an agent on a messaging service | a messaging relay |
| another gohort's GPU or models | a peer |
| to hand your setup to someone else | a template |

## Flows

A flow is saved under Admin > Extensions > Flows: its inputs, then its steps
in order, each a connector and a contract, and what feeds the contract's
inputs, one per line:

```
inputs:  note (file, required)
listen:  speech.transcribe on ears         audio = {input.note}
draw:    image.generate on acme-images     prompt = A watercolor of {listen.text}
```

`{input.note}` is the flow's input; `{listen.text}` is the text the step
`listen` gave. Only the last step's outputs come back, here the picture.
Saving checks it all fits: each connector gives its
contract, each input a contract needs is fed, by a value of the right type.
The flow is then the tool `flow_<name>`, given to an agent like any other
tool; what the last step gives comes back to the agent, files saved in its
workspace. Each step's call is counted and priced on its connector's row,
and Test runs the flow once on sample inputs.

A flow can also start on its own. Under "Starts when", pick a monitor (a
watch noticing a change, a webhook, a timer) and the input its text goes
into: each time it fires, the flow runs with no agent in between and its
`message.send` step sends what it made. The flow must have that step. Its
runs a day from the event are capped (24 unless set); past the cap, or if a
step fails, the monitor delivers the event its usual way with why in front.

Templates under Admin > Extensions > Templates add a `message.send`
connector: *Slack (send messages)* and *Discord (send messages)* through a
bot, *ntfy (send notifications)*, and *ntfy (send files)*, which attaches
what a flow made. Telegram, and Slack or Discord webhooks, keep their secret
in the address, which a connector never holds, so they have no template.

## An example from start to finish: hosted embeddings

The connector: inside `embed.text`, mapping one request, outside OpenAI with
its key.

1. **Admin > Extensions > Templates > Add a template…**, pick *OpenAI
   (embeddings)*, answer its questions (the key, a name, the model). This
   creates a credential, disabled, and a connector, unapproved.
2. **Admin > Extensions > API Credentials:** enable the credential.
3. **Admin > Extensions > Connectors:** approve the connector, then press
   Test on its row. It embeds two sentences and checks for two vectors of
   the same length.
4. **Admin > Capabilities > Embeddings:** set "Embed on" to *Service:
   openai-embeddings* and save.
5. **Admin > Maintenance > Vector Index:** run *Repair the vector index*.
   Vectors made by the old embedder are in a different space, so search
   skips them until they are made again, one embed call per stored piece.

From then on the connector's row shows its calls, failures and cost. If
OpenAI changes something, ✨ Fix is on the same row.

## Not there yet

The model is where gohort is going; some pieces still bend it.

- **Messages arriving do not start flows yet.** A monitor's fire (a watch
  noticing a change, a webhook, a timer) can start one; a message arriving
  on a messaging relay cannot, and only an admin attaches a flow to an
  event.
- **A program's network access belongs to the job.** A video downloader's
  program is listed with the network, but the setting is the job's, on
  System Hooks, not the connector's.
- **An adapted System Hook is stored as a connector** already, and is set up
  and fixed from System Hooks.
- **Older image backends** (`rest_image`) still work. Fix shows one as a
  service spec, and saving it there converts it.

The plan for closing these is
[design/connectors-and-flows.md](design/connectors-and-flows.md).
