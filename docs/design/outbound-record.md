# Outbound record: what left, who sent it, and what it was read from

Status: **proposed, not built** (2026-11-16). Nothing here is decided until
the open questions at the end are settled.

Upgrade note needed: yes. It adds new stored records (the record itself, the
"internal" mark on sources, a per-user setting), an admin setting, and a
behaviour change once enforcement is turned on. **No migration**: every new
field defaults to empty, so an existing install starts with nothing marked
internal and every user on the default trust level, and nothing changes
until the administrator turns a mode on.

## The gap

Every control built so far answers one question: *does the agent do what its
owner meant?* The guardrail warden, the tool-result scan, the write ask
(`OutboundWriteRefusal`, `core/authorizations.go`), the proxy tunnel check
(`OutboundTunnelRefusal`), and the change approvals all protect the owner
from a steered or mistaken agent.

None of them protects the organisation from the owner. When the owner is the
one moving internal data out, every gate asks the person doing it:

- **Owners approve their own sends.** The write card goes to the agent's
  owner, and "Always allow <host>" trusts the host for every agent they own
  (`OutboundWrite.Ask`). A user who means to send data somewhere approves it.
- **No single record of what left.** The paths out are scattered and most
  only leave a server-log line: `[outbound]` decisions, `[fetch_url]`,
  `[hook/fetch]`, `[hook/relay] tunnel host: X sent, Y received`. Channel
  posts, publishing, credentialed API writes, and replies to outside callers
  leave no admin-visible trace at all.
- **Nothing links what a turn read to where it sent.** A turn that searches
  an internal collection and then posts to a webhook looks the same as a
  turn that summarises a public page and posts it.
- **Tunnels show only host and size.** A tunnel to a trusted host can carry
  anything.
- **Servitor egress from a remote box is out of sight.** The classifier asks
  on every `curl`/`wget`/`nc` (`net_egress_cmds`,
  `apps/servitor/risk_classify.go:539`), but the owner answers that ask too.

What an administrator has today: the Oversight page
(`apps/admin/page_governance.go`) lists what users built and shared, the
capability ceilings, and "Sites agents may send to"
(`apps/admin/outbound_sites.go`, where the admin's Never wins over everyone).
That is a blocklist for sites the admin already knows about. It says nothing
about what happened.

## What it can and cannot catch

This has to be said up front, or the feature will be read as a guarantee.

A user who can see internal data in their browser can copy it out by hand.
Gohort cannot see a paste, a screenshot, or a phone camera. The record covers
**the agent used as the conduit**: sends that go through gohort's tools,
channels, publishing, proxies, and outside-facing endpoints. That is the case
worth covering, because it is the one that scales: unattended, scheduled,
bulk, and repeatable without the person in the loop.

Also out of scope:

- **The model provider.** Prompts sent to a hosted model are the
  administrator's own configuration choice, not a user's send. The privacy
  gate on lead levels already governs that.
- **The user's own accounts outside gohort.** A credential the user brought
  is theirs to use elsewhere.
- **Side channels from the sandbox.** Raw sockets are already closed by
  default (`tune_shell_network_closed`); DNS and timing channels are not
  something this record tries to see.

## The shape

Three parts, built in order, each useful alone.

### 1. The record (always on once built)

One append-only table in RootDB, written by the framework, readable only by
an administrator. One row per thing that left:

| Field | What it holds |
|---|---|
| `at` | When. |
| `user` | The person the run acts for (the agent's owner, or the person chatting with a shared agent). |
| `agent` | The agent, and the dispatch lineage when a sub-agent sent it. |
| `run` | Session id, and whether it was attended or unattended (schedule, channel, wake). |
| `kind` | `web_write`, `tunnel`, `web_read`, `message`, `publish`, `api_write`, `servitor_egress`, `outside_reply`. |
| `dest` | Host, channel and recipient, publish target, credential name, or outside key name. |
| `size` | Bytes the agent chose to send: the URL plus body, the message text plus attachments, the published content. |
| `decision` | How it got out: trusted by owner, trusted by admin, allowed once, approved on a card, a registry, no check on this path. |
| `sources` | What the run read before the send (part 2). |
| `flags` | Why it is worth a look (below). |
| `digest` | A hash of what was sent, so the admin can tell "the same thing sent twice" from "two different things", without the record holding the content. |

**No content by default.** The record holds metadata and a hash. Holding the
content would make the record the most sensitive store on the instance, and
put every user's private sends in front of the admin whether there is cause
or not. Whether an admin can turn on content capture for one user is an open
question (Q2).

**Where it is written.** The paths out, and the hook point for each:

| Path | Hook |
|---|---|
| fetch_url write, fetch hook (curl/wget shims) | `OutboundWriteRefusal`, after `Decided`/`Ask` returns "" |
| Proxy tunnel | `OutboundTunnelRefusal`, same place, plus the byte counts already logged at tunnel close |
| fetch_url read with a payload in the URL | `wrapToolsForActivity` (`apps/orchestrate/runner_tool_catalog.go:819`), for CapNetwork tools (see Q1) |
| Credentialed API writes | the credential auto-route in `tools/websearch/websearch.go:323` and the api temp tool dispatch |
| send_message, channel replies | `send_message` (`apps/orchestrate/channel_tools.go:421`) and `recordChannelPost` (`:660`) |
| publish, publish_document | `apps/orchestrate/publish_tool.go:60`, `apps/publish/tools.go` |
| Servitor egress | the `net_egress` approval in `apps/servitor` |
| Outside /v1 replies, exposed MCP | `apps/openaiapi`, `apps/mcpserver`: one row per call with the key name and reply size |

Each path calls one core function (`RecordOutbound(ctx, OutboundEvent)`), so
the record has one writer and one shape. The run's identity rides on the
context the way maintenance progress does, so no handler signature changes.

**Append-only means no delete route.** Only the retention sweep removes rows
(default 90 days, Q4). The admin can read and export it, not edit it.

**Across peers.** The calling instance records the send; the serving
instance shows what its Monitor row already shows (the resource and the
peer's labels). Nothing about the peer's users is stored on the serving side,
per the peer-key rule.

### 2. Sources: linking what was read to what was sent

The run keeps a short list of the sources it read: collections searched,
Servitor systems queried, file stores opened, connectors read, uploaded
files. `wrapToolsForActivity` already sees every tool call, so this is one
append per read call, not new plumbing in each tool.

Each send records the list as it stands at that moment. The admin reads
"posted 14 KB to hooks.example.net after searching *Contracts* and reading
*customer-export.csv*", not just "posted 14 KB".

Sources can be **marked internal**: a field on the collection, Servitor
system, file store, or connector, set by an administrator (and offered to the
owner of the source, Q5). A run that has read an internal source is
*carrying* internal data for the rest of that session.

Session, not turn: data read on turn 3 is still in the history on turn 7.
Memory and notes can carry it further, across sessions; this design accepts
that limit rather than tracking taint through memory.

### 3. Flags and enforcement

**Flags** (computed when the row is written, shown as chips):

- **new destination**: the first send to this host or recipient by this user.
- **carrying internal**: the session read an internal source, and this send
  goes outside the instance.
- **volume**: this user's sends today pass a threshold (admin setting).
- **unattended, off-hours**: an unattended run sending outside the user's
  working hours (the per-user time zone already exists).
- **spread**: many distinct new destinations in a short window.

**One admin setting, Outbound control: Off / Watch / On.** This is the same
setting the tabled outbound-control work proposed, so the two land together:

- **Off**: the record is still written (it costs a row per send); no flags
  are raised to the admin.
- **Watch** (default once built): flags are raised; nothing is blocked. The
  admin gets a notice for each *carrying internal* send and a daily digest of
  the rest.
- **On**: a send flagged *carrying internal* needs **administrator**
  approval, not the owner's. Watched runs show a card that says it is waiting
  for an administrator; unattended runs queue it, the way an unattended write
  queues for the owner today. Everything else behaves as in Watch.

**Per-user trust** (one field on the user, admin-set):

- **Standard** (default): as above.
- **Reviewed**: in On mode, every *new destination* from this user also
  needs administrator approval, not only internal-carrying ones. This is the
  lever for a user the admin has reason to watch, without blocking everyone.

The admin's approval trusts the destination for that user (or for everyone,
a choice on the card), so a legitimate integration asks once.

## The admin's view

A new table on the Oversight page, "Outbound record", built from the
existing `ui.Table` primitive:

- columns: when, user, agent, kind, destination, size, sources, flags;
- filters: user, kind, flag, date range;
- a row opens the detail: the full source list, the decision, the lineage,
  and links to the user's agent and the run (the run's transcript stays the
  owner's; the admin sees that the run exists, Q3);
- an export of the filtered rows (CSV), for an investigation that leaves
  gohort.

A count of unreviewed flags on the admin landing page, so it is seen without
going looking.

## The user's view

Users are told. A user-facing line on the Account page and in the agent's
Security panel: "Gohort keeps a record of what your agents send outside it
(where, when, how much, and what they had read) that your administrator can
see. It does not keep what was sent." When Watch or On is set, the notice is
shown once on next sign-in.

Each user can see their own rows (Q3). A record people know about deters
more than one they discover, and it lets a user check an agent of theirs that
is sending somewhere they did not expect.

## Build stages

1. `RecordOutbound` and the table, wired to the paths that already make a
   decision (`OutboundWriteRefusal`, `OutboundTunnelRefusal`); the admin
   table, read-only. Proves the shape on the two paths with the most
   information.
2. The remaining paths: messages, publish, credentialed writes, Servitor
   egress, outside replies.
3. Sources: the per-session read list and the *internal* mark.
4. Flags and the Off / Watch / On setting in Watch only; the user notice.
5. On mode: administrator approval for internal-carrying sends; per-user
   trust.

Stages 1 to 4 never block a send. Only stage 5 changes behaviour, and only
when the admin turns it on.

## Open questions

**Q1. Do reads count?** A GET can carry data in its query string. Recording
every GET would flood the record with search and page reads. Proposal: record
a read only when the URL past the host and path is longer than a threshold
(say 256 bytes), or when the session is carrying internal data. Otherwise
reads are counted per host per run, not one row each.

**Q2. Content capture.** Should an admin be able to turn on content capture
for one user under review? If yes: forward only, the user is told it is on,
and captured content expires sooner than the rest of the record. Proposal:
not in the first build; the hash and the source list are enough to start an
investigation, and the run's own transcript is still there.

**Q3. Who sees what.** Admin sees every row. Does the user see their own
rows? Proposal: yes. Does the admin get to open the run's transcript from a
row? Proposal: no, the admin sees the row and the run's existence; reading
the transcript stays the owner's (an existing posture we should not
quietly change here).

**Q4. Retention.** 90 days, admin-settable? Longer for flagged rows?

**Q5. Who may mark a source internal.** Admin only, or also the source's
owner? An owner marking their own collection internal costs them nothing and
protects it; an owner *un*marking one the admin marked should not be
possible.

**Q6. Thresholds.** Volume and spread need numbers. Proposal: ship with
generous defaults (volume 5 MB/day/user, spread 10 new destinations an hour)
and let the admin change them, rather than tune them up front.

**Q7. Relation to the tabled outbound-control work.** That design (Draft /
Approved tools, Off / Watch / On) guards the agent; this guards the user. One
setting for both, as proposed here, or two?
