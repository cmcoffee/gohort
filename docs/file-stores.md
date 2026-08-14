# File stores — a folder an agent can search

Status: built (v0.6.096). `apps/filestore`.

## Why it exists

There was no good way to give the system a folder of files. The existing routes each fail a
different way:

- **A Collection** chunks and embeds. That is the wrong retrieval model for anything you would
  *grep* rather than *ask about*: semantic chunking destroys the line structure that makes a log, a
  config tree or a CSV export searchable, and embedding a gigabyte of stack traces buys nothing.
- **An attachment** is one file at a time and lands in the context window.
- **A shell tool** is an unbounded read waiting to happen.
- **A servitor bundle** is the closest thing and is better in most ways (see below) — but it costs a
  per-bundle setup, and that ceremony is the thing being avoided here.

## What it is

An admin-registered folder, **read-only**, reached by regular-expression search with hard caps.
Attached to an agent through the existing Sources picker, which is what gets it *named* tools
(`search_support_bundles`) rather than a generic reader competing with `knowledge_search`.

Subfolders are optional and require no declaration. Drop `scan-2026-08-13/` into the store and it is
immediately there; the tools take an optional `within` to scope to it. **That is the whole setup per
scan**, which is the point.

Three tools per store: `list_<slug>`, `search_<slug>`, `read_<slug>`.

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

The reverse view — which of your agents a given store is linked to — is behind **Linked agents…** in
the same modal. It is the same pill control tools and credentials use, on the same endpoint
(`api/tool-scope?kind=source`), because "where did this reach" is the question you ask about a folder
of somebody's logs, and answering it by opening four agents in turn is how you come to believe an
attachment took when it did not.

There is **no all-agents option**, unlike tools. A tool can sensibly live in a user-wide pool;
"every agent I own can read this folder" is a grant nobody should be able to make in one click, and
the admin-side assignment already decides who may reach it at all.

The Sources entry disappears entirely when you have no sources — an empty toolbar entry reads as a
broken feature, and it is the only Configure entry whose subject can be absent (an agent always has
tools, memory, rules).

The `attached_sources` field behind it is also reachable through the agent tool (Builder), which was
the ONLY door until v0.6.110 — a poor one for the field most likely to be wrong, since a missing
attachment fails silently: the tools are simply absent and the agent says it does not know what you
mean.

## Renaming one

The tool names come from a **handle** minted from the name the first time a store is saved
(`Support bundles` → `list_support_bundles`, `search_support_bundles`, `read_support_bundles`).
The handle does not move afterwards. **Renaming a store changes the label, not the tool names.**

It has to work that way: the handle is what agent attachments are keyed on (`attached_sources` stores
`files:<slug>`) and what a minted command tool's FROZEN `path_scope` names. Moving it on rename would
break every approved command tool pointed at the store, failing closed with "there is no file store
called X you can reach" — an approved capability quietly dying because someone fixed a typo in a
label.

The new name is not wasted: it is what an agent READS. The tool descriptions carry the current label,
so the model sees `Search the "Customer captures" file store` while the tool is still called
`search_support_bundles`.

The admin table's **Agent tools** column prints the names in force, built from the same definition
`ItemTools` builds from, so the page cannot claim a name nothing answers to.

To actually change the handle, make a new store and re-link it: the attachments and any `path_scope`
declarations have to move deliberately, which is the point.

## When a search takes a while

A regex over a multi-gigabyte bundle takes the time it takes, and that is fine — what is not fine is
being unable to tell it apart from a hang. Three things say so now:

- **A heartbeat.** Any tool call running longer than 15s gets `search_x — still running (30s)` on the
  activity pane, every 15s, until it returns. It lives in the framework's tool wrapper
  (`wrapToolsForActivity`), not here, so every slow tool gets it. A handler has no context and no
  channel of its own; the wrapper is the only thing that knows a call is in flight.
- **A cost line.** A search that took over 5s appends `(read 3140 files, 12480 MB, in 1m11s)`. A slow
  search that explains itself reads as a big bundle, which is what it is.
- **Runaway guards, not deadlines** — 15 minutes and 64 GB. Set where "still working" stops being
  plausible, not where patience runs out. A short clock would trade a slow answer for a wrong one:
  the model gets a partial result and reports an absence nobody established. When one does fire the
  result says `INCOMPLETE`, in different words from the match cap, because "there are more matches"
  and "part of the store was never read" are different facts.

**Non-regular files are skipped entirely** — FIFOs, device nodes, sockets. `os.Open` on a FIFO blocks
until someone opens the other end, and `/dev/zero` reads without ever reaching EOF; either one hangs
a search that nothing upstream can cancel. They turn up in a captured filesystem tree without anyone
putting them there deliberately.

## Who may reach one

Configuring a store is admin-only. **Reading one is controlled separately**, by the store's
`allowed_users` — "Assigned to" on the admin form. Empty means every user.

That split exists because the two halves were mismatched: registering a path was gated from the
start, and reading whatever is under it was not gated at all. An admin points a store at
`/data/customer-logs` and, before this, any account with an agent could attach it.

Checked at every door, not just in the picker:

- the Sources list (`List`) — filters to what the user may reach
- `ItemTools` — a stale attachment yields no tools rather than tools that refuse on every call
- `Fetch` — the generic pull path
- the path scope (`resolveScope`) — the door a minted servitor command tool comes through, and the
  one that would otherwise be missed
- upload — **including for admins**: admin manages the list, membership decides reach

A refusal on the path scope says "no file store called X **you can reach**" rather than "not yours",
because confirming a store exists is a fact the caller can do nothing with and should not have.

Empty stays open deliberately. Closing by default would silently break every store registered before
this existed, and a security change that presents as "the tools vanished" is one nobody diagnoses
correctly.

## Running a command against one

A store answers "where does X appear". To RUN something over the same folder (unpack an archive, run
an extractor), add a servitor appliance of type `command` and connect the agent to it. Three wires,
and they are separate on purpose:

1. **The store is assigned to you** (admin) — `AllowsUser`.
2. **The agent is connected to the command appliance** in servitor — that is what mints the command
   tools at all (`applianceEnabledForAgent`).
3. **The folder parameter declares `path_scope: "files:<slug>"`** — the mint prompt is handed your
   stores (`PathScopeRoots`) and told to prefer a scope over an enum, because an enum is frozen when
   written and a drop folder is not.
4. **The store is linked to that agent** under Configure → Sources.

The agent then calls the tool with a folder NAME, never a path
(`parse_bundle({"dir": "scan-2026-08-13"})`), and the names currently valid are listed in that
parameter's description — read when the catalog is built, so a folder added mid-conversation still
resolves even though it is not in the list. The description says so, because a model handed a list
otherwise treats it as exhaustive and refuses the folder somebody just named.

The appliance's **Work Dir is not a containment boundary** — it is the process cwd, and a template
containing `../..` walks straight out of it. `path_scope` is the boundary: it resolves symlinks and
refuses anything not strictly inside the root, and it substitutes an ABSOLUTE path, so the command
works regardless of cwd. Set Work Dir for where relative output should land, not for safety.

Check step 3 landed before approving: the approval row's Checks column shows `dir → files:<slug>`
when the constraint is there, and flags a path-ish parameter that has none. That flag matters,
because a tool minted WITHOUT the scope still works and looks completely normal at runtime — quoting
stops a value contributing shell syntax and does nothing about `../../var/lib/something` being a
well-formed single argument.

Step 4 was added in v0.6.112. Before it, the two halves disagreed: a store's own tools appeared only
on an agent it was attached to, while a command tool's `path_scope` resolved against the USER — so an
agent nobody had linked the store to could still run a command against it. One link, one meaning.

The gate does NOT refuse in three cases, all deliberate: no agent in play (a CLI path — the user gate
still applies), no app owning agent records in the deployment (the feature would be dead rather than
ungated), and a scope kind that is not an attachable source (nothing to attach). A sub-agent needs
its OWN link; holding it on the parent is not enough, matching how a sub-agent must be connected to a
machine in its own right before it gets that machine's tools.

## Getting files in

Two routes, deliberately:

- **Put them there.** scp, a mount, a cron job. Nothing to call.
- **Upload.** `POST /filestore/api/upload?slug=<store>&within=<subfolder>`, multipart, streamed.
  The subfolder is created if absent. Admin-gated: it writes to the host filesystem.

Writing takes a stricter path rule than reading. `within` must be a single name, because cleaning
would happily turn `../escaped` into `escaped` and `/etc/cron.d` into `etc/cron.d` — contained,
neither an escape, and both a surprise. Reading stays permissive, because a nested path there is a
real search result being read back.

**Archives are expanded**, through `core.ExpandArchives` — lifted out of servitor's bundle ingest
once this became its second caller. tar, tar.gz, tar.bz2, zip, and lone .gz/.bz2 streams; nested to
a depth cap; each archive replaced by a directory named for it so an extracted file still records
where it came from. Formats with no built-in expander (.7z, .xz, encrypted) are reported as
`unopened` rather than ignored, because a store that looks thin because half of it is in a .7z is a
different problem from one that is thin because nothing was captured.

## What it deliberately does not do

**Run commands.** A servitor local command appliance (`Type=="command"`, `WorkDir` on the folder)
already mints frozen command templates behind an owner approval gate, with typed parameters quoted
at runtime so a placeholder can never contribute syntax. A second command path here would be a
weaker copy. The two compose: this answers "where does X appear", the appliance answers "run the
extractor over it".

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
This is persistent, admin-registered, read-only. Same mechanics, different mount and different
trust. Folding both onto one engine is a real lift-to-core candidate.

## The agent

`extras/investigator.agent.json`, importable at `POST /orchestrate/api/agents/import`. Two config flags carry
the memory requirement, and the prompt has to agree with both:

- `memory_mode: "agent"` — the Lessons-learned directive rather than the personalization one.
- `disable_inferred: true` — Reference Memory off. This is the layer that would compound one
  incident's findings into the next investigation by similarity, which is the actual mechanism by
  which an agent mixes up two issues.
- The prompt says which is which, because the mode is a **directive to the model, not a gate on the
  write**: *"If you cannot phrase a lesson without naming this particular incident, it is not a
  lesson."*

Per-investigation specifics live in the session transcript and are discarded with it. Nothing needs
per-bundle memory scoping, because nothing durable is written about a bundle.

`core/machine_extras_test.go`'s sibling `apps/orchestrate/extras_agent_test.go` pins that those two
flags and the prompt keep agreeing.

## Setting it up

```sh
# 1. Register the folder: Admin → Files → Add file store.
#    Or the endpoint, if you prefer:
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/filestore/api/stores \
  -H 'Content-Type: application/json' \
  -d '{"name":"Support bundles","path":"/var/log/bundles","description":"Customer support captures, one folder per ticket."}'

# 2. Create the agent.
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/agents/import \
  -H 'Content-Type: application/json' -d @extras/investigator.agent.json

# 3. Attach the store to it: agent editor → Sources → File stores.

# 4. Drop a scan in, or upload one.
curl -sS -b cookies.txt -F 'file=@server.log' \
  'http://127.0.0.1:8181/filestore/api/upload?slug=support_bundles&within=scan-2026-08-13'
```

Then open a session and name the scan. Next scan is a new subfolder and a new session; there is
nothing to configure between them.
