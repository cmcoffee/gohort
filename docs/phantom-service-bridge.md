# Phantom Service Bridge Contract

This is the single source of truth for the seam between phantom (the
server-side conversational message engine) and a **service bridge** (the
client-side process that connects a real messaging platform to phantom).
Any bridge, in any language, that honors this contract can feed messages
into phantom's agent engine and deliver its replies.

> **Terminology.** We call a messaging platform (iMessage, Telegram,
> Slack, email, a generic webhook source) a **service**, not a "channel."
> "Channel" already means the Master Control agent-thread concept
> elsewhere in gohort. A service is identified by a short lowercase id:
> `imessage`, `telegram`, `slack`, etc. Empty/unset always normalizes to
> `imessage` for backward compatibility.

## The model

- **One bridge speaks one service.** A bridge authenticates with an API
  key, and the key declares its service. The server tags everything that
  arrives on that key with the key's service and hands that bridge only
  its own service's outbound messages. Two bridges therefore never see or
  drain each other's traffic.
- **Inbound is push, outbound is pull.** The bridge POSTs incoming
  messages to phantom (`/api/hook`) and polls phantom for replies to
  deliver (`/api/poll`). phantom never connects out to a platform; the
  bridge owns all platform I/O.
- **A multi-service deployment runs multiple bridges**, each with its own
  service-bound key.

```
  platform  ──in──>  bridge  ──POST /api/hook──>  phantom (agent turn)
  platform  <─out──  bridge  <─GET  /api/poll───  phantom (outbox)
```

## Authentication and service binding

Both endpoints are machine-to-machine and authenticated by a single
header:

```
X-API-Key: <secret>
```

The server accepts either:

1. a **phantom API key** (`POST {phantom}/api/keys`), whose `service`
   field selects the bridge's service; or
2. the **core desktop-bridge key** (auto-provisioned for the
   gohort-desktop daemon), which is treated as `imessage`.

The service is derived from the **key**, not from the request body. A
bridge cannot poll or post for a service other than its key's. Minting a
service-bound key:

```
POST {phantom}/api/keys
{ "name": "telegram-bridge", "service": "telegram" }
```

The response includes the secret `key` (shown once). Omitting `service`
yields an `imessage` key.

## Inbound: deliver a received message

```
POST {phantom}/api/hook
X-API-Key: <secret>
Content-Type: application/json
```

Body:

| Field          | Type       | Req | Meaning |
|----------------|------------|-----|---------|
| `chat_id`      | string     | yes | Opaque, stable thread id. One per conversation (1:1 or group). The bridge chooses the format; phantom treats it as an opaque key. Keep it stable for the life of the thread. |
| `handle`       | string     | no  | The sender's address (phone, email, platform user id). **Empty means the message was sent by the owner from another device** (a "from me" echo), which phantom records but does not reply to. |
| `display_name` | string     | no  | Human-readable sender name, used in history rendering. |
| `text`         | string     | no* | The message text. |
| `images`       | string[]   | no* | Base64-encoded image data. |
| `videos`       | string[]   | no* | Base64-encoded video data. |
| `timestamp`    | string     | no  | RFC3339 send time. Server stamps `now()` if absent. |
| `row_id`       | int64      | no  | A monotonic per-message id from the source, used only to de-duplicate redelivery. Omit if the platform has no such id; send a stable unique value if it does. |

\* At least one of `text`, `images`, `videos` must be present.

Semantics a bridge must respect:

- **`chat_id` is the thread key.** Storage, history, memory, and routing
  all hinge on it. Do not reuse a `chat_id` across distinct conversations
  and do not change it for an ongoing one.
- **Owner echoes use empty `handle`.** If the platform reports messages
  the owner sent from another device, post them with `handle: ""`. They
  are recorded for context but never answered.
- **Inbound text cleaning is the server's job**, gated per service (the
  `ServicePolicy.CleanInbound` hook). Send the platform's raw text; do
  not pre-strip it to match another service's quirks.

Response:

- `202 Accepted` on success (the turn, if any, runs asynchronously; the
  reply arrives later via the outbox, not in this response).
- `400` if `chat_id` and at least one content field are missing.
- `401` if the key is invalid.

## Outbound: fetch replies to deliver

```
GET {phantom}/api/poll
X-API-Key: <secret>
```

Returns a JSON array of outbox items **for this key's service only**.
The act of returning them **deletes them from the server** (drain-once).
The server therefore guarantees at-most-once handoff; **the bridge owns
retry**. Keep returned items in an in-memory queue and re-attempt
delivery until the platform confirms, rather than re-polling for them.

Outbox item shape:

| Field     | Type     | Meaning |
|-----------|----------|---------|
| `id`      | string   | Unique item id (use for your own dedup/retry bookkeeping). |
| `chat_id` | string   | The thread to deliver to (same opaque key the inbound used). |
| `service` | string   | The service this item belongs to (matches your key; informational). |
| `handle`  | string   | The address to send to, when the platform addresses by handle rather than thread. |
| `text`    | string   | The message body to send (may be empty for an attachment-only item). |
| `images`  | string[] | Base64-encoded images to attach. |
| `videos`  | string[] | Base64-encoded videos to attach. |
| `type`    | string   | `reply` (answer to a message), `announce` (unsolicited: a scheduled/proactive message), or `status` (a short mid-turn progress note). All are delivered the same way; `type` is a hint for surfacing. |
| `created` | string   | When the item was queued. |

Delivery notes:

- **Ordering.** Deliver items in array order. Some services receive a
  video and its accompanying text as two separate items a few seconds
  apart (the `ServicePolicy.AttachmentDelay` behavior) so the upload
  lands first; preserve order and the gap is already encoded by when the
  items appear.
- **Formatting.** The server currently renders outbound text as plain
  text sized for SMS. Per-service formatting (markdown passthrough,
  different length limits) is a planned `ServicePolicy` extension; until
  then, send `text` as given.

## Identity and threading summary

- `chat_id`: opaque, stable, per-conversation. The only required routing
  key.
- `handle` / `display_name`: who the message is from/to, for history and
  addressing.
- Empty `handle` inbound: owner self-echo (recorded, not answered).
- Aliases (multiple addresses for one person/thread) are resolved
  server-side; a bridge just reports the address it actually saw.

## Server side: the ServicePolicy adapter

A service's server-side deltas live in one registered value (see
`apps/phantom/service.go`). The engine is otherwise channel-agnostic.

```go
type ServicePolicy struct {
    CleanInbound    func(string) string // store-time inbound text fix (nil = as-is)
    AttachmentDelay bool                // stagger video+text into two outbox items
}

func init() {
    RegisterServicePolicy("imessage", ServicePolicy{
        CleanInbound:    stripLeadingArtifact,
        AttachmentDelay: true,
    })
}
```

Adding a service server-side is registering one of these. An unknown
service falls back to a safe generic policy (no cleaning, atomic
delivery). Planned additions (when the second real service lands):
`Markdown bool` and `ChunkSize int`, with all outbound markdown
stripping moved to the single `enqueueOutbox` chokepoint so it can be
gated per service.

## Bridge side: responsibilities

A bridge is a small universal harness around a platform-specific core.

**Universal harness (identical across services, the part worth sharing):**

1. Hold the service-bound API key; send it as `X-API-Key` on every call.
2. Receive a platform message, map it to the hook payload, POST it to
   `/api/hook`.
3. Poll `/api/poll` on an interval; for each returned item, deliver it
   on the platform.
4. Own retry: keep undelivered items queued in memory and re-attempt;
   never rely on re-polling to recover them.
5. De-duplicate inbound (using `row_id` or a platform message id) so a
   redelivery from the platform does not double-post.

**Platform-specific core (different every service, not shareable):**

- `connect`: establish the platform session (Telegram long-poll, Slack
  socket mode, IMAP, AppleScript, a webhook listener, etc.).
- `receive -> message`: translate a platform event into the hook payload
  (a `chat_id`, `handle`, `text`, attachments).
- `send(item)`: translate an outbox item into a platform send.

The reference bridge skeleton (the shared harness plus this small
platform interface) will be extracted once a second real bridge exists,
so it is grounded in two services rather than generalized from iMessage
alone.

## Stability

The field sets above are additive: new optional fields may appear, but
existing field names and meanings are stable. A bridge should ignore
unknown fields. Everything defaults to `imessage` when a service is
unset, so a pre-service bridge keeps working unchanged.

## Reference (server implementation)

- Endpoints + key handshake: `apps/phantom/web.go` (`handleHook`,
  `handlePoll`, `handleKeys`).
- Service model + policy registry: `apps/phantom/service.go`.
- Wire shapes: `apps/phantom/phantom.go` (`APIKey`, `Conversation`,
  `OutboxItem`, `bridgeKeyService`, `enqueueOutbox`, `drainOutbox`).
- Project notes + roadmap: memory `project_phantom_multichannel`.
