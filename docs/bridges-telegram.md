# Telegram bridge — connector contract

This is the spec for a **Telegram connector**: a small external process that relays
between Telegram's Bot API and gohort's Bridges transport. The gohort side is
already done — a `telegram` service entry (`core/channel.go` `bridgeServices`),
service-agnostic routing, the outbox, groups/aliases, and the approval gate all
work for any service. The connector is the only thing left to build.

Unlike the iMessage bridge (which needs a Mac running the desktop daemon), a
Telegram connector is **cloud-friendly**: a bot token + a process that can reach
your gohort host. It can run anywhere — the homelab, a small VM, a container.

## The shape

```
   Telegram  ──getUpdates / webhook──▶  CONNECTOR  ──POST /bridges/api/hook──▶  gohort
   Telegram  ◀──sendMessage / sendPhoto── CONNECTOR  ◀──GET /bridges/api/poll──   gohort
```

The connector speaks exactly two gohort endpoints, both authenticated with the
bridge key in an `X-API-Key` header. It is stateless beyond an inbound dedup set
and an update offset.

## One-time setup

1. **Create the bot**: talk to [@BotFather](https://t.me/BotFather), `/newbot`,
   grab the bot token. Run `/setprivacy` → **Disable** if the bot should see all
   group messages (not just commands/replies).
2. **Mint a bridge key**: the manual "Add a bridge" form is hidden in the
   dashboard until a manual-key connector exists — when you build this one,
   restore that section (see the breadcrumb in `apps/bridges/page.go`) or POST
   `{"name":"Telegram","service":"telegram"}` to `/bridges/api/keys`. Copy the
   key (shown once). This is the `X-API-Key`.
3. **Bind a channel**: in Agency, attach a Channel to the agent that should
   answer — Service `telegram`, Address empty for **whole-service** (every chat
   the bot is in routes to this agent) or a specific `chat_id` for **per-chat**.
4. **Run the connector** with three settings: `GOHORT_BASE_URL` (e.g.
   `https://gohort.example.com`), `BRIDGE_KEY`, `TELEGRAM_BOT_TOKEN`.

## Inbound — Telegram → gohort

Receive updates with either long-polling (`getUpdates` with an incrementing
`offset`) or a Telegram webhook. For each `message` update, POST to
`{GOHORT_BASE_URL}/bridges/api/hook` with header `X-API-Key: <BRIDGE_KEY>` and body:

```json
{
  "chat_id":      "tg;-;123456789",
  "handle":       "@alice",
  "display_name": "Alice Smith",
  "text":         "hey, what's on my calendar?",
  "images":       ["<base64>", "..."],
  "msg_id":       "5512"
}
```

Field mapping:

| gohort field   | Telegram source | notes |
|----------------|-----------------|-------|
| `chat_id`      | `message.chat.id` | **encode with the format below** — this is the routing + reply key |
| `handle`       | `from.username` (`@name`) or `from.id` | stable per person; used for aliases/pre-auth |
| `display_name` | `from.first_name` + `from.last_name` | names the contact in transcripts |
| `text`         | `message.text` / `message.caption` | |
| `images`       | largest `message.photo[]` (or image `document`) | download via `getFile`, base64-encode; omit if none |
| `msg_id`       | `message.message_id` | inbound **dedup** — gohort drops repeats per `(chat_id, msg_id)` |

The server returns `202 Accepted` immediately and runs the agent asynchronously;
the reply comes back through the outbox (below), not in the hook response.

### chat_id format — REQUIRED

gohort parses two things out of a `chat_id`, so the encoding is a contract, not
cosmetic:

- **Group vs 1:1**: a `chat_id` containing `;+;` is treated as a **group**
  (identity is the stable chat id, no single handle); `;-;` is a **1:1**.
- **Handle extraction**: for a 1:1, the segment after the last `;` is used to
  alias-match the person.

Use a stable `tg` prefix and the right separator:

| Telegram chat type | `chat.id` example | gohort `chat_id` |
|--------------------|-------------------|------------------|
| private (1:1)      | `123456789`       | `tg;-;123456789` |
| group / supergroup | `-1001234567890`  | `tg;+;-1001234567890` |

On outbound (below) strip the `tg;-;` / `tg;+;` prefix to recover the raw
`chat.id` for the Bot API call.

## Outbound — gohort → Telegram

Poll `GET {GOHORT_BASE_URL}/bridges/api/poll` with header `X-API-Key: <BRIDGE_KEY>`
on an interval (2–5s). It returns and **removes** this service's pending items,
oldest first:

```json
[
  { "id": "…", "chat_id": "tg;-;123456789", "handle": "@alice",
    "text": "You have 2 events today: …", "images": ["<base64>"],
    "type": "reply" }
]
```

For each item:

1. Recover the Telegram chat id: strip the `tg;-;` / `tg;+;` prefix from `chat_id`.
2. If `images` is non-empty, `sendPhoto` each (decode base64 to bytes); then
   `sendMessage` with `text` (send images first so the caption/text reads after).
3. `type: "status"` items are mid-turn progress pings ("Working on it…") — deliver
   them like a normal message, or suppress them if you prefer terse threads.

**Markdown**: gohort delivers **plain text** for Telegram by default
(`bridgeServices["telegram"].RendersMarkdown = false`), so send with no
`parse_mode`. If you implement proper MarkdownV2 escaping in the connector, flip
that flag to `true` in `core/channel.go` and gohort will stop flattening — then
you own the escaping.

## Robustness checklist

- **Dedup is gohort's job** on `msg_id`, but only call the hook once per update;
  advance your `getUpdates` offset only after a successful POST.
- **Drain ≠ delivered**: an item is removed from the outbox when you poll it. If a
  `sendMessage` fails, retry on your side — re-polling won't return it again.
- **Attachment size**: Telegram caps photos ~10MB; downscale before `sendPhoto`.
- **Rate limits**: ~30 msg/s globally, ~1 msg/s per chat. Queue outbound per chat.
- **Groups**: never map a member's handle to the group — the group's identity is
  its `chat_id` (`;+;`). gohort enforces this; mirror it in your encoding.

## What works today vs. later

- **Inbound → agent → reply**, **whole-service and per-chat bindings**, **groups**,
  **aliases**, **proactive `send_message` to a Telegram chat** (outbound now
  resolves the transport from the conversation, not a hardcoded default), and the
  **approval gate** for proactive sends — all functional with the generic code.
- **Rich markdown** (MarkdownV2) and **inline keyboards / buttons** are future
  connector enhancements; the MVP is plain text in and out.
