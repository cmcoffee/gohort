# An app's credentials, the way an agent's are lent

Status: **design, not built**. Every stored change is additive, so **no
migration**. The default is **Mine for reads** for every app, the ones
already shared included (settled with the owner 2026-11-25).

## Where it stands

An app's scripts (data sources, actions) run as the app's **owner**
(`tools/appscript`: the session's `Username` is the owner). So:

- Every credential the owner may use is granted to every app they own, as
  `fetch_via:<name>` (`ownerFetchViaCaps`), and a plain fetch to a host a
  credential covers goes through it.
- A script that declares `tool:<name>` calls one of the owner's tools, with
  that tool's own credential setup.
- When the app is shared, everyone's runs are the owner's runs: reads and
  writes go out on the owner's keys, as the owner, for whoever pressed the
  button. The person the run is for is known (`Job.Caller`) but credentials
  never look at it.

A shared **agent** does it differently, and carefully
(`sharing-people-and-teams.md`):

- A recipient's run resolves credentials **as the recipient**: their own
  key first, then a lend of the owner's that covers that agent
  (`SecureCredential.SharedForAgents`, `credLendCoversAgent`), then the
  deployment's (`ResolveIn`, core/secure_api.go).
- Each credential gets a choice per agent, on the agent's "What it reaches"
  table: **Mine for reads** (the default), **Theirs**, **Mine, writes too**,
  **Disallow** (`share_plan.go`). `applyCredentialAnswer` turns the choice
  into lends (`LendForAgent`) and `WithheldFromRecipients`.
- A call is a read by its method (GET, HEAD, OPTIONS) or by matching the
  owner's "Calls that only read" (`credentialCallReads`). A write on a
  reads-only lend is refused at dispatch (`apiCall.allowed`) with "connect
  your own account", unless the person has their own key.
- A recipient's own key is kept per person under the owner's credential name
  and used for every call once connected (`SaveSharedAccount`,
  `recipientOwnSecret`), from Account, Connected accounts.
- Maintainers may only narrow (`maintainerMayChoose`).

The gate is in the shared dispatch path, so `fetch_via` and secure API calls
from a sandbox already go through it. Apps miss it only because their runs
are the owner's.

## What changes

### 1. A run for someone else resolves credentials as them

When an app's script runs for a person other than its owner (`Caller` is
not the owner: a shared app's button, its page's data, its agent acting for
that person, its MCP tools called by them), credential calls resolve **as
that person, through lends scoped to this app**, exactly as an agent
recipient's do. Everything else about the run stays the owner's: the
workspace, the records the script reads, the file stores, the tools.

- The run carries the app as the lend's scope, the way a run carries its
  agent. A call with no app in context and no own key of the person's
  fails closed, as an agent's does.
- The owner's own runs, and a scheduled run (nobody pressed anything), are
  the owner's, unchanged.
- A refused write reaches the script as the hook's error, and the action's
  outcome says it in the person's words: "This needs your own <name>
  account: connect it under Account, Connected accounts."

### 2. Each credential gets a choice per app

The owner chooses for each credential the app uses, on the app's Settings
page (the ⚙), in a "What it reaches" section built from the same table and
choices as an agent's (`reachTable`, `credentialRowChoices`):

| Choice | For someone the app is shared with |
|---|---|
| **Mine for reads** (default) | Reads use the owner's key; a write needs their own account. |
| **Theirs** | They use their own account for everything. |
| **Mine, writes too** | Everything on the owner's key, arriving as the owner. |
| **Disallow** | The credential is not reached at all in their runs. |

And one more column the agent table does not need: **Used by the app**,
on or off, so an owner can keep a credential out of the app entirely, their
own runs included (an agent's `DisabledCredentials`).

Maintainers of the app (`app-maintainers.md`) may only narrow, by the same
rule as an agent's (`maintainerMayChoose`).

### 3. Which credentials the table lists

The ones the app names or uses, not every credential the owner has:

- those an imported app declares (its needs, mapped under Connections);
- those a script declares (`fetch_via:<name>`);
- those the app's runs have actually used, recorded the first time a run
  resolves one (a small per-app table).

A credential with no choice made takes **Mine for reads**, on an app
shared before this as on a new one, and is listed for the owner to change.

### 4. Their own key

Nothing new: the person connects their own account under Account,
Connected accounts, keyed by the owner's credential name, and it is used for
every call once connected, as for an agent.

## Stored-shape changes

All additive:

- `SecureCredential.SharedForApps map[string][]string`: person to app
  scopes ("<slug>"; the owner is the credential's), beside
  `SharedForAgents`. A field of its own rather than app ids put into
  `SharedForAgents`, so one id never means two kinds of thing to the code
  that checks it (the lesson of name-keyed settings).
- `AppSpec.WithheldFromRecipients []string`, `AppSpec.DisabledCredentials
  []string`, `AppSpec.CredentialsDecided bool`, as on `AgentRecord`.
- A per-app "credentials used" table in the owner's store, name and first
  use.

**No migration.** Nothing stored is rewritten: a credential with no choice
is read as Mine for reads. That is a change in behaviour for apps already
shared (see "Settled", below), not in data.

## Order

1. The run's credential identity and scope (item 1), with the refusal
   message, and the owners' notice below. From this step a shared app's
   recipients read on the owner's keys and need their own account to
   write.
2. The "What it reaches" table on the app's ⚙ (item 2), and the record of
   what an app uses (item 3).
3. Maintainers' narrowing.

## Settled

- **The default is Mine for reads, for every app** (the owner, 2026-11-25),
  the ones already shared included, as for an agent. An app shared today
  whose recipients write somewhere stops doing that on the owner's key: the
  write is refused until they connect their own account, or the owner
  chooses Mine, writes too for that credential. So the owner of each shared
  app that has used a credential is told once, in the share notices: what
  changed, which of the app's credentials it affects, and where to choose
  (the app's ⚙, What it reaches).

## Open questions

1. **Data sources on page load.** A data source runs for whoever opens the
   page, so with Theirs, a person without their own account sees the page
   fail to load its data. Recommended: the data source's error says what to
   connect, and the page helper's error carries it to the page, as an
   action's does.
2. **Tools a script calls (`tool:<name>`).** A tool brings its own
   credential setup and runs as the owner. Recommended: the same rule
   applies inside it when the run is for someone else, since it is the same
   dispatch path; listed here because it widens what item 1 touches.

## Considered and not chosen

- **Putting app scopes in `SharedForAgents`.** No new field, but an agent
  id and an app slug in one list is the shape that let a renamed tool
  escape its owner's settings before.
- **A per-app whitelist only, with no read/write split.** Simpler, but it
  leaves a shared app writing as its owner for everyone, which is the gap.
- **Running a recipient's app script entirely as them.** Their records and
  workspace instead of the owner's would break every app's data model; only
  credentials move.
