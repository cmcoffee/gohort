# Public App Submissions — writes on the capability URL

Status: **design / target** (not built).

Today `/custom/pub/<token>/` is stateless by construction. The page renders,
data sources run in the owner's sandbox, `GET records` returns `[]`, and every
write, action fire and chat request is refused (`handlePublic`,
`apps/customapps/customapps.go:1264`). This spec opens exactly one write: an
anonymous visitor submitting a declared form, into a store the visitor can
never read back.

## Why this instead of authentication

The want behind "can gohort host a separate web app" is usually a surface
strangers can *submit to*, not one they *log into*. Those are very different
features. A login means a second identity stack living next to `core/auth.go`
(1982 lines of lockout, session sliding, CSRF, reset and signup that took real
work to get right), and a weaker copy of that is the worst artifact this
direction could produce.

A dropbox needs none of it. The token already is the credential, the store is
already per-owner, and the sandbox already runs the compute. What is missing is
one endpoint and the discipline around it.

If real logins turn out to be necessary later, the answer is delegation
(OAuth to an external provider, or app-scoped `AccountToken`s), not a user
table in `AppSpec`.

## The model in one line

A published app becomes a **dropbox**: write-only for visitors, read-only for
the owner's sandboxed scripts. Nothing the framework serves ever echoes a
submission back. Only the owner's own data source can, and only deliberately.

That default matters. A guestbook where visitors see each other's entries is
buildable, but it takes the owner writing a script that emits them. The
framework never makes that choice on the owner's behalf.

## Storage

A new table beside the existing one, in the same store:

| | table | holds |
|---|---|---|
| existing | `custom_records:<slug>` | the app's CONFIG, owner-written |
| new | `custom_submissions:<slug>` | visitor submissions, append-only |

Both resolve through `recordBase(spec, spec.Owner)`, so `PrivateDB` routing is
inherited with no new plumbing: an app on its own kvlite file keeps its
submissions there too.

**Keyed by slug, not by token.** `handlePublishApp` regenerates the token on
every publish, so a token-keyed dataset would be silently orphaned each time
the owner rotated the link. The token is a credential; the slug is the
identity.

**Never mixed with `custom_records`.** `handlePublicData` feeds the owner's
records to the script as the app's setup (which site to pull, which city to
report). Letting strangers append to that table makes the owner's own
configuration attacker-writable. Two tables, one direction.

Record shape:

```json
{
  "<record_key>": "<server-allocated id>",
  "created": "2026-08-30T18:04:11Z",
  "fields": { "name": "...", "message": "..." }
}
```

Everything under `fields` is attacker-controlled; everything outside it is
ours. Nesting rather than flattening (which is what `handleRecords` does for
owner records) puts that trust boundary in the data itself, so a submitted
field named `created` or one matching the record key cannot collide with the
stamped metadata. The shape is the enforcement.

## The endpoint

```
POST /custom/pub/<token>/submit
Content-Type: application/json
{ "name": "...", "message": "..." }

200 {"ok":true}
```

Nothing else comes back. No id, no echo, no count. An id is a read handle, and
returning one invites `GET submit?id=` as the next request.

| status | when |
|---|---|
| 403 | app has not enabled submissions |
| 413 | body over cap |
| 422 | shape violation (names the field) |
| 429 | rate ceiling |
| 507 | count or byte ceiling reached |

507 rather than 429 for a full store: retrying later does not help, and the
message needs to say so.

Why a new noun instead of reusing `POST records`: `GET records` on the public
mount returns `[]` forever, by design. Making POST to that same path succeed
creates a read/write pair that is not a pair. `submit` names the asymmetry.

**Wiring, without touching the runtime.** `publicPageBytes` already adapts the
stored page for anonymous serving: it rewrites the mount prefix, sets
`public: true`, drops the Back link, and swaps session-bound panels for a
locked empty state. Add one more pass over the same page map: for every
`form_panel` body, set `post_url` to `submit`. `app_def` emits
`PostURL: "records"` (`apps/orchestrate/app_def_tool.go:806-808`) and
`post_url` is a plain JSON field on the body, so this is the identical shape of
edit `publicizeSessionPanels` already performs. No `core/ui` change, no runtime
change, no app-specific knowledge anywhere.

`FormPanel.Invalidate` already lists the app's `data/<name>` sources
(`appRecordWriteInvalidations`), so a submit re-fetches every computed panel.
A live count, or a "thanks, you are number 14" display, works with no extra
machinery.

## Enablement is the owner's, not the agent's

```go
// AcceptsSubmissions allows ANONYMOUS visitors on the public capability URL to
// append to the app's submission store. Off unless the owner turns it on from
// the Custom Apps index; publishing alone never enables it. Deployment-local,
// cleared on export like Shared / PublicToken / Disabled.
AcceptsSubmissions bool `json:"accepts_submissions,omitempty"`
```

**`app_def` must not be able to set this.** The agent authors the form; the
human decides whether strangers may fill it. This is the same split
`private/anvil/actions.go` makes for `ProjectAction`s, where the operator
writes the command and the model may at most invoke one that opted in, and it
is the same reason: the act has a blast radius outside the app.

Enforce it, do not merely document it. The create/update path drops the key if
an LLM sends it and reports the drop, the way `unknownSectionKeyNotes`
(`app_def_tool.go:603`) already reports ignored section keys. A rejected key
with a note beats a silently honored one. The tool schema never mentions the
field at all.

The toggle sits beside Publish/Unpublish in the index, and is refused while
`PublicToken == ""`: accepting submissions on an unpublished app is a control
that does nothing.

Lifecycle:

- turning it **off** stops new writes, keeps what was collected
- **unpublishing** revokes the link, which stops writes first (token lookup
  fails before anything else runs)
- **deleting** the app drops the submissions with it (`handleDeleteApp` already
  unsets the public index; add the table drop there)

## Validation: the declared form is the schema

On submit, against the form section's `fields`:

1. **Drop undeclared keys.** Do not 422 on extras: a stale cached page will
   send them, and hard-failing turns a form edit into an outage. Log the drop
   once per app per minute.
2. **Required declared fields missing** → 422, naming the field.
3. **Scalars only.** Strings, numbers, bools. Nested objects and arrays are
   refused; nothing the form panel produces has that shape, and accepting it is
   how a store grows a structure nobody designed.
4. **Caps**: per value 4 KiB, per body 64 KiB via `http.MaxBytesReader`. Field
   count is bounded automatically by the declared list.
5. **Stamp last.** The record key and `created` are written after validation,
   outside `fields`, so a submitted value named either cannot reach them.

## Ceilings, and what happens at them

All per app.

**Rate.** Reuse the existing two-tier shape. `publicAppRequests` (120/min,
keyed by `RequestSource`) already covers the subtree. Add `publicAppSubmits`
keyed **both** ways: per source IP (10/min) and per app (60/min). Per-source
alone does nothing against a link shared widely, which is the case the public
mount exists for; per-app alone lets one client eat the whole budget.
`handlePublicData` already reasons this way for scripts, and it applies harder
to writes.

**Count.** `maxSubmissions` per app, default 5000. At the ceiling, **refuse.
Never evict.** Eviction hands an attacker a delete primitive: 5001 pieces of
junk silently destroy the real submissions underneath. Refusing is visible and
recoverable, and the recovery path (export, then clear) is specified below
precisely because the ceiling assumes it exists.

**Bytes.** A running total per app, default 32 MiB, same refuse-do-not-evict
rule.

Every ceiling refusal emits a `Warn` naming the app, matching the existing
script-ceiling line. A full store is an operator problem, and silence makes it
a mystery.

## What stays refused

Unchanged, and deliberately:

- **`GET records` → `[]`.** Visitors never read the store, neither the owner's
  nor each other's.
- **`actions` → 403.** An action runs owner-authored code that persists and can
  carry the owner's credentials (`runActionAndPersist`, plus the `fetch_via`
  auto-grant). "A stranger appends a row" and "a stranger triggers owner code
  holding owner credentials" are different risks. This step buys the first one
  only.
- **Chat, pipeline, workbench** stay swapped for the locked empty state by
  `publicizeSessionPanels`.
- **Edit and delete of a submission**: there is no visitor-facing handle to
  one, so there is nothing to address.

## The new risk this creates

**This is the first time an app's store can contain bytes an attacker chose.**
Everything downstream of that store was built when it held only owner-authored
data, and three places say so out loud.

1. **`html` sections are not sanitized.** `app_def`'s own help states the blob
   is trusted because it is "owner-authored, owner-served" and warns against
   interpolating untrusted data into it. That sentence is currently a style
   note. With submissions it becomes a security boundary, on the same origin
   that holds admin sessions. `docs/custom-app-tier.md` argues the general case
   already. Minimum: warn at enable time when the app has an `html` section
   that fetches a data source. Better: submissions reach the page only through
   typed sections, which escape.
2. **Data-source scripts receive the bytes as an environment variable.** They
   run sandboxed with no raw network, which is the right containment, but a
   script that interpolates a submitted value into a URL or a shell command is
   now injectable. The existing authoring guidance to `quote()` every
   interpolated value stops being advisory for any script that reads
   submissions.
3. **Anything that forwards a submission to an agent.** Out of scope here, since
   the public mount has no chat. But the moment a schedule or an action
   summarizes submissions with a model, that text is untrusted input and needs
   the fencing described in `docs/tool-result-scan.md`.

The enable-time warning should say the honest version: *anything typed here
reaches your app's scripts and your dashboard, so treat it like email from a
stranger.*

## Owner-side surfaces

**Read.** Expose submissions to data-source scripts as a second environment
variable, `submissions`, holding the JSON array, exactly parallel to `records`.
No new section kind and no new component: an existing typed table with
`source_script` renders them the moment a script emits them. A
`kind: "submissions"` section would be a specialized leak of precisely the sort
`CLAUDE.md` forbids.

**Export.** CSV or JSON download from the index, owner-gated, through the
existing `core/export_registry.go`.

**Clear.** A destructive button with a confirm. The count ceiling refuses
rather than evicts, which is only a safe choice if the owner has a way out.

**Notice.** The index row shows the submission count and byte total beside the
public URL, so a filling store is visible before it is full.

Not in this step: notifying the owner when a submission arrives. The trigger
and event machinery (`core/trigger.go`, `core/event_monitor.go`) is its natural
home, and it is a separate feature with its own failure modes.

## Disclosure to the visitor

A published app accepting submissions renders a one-line footer stating that
submissions are stored by the app's operator and are not public. This is not
legal boilerplate. It is the same reasoning as `publishLimitationNote`: a
surface that quietly does something the reader would not guess produces "it is
a bit buggy" reports, and the hunt starts in the wrong place.

## Tests

In-package, extending `apps/customapps/public_limit_test.go` and
`public_panels_test.go`:

- writes 403 when `AcceptsSubmissions` is false, including on a published app
- accepted when true, and the record lands in `custom_submissions:<slug>`,
  never in `custom_records:<slug>`
- undeclared keys dropped; a missing required declared field 422s
- a client-supplied record key and `created` cannot overwrite the stamped ones
- body over cap 413s; a single value over cap 422s
- the count ceiling refuses **and does not evict**: assert the first submission
  is still readable after the ceiling is hit
- `GET records` still returns `[]` once submissions exist
- `actions` still 403
- `publicPageBytes` retargets `form_panel.post_url` to `submit`, and the
  authenticated page still posts to `records`
- `app_def` create/update drops `accepts_submissions` and says so

## Rollout

1. Field, storage, endpoint, validation. Flag defaults off, so nothing changes
   for any app that exists today.
2. Index toggle, count and byte display, clear.
3. The `submissions` environment variable and export.
4. The `html` and injection guard in `app_def`.

Bump `version.txt` on every commit (no trailing newline).
