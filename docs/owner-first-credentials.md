# Owner-first credentials and tools

**Status:** Proposed, 2026-10-07. Nothing here is built.
Supersedes, once built: the credential half of the global plane in
`tool-credential-namespacing.md`, and the binding model in
`secured-credential-tool-binding.md`. Builds on the three sharing rungs in
`sharing-governance.md` (own, named people, deployment).

## Why

Every credential and every tool has an owner. Today a credential can also have
no owner (`Owner == ""`, a deployment credential), and that one exception is
where the model leaks:

- **A deployment credential is reachable by anything a permitted user writes.**
  Its Access list (`AllowedUsers`) decides *who*; *how* is the `fetch_url`
  auto-route, the `fetch_url_<cred>` catalog tool, any tool the user authors
  that declares it, or a script's `fetch_via`.
- **Securing it does not close that.** A secured credential is reachable only
  through tools that declare it, but declaring it binds the tool at creation
  for any user (`tool_def` create, `create_api_tool`, `temptool_create` all
  call `ApproveToolBinding`). The 2026-09-29 rule that a new tool is bound to a
  deployment key only by an administrator is checked at dispatch
  (`EnforceSecuredBinding`) and skipped at authoring.
- **A shared tool finds its credential by name.** A tool names a credential
  (`TempTool.Credential = "gitlab"`), and `Resolve(name, user)` gives the
  recipient their own same-named credential, else the global one. That works
  when the names agree and picks the wrong key when Bob's "gitlab" is a
  different server.
- **Nothing tells a recipient what a shared tool needs.** It either works by
  name, or fails at call time with an error about a credential they have never
  heard of.

## The model

1. **Every credential has an owner.** What was a deployment credential is an
   administrator's credential shared with everyone: the deployment rung of
   sharing, applied to a credential, not a different kind of thing.
2. **A tool depends on an API, not on a credential name.** The dependency is
   the API's definition (below). In the owner's copy it is bound to the
   owner's credential.
3. **Sharing a tool shares the tool and the API definition, never the
   secret.** The recipient gets their own credential made from the
   definition, already bound to the tool, marked "needs your key".
4. **A binding is the user's own choice, stored per (user, tool, API).** It is
   never inferred from a name.
5. **Whether a shared credential is "secured" is a choice on the share,** not
   a property of the credential.
6. **A shared agent is usable once every API its tools need is bound.** Until
   then it shows as "needs setup".

## What travels with a shared tool

| Travels | Stays with the owner |
| --- | --- |
| The tool: its actions, templates, params, scripts | The secret: API key, password, tokens |
| The API definition: base URL, auth type and header name, allowed and denied endpoints, allowed methods, confirm-before settings | The owner's Access list and share lists |
| For OAuth: a reference to the owner's app registration (see OAuth) | The call history (audit ledger) |

The definition lands on the recipient's side as a credential **they own**:
they can edit it (widen its endpoints, say), because it only ever spends their
own key.

## Binding

A tool's dependency on an API is a **slot**: the API definition it was built
against. A slot is filled by a credential the user may use whose base URL
covers the definition's base URL. Three ways to fill one:

1. **Their key.** The definition that came with the tool, with the user's own
   secret pasted in. The default for a shared tool.
2. **A credential they already have** for that host. Offered first when one
   exists, so a user does not end up with two GitLab credentials.
3. **A credential shared with them,** when its share allows use in their own
   tools (see Sharing a credential).

Rules:

- **One candidate is pre-selected, never silently bound.** When exactly one
  credential the user may use covers the slot, binding is one click.
- **The binding outlives edits.** Repairing a tool in place (`tool_def`
  update) keeps it; the binding belongs to the user and the tool, not to a
  version of the tool.
- **Dispatch checks the binding, not the name.** A call resolves the
  credential from the user's binding for that tool's slot; a name match alone
  never resolves.
- **An unbound slot fails before the call,** naming what to bind and where.

## Sharing a credential

Sharing a credential (the key, not just the definition) already exists on the
named-people rung (`SharedReadOnly` / `SharedReadWrite`: writes arrive at the
far end as the owner). It gains one choice:

- **Usable in their own tools:** the recipient can bind it to any slot it
  covers.
- **Only through my tools:** the recipient can use it only through tools the
  owner named. This is what Secured means now, moved onto the share.

A deployment credential, in this model, is an administrator's credential
shared with everyone, usually "only through my tools". The authoring hole above
disappears: nothing binds at creation, and the share decides what is bindable.

## OAuth

OAuth travels the same way as an API key. It has two secrets where an API key
has one:

- **The user's token** is personal, exactly like a key: each user connects and
  gets their own, and it never travels.
- **The app registration's client secret** identifies the integration, not a
  user. It never reaches the recipient: gohort keeps it on the server and uses
  it only to exchange the code for a token.

So the shared definition carries a reference to the owner's app registration,
and the recipient's "needs your key" is a Connect button. Two things differ
from a key, and both need to be visible:

- **The recipient depends on the owner's registration.** If the owner deletes
  the credential, rotates the client secret, or the provider revokes the app,
  every connection through it stops. The recipient's binding says whose
  registration it uses; the owner sees who depends on it before deleting.
- **A registration is tied to this deployment's callback URL.** A tool shared
  to another gohort (peer sharing) needs a registration there.

## Agents

A shared agent arrives with its tools' slots filled the same way, so most
become "needs your key". The agent shows as **needs setup** until every slot is
bound: one list, each API with its picker (the relink picker primitive,
`OrchestratorRowAction.PickerSource`, already does pick-a-target-then-act).
Not runnable with some tools missing: an agent that quietly behaves differently
for its recipient than for its owner is worse than one that says it is not
ready.

## What this removes

- `Owner == ""` credentials (migrated to an administrator).
- Binding by name: `Resolve`'s same-name fallback for a tool's credential.
- `Secured` as a credential field (it becomes a share option),
  `ApprovedToolBindings` auto-filled at authoring, and the dispatch-only
  admin rule for deployment keys.
- The `fetch_url` auto-route and the `fetch_url_<cred>` catalog tool for a
  credential shared "only through my tools". For the owner's own use, and for a
  share that allows own tools, they stay.

Kept: per-user credentials (`CredScope = per_user`: shared setup, each user's
own secret) are a special case of "their key"; revocation tombstones; URL
allow and deny lists, judged as written and as the server reads them
(`credentialURLReadings`).

## Enforcement

One question at dispatch: **which credential does this user's binding for this
tool's slot name, and may this user use it this way?**

- Binding present and the credential is theirs: send.
- Binding to a shared credential: the share must still include the user, and
  allow this tool (own tools, or one of the owner's named tools).
- No binding: refuse before sending, naming the slot and where to bind it.
- A plain fetch to a host only an "only through my tools" share covers: refuse
  with the guidance shipped in v0.7.429 (`SecuredCoverRefusal`): repair the
  bound tool in place, or stop and ask.

## Migration

Nothing that works today should stop working on upgrade.

1. **Deployment credentials** get an owner: an administrator chosen at
   upgrade. Shared with everyone, keeping today's Access list as the share
   list. A secured one becomes an "only through my tools" share naming its
   current `ApprovedToolBindings`.
2. **Bindings from name resolution:** for every tool a user can run, bind its
   slot to whatever `Resolve(name, user)` gives them today, and list those
   bindings on the user's setup page so they can see and change them.
3. **Tools with a slot nothing resolves for** become "needs setup" rather than
   failing at call time.

## Decisions

Settled in discussion (2026-10-07):

- Owner-first: every credential and tool has an owner.
- The API definition travels with a shared tool; the secret never does.
- The recipient owns the credential made from the definition.
- OAuth travels like a key; the app registration stays with its owner and is
  referenced, with the dependency visible.

Open:

1. **Unbound slots:** whole agent unavailable until bound (proposed above), or
   runnable with those tools withheld and a visible notice?
2. **Who owns migrated deployment credentials:** one chosen administrator, or
   each credential's creator where the audit log records one?
3. **Repairs to a shared tool:** do recipients run the owner's fix live, or an
   approved version (the snapshot-versioning question from 2026-09-24)?
   Bindings carry over either way.
4. **Peer sharing to another gohort:** does the definition travel with an
   empty OAuth registration for the far side to fill, or is an OAuth API not
   shareable across machines?

## Phases

1. **Slots and bindings, owner's side.** A tool records its API definition;
   dispatch resolves through the binding, with name resolution as the
   fallback that migration fills in. No behaviour change.
2. **Shared tools carry the definition.** The recipient's credential is made
   from it; "needs your key"; setup page; one-click candidates.
3. **Agents: needs setup.**
4. **Credential shares gain "only through my tools".** Secured moves onto the
   share; authoring stops auto-binding.
5. **Migration of `Owner == ""` credentials,** then removal of the name
   fallback and the old fields.
