# Sharing with people and teams

**Status:** Proposed, 2026-11-22. Settled in discussion with the owner;
the open decisions are listed at the end. Phase 1 is built (0.9.618).

**Supersedes:** the deployment rung of `sharing-governance.md` (the global
plane, promotion into it, and the admin console over users' own things), and
in `owner-first-credentials.md`: deployment APIs, the record split, the
migration, and "a shared agent is unavailable until every bring-your-own API
is connected". What that doc settled about OAuth, repairs and the call-time
check still holds, except where this one says otherwise.

## The model

**Sharing goes one way: an owner shares with people, teams, or Everyone.**
Everyone is a team that always holds every signed-in user. There is no
separate deployment plane: no global tool catalog to adopt from, no handing a
credential to the deployment, no deployment skill list. Something that should
reach the whole deployment is shared with Everyone, through the same dialog
and the same rules as sharing with one colleague.

**A credential is never shared on its own.** What is shared is an agent, an
app or a tool. It carries how to reach its API, never the secret. The owner's
key is reachable by a recipient only through the tools that came with the
share: to the owner it behaves as it always has, to every recipient it is
secured to those tools (the existing "secured" rule, applied per recipient).
A recipient cannot call the API with it directly, cannot build tools of their
own on it, and never sees it. Revoking the share, or changing the API's
choice, ends it at once.

**Every call records which person made it.** At the provider a call through
the owner's key arrives as the owner, so gohort's record is the only place
that says who.

## Each API in a share

Sharing an agent, an app or a tool lists every API it reaches (its own
tools, toolboxes, scripts that declare `fetch_via`, tools its skills bring,
tool steps of its pipelines, MCP connectors with credentials, servitor
appliances), and the owner chooses for each one:

| Choice | Before they connect | After they connect their own account |
| --- | --- | --- |
| **My account for reads, theirs for writes** (default) | reads with the owner's account; a write asks them to connect | everything with theirs |
| **Bring your own** | the share needs setup until they connect | everything with theirs |
| **Share mine, writes included** (an explicit choice, warned: writes land as the owner) | everything with the owner's | everything with theirs |
| **Disallow** | the tools on it are withheld | withheld |

**Once someone connects their own account, every call to that API uses it,
reads included.** They then see what their own access allows, the record at
the provider names them, and the owner's key is spent only on people who
have not connected. Reads and writes always go through the same account, so
the agent never finds something it then cannot act on.

**No quiet fallback.** If their account stops working (an expired token,
revoked access), the call fails with "reconnect your account". It does not
fall back to the owner's key, which would hide the problem and hand them the
owner's permissions again. To go back to the owner's key for reads, they
disconnect.

**Three things are said where they matter:**

- **When sharing:** "People who connect their own account will see what
  their own access allows, which may be less than yours. Until they do, they
  read with your access."
- **When connecting:** "From now on this reads as you. It may find less than
  it did, because it was reading with [owner]'s access."
- **When the far end says not found:** a provider often answers "not found"
  for what you may not see. Under the person's own account, gohort says so
  ("your account can't see this; [owner]'s can") instead of reporting a
  broken tool.

**The person's own account lives in the existing per-person slot**
(`<name>__usecret__<user>`, `<name>__usertok__<user>`), under the shared
API's name, and shows under Account > Connected accounts. No new record: the
October split into API and connected-account records is not needed.

**"Bring your own" keeps October's rule:** a share with such an API needs
setup until the person connects, and says which accounts. The default choice
never blocks: reads work from the start.

## Defaults, and maintainers

**Sharing gives the people added the same experience the owner has.** Adding
people or a team to an agent (as users or maintainers) lends each credential
of the owner's it uses to them **for reads, for that agent only**, with no
question asked: it works for them from the start, and a write waits for
their own account. A row's standing choice wins (Theirs or Disallow lends
nothing, Mine with writes too lends writes), a key set to be lent to nobody
stays unlent, and people already on the agent are left as they were. Taking
somebody off takes their lend back.

**Only a credential's owner can let more go out on it.** A maintainer sees
the complete picture ("What it reaches", every tool, skill, document and
credential), and on a credential row may move the owner's key to Theirs or
Disallow, or from writes down to reads, never further: the server holds them
to it. When they use the agent they go through the same gate as everybody it
is shared with: reads on the owner's key, writes on their own account.

**Keeping it working while the owner is away:** a key that expired or was
revoked is worked around by people connecting their own accounts for it
(Connected accounts), or by a maintainer moving the row to Theirs. A broken
tool is fixed by a maintainer as a copy only that agent uses
("<tool>_fix", scoped to the agent, the original switched off for that agent
alone): only an HTTP tool or toolbox, only its request's shape, the
original's credential and response pipe kept whatever was sent. The owner
is told, and keeps it (into the original) or discards it. An
API that moved, or an owner gone for good, is handing over ownership.

## Reads and writes

**One rule decides whether a call reads or writes** (`temptool.ActionWrites`,
built in 0.9.618): GET, HEAD and OPTIONS read, any other method writes, a
script writes, and the owner of a toolbox can mark an action that only reads
although it is sent as a write (`reads: true`, such as a search sent as a
POST). The mark is the owner's, who owns the key it is spent on, so nobody
can call a write a read to get it past their key.

**What an account may do is still best set at the provider:** an owner who
shares reads should share a read-only token where the provider offers one.
Gohort's rule is the second guard.

## Admin

**An administrator governs what is shared, and can stop what is not.**

- **Shared with others:** full visibility and take-back, as today. Sharing
  with **Everyone** waits for an administrator's approval, because it runs
  the owner's code and spends the owner's key for every user. Sharing with
  named people and teams goes straight through.
- **Which version recipients get:** named people and teams get the owner's
  saved changes live, as settled in October. Everyone gets the version an
  administrator approved, and each update is approved the same way, so a fix
  never lands half-tested on the whole deployment.
- **Private things: a stop switch, nothing more.** One list across every
  kind: name, owner, kind, last used, and what it reaches (hosts,
  credentials, raw network), with one action, **Disable**. A disabled item
  stops working, and its owner is told why and sees it on their own page.
  Administrators no longer get Export, Configure, Promote, the full contents
  (templates, scripts), or Delete for someone else's private things; Delete
  happens with the account. Credentials keep Disable and lose Delete.
- **Kept as it is:** "Drafted tools waiting for review", the approval a tool
  an assistant drafted for someone waits for before their agents can run it.
  It is about what may run on the server, not about taking anything.

This is the product's stance, not a hard boundary: whoever runs the server
can read its database. What changes is that the admin screens stop inviting
anyone to go through people's things.

## Ownership

**Ownership can be handed over:** to another person, or to an administrator.
This replaces a separate kind of company-owned thing (a credential handed to
the deployment, a deployment skill). A company key nobody personally holds
is an administrator's own, shared through the tools they choose, as October
settled. Before an account is removed, what it shares with others is offered
for handing over, so removing someone does not quietly break the people who
used their tools.

## What already exists keeps working

No migration. Each existing record reads as one of the choices above and
behaves as it does today:

| What is stored today | Reads as | Behaves |
| --- | --- | --- |
| A credential lent read-only alongside an agent share | My account for reads, theirs for writes | Reads as today. A write uses the person's own account where they have one; until then it is refused with "connect your account" |
| A credential lent read-write | Share mine, writes included | As today |
| Nothing lent | Bring your own | As today |
| A credential lent by hand, tied to no agent | kept, deprecated | As today; no new ones offered |
| A deployment credential (`Owner == ""`, `AllowedUsers`) | kept, deprecated | As today; no new ones offered |
| A global (shared) tool, its adoptions and releases | kept, deprecated | As today; new wide sharing goes to Everyone |
| A credential handed to the deployment, a deployment skill | kept, deprecated | As today |
| An agent, pipeline, machine, collection or guide with Everyone | shared with Everyone | As today |

**One narrowing, with no data change:** a credential lent alongside an
agent share is limited today to that agent's runs, not to its tools. It
becomes limited to the share's tools, as every recipient's reach is above.

Everything marked deprecated is removed in 0.10.

## Requests carry a short form (secondary)

Every request that waits on an administrator (sharing with Everyone, an
update to what Everyone runs, re-enabling something an administrator
disabled, access to a key that needs approval) carries a short form the
requester fills in: what it is for, who needs it, which actions and whether
they write, and for how long. The administrator decides from that, not from
a bare name. The fields are settled when this phase is built.

## Phases

1. **Toolbox actions:** per-agent switches for each action of a toolbox, and
   each action marked as a read or a write. **Built, 0.9.618.**
2. **The per-API choice** on agent, app and tool shares, with the split as
   the default; reads move to the person's own account once connected, with
   no fallback; every recipient's reach limited to the share's tools; writes
   on read-only lends use the person's own account; the three notices.
3. **Everyone as a team:** administrator approval for sharing with Everyone,
   approved versions for it; the global catalog, credential handover,
   deployment skills, hand-made lends and new deployment credentials stop
   being offered (kept working, deprecated).
4. **Admin:** the stop list with Disable and its notice for every kind; the
   take-over actions on others' private things removed.
5. **Handing over ownership,** and the offer before an account is removed.
6. **Request forms.**
7. **0.10:** the deprecated shapes removed.

## Decisions

Settled (2026-11-22):

- Sharing is owner to people, teams or Everyone; Everyone is a team. The
  deployment plane is retired, kept working until 0.10, with no migration.
- A credential is reached by a recipient only through the shared tools;
  credentials are not shared on their own.
- Four choices per API, the split as the default. Once a person connects,
  everything uses their account; no fallback to the owner's.
- No "owner's account for reads, always" choice: it guarantees an agent
  that finds what it cannot act on.
- Reads and writes by method, with the owner's `reads` mark; read-only
  tokens at the provider recommended.
- Sharing with Everyone waits for an administrator, and gets approved
  versions; people and teams get changes live.
- Administrators govern what is shared and can only Disable what is not.
- Ownership can be handed over; it replaces company-owned kinds.
- **Sharing lends the owner's keys for reads by default** to the people
  added, for that agent only; nobody is asked. Existing recipients are left
  as they were.
- **Only a credential's owner can lend it further.** Maintainers see
  everything and may only narrow the owner's key; they use the agent through
  the same gate as its users.
- **Sharing with Everyone needs approval for what runs code or spends a
  key** (apps, tools, agents and pipelines with an API on the owner's
  account, machines) and for skills, which act on everyone's turns.
  Collections and guides go straight through: they only hold reading
  material, and an administrator can still take them back.

Open:

1. **The request form's fields** (phase 6).
