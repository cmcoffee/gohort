# Bridge connector contract — and iMessage without the desktop app

A **bridge** is any process that relays a messaging service to gohort. It is
defined entirely by a contract, not by a codebase: POST inbound to
`/bridges/api/hook`, poll `/bridges/api/poll` for outbound, authenticate with a
bridge key that declares its service. The gohort-desktop daemon is one
implementation of that contract for iMessage, not a requirement — anything that
speaks it is a bridge, including a shell script under `launchd`.

The Bridges app itself is pure transport: no persona, no LLM, no tools. A
Channel (core) binds a conversation to an agent; the agent owns all behaviour.
See `docs/channel-model.md` for that half, and `docs/bridges-telegram.md` for
this contract worked through end to end for a cloud-friendly service.

## Auth

Every request carries `X-API-Key: <bridge key>`. Both endpoints are registered
as public paths so the key, not a session cookie, is what authenticates them.

The key **declares the service** (`imessage`, `telegram`, …). The server reads
the service off the key rather than off the request, so one key relays one
service and a compromised key cannot post as another.

There is deliberately no "Add a bridge" button today: the only live connector
auto-registers its own key on first connect, so a manual mint would create a key
for something that does not exist. Minting one for a hand-rolled connector means
either POSTing to `/bridges/api/keys` with `{name, service}` or letting the
desktop daemon register once and reusing what it made.

**One connector, one row.** The desktop path uses the well-known id
`desktop:<user>`. A second key for the same owner and service shows up as a
second bridge — which is correct for a second Mac, and confusing if you meant to
replace the first. The server prunes same-service records that have **never**
been seen, on the grounds that a secret which authenticated nothing cannot be
in use; one that has been seen is treated as a real second connector and kept.

## Inbound — service → gohort

`POST /bridges/api/hook`, JSON body, `202` on acceptance. Fields:

| field | meaning |
|---|---|
| `chat_id` | the conversation's stable id — **format matters, see below** |
| `handle` | the sender's address (phone, email, service handle) |
| `display_name` | the sender's name |
| `conversation_name` | the group/room title, when it has one — names the thread, distinct from the sender |
| `text` | the message body |
| `images` / `videos` / `audios` | base64 attachments; audio is transcribed, video is sampled to frames |
| `msg_id` | the connector's own stable message id — **send this** |
| `row_id` | numeric fallback id (what the iMessage relay sends) |
| `timestamp` | RFC3339, when the message was SENT |

**`chat_id` encodes two facts** and is parsed, not merely stored: a `chat_id`
containing `;+;` is a **group** (identity is the chat, there is no single
handle); `;-;` is a **1:1**, and the segment after the last `;` is used to
alias-match the person. Pick a stable service prefix — `tg;-;123456789`,
`tg;+;-1001234567890`.

**Send an id.** Dedupe keys on `msg_id`, falling back to `row_id`. With neither,
the server falls back to comparing content, which cannot tell a re-delivery from
two people saying "ok" in the same room — and a duplicate inbound is what starts
a self-thread loop, because two identical messages produce two replies that
arrive as two more messages.

**Send a real timestamp.** Without one every inbound looks like it happened now,
which is how replayed history wakes an agent as if it were live conversation.

## Outbound — gohort → service

`GET /bridges/api/poll` with the same header, on an interval (2–5s is typical).
It returns this service's pending items oldest-first **and removes them** — a
drain, not a peek. If your process crashes between the poll and the send, that
message is gone, so send promptly and log failures.

Items carry `id`, `chat_id`, `handle`, `service`, `text`, base64 `images` /
`videos`, and `type` (`reply` or `status`). Text arrives already flattened from
markdown, because a plain-text transport renders `**bold**` and `[text](url)` as
punctuation. An `agent` field appears only when that agent opted into signing
its messages; it is already prefixed into the text as a name tag.

## The handle rules, which are easy to get wrong

The iMessage daemon **clears the handle** on a message the owner sent
themselves (`is_from_me`). The server's `IsOwnerHandle` therefore treats an
empty handle as *the owner* — and `SameHandle`, used to match roster entries,
treats an empty handle as *no match at all*. That asymmetry is deliberate: "the
connector cleared the handle" identifies the owner and nobody else, so treating
it as a match against an arbitrary roster entry would hand every self-sent
message somebody else's authorization.

If you write your own connector, either follow the same convention or always
send a real handle. Do not invent a third meaning for empty.

## Writing an iMessage connector without the desktop app

Everything above is what the desktop daemon does. On a Mac you can do it in a
script, with no `.app`, no Wails, and no code signing:

- **Inbound**: read new rows from `~/Library/Messages/chat.db` (the `message`
  table joined to `chat`), and POST each as a hook request. Send `row_id` as the
  message's `ROWID` — that is exactly what the existing relay does — and set
  `timestamp` from the message date rather than from now. Requires Full Disk
  Access for whatever runs the script.
- **Outbound**: poll, then hand each item to Messages via `osascript`. Recover
  the raw conversation id by stripping your `;-;` / `;+;` prefix.
- **Own messages**: clear `handle` when the row is `is_from_me`, per the rule
  above.
- **Run it** under `launchd` so it survives logout and restarts with the Mac.

The trade against the desktop app is scope, not capability: the daemon also
carries the client-side tools (filesystem, screenshot, and the rest) over its
own WebSocket, which a hook/poll script does not. For iMessage alone, the two
endpoints are the whole story.

## Robustness checklist

- Keep an inbound dedup set; never re-POST an id you have already sent.
- Treat `202` as accepted and anything else as retryable, with backoff.
- Poll on a fixed interval — outbound latency is your poll interval, and there
  is no push.
- Log a send failure loudly. A drained item that never reached the service is
  invisible to gohort, which believes it delivered.
