# Owner-first: tools, APIs and connected accounts

**Status:** Proposed, 2026-10-07; every decision settled. Nothing here is
built.
Supersedes, once built: the credential half of the global plane in
`tool-credential-namespacing.md`, and the binding model in
`secured-credential-tool-binding.md`. Builds on the three sharing rungs in
`sharing-governance.md` (own, named people, deployment).

## The chain

**Tool → API → connected account.** Three layers, each with its own owner, and
only the last one secret.

**Share a tool, and the person who receives it gets everything except the
authentication:** the tool and its API work as they are, and the only thing
they fill in is their own key, password or OAuth connection.

| Layer | Holds | Owned by | Shared |
| --- | --- | --- | --- |
| **Tool** | Actions, templates, params, scripts; which API it uses | Its author | Yes, on any rung. It carries its API with it |
| **API** | The recipe to connect: base URL, auth type and header name, allowed and denied endpoints, allowed methods, confirm-before settings, the OAuth app registration | A **deployment API**: the deployment, governed by an administrator. A **personal API**: its creator, and an adopter owns their copy | Deployment API: referenced by everyone on its allow list. Personal API: copied, as a template |
| **Connected account** | The secret for one API: key, password, OAuth token | The person who connected it | Only through an agent share (see Agents) |

At call time a tool resolves its API, then **the calling user's** connected
account for that API, then sends. With no account connected, the call stops
before anything is sent and says which API to connect, and where.

These are the two nouns users already see: **Extensions > APIs** and
**Extensions > Connected accounts**.

## Why

Today one record, `SecureCredential`, is both the recipe and the secret, and
an ownerless one (`Owner == ""`, a deployment credential) is where the model
leaks:

- **A deployment credential is reachable by anything a permitted user
  writes.** Its Access list (`AllowedUsers`) decides *who*; *how* is the
  `fetch_url` auto-route, the `fetch_url_<cred>` catalog tool, any tool the
  user authors that declares it, or a script's `fetch_via`.
- **Securing it does not close that.** A secured credential is reachable only
  through tools that declare it, but declaring it binds the tool at creation
  for any user (`tool_def` create, `create_api_tool`, `temptool_create` all
  call `ApproveToolBinding`). The 2026-09-29 rule that a new tool is bound to
  a deployment key only by an administrator is checked at dispatch
  (`EnforceSecuredBinding`) and skipped at authoring.
- **A shared tool finds its credential by name.** `TempTool.Credential =
  "gitlab"` resolves through `Resolve(name, user)` to the recipient's
  same-named credential, else the global one. Right when names agree, the
  wrong key when Bob's "gitlab" is another server.
- **Nothing tells a recipient what a shared tool needs.** It works by name, or
  fails at call time naming a credential they have never seen.

The bottom layer already exists in one case. A credential with `CredScope =
per_user` is a shared recipe whose secret is stored per user:
`<name>__usecret__<user>` for a key (`SaveUserSecret`), `<name>__usertok__<user>`
for an OAuth token (`SaveUserToken`), chosen by `resolveSecret`. Those
per-user records are connected accounts in all but name. This design makes
that split the only one: every API's secrets live in connected accounts.

## Where an API lives

An API lives in one of two places:

- **Deployment APIs, the normal home for a shared service.** One definition
  per service, in one catalog, with an **allow list** of who may use it.
  Tools refer to it; nobody holds a copy. A fix (a new endpoint, a corrected
  base URL) reaches everyone at once. The allow list and the endpoint lists
  are the administrator's governance: who may use GitLab from oddjob at all,
  and what any tool on it may call, whosever account it is.
- **Personal APIs, for what only their creator uses.** The creator owns it
  and edits it freely.

**Editing a deployment API:** anyone may request a change, and an
administrator approves it before it takes effect, as with edits to a
published tool today. An administrator may also edit it directly.

**Promoting a personal API:** its creator requests promotion to the
deployment catalog, through the same administrator-approval path tools use
today. Promotion moves the API (it does not copy it), so tools already on it
keep working; the allow list starts as the creator and whoever their shares
reached.

Nothing in an API is secret, so a deployment API holds no secret: every
account on it is someone's own, or an administrator's account shared through
named tools (see Decisions).

## Sharing and adopting

Sharing a tool shares the tool and its API, never an account.

**A tool on a deployment API** carries a reference to it:

- **On the allow list:** the API shows as "connect your account". They
  connect, and the tool works.
- **Not on the allow list:** the API shows as "request access", which asks an
  administrator. Until then the tool does not run for them.

**A tool on a personal API** carries a copy:

- **The adopter gets their own copy of the API**, marked
  "connect your account". They paste a key, or click Connect for OAuth, and
  the tool works.
- **If a deployment API or one of theirs already covers that host,** they
  are offered it instead, so nobody ends up with two GitLab APIs. Choosing it
  is one click; it is never chosen silently.
- **The adopter can edit their copy,** widening its endpoints, say: it only
  ever spends their own account.
- **A tool refers to its API by a stable id,** never by name. Two users'
  "gitlab" APIs are two APIs.

Sharing a personal API on its own (without a tool) is the same: a template
the adopter copies and connects. A deployment API is not shared: its allow
list says who may use it.

**Building more on an adopted API.** The limit follows whose account pays and
acts, not the API, which is only a recipe:

- **With their own account,** the adopter may build any tools they like on
  the API. It is their copy and their key: they could have set it up from
  scratch and built the same things, so holding them to the shared tool would
  only add friction. Every call acts as them and is recorded as theirs. The
  API's endpoint and method lists still apply: on a copy they are theirs to
  change, on a deployment API they are the administrator's.
- **With an account someone shared with them** (see Agents), they may
  use it only through the tools its owner named. The key is not theirs, so
  its owner decides what it is spent on.

## OAuth

OAuth is a connected account like any other. It has two secrets where a key
has one:

- **The user's token** is the connected account: personal, made by clicking
  Connect, never shared.
- **The app registration's client secret** is part of the API, not of any
  account. It never reaches the adopter: oddjob keeps it on the server and
  uses it only to exchange the code for a token.

So an adopted OAuth API references the owner's app registration, and two
things differ from a key, both made visible:

- **Adopters depend on the owner's registration.** If the owner deletes the
  API, rotates the client secret, or the provider revokes the app, every
  account connected through it stops. The adopter's API says whose
  registration it uses; the owner sees who depends on it before deleting.
- **A registration is tied to this deployment's callback URL,** so an OAuth
  API is not shareable to another oddjob instance. The far side registers
  its own app with the provider and makes its own API.

## Agents

Sharing an agent lists **every API it can reach**, and the owner chooses for
each one:

| Choice | Meaning |
| --- | --- |
| **Share mine** | The agent's tools keep using the owner's linked account. Reachable only through this agent's tools; every call records which recipient made it; the secret stays on the server |
| **Bring your own** (default) | The recipient connects their own account; the API shows as "connect your account" |
| **Disallow** | The tools that use this API are withheld for the recipient. The agent runs without them, and its setup says what was left out and why |

Example: GitLab as **share mine**, Confluence as **bring your own**. The
agent reads GitLab with the owner's account and writes Confluence as the
recipient.

**What an account may do is set at the provider.** An owner who shares their
GitLab account sets that account up read-only at GitLab (a token scoped to
read). Oddjob does not guess "read" from HTTP methods. Its own reads-only
setting (GET and HEAD) stays as an option for providers whose tokens cannot
be scoped.

**Oddjob keeps the boundaries around a shared account:**

- Reachable only through the tools on that shared agent: not from tools the
  recipient builds, not from a copy of the agent.
- Ends immediately when the owner revokes the share or switches the API to
  bring your own; the API then shows as "connect your account".
- Every call through it records which recipient made it. At the provider it
  arrives as the owner, so oddjob's audit is the only place that says who.

**The list covers every route an agent reaches an API by:** its own tools
(api and toolbox tools, scripts that declare `fetch_via`), deployment tools it
has adopted, tools its skills bring, tool steps in its attached pipelines, MCP
connectors with credentials, and servitor appliances (their SSH credentials
are accounts too).

**Sub-agents are not shared.** A shared agent can dispatch only the
recipient's own agents, never the owner's. Sharing a chain would hand the
recipient agents, and accounts, they never saw.

**A gap in the list fails closed.** A shared agent may reach only the APIs on
its list marked share mine or bring your own; anything else is refused at
call time. A missed route then means a tool that does not work for the
recipient, never the owner's account leaking. The list and that check come
from the same resolver, so they cannot drift apart.

**Changes after sharing:** a tool added later with a new API starts as bring
your own for existing recipients, and the owner is told, to choose. Nothing is
shared automatically.

**Needs setup:** a shared agent lists each bring-your-own API the recipient
has not connected, and is unavailable until every one is.
Disallowed APIs are not on that list: they are withheld by the owner's choice.
The relink picker primitive (`OrchestratorRowAction.PickerSource`, pick a
target then act) covers "use an API you already have".

## Repairs

A repair is made in a **working copy** first, tested, then published. Who
publishes depends on where the thing lives.

**A tool**

1. The owner, or Builder for them, edits their own working copy in place.
   While it is being fixed, only the owner's own agents use it.
2. They test it with their own account (`tool_def` test) until it passes.
3. Then it reaches others by where it is shared:
   - **Named people get the owner's fix live.** The owner chose to trust that
     small group with their changes; the edit reaches them when it is saved.
   - **The deployment rung gets an approved version.** The owner requests the
     update, an administrator approves the diff, and everyone moves to the new
     version at once (the snapshot versioning proposed 2026-09-24; edits to a
     published tool already reach only the editor's agents until an update is
     approved). Until then everyone keeps the last approved version: a fix
     never lands half-tested on everyone.

**A deployment API** (its base URL moved, its version changed, its auth
header changed). Users cannot edit it, so the fix goes the same way, with a
working copy of the API:

1. **The drafter makes a working copy of the API,** tied to a change request.
   Only the drafter's own tools and tests use it, with their own account. It
   keeps the deployment API's denied endpoints, so testing cannot step
   outside the administrator's limits.
2. **The administrator sees the diff with the drafter's passing test
   results,** and approves, or edits the deployment API directly (often the
   fastest fix).
3. **On approval everyone moves to the fixed API,** and the drafter's working
   copy is retired: their tools point back at the deployment API.
4. **While it is broken, failures say so.** Affected users see that the API
   is failing and a fix is waiting for approval, not an opaque 404. The
   administrator is told, with the failing calls attached.

**Builder repairs the right layer.** When the same failure hits every tool on
an API (the base URL answers 404, auth is refused everywhere), the API is
broken, not the tools. Builder drafts an API change request instead of
rewriting tools that were never broken. This is a structural check on the
failures, not prompt copy.

## Enforcement

One question at dispatch: **what is this tool's API, and does the calling
user have a connected account for it they may use this way?**

- The user's own account: send, within the API's endpoint and method lists.
  On a deployment API the user must also be on its allow list.
- No account: refuse before sending, naming the API and where to connect.
- An owner's account shared through an agent: the share must still include
  the user, the call must come from one of that agent's own tools, and the
  API must be marked share mine.
- An API the shared agent's list does not mark share mine or bring your own:
  refuse.
- A plain fetch to a host covered by an account the user may reach only
  through named tools: refuse with the guidance shipped in v0.7.429
  (`SecuredCoverRefusal`): repair the bound tool in place, or stop and ask.

URL lists keep being judged as written and as the server reads them
(`credentialURLReadings`, v0.7.429).

## What this removes

- Ownerless secrets. `Owner == ""` credentials become deployment APIs, which
  hold no secret; a global secret becomes an administrator's shared account
  (below).
- Name resolution of a tool's credential (`Resolve`'s same-name fallback).
- `Secured` as a credential field, and `ApprovedToolBindings` filled at
  authoring: a tool uses its API, and an account's own share decides which
  tools may spend it.
- The `fetch_url` auto-route and `fetch_url_<cred>` for an account reachable
  only through named tools. For a user's own accounts they stay.

Kept: revocation tombstones; endpoint and method lists; confirm-before
settings; the audit ledger, now recorded per connected account.

## Migration

Nothing that works today should stop working on upgrade.

1. **Each credential splits** into an API (its configuration) and connected
   accounts (its secrets): one for the owner's own secret, one per user for a
   `per_user` credential's stored keys and tokens.
2. **Deployment credentials** (`Owner == ""`) become deployment APIs, with
   today's Access list (`AllowedUsers`) as the allow list. Their per-user keys
   and tokens become those users' accounts. A credential with one global
   secret also gets a **shared account** owned by an administrator chosen at
   upgrade, usable by today's Access list and, if it was secured, only through
   today's `ApprovedToolBindings`.
3. **Tools** get their API's id from what `Resolve(name, owner)` gives today.
   Each user who runs a tool through name resolution gets that resolution
   recorded, and listed on their setup page so they can see and change it.
4. **Agents already shared** get their list made at upgrade with each API set
   to what works today (share mine where the recipient reaches the owner's
   credential now, bring your own otherwise), shown to the owner to review.
5. **A tool whose API has no account for its user** becomes "connect your
   account", rather than failing at call time.

## Decisions

Settled in discussion (2026-10-07):

- Owner-first: every tool and account has an owner. An API is personal
  (owned by its creator) or a deployment API (governed by an administrator).
- **Deployment APIs are global, with an allow list,** and referenced, not
  copied: one definition per service. Changes are requested by anyone and
  approved by an administrator, who may also edit directly. A personal API
  is promoted through the same approval path tools use.
- Tool → API → connected account.
- An API is a recipe, shared as a template; the adopter fills in their own
  key, password or OAuth.
- OAuth is a connected account; the app registration belongs to the API, is
  referenced by adopters, and the dependency is visible.
- An adopter with their own account may build more tools on an adopted API;
  a shared account is limited to the tools its owner names.
- **A connected account can be shared, only through an agent share,** per
  API: share mine, bring your own (the default), or disallow. What a shared
  account may do is set at the provider.
- **Sub-agents are not part of an agent share:** a shared agent dispatches
  only the recipient's own agents.
- The list of an agent's APIs fails closed, and comes from the same resolver
  as the call-time check.

- **An adopted personal API is a copy** the adopter owns, not a reference to
  the owner's. A reference would pass the owner's later changes to everyone, and
  let the owner change what the adopter's own key may do. The owner's fixes
  reach adopters later as an "update available" notice they choose to take.
 

- **A shared agent is unavailable until every bring-your-own API is
  connected,** rather than running with those tools withheld: an agent that
  quietly behaves differently for its recipient than for its owner is worse
  than one that says it is not ready. Disallowed APIs do not count; they are
  withheld by the owner's choice.

- **Repairs go through a working copy:** fix, test with your own account,
  then publish. Named people get a tool owner's fix live; the deployment rung
  gets an administrator-approved version. A broken deployment API is fixed
  by a change request whose drafter tests it on a working copy, or by an
  administrator directly. Builder repairs the API, not the tools, when every
  tool on it fails the same way.

- **A company key nobody personally holds** (a shared search or LLM key) is
  an administrator's account, shared through tools the administrator names:
  the same boundary as an agent share. It is reachable only through those
  tools, every call records who made it, and the secret stays on the server.

- **An OAuth API is not shareable across oddjob instances.** Its app
  registration is tied to this deployment's callback URL; another instance
  registers its own.

Open: none. Every decision this design raised is settled; what remains is
building it, in the phases below.

## Phases

1. **Split the record.** API and connected-account records, migrated from
   every `SecureCredential`; dispatch resolves tool → API → account, with
   name resolution as the fallback the migration fills. No behaviour change.
2. **The two homes.** Deployment API catalog with allow lists, change
   requests and administrator approval, promotion of personal APIs. Tools
   carry their API: a reference to a deployment API, a copy of a personal one;
   "connect your account" and "request access"; the offer of an existing API
   for that host.
3. **Agent shares: the API list.** Share mine, bring your own, disallow; one
   resolver for the list and the call-time check; needs setup.
4. **Shared accounts** replace Secured and key lending; authoring stops
   auto-binding.
5. **Repairs and versions.** Working copies for tools and deployment APIs,
   change requests with test results, approved versions on the deployment
   rung, the "failing, fix waiting" notice, and Builder's API-layer check.
6. **Ownerless credentials migrated,** then the name fallback and the old
   fields removed.
