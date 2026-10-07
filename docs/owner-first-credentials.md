# Owner-first: tools, APIs and connected accounts

**Status:** Proposed, 2026-10-07. Nothing here is built.
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
| **API** | The recipe to connect: base URL, auth type and header name, allowed and denied endpoints, allowed methods, confirm-before settings, the OAuth app registration | Its creator. An adopter owns their copy | Yes, as a template |
| **Connected account** | The secret for one API: key, password, OAuth token | The person who connected it | No (see Open decision 1) |

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

## Sharing and adopting

Sharing a tool shares the tool and its API, never an account.

- **The adopter gets their own copy of the API** (Open decision 2), marked
  "connect your account". They paste a key, or click Connect for OAuth, and
  the tool works.
- **If they already have an API for that host,** they are offered it instead,
  so nobody ends up with two GitLab APIs. Choosing it is one click; it is
  never chosen silently.
- **The adopter can edit their copy,** widening its endpoints, say: it only
  ever spends their own account.
- **A tool refers to its API by a stable id,** never by name. Two users'
  "gitlab" APIs are two APIs.

Sharing an API on its own (without a tool) is the same: a template the
adopter copies and connects.

**Building more on an adopted API.** The limit follows whose account pays and
acts, not the API, which is only a recipe:

- **With their own account,** the adopter may build any tools they like on
  the API. It is their copy and their key: they could have set it up from
  scratch and built the same things, so holding them to the shared tool would
  only add friction. Every call acts as them and is recorded as theirs, and
  their copy's endpoint and method lists still apply.
- **With an account someone shared with them** (Open decision 1), they may
  use it only through the tools its owner named. The key is not theirs, so
  its owner decides what it is spent on.

## OAuth

OAuth is a connected account like any other. It has two secrets where a key
has one:

- **The user's token** is the connected account: personal, made by clicking
  Connect, never shared.
- **The app registration's client secret** is part of the API, not of any
  account. It never reaches the adopter: gohort keeps it on the server and
  uses it only to exchange the code for a token.

So an adopted OAuth API references the owner's app registration, and two
things differ from a key, both made visible:

- **Adopters depend on the owner's registration.** If the owner deletes the
  API, rotates the client secret, or the provider revokes the app, every
  account connected through it stops. The adopter's API says whose
  registration it uses; the owner sees who depends on it before deleting.
- **A registration is tied to this deployment's callback URL.** Sharing to
  another gohort needs a registration there (Open decision 5).

## Agents

A shared agent arrives with its tools and their APIs. It shows as **needs
setup**, listing each API without a connected account, until every one is
connected (Open decision 3). The relink picker primitive
(`OrchestratorRowAction.PickerSource`, pick a target then act) covers the
"use an API you already have" choice.

## Enforcement

One question at dispatch: **what is this tool's API, and does the calling
user have a connected account for it they may use this way?**

- The user's own account: send, within the API's endpoint and method lists.
- No account: refuse before sending, naming the API and where to connect.
- A shared account (if Open decision 1 allows them): the share must still
  include the user, and allow this tool.
- A plain fetch to a host covered by an account the user may reach only
  through named tools: refuse with the guidance shipped in v0.7.429
  (`SecuredCoverRefusal`): repair the bound tool in place, or stop and ask.

URL lists keep being judged as written and as the server reads them
(`credentialURLReadings`, v0.7.429).

## What this removes

- `Owner == ""` credentials, migrated (below).
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
2. **Deployment credentials** (`Owner == ""`) get an owner, an administrator
   chosen at upgrade. A credential with one global secret becomes that
   administrator's API with a **shared account** (Open decision 1), shared
   with today's Access list and, if it was secured, only through today's
   `ApprovedToolBindings`.
3. **Tools** get their API's id from what `Resolve(name, owner)` gives today.
   Each user who runs a tool through name resolution gets that resolution
   recorded, and listed on their setup page so they can see and change it.
4. **A tool whose API has no account for its user** becomes "connect your
   account", rather than failing at call time.

## Decisions

Settled in discussion (2026-10-07):

- Owner-first: every tool, API and account has an owner.
- Tool → API → connected account.
- An API is a recipe, shared as a template; the adopter fills in their own
  key, password or OAuth.
- OAuth is a connected account; the app registration belongs to the API, is
  referenced by adopters, and the dependency is visible.
- An adopter with their own account may build more tools on an adopted API;
  a shared account is limited to the tools its owner names.

Open (proposed answers in **bold**):

1. **Can a connected account be shared?** Some services have one company key
   nobody personally holds (a shared search or LLM key). **Proposed: only as
   an explicit share by its owner, usable only through tools the owner names.**
   That is where Secured ends up, and today's lending of a key to named people
   (reads only, or reads and writes) becomes the same kind of share.
2. **Is an adopted API a copy or a reference?** A reference passes the
   owner's fixes to everyone, and lets the owner change what the adopter's key
   may do. **Proposed: a copy**, with an "update available" notice later.
3. **Unconnected APIs in a shared agent:** **proposed: the agent is
   unavailable until every API is connected,** rather than running with those
   tools withheld.
4. **Repairs to a shared tool:** recipients run the owner's fix live, or an
   approved version (the snapshot-versioning question from 2026-09-24)?
   Accounts carry over either way.
5. **OAuth across gohort instances:** does the API travel with an empty
   registration for the far side to fill, or is an OAuth API not shareable
   across machines?

## Phases

1. **Split the record.** API and connected-account records, migrated from
   every `SecureCredential`; dispatch resolves tool → API → account, with
   name resolution as the fallback the migration fills. No behaviour change.
2. **Tools carry their API.** Sharing a tool or an API copies the API to the
   adopter, "connect your account", the offer of an existing API for that
   host.
3. **Agents: needs setup.**
4. **Account shares** (Open decision 1) replace Secured and key lending;
   authoring stops auto-binding.
5. **Ownerless credentials migrated,** then the name fallback and the old
   fields removed.
