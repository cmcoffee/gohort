# File stores

A file store is a folder on the gohort server that agents can search and
read. You register the folder once, attach it to an agent, and from then on
anything dropped into it is there for the agent to find: a folder of build
logs, one subfolder per support case, a config tree, a CSV export.

Agents read a store by regular expression, a window at a time, never a whole
file at once. Everything else an agent does in a store (running a command,
copying files in) goes through a tool an administrator made and approved.

## When to use one

| You have | Use |
|---|---|
| Documents to ask questions about: manuals, policies, write-ups | A **Knowledge** collection, which searches by meaning |
| Files you would `grep`: logs, captures, configs, exports | A **file store** |
| One file for one conversation | An attachment in the chat |
| An investigation that needs encrypted storage and a per-file index | A Servitor evidence bundle, or a store with **Index folders as evidence** on |

A collection breaks text into passages and embeds them, which destroys the
line structure that makes a log searchable. A store keeps the files exactly as
they are.

## Setting one up

1. **Register the folder.** Under **Administrator › Integrations › File
   stores**, press **Add file store**. Give it a name, the folder's absolute
   path on the server, and a line under **What lands here** (the agent reads
   it to decide when to use this store). Tick who may reach it.
2. **Attach it to an agent.** On the agent's page, open **Configure ›
   Sources** and turn the store on. **Linked agents** in the same window shows
   every agent a store is attached to.
3. **Put files in it** (see [Getting files in](#getting-files-in)), then ask
   the agent about them.

Subfolders need no setup. Drop `scan-2026-08-13/` into the store and it is
there; the agent can narrow a search to it by name.

### What the agent gets

A store named "Build logs" gives an attached agent:

- `list_build_logs`: what is in the store or one of its folders
- `search_build_logs`: a regular-expression search, with lines of context
- `read_build_logs`: a window of a file around a line
- `list_build_logs_commands`: the commands an administrator has registered on
  the store, with the administrator's guide to each

With **Index folders as evidence** on, it also gets `summarize_build_logs` (what a folder
holds, the period it covers, its noisiest files) and `timeline_build_logs`
(lines from several files merged in time order).

The tool names come from the store's name the first time it is saved, and stay
the same after that. Renaming a store changes the label the agent reads, not
the tool names, so attachments and approved tools keep working.

### Limits on a search

These are fixed, and they keep one search from filling the conversation:
60 matches, 400 characters per line, 8 lines of context, 400 lines per read
and 5,000 files walked. A search that hits a cap says so, so the agent knows
it saw the first 60 matches and not all of them.

A large store can take a while to search. A call that runs past 15 seconds
shows that it is still running, and a slow search reports how much it read.

## Who may reach one

Only an administrator registers or configures a store. Who may use it is set
under **Who may reach it** on the store's **Manage** panel, by person, by
team or as **Everyone**. A new store reaches nobody until someone is added,
administrators included.

| Level | A person can | Their attached agent can |
|---|---|---|
| **Read Only / No Commands** | browse, search, read | list, search, read, and use the evidence tools |
| **Full Access** | also upload and run the store's commands | also run its approved commands and pull files in, when its attachment has **Can make changes** on |

People and teams start at Full Access; Everyone starts at Read Only, so
opening a store to every account never hands out its commands by accident.

An agent reaches a store only when it is attached and its owner may reach the
store. There is no "every agent" option: each attachment is made on purpose.

## Getting files in

- **Put them there**: scp, a mount, a scheduled copy. Nothing to call.
- **Upload** (Full Access): `POST /filestore/api/upload?slug=<store>&within=<folder>`.
  The folder is made if it is missing.
- **A command** that downloads into a folder, run from the store's page, by an
  app, or by an agent through its approved tools.
- **A pull** from another store (see [Pulling between stores](#pulling-between-stores)).

Archives are unpacked when they are uploaded: tar, tar.gz, tar.bz2, zip, and lone
.gz or .bz2 files. Formats gohort cannot open (.7z, .xz, encrypted files) are
listed as not opened, so a store that looks thin because half of it is still
packed says why.

## Commands

A command is a program an administrator registers on a store, which runs
against one folder: decrypt it, unpack a proprietary format, run an extractor,
download into it. Register one under **+ Add a command** on the store's row.
A person with Full Access runs it from there. Some commands take two steps:
the first prints a challenge, a person gets the response from elsewhere, and
the second step takes the response.

Commands run without a shell. The folder and any input are passed as separate
arguments, so nothing in them is expanded or interpreted, and an input
starting with `-` is refused. An input is never written to the log.

### Letting agents run a command

An agent never writes its own command line. It gets a command only as named
tools an administrator approved, one per allowed use:

1. **Show it how**: run the command a few times on the store's row. Each run
   is kept as an example.
2. **Uses**: list the argument lines agents may run, with `{placeholders}` for
   what the agent fills in, for example `download --src={source} --dst={folder}`.
   Add a guide under **How to use it** for what the runs cannot show.
3. **Map with Builder**: an administrator-only conversation in which Builder
   reads the runs and the guide, tries the command, and writes the tools.
4. **Mapping**: check what Builder wrote.
5. **Agents**: turn the switch on. That is the approval. Changing the mapping
   or the allowed uses turns it off again.

An approved tool runs its line exactly as written. Each placeholder takes one
argument's worth of value, and a file or folder value must be inside the store.

### Long runs

A command or a copy takes as long as its input needs, so none has a fixed time
limit. An agent's call still running after a minute moves to the background,
keeps running, and reports back in the conversation when it ends; the live
pill shows its latest output and a Stop button. A run that prints nothing for
five minutes, or writes more than 50 GB, is stopped. The limits are under
**Administrator › Tuning › Tools & background work**.

## Keeping a store tidy

- **Delete folders older than (days)**: 0, the default, keeps everything. A
  number makes old folders eligible; nothing is deleted until an administrator
  runs the sweep under **Administrator › Maintenance › Reclaim space**, where
  a dry run lists exactly what would go.
- **Temporary store**: for a working area whose contents are copied onward.
  Each folder is removed on its own timer (**Keep each folder for (hours)**, 24
  by default). A folder in use by a command, a copy or an upload is never
  removed.
- **Size cap (GB)**: uploads, commands and copies that would take the store
  past it are refused. Nothing is deleted to make room.
- Every write also keeps a free-space floor on the disk (10 GB by default,
  under **Administrator › Tuning › Storage & retention**).

Only folders directly inside the store are ever removed: not the store itself,
not loose files at its top, and not a link to somewhere outside it.

## Pulling between stores

A common setup is two stores: a private working store where files land and a
command prepares them, and the store agents read, which copies in only the
prepared part.

Set **Pull from another store** on the store that receives. **Part of each
folder to copy** is the subfolder taken when a folder is pulled (`prepared` by
default; `.` copies the whole folder).

- A pull copies; the source keeps its own files.
- Pulling into a folder that exists adds and updates, and removes nothing.
- A half-finished copy is never visible, and a stopped one is cleaned up.

An administrator pulls from the **Pull** panel on the store's row. An attached
agent whose owner has Full Access gets a `pull_<store>` tool, and an app can be
granted pulling. The agent sees the other store's file and folder names, so
it can ask for one, but never their contents.

## Each person's own folder

**Each person gets their own folder** (in the store's Settings, off by
default) turns one store into a private working area for each person who
reaches it. Each person reaches only `people/<name>/` inside the store, made the
first time they use it, through every route: their agents' tools, commands,
uploads, apps and pulls.

It is for a store an app shares with many people, such as captures each person
uploads to an analyzer. Without it, everyone using the app shares one folder,
and one person's uploads turn up in the next person's searches.

- An agent working for someone else, such as a shared specialist or an app's
  agent, reaches the folder of the person the run is for.
- The size cap covers the whole store, not each person. The cleanup timers
  apply to the folders inside each person's folder.
- Files already at the top of the store stay there, and nobody reaches them
  while the setting is on. Turning it off shows everyone who reaches the store
  every person's folder.
- It cannot be combined with **Index folders as evidence** yet.
- An app's scripts share one working folder whoever is running them, so an app
  should keep results in the store or hand them back, not cache them there.

## Apps that use a store

Which apps may use a store is an administrator's choice, under **Apps that may
use it** on the store's row. Reading always comes with the grant; **Write into
it**, **Run its commands** and **Pull files and folders in** are granted
separately. An app's scripts then reach the store with `store_path`,
`store_action`, `store_command` and `store_pull`. Ask Builder; it lists what
each granted store offers.

## Handing a folder to a conversation

Once a folder is ready, a script can open a new conversation with an agent,
with the first message already in it:

```
POST /orchestrate/api/sessions/start
     {"agent":"Investigator", "message":"What failed in scan-2026-08-14?", "title":"scan-2026-08-14"}
```

The message is sent when the conversation is opened, as though the person
typed it, so approvals and streaming work as they always do. Each call makes a
new conversation, so two cases never share one.

## Tips

- Write **What lands here**. It is how the agent knows to reach for this store
  and not another.
- For an agent that works one case after another, keep its memory to lessons
  rather than case details, and turn Reference Memory off so one case's
  findings do not surface in the next.
- `extras/investigation.machine.json` is an investigation workflow that
  triages a folder, commits to one hypothesis and tests it. Attach a store to
  the agent that runs it.

The design record, with the reasoning behind each choice, is
[docs/design/file-stores.md](design/file-stores.md).
