# Servitor

Servitor holds your **systems**: Linux machines it reaches over SSH, and code
repositories. It maps each one, investigates it when you ask a question, and
keeps what it learns. Agents reach a system through Servitor, so an agent can
answer "why is this service failing" from the machine itself, or "what does
this setting do" from the code, without ever holding a shell or a password.

Every command Servitor runs is checked first. Reading is let through;
anything that changes the system asks you, unless you have said it may.

## The kinds of system

- **SSH**: a machine reached by host, port and user, signing in with a
  password, a private key or both. A **Sudo password** or **Root password**
  lets commands that need root run: Servitor supplies the password itself, and
  it never reaches the model or the transcript.
- **Git Repository**: a repository cloned from its URL (with a branch and an
  access token for a private one), or, for an administrator, a folder on this
  server. **Skip Directories** leaves out vendored or generated code.
  **Refresh** pulls the latest code before re-reading it.
- **Remote (peer)**: a machine this deployment cannot reach but another
  gohort instance can. The peer is only a wire: the investigation, your
  approvals and what is learned all stay here, and only each command travels.
  The peer has to grant **Run commands** on its key for you.

To add one, open **Servitor** and press **New**.

## Mapping, and what it keeps

When a system is added, Servitor **maps** it: what runs there, the services,
the versions, the layout of a repository. That becomes the system's
**Profile**. Investigations add more as they go: facts, the techniques that
worked, notes. **Refresh** maps it again, and **Map App** maps one program's
subcommands and options in detail.

What it keeps is the system's, and you manage it from the toolbar:

- **Profile**: what the map found.
- **Rules**: instructions for investigating this system (where its logs are,
  what never to restart).
- **Memory**: saved facts, Reference Memory and Graph Memory, as for any agent
  (see [Memory](memory.md)).
- **Export knowledge** (under **More**): everything learned, as a Markdown file,
  with no credentials in it.
- **Clear Memory** (under **More**): forget the profile, facts and notes.

## Investigating

Pick the system and ask, in Servitor's chat:

> *"Disk filled up last night. What wrote it, and is it still growing?"*

Servitor plans the investigation, runs commands, reads what comes back, and
answers with what it found and the commands behind it. A **Terminal** pane
beside the chat gives you the machine yourself, when you want to look.

### Commands that need approval

Each command is checked before it runs. Commands that only read run. One that
falls into a risky category asks first, on a card in the chat:

- **Allow** runs it this once.
- **Always** saves a rule covering commands like it, shown for you to edit
  before it is saved: `<number>` matches any number, `<text>` any word
  without a slash, and `*` any part of a name inside its folder.
- **Deny** refuses it.

**Permissions** (under **More**) chooses which categories run without asking:
database writes, file changes, outbound calls, system control, software
installs and unrecognized commands. Every category you leave unticked still
asks each time. The same window lists your **Always** rules, to remove.

Lookups (ping, dig, traceroute and the like) are let through to private
addresses and the system's own domain. Anywhere else asks, because the name a
lookup sends can carry data off the system. **Lookup destinations**, on the
system's form, lists more places they may go.

## Giving a system to an agent

An agent reaches a system only when the system is in its **Sources**. On the
agent's page, open **Configure › Sources** and turn the system on. The agent
then gets two tools for it:

- `search_<system>_knowledge`: what Servitor has already learned about it.
  Instant, and tried first.
- `investigate_<system>`: sends a question to Servitor's investigator, which
  works it out on the live system and reports back. It takes tens of seconds.

The agent asks questions; it never gets a shell. Servitor's investigator runs
the commands, under the same checks as when you ask yourself:

- When a person is watching the agent's conversation, the investigation runs
  as a session of its own in the system's Sessions. A command that needs
  approval waits there on an ordinary card, and the agent's conversation
  links to it. When you answer, the investigation carries on, and its findings
  return to the conversation that asked.
- When nobody is watching (a schedule, another app), the investigation is
  read-only: a command that needs approval is declined.

**Access**, on Servitor's toolbar, shows which agents may use the system and
the commands they have asked to run on it.

## Sharing a system

A system's owner can share it with people and teams (**Shared with**, on its
form). Each person keeps their own chat sessions with it, at one of two
levels:

- **Read**: their questions are answered from what Servitor has already
  gathered, and nothing runs on the system for them.
- **Full access**: Servitor investigates it live for them, with its stored
  credentials.

The credentials are never shown to them, and only the owner or an
administrator can change or delete the system. To give it to one of their own
agents, they add it to that agent's Sources. A system cannot be newly shared
with Everyone.

**Peers that may reach this** lets another gohort instance use this machine as
its wire, for a system it cannot reach itself. Its own Servitor investigates,
with its own records; nothing this deployment has learned goes to it.

## Evidence files

Log bundles and other evidence go in a [file store](file-stores.md) with
**Index folders as evidence** on, where an agent gets a summary and a merged
timeline of each folder. An older Servitor evidence bundle can be moved there
from its edit panel (**Move to a file store**).

## Tips

- Write the system's **Rules** early: where its logs are, what is safe to
  restart, which service is the important one. Every investigation reads them.
- Use **Always** for the harmless commands that keep asking, and keep the
  rule as narrow as the card lets you.
- For a question that spans systems ("this error on the server: which code
  writes it?"), give an agent both the machine and the repository as Sources
  and ask the agent. It investigates each and joins the answers.
