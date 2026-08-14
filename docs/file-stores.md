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
