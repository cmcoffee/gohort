# Web chat as a bridge, and external contacts

Status: proposed (2026-11-14). Nothing built.

## Why

Two ways bring people with no gohort account to an agent through someone
else's platform (docs/design/outside-audience.md): a Teams bot, and a
platform's own chat (a customer portal's widget, a help desk, a voice line)
calling the Model API. They are the same thing, a platform carrying its
conversations into an agent, but they are set up in different places:

| Piece | Where it is now |
|---|---|
| The Teams bot | Bridges › Message bridges |
| The key a platform's chat calls with | Account › API keys, kind "People outside gohort" |
| Whether the agent serves them, and what it lends them | The agent's Access page |
| Who they are | The agent's Access page, and Admin › People |

Someone connecting a website chat looks for a key on their Account page;
someone connecting Teams goes to Bridges. And the people are called
"people outside gohort", where every other bridge calls the person on the
other end a **contact**.

This makes the platform's chat a bridge beside Teams, lists each bridge's
contacts on the bridge, and names them as the rest of Bridges does.

## Terms

- **Contact**: a person on the other end of a bridge, as Bridges already
  says (an iMessage or Signal handle, a Teams user).
- **External contact**: a contact with no gohort account whom an agent
  serves in a store of their own (`outside:<source>:<person>`), with the
  owner's memory underneath read-only, lent tools only, and per-person
  limits. Today: Teams senders (when the agent serves them) and the people
  a web chat names. iMessage and Signal contacts are contacts but not
  external ones: they run in the owner's channel for that room.

## What exists (verified in code)

| Piece | Where |
|---|---|
| The outside key kind | `core/account_token.go`: `Scope.Outside`, `KeyKindOutside`; it opens one door (`OutsideAudienceDoor`, the Model API) and only `agent:` targets |
| Naming the person | `apps/openaiapi/openaiapi.go` `outsidePerson`: the `X-End-User` header or the request's `user` field, refused when missing |
| Their store | `agenthost.OutsideScope(token.ID, person)`: the key's ID is the source, so a key's contacts are the stores named `outside:<key id>:` |
| Teams' contacts | `apps/bridges/teams.go` `outsideScope`: the bot's app ID is the source, a DM per person, a group per conversation |
| The agent's settings | `apps/orchestrate/page_agent_access.go`, section "People outside gohort": the switch, lent tools and credentials, per-person limits, and the agent's list |
| The admin list | `apps/admin/page_outside_people.go`: everyone, across owners |
| Key lifetime | `tune_account_key_lifetime`, 90 days by default: an outside key stops working when it expires |

## Proposal

### A. A Web chat bridge

In Bridges › Message bridges, beside the Teams bot: **Add a web chat**.

- Its form: a name ("Customer portal"), the agent or agents it reaches
  (only those set to serve external contacts), and Make key. The key is
  shown once, as connector keys are.
- Its page: the endpoint (`<External URL>/v1/chat/completions`, model
  `agent:<id>`) and an example call; the rule, plainly, that the key lives
  on the platform's server and never in a browser, and that every call
  names its person (`X-End-User` or `user`); when it was last called; its
  contacts (B); Pause and Delete.
- **Pause** refuses its calls with a fixed message, without revoking the
  key, so the platform can be switched back on without a new key.
- **Delete** revokes the key. Its contacts stay, listed under External
  contacts with "bridge removed", until someone deletes them, since a
  request to be forgotten may come later.

### B. Each bridge lists its contacts

The Teams bot and each web chat show their own external contacts: the
stores whose source is that bot's app ID or that key's ID, already the first
part of every store's name, so this is a filter over what exists. Each row
opens the same view as today (what the agent saved, the conversations), with
Clear and Delete.

### C. The agent keeps what protects it

The agent's Access page keeps its section, renamed **External contacts**:
the switch (**Serves external contacts**), the tools and credentials it
lends them, and the per-person limits. Those guard the owner's agent and
budget whichever bridge the people arrive by, so they stay on the agent.
Its list stays too: everyone who has talked to this agent, across bridges,
each row naming its bridge.

### D. The Account page

The "People outside gohort" kind leaves the Account page's New API key. A
key of that kind already made is still listed there, marked "Web chat:
managed in Bridges" with a link, and can still be revoked.

### E. Admin

Admin › People › **External contacts**: everyone across owners and bridges,
each row naming its owner and bridge, for when an owner is gone or a
request to be forgotten reaches the administrator. The Tuning category of
the same ceilings is renamed with it.

### F. The rename

"People outside gohort" becomes **external contacts** in everything people
read: the admin section and Tuning category, the Access page section and
switch, key-kind labels, refusals people see ("this agent does not serve
external contacts"), and help text. Not renamed:

- **The text models read.** Prompts describe these people to the agent;
  rewording them changes what the model reads, and can change how agents
  behave. A separate, tested change if wanted.
- **Code and stored names.** `outside:` stores, `OutsideScope`,
  `Scope.Outside` and the tunable keys stay, so nothing stored changes.

## No migration

A web chat bridge is an outside key: the same record, of the same kind, in
the same table. Bridges lists the owner's outside keys as web chats and
mints new ones; nothing existing is rewritten. The bridge's name is the key's
name. Pause is one new field on the key's scope, absent on every key now,
which reads as not paused. An existing outside key appears in Bridges as a
web chat the first time the page is opened.

## Steps

1. The rename (F), on its own: copy only, no behaviour change.
2. Bridges: the Web chat bridge (A), minting and listing outside keys,
   Pause, Delete; the Account page's kind moved (D).
3. Contacts per bridge (B), for Teams and web chats; the bridge named on
   every contacts list (C, E).

## Open questions for the owner

1. **Key lifetime.** An outside key expires with every personal key, 90 days
   by default, and the website's chat then stops answering. A bridge key is
   a server's credential, like a connector key, not a person's: should it
   be exempt from the lifetime, or show its expiry on the bridge with a
   Renew that swaps in a new key?
2. **One agent per web chat, or several?** A key can reach several agents
   today (the platform picks with `model: agent:<id>`). One per bridge reads
   simpler on the page; several keeps what keys can do now.
3. **Signed identities.** The outside-audience design left an optional
   hardening: the platform signs each person's ID with a per-bridge secret,
   and an unsigned one is refused. Now, as a setting on the bridge, or
   later?
4. **iMessage and Signal contacts.** Should a bridge like iMessage be able
   to serve its contacts as external ones (a store each, lent tools,
   limits), the way Teams does? That would be its own design.
