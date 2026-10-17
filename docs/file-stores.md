# File stores: a folder an agent can search

Status: built (v0.6.096). `apps/filestore`. Commands for agents, background runs, temporary stores,
pulls between stores and app grants: v0.8.223 to v0.8.251 (2026-10-14).

## Why it exists

There was no good way to give the system a folder of files. The existing routes each fail a
different way:

- **A Collection** chunks and embeds. That is the wrong retrieval model for anything you would
  *grep* rather than *ask about*: semantic chunking destroys the line structure that makes a log, a
  config tree or a CSV export searchable, and embedding a gigabyte of stack traces buys nothing.
- **An attachment** is one file at a time and lands in the context window.
- **A shell tool** is an unbounded read waiting to happen.
- **A servitor bundle** is the closest thing and is better in most ways (see below), but it costs a
  per-bundle setup, and that ceremony is the thing being avoided here.

## What it is

An admin-registered folder, **read-only**, reached by regular-expression search with hard caps.
Attached to an agent through the existing Sources picker, which is what gets it *named* tools
(`search_build_logs`) rather than a generic reader competing with `knowledge_search`.

Subfolders are optional and require no declaration. Drop `scan-2026-08-13/` into the store and it is
immediately there; the tools take an optional `within` to scope to it. **That is the whole setup per
scan**, which is the point.

Four read tools per store: `list_<slug>`, `search_<slug>`, `read_<slug>`, and
`list_<slug>_commands` (what an admin has registered against it, and the admin's guide to each).
Beyond reading, an agent gets only what an admin chose to give it: a `pull_<slug>` tool when the
store pulls from another and **Let attached agents pull** is on, and the tools of each command an
admin has mapped and approved (see [Commands for agents](#commands-for-agents)). It never gets a
free-form way to write into a store or run something in it.

## The caps are not configurable

60 matches, 400 runes per line, 8 lines of context, a 400-line read window, 5000 files walked. Every
one exists because the failure it prevents is a turn that dumps a file into the context window and
takes the conversation with it. An operator who could raise them would raise them exactly once, in
the moment they were most sure they needed the whole file.

A capped search **says so**, because "60 matches" and "the first 60 of many" are different answers,
and acting on the first as though it were the second draws a conclusion from a truncated set.

## Attaching one to an agent

**Configure → Sources** in the chat toolbar. Stores appear grouped under their source label, each row
carrying the tools it adds. Attaching commits immediately.

The reverse view (which of your agents a given store is linked to), is behind **Linked agents…** in
the same modal. It is the same pill control tools and credentials use, on the same endpoint
(`api/tool-scope?kind=source`), because "where did this reach" is the question you ask about a folder
of somebody's logs, and answering it by opening four agents in turn is how you come to believe an
attachment took when it did not.

There is **no all-agents option**, unlike tools. A tool can sensibly live in a user-wide pool;
"every agent I own can read this folder" is a grant nobody should be able to make in one click, and
the admin-side assignment already decides who may reach it at all.

The Sources entry disappears entirely when you have no sources: an empty toolbar entry reads as a
broken feature, and it is the only Configure entry whose subject can be absent (an agent always has
tools, memory, rules).

The `attached_sources` field behind it is also reachable through the agent tool (Builder), which was
the ONLY door until v0.6.110: a poor one for the field most likely to be wrong, since a missing
attachment fails silently: the tools are simply absent and the agent says it does not know what you
mean.

## Renaming one

The tool names come from a **handle** minted from the name the first time a store is saved
(`Build logs` → `list_build_logs`, `search_build_logs`, `read_build_logs`).
The handle does not move afterwards. **Renaming a store changes the label, not the tool names.**

It has to work that way: the handle is what agent attachments are keyed on (`attached_sources` stores
`files:<slug>`) and what a minted command tool's FROZEN `path_scope` names. Moving it on rename would
break every approved command tool pointed at the store, failing closed with "there is no file store
called X you can reach": an approved capability quietly dying because someone fixed a typo in a
label.

The new name is not wasted: it is what an agent READS. The tool descriptions carry the current label,
so the model sees `Search the "Customer captures" file store` while the tool is still called
`search_build_logs`.

The admin table's **Agent tools** column prints the names in force, built from the same definition
`ItemTools` builds from, so the page cannot claim a name nothing answers to.

To actually change the handle, make a new store and re-link it: the attachments and any `path_scope`
declarations have to move deliberately, which is the point.

## When a search takes a while

A regex over a multi-gigabyte bundle takes the time it takes, and that is fine: what is not fine is
being unable to tell it apart from a hang. Three things say so now:

- **A heartbeat.** Any tool call running longer than 15s gets `search_x — still running (30s)` on the
  activity pane, every 15s, until it returns. It lives in the framework's tool wrapper
  (`wrapToolsForActivity`), not here, so every slow tool gets it. A handler has no context and no
  channel of its own; the wrapper is the only thing that knows a call is in flight.
- **A cost line.** A search that took over 5s appends `(read 3140 files, 12480 MB, in 1m11s)`. A slow
  search that explains itself reads as a big bundle, which is what it is.
- **Runaway guards, not deadlines**: 15 minutes and 64 GB. Set where "still working" stops being
  plausible, not where patience runs out. A short clock would trade a slow answer for a wrong one:
  the model gets a partial result and reports an absence nobody established. When one does fire the
  result says `INCOMPLETE`, in different words from the match cap, because "there are more matches"
  and "part of the store was never read" are different facts.

**Non-regular files are skipped entirely**: FIFOs, device nodes, sockets. `os.Open` on a FIFO blocks
until someone opens the other end, and `/dev/zero` reads without ever reaching EOF; either one hangs
a search that nothing upstream can cancel. They turn up in a captured filesystem tree without anyone
putting them there deliberately.

## Uploading, and deleting

Reaching a store and WRITING into it are separate grants. **Let assigned users upload** is off by
default: the common store is a directory something else fills, and registering `/var/log` should not
let every account with an agent add files to it. An admin can upload either way, still bounded by
the assignment: membership decides reach, and reach gates upload.

**Delete folders older than (days)** is 0 by default, which keeps everything forever. That is the
only safe default for a store that may hold the only copy of an incident's evidence.

Nothing deletes on a timer. The window makes a folder ELIGIBLE; an admin runs the sweep from
Maintenance, where a dry run lists exactly what the delete would remove: the same walk behind both,
so what was read is what goes.

What the sweep will touch, and nothing else: a **directory** that is a **direct child** of a store
root, in a store with a window set, whose newest content is past it. Not the root, not loose files at
the root, not anything nested, and not a symlinked folder pointing outside the store (resolution goes
through `SubRoot`, so an escape is skipped and logged rather than followed). The allowlist names what
may GO, never what may stay, the same argument as `core/workspace_reap.go`, for the same reason: a
missed case here costs unreclaimed disk, while the inverse costs somebody's evidence.

Age is read from the folder and its immediate contents rather than a full walk, because a bundle
expands to tens of thousands of files and this runs over every folder of every store. A write buried
deep may not refresh it, so leave headroom rather than setting the window to the exact age you care
about.

### Temporary stores

**Temporary store** is for a working area whose contents are copied onward: raw downloads that are
extracted and pulled into another store. Each folder is removed on its own timer, **Keep each folder
for (hours)** (24 by default), checked every 15 minutes, with no admin sweep. A folder that a
command, a copy or an upload is using is never removed, whatever its age (`core/pathhold`). Leave
it off for a store that holds the only copy of something: those wait for an admin.

### Size cap and free space

**Size cap (GB)** refuses work that would take the store past it: uploads, commands and copies into
the store fail saying how full it is. Nothing is deleted to make room; refusing is the only safe
answer for a store that may hold evidence.

Separately, every write on the disk keeps a free-space floor, **Keep free on disk (GB)** under Admin,
Limits (10 GB by default; a percentage floor exists and is off by default, since 5% of a large disk
can be far more than 10 GB). A copy or a command that would cross it is refused up front and stopped
if it gets there while running (`core/diskguard`).

## Commands on a folder

`ExpandArchives` deliberately refuses `.enc` / `.gpg` / `.pgp` / `.aes` and reports them as
**unopened** rather than pretending: a store that looks thin because half of it is still encrypted
is a different problem from one that is thin because nothing was captured.

What happens next is a **command**: an admin-registered binary that runs against ONE folder, from
a button on the store's page.
Decrypting is the first one, and the reason this is a list rather than a `decrypt_command` field.
Redacting before anyone reads it, unpacking a proprietary container, running an extractor that turns
a binary dump into text, building an index: all of them are "run a registered binary against this
folder, then the files are ready". A field named after the first instance makes the second one a
second field, a second endpoint and a second UI.

```
<cmd> <folder>            one phase: run it, the folder is ready
<cmd> <folder>            two phases, first call: prints a challenge
<cmd> <folder> <input>    two phases, second call: the response
```

```
POST /filestore/api/commands/run?slug=<store>&within=<folder>&command=<name>
     {}              → one phase: {"output": …} - two phases: {"challenge": …, "input_label": …}
     {"input":"…"}   → two phases, second call: {"output": …}
```

Two phases exist for the case that prompted this: the answer to the challenge is looked up
out-of-band by a person, so nothing here can obtain it and nothing here should try. An action needing
no input declares one phase and just runs.

Output is passed through **verbatim** in both directions, combined stdout and stderr. Nothing parses
it: the format belongs to a binary this code does not own, and a parser guessing at it is how the
first real bundle breaks. Reading only stdout is how a challenge printed to stderr becomes "it
printed nothing".

**No shell.** Command, folder and input are exec'd as separate arguments, so quoting, word-splitting
and metacharacters have nowhere to happen: the test proves an input containing `$` and `;rm -rf /`
arrives intact as one argument. The folder is resolved by `SubRoot`, so a traversal is refused before
anything runs.

**The input is never logged.** On a two-phase action it is the secret half of an out-of-band
exchange, and a log line is the one place it would outlive the request.

Registering a command is admin-only, and for a sharper reason than the store list: it names a binary
the server will execute. That is the same decision as registering the directory it runs against, and
neither is a user's to make. Running one from the page takes the WRITE grant (`Let assigned users
upload`), because it rewrites the folder. An input starting with `-` is refused, since the binary
would read it as an option rather than a value.

## Long runs

A download or an extraction takes as long as its input makes it take, so nothing that runs a
command has a fixed time limit. Instead (`core/bgrun`, Admin, Timeouts and Limits):

- **Move a running tool to the background after** (60s): an agent's call that is still running
  then leaves the turn, keeps running as a task, and reports back in the conversation when it ends.
  The live pill shows it with its latest output line and a Stop button.
- **Stop a running tool after no output for** (5 minutes): the limit that catches a hung run. A
  command that prints nothing while it works is kept alive by a heartbeat, so only real silence
  counts.
- **Stop a running tool after it writes (GB)** (50) and the free-space floor stop a run that would
  fill the disk.

An app's actions run the same way (see [Apps that use a store](#apps-that-use-a-store)).

## Commands for agents

A registered command is a button for a person. An agent reaches one only through tools an admin
built for it, and only after the admin approved them: a named tool carries intent, a free-form
command line does not, and approval only means something if the thing approved is the thing that
runs. Each command row has the panels for this.

**Show it how.** The admin runs the command here, in a box that takes only its arguments (quote a
value with spaces; nothing is expanded, no wildcards, variables or pipes). Each run is kept as an
example, with how it exited and the end of what it printed, and marked as a new use or the next
step of one, so a two-phase command (a challenge, then the response) reads as one use in two steps.
The admin can edit a run's note, move it to another use, or remove it.

**Uses.** Two fields:

- **Allowed uses**: the argument lines after the binary, one per line, with `{placeholders}` for
  what the agent fills in (`download --src={source} --dst={folder}`). Each line becomes one tool,
  and **an agent can run nothing else**. Lines written here tell Builder what to map; Builder adds
  every line it maps.
- **How to use it**: a markdown guide for what runs cannot show (that a signature is answered by
  someone in support, that a response works once, where the output lands). Builder reads it first
  when mapping, and agents read it in `list_<slug>_commands`; each mapped tool points there.

**Map with Builder.** An admin-only conversation with Builder about this one command, opened from
its row. Builder reads the runs (`admin_examples`) and the guide, runs the command itself with any
arguments to learn it (`probe_command`: it is told a probe is real, runs as the server's account in
the store's top folder, and to ask before one that would change anything), and writes the mapping
(`propose_tools`). Earlier mapping conversations are listed in the panel and can be reopened. This is
the ONLY place a command's tools can be written: no other Builder session or agent can change them.

**Mapping** shows what Builder wrote: each action, its line and its parameters.

**Agents.** The switch on the row is the approval. What Builder wrote is inert until it is on, and a
re-map or a change to the allowed uses turns it off again. Changing only the guide keeps it on: that
changes what agents are told, not what they can run.

What an approved command gives an attached agent is one tool per allowed line,
`<command>_<store>_<action>`, and a call:

- runs the binary and that line exactly as written, directly as the server's account with **no
  shell and no sandbox**, the way a person's click runs (so a binary with its own saved login works);
- fills each placeholder with ONE argument's worth of value, whatever it contains (the line itself
  is split the way a person typing it means it, so a quoted part of it stays one argument);
- refuses a value that would stand alone as an option (starts with `-`);
- proves a file or folder value is inside the store and passes its full path (`path_scope`);
- runs as a background-capable call, reporting back when it outlives the turn.

A store's folders are `PathScope.TrustedOnly`: they resolve only for these admin-mapped tools (and
for an admin-owned servitor system). An agent's own scripts, its workspace shell and any tool a
non-admin built cannot reach them, so a binary found some other way still cannot touch the store.

Before v0.8.249 a command could also be "open", its mapped lines run in the sandbox. That mode is
gone: at start each such command's mapped lines become its allowed uses, and one that was approved
is switched off and logged, since its lines now run directly. Turn it back on from its row.

## Evidence: a folder indexed like a bundle

With **Index folders as evidence** on (a store's Settings), each subfolder is indexed the way a
servitor evidence bundle is (`apps/filestore/evidence.go`, `core/bundle`): every file's format,
line count, the period it covers, which hosts wrote it and its error and warning counts, with
timestamps parsed so files from several hosts merge in time order. An attached agent gets two more
tools, each taking the folder by name:

- `summarize_<store>`: what the folder holds, what period, which files are noisiest, and what is
  present but unread (binaries, archives nobody opened). The agent's first look at a case.
- `timeline_<store>`: lines from several files merged in time order, narrowed by `since`,
  `until` and a path filter.

The folder is never changed: the index is built by reading it (`bundle.IngestFrom`), unpacks
nothing in place, and is kept as a second copy, encrypted, in the bundle store, under the store's
own name so two stores never mix and each folder is its own bundle. Up to 8 GB per folder is
indexed; past that the index says it covers only part of the folder. An archive still packed in
the folder is listed as not opened rather than read as a binary.

The index follows the folder:
- **Indexed when files arrive** by upload or pull, at once.
- **Picked up on a 15-minute sweep** when the folder changed some other way (a command's download,
  a copy by hand); a folder's reading (file count, bytes, newest change) says whether it did.
- **Indexed on demand** when an agent asks about a folder that is not indexed or has changed: the
  call indexes it first and moves to the background past a minute, as a long command does.
- **Wiped** when the folder is deleted (by the sweep), when the switch is turned off, or when the
  store is removed.

The store's Manage panel lists each folder's index under **Evidence index**, with **Index now**,
its progress and Stop. Only subfolders are indexed: a flat store's top-level files are in no case.

## Pulling between stores

The flow this serves: files land in a private working store (often a temporary one), a command
prepares them there (an extractor writing a `prepared/` folder), and the store agents read copies
the prepared part across. The working store is never attached to an agent.

**Pull from another store** is set on the RECEIVING store, and that link is the permission. **Part
of each folder to copy** is the subfolder copied when a folder is pulled (`prepared` by default; `.`
copies the whole folder); a file is always copied whole.

- A **copy, never a move**: the source keeps its own until its lifetime or retention removes it.
- A pull into a folder that already exists **adds and updates**, never removes. An agent or an app
  can ask for `replace`, which clears it first; the panel never does.
- Nothing half-copied is visible: the copy goes into a hidden `.incoming-<item>` and is renamed into
  place when complete, and a stopped copy removes it. Both sides are held so neither store's sweep
  can take them mid-copy.
- It is refused up front when the receiving store's cap or the disk's floor has no room, and
  checked again as it goes.

Three ways to start one, all through one registry, so a second request for the same item joins the
first rather than starting another copy: the **Pull** panel on the receiving store's row (admin),
an app granted pulling, and `pull_<slug>(item)` for an attached agent when **Let attached agents
pull** is on. The agent sees the other store's item NAMES so it can ask for one, never their
contents.

The panel's endpoints, admin-only:

```
GET  /filestore/api/pull/items?slug=<dest>                 the source's files and folders, and which are here
POST /filestore/api/pull?slug=<dest>&item=<name>           start (a folder adds to a copy already here)
GET  /filestore/api/pull/progress?slug=<dest>&item=<name>  {"progress"} while it runs, {"outcome"} after
POST /filestore/api/pull/cancel?slug=<dest>&item=<name>    stop it
```

## Apps that use a store

A store is deployment configuration, so which app may use one is an admin's decision, made under
**Apps that may use it** on the store's row. The grant belongs to the APP (owner and slug) and covers
whoever uses the app, the way a tool carries what its author was allowed. Reading is always part of
it; **Write into it**, **Run its commands** and **Pull files and folders in** are each granted on
their own.

The app's scripts reach a granted store four ways (`from gohort import ...`):

- `store_path("<store>")`: the store's folder mounted in the script's sandbox, read-only, or
  read-write with the write grant (add, change and delete files with ordinary file code).
- `store_action("<store>", "<command>", "<action>", {"<value>": ...})`: one of a command's mapped
  actions, once an admin has approved the mapping (Agents on its row). It runs the approved line
  with the values in its `{placeholders}`, exactly as an agent's tool for it does: a list fills
  its placeholder once per item, and an optional value left out takes its option with it
  (`--src {source}` drops both). A folder value is a folder or file in that store, by its name.
  Builder's `app_def(action="stores")` lists each action's line and values.
- `store_command("<store>", "<folder>", "<command>", input=...)`: a registered command with no
  mapped actions, on one folder, in the button's shape, `<cmd> <folder> [input]`. A two-phase one
  returns its challenge, and a second call with `input` finishes it.
- `store_pull("<store>", "<item>", replace=False)`: a file or folder copied in from the store it
  pulls from.

An app's actions run as jobs: a request waits briefly, and a slow action carries on with its
button showing a spinner, the latest line its script printed, and Stop, rejoined when the page is
reopened. The live pill lists it too.

**The app's agent.** In the app's own chat (since v0.8.251), its agent gets:

- each **action** as a tool (`<app>_<action>`): a call is the person's click, run as them with their
  records, shown on the button's spinner and stoppable there; an action with a confirm prompt asks
  the person first; the inputs an action declares are the tool's parameters;
- each **data source** as `read_<app>_<name>`, the JSON the page draws;
- `<app>_status`: the person's actions running now with the latest line each printed (what a
  progress bar on the page draws from), those that ended in the last half hour and how, and the
  newest records.

So the agent can do what the page's buttons do and see what the page shows, under the app's grants,
and nothing more. Outside the app's chat it gets none of these, and the page's own `ask` reaches the
agent with no tools at all.

## Handing off to a conversation

Once a folder is ready, `POST /orchestrate/api/sessions/start` opens a **new** session on an agent
with a first message already in it:

```
POST /orchestrate/api/sessions/start
     {"agent":"Investigator", "message":"What failed in scan-2026-08-14?", "title":"scan-2026-08-14"}
  → {"session":"s…", "agent":"ag-1", "url":"/orchestrate/?agent=ag-1&session=s…"}
```

A new session every time, deliberately: two bundles in one thread is the cross-contamination the
investigation workflow's memory settings exist to prevent, since a machine's blackboard holds the
triage and the hypothesis for the life of a session.

The message is **stored, not sent**. Sending server-side would run a turn with nobody watching,
streaming into a page that is not open yet, with a failure nobody sees. Instead the session carries
the prompt, and the chat panel sends it as the user's first message on open, through the normal
composer, so streaming, cancel, approval prompts and the machine's first phase behave exactly as they
do when a person types it.

It is served only while the session is still **empty**, which is the whole idempotency guard: once
the message lands the session has messages and the field stops arriving, so a reload cannot re-send
it. A half-typed message in the composer wins over the pre-filled one.

## Who may reach one

Configuring a store is admin-only. **Reading one is controlled separately**, by the store's
`allowed_users`: "Assigned to" on the admin form. Empty means every user.

That split exists because the two halves were mismatched: registering a path was gated from the
start, and reading whatever is under it was not gated at all. An admin points a store at
`/data/customer-logs` and, before this, any account with an agent could attach it.

Checked at every door, not just in the picker:

- the Sources list (`List`): filters to what the user may reach
- `ItemTools`: a stale attachment yields no tools rather than tools that refuse on every call
- `Fetch`: the generic pull path
- the path scope (`resolveScope`): the door a minted servitor command tool comes through, and the
  one that would otherwise be missed
- upload, **including for admins**: admin manages the list, membership decides reach
- the store's folders as a path scope are `TrustedOnly`: only admin-mapped command tools and an
  admin-owned servitor system resolve them (see [Commands for agents](#commands-for-agents))

A refusal on the path scope says "no file store called X **you can reach**" rather than "not yours",
because confirming a store exists is a fact the caller can do nothing with and should not have.

Empty stays open deliberately. Closing by default would silently break every store registered before
this existed, and a security change that presents as "the tools vanished" is one nobody diagnoses
correctly.

## Running a command through servitor

The way to let agents run something over a store is to register the binary on the store and map
it ([Commands for agents](#commands-for-agents)). The older route, a servitor appliance of type
`command` with a parameter scoped to the store, is closed since v0.8.330. A minted tool always ran
over SSH, and a local command system has no host, so its tools dialled port 22 on this machine as
root with the operator's key, never `sh` as the gohort process. A local command system now mints,
runs and watches nothing, and no new one is made; an existing one keeps its `run_command`.

What follows still describes a minted tool whose folder parameter is scoped to a store, on an SSH
system an **admin** owns (since v0.8.235, only there: the store's folders are `TrustedOnly`, and a
user's own system cannot reach them). The wires are separate on purpose:

1. **The store is assigned to you** (admin): `AllowsUser`.
2. **The agent is connected to the system** in servitor, that is what mints the command tools at
   all (`applianceEnabledForAgent`).
3. **The folder parameter declares `path_scope: "files:<slug>"`**: the mint prompt is handed your
   stores (`PathScopeRoots`) and told to prefer a scope over an enum, because an enum is frozen when
   written and a drop folder is not.
4. **The store is linked to that agent** under Configure → Sources.

The agent then calls the tool with a folder NAME, never a path
(`parse_bundle({"dir": "scan-2026-08-13"})`), and the names currently valid are listed in that
parameter's description: read when the catalog is built, so a folder added mid-conversation still
resolves even though it is not in the list. The description says so, because a model handed a list
otherwise treats it as exhaustive and refuses the folder somebody just named.

The appliance's **Work Dir is not a containment boundary**: it is the process cwd, and a template
containing `../..` walks straight out of it. `path_scope` is the boundary: it resolves symlinks and
refuses anything not strictly inside the root, and it substitutes an ABSOLUTE path, so the command
works regardless of cwd. Set Work Dir for where relative output should land, not for safety.

Check step 3 landed before approving: the approval row's Checks column shows `dir → files:<slug>`
when the constraint is there, and flags a path-ish parameter that has none. That flag matters,
because a tool minted WITHOUT the scope still works and looks completely normal at runtime: quoting
stops a value contributing shell syntax and does nothing about `../../var/lib/something` being a
well-formed single argument.

Step 4 was added in v0.6.112. Before it, the two halves disagreed: a store's own tools appeared only
on an agent it was attached to, while a command tool's `path_scope` resolved against the USER, so an
agent nobody had linked the store to could still run a command against it. One link, one meaning.

The gate does NOT refuse in three cases, all deliberate: no agent in play (a CLI path, the user gate
still applies), no app owning agent records in the deployment (the feature would be dead rather than
ungated), and a scope kind that is not an attachable source (nothing to attach). A sub-agent needs
its OWN link; holding it on the parent is not enough, matching how a sub-agent must be connected to a
machine in its own right before it gets that machine's tools.

## Getting files in

- **Put them there.** scp, a mount, a cron job. Nothing to call.
- **Upload.** `POST /filestore/api/upload?slug=<store>&within=<subfolder>`, multipart, streamed.
  The subfolder is created if absent. An admin, or an assigned user when **Let assigned users
  upload** is on: it writes to the host filesystem.
- **A command.** A registered download command run from the page, by an app granted commands, or by
  an agent through its mapped tools.
- **A pull** from another store ([Pulling between stores](#pulling-between-stores)).

Writing takes a stricter path rule than reading. `within` must be a single name, because cleaning
would happily turn `../escaped` into `escaped` and `/etc/cron.d` into `etc/cron.d`: contained,
neither an escape, and both a surprise. Reading stays permissive, because a nested path there is a
real search result being read back.

**Archives are expanded**, through `core.ExpandArchives`: lifted out of servitor's bundle ingest
once this became its second caller. tar, tar.gz, tar.bz2, zip, and lone .gz/.bz2 streams; nested to
a depth cap; each archive replaced by a directory named for it so an extracted file still records
where it came from. Formats with no built-in expander (.7z, .xz, encrypted) are reported as
`unopened` rather than ignored, because a store that looks thin because half of it is in a .7z is a
different problem from one that is thin because nothing was captured.

## What it deliberately does not do

**Let an agent act freely.** An agent searches and reads a store directly; everything else it does
there goes through a tool an admin made and approved: a mapped command's allowed lines, a pull, or
an app's actions under the app's grant. There is no generic write, delete or run tool for agents,
and a model-written command line never runs.

**Compete with servitor bundles.** A `Type=="bundle"` appliance is better on every axis except
setup cost: encryption at rest, line-slice storage built for hundred-megabyte files, nested archive
expansion, and a per-file index (line count, format, time span, emitting host, severity histogram)
that powers `bundle_summary` and `bundle_timeline`. Reach for a bundle when the investigation earns
that; reach for a store when the ceremony is the problem.

**Own the expander.** `core.ExpandArchives` is shared with servitor's bundle ingest. It treats an
archive as untrusted input: member names resolved against the destination before anything is
created, links never re-created (a tree that reconstructs a filesystem can point out of itself no
matter how carefully the names were checked), declared sizes ignored in favour of a hard cap, and
nesting depth-capped.

**Replace tools/files.** That is the session *workspace*: ephemeral, per-session, read AND write.
This is persistent and admin-registered, read-only to agents. Same mechanics, different mount and
different trust. Folding both onto one engine is a real lift-to-core candidate.

## The agent

The original `extras/investigator.agent.json` was replaced in v0.6.107 by a machine,
`extras/investigation.machine.json`, which triages, commits to one hypothesis and tests it against
independent evidence. See [docs/investigation.md](investigation.md). Attach a store to whichever
agent runs it.

The memory advice still stands: for an agent that investigates one bundle after another, keep its
memory to lessons rather than incident specifics, and turn Reference Memory off, since that is the
layer that would carry one incident's findings into the next by similarity.

## Setting it up

```sh
# 1. Register the folder: Admin → Files → Add file store.
#    Or the endpoint, if you prefer:
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/filestore/api/stores \
  -H 'Content-Type: application/json' \
  -d '{"name":"Build logs","path":"/var/log/builds","description":"Build logs, one folder per CI run."}'

# 2. Attach the store to an agent: Configure → Sources → File stores.

# 3. Drop a scan in, or upload one.
curl -sS -b cookies.txt -F 'file=@server.log' \
  'http://127.0.0.1:8181/filestore/api/upload?slug=build_logs&within=scan-2026-08-13'
```

Then open a session and name the scan. Next scan is a new subfolder and a new session; there is
nothing to configure between them.

To let the agent run something there as well: register the binary on the store's row (Commands),
run it a few times under **Show it how**, write its **Uses** and guide, map it under **Map with
Builder**, check what was written under **Mapping**, and turn **Agents** on.
