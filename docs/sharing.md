# Sharing

Sharing goes one way: **an owner shares with people, teams or Everyone.**
What you share stays yours. The people you add use it, each with their own
conversations, memory and records, and you can take it back at any time.

You can share agents, apps, pipelines, machines, knowledge collections,
skills, guides and Servitor systems. Each has a share list in the same shape:
pick people, a team, or Everyone. (A Servitor system is the exception: it is
shared with people and teams, never newly with Everyone.)

## Who you can share with

- **People**: anyone with an account on the deployment.
- **Teams**: an administrator makes teams under **Administrator › People ›
  Teams** and puts people on them. You can share with any team you are on,
  and everyone on it gets the share, including people who join later.
- **Everyone**: every signed-in user. Adding Everyone is a **request**: it
  waits for an administrator under **Requests to share** and takes effect
  once approved. Taking Everyone off is yours and applies at once.

Sharing with people and teams takes effect at once, and they get your saved
changes as you make them.

## Sharing an agent

Open the agent and choose **Agent › Share**. It opens the **Share** tab of the
agent's Security page:

- **Who may run this agent**: the people, teams or Everyone who may use it.
- **Who may maintain this agent**: people who adjust it for you (see
  [Maintainers](#maintainers)).
- **What a recipient sees**: which of your memory layers travel with it
  (see [Memory](memory.md#when-an-agent-is-shared)).
- **External contacts**: whether people with no gohort account can talk to
  it, and **People it has served**, the ones who have.

### What travels with it

Your tools, documents and skills travel with the agent, usable **through this
agent and nowhere else**. A recipient cannot attach them to an agent of their
own. Each person gets their own sessions and memory under it, and nobody sees
anyone else's conversations.

**What it reaches**, on the agent's own page (**Agent › Edit**), lists
everything the agent depends on. Check it before you share, and come back to
it when something works for you and not for them.

### Credentials: whose account a call goes out as

A credential is never handed over. A recipient reaches your key only through
the agent's tools, and never sees it. Each credential row in **What it
reaches** chooses whose account the calls use:

| Choice | Until they connect their own account | After they connect |
|---|---|---|
| **Mine for reads** (the default) | Reads use your key; a write asks them to connect their own | Everything goes out as them |
| **Theirs** | The tools need their own account | Everything goes out as them |
| **Mine, writes too** | Everything uses your key, so what they write arrives as you | Everything goes out as them |
| **Disallow** | The tools on it are withheld from them | Withheld |

People connect their own accounts under **Extensions › Connected accounts**.
Once they do, every call to that service goes out as them, reads included, so
they see what their own access allows (which may be less than yours) and the
service's own records name them.

There is no quiet fallback: if their account stops working, the call says to
reconnect it, rather than switching back to your key. To go back to reading
with your key, they disconnect.

Whether a call reads or writes is decided by how it is sent. GET, HEAD and
OPTIONS read; anything else, and any script, writes. A toolbox's owner can
mark an action that only reads although it is sent as a write (a search sent
as a POST). Where the service offers a read-only token, use one for a key you
lend for reads: gohort's rule is the second guard, not the only one.

Every call records which person made it, so gohort's dispatch ledger says
who even when the service only saw your key.

## Maintainers

A maintainer keeps something of yours working without owning it.

**On an agent**, a maintainer opens it in an editor of their own and changes
its prompt, rules and behaviour. Each change is saved on your agent, with its
history naming who made it, so you can roll it back. What stays yours:

- who uses it and who maintains it, and publishing it
- its guardrails and security settings
- what it remembers
- giving it a tool, skill or collection: a maintainer may take one away,
  never add one

A maintainer sees everything the agent reaches, and on a credential row may
only narrow your key (to **Theirs**, to **Disallow**, or from writes down to
reads). Only you can lend it further. When a maintainer uses the agent, they go
through the same rules as everyone it is shared with.

**When a tool breaks while you are away**, a maintainer can fix it under **Fix
a tool**: a corrected copy that people using this agent run until you return.
You find it under **Fixes from maintainers** on the agent's page: **Keep**
puts it into your own tool, **Discard** drops it. You, and anyone you lent the
key to with writes, keep running your original until then.

Maintaining is a separate grant from using: add a maintainer to **Who may run
this agent** too if they should use it.

## Sharing an app

An app's **Share** dialog has three lists:

- **Shared with**: who may use it. Each person keeps their own records in it
  and never sees anyone else's, unless the app keeps a shared collection.
- **Maintainers**: people who can change its sections and wording through
  Builder. Its scripts, page code, sharing and settings stay yours, and every
  change they make is kept in its history under their name.
- **Managers**: people who run the app for its users. They get the actions
  and sections the app marks for managers.

The dialog also says which file stores the app reaches, and whether each
person gets their own folder in them.

## Taking it back

Remove a person or team from the list and the share ends for them at once,
along with any key lent to them through it.

## What an administrator sees

- **Requests to share** (**Administrator › Oversight**): requests to share
  with Everyone, waiting for a decision.
- **Shared with everyone** (same tab): everything shared with the whole
  deployment, with **Take back**. Taking something back leaves it with its
  owner and keeps the people and teams the owner named.
- **User-owned agents**, **User-owned pipelines** and **User-owned
  machines** (same tab): what people have made and whom they share it with.

An administrator approves what reaches Everyone, because it runs the owner's
tools and spends the owner's keys for every user. What people share with each
other is between them.
