# Servitor Evidence Bundles — asking questions of files you hand it

Status: **slices 1, 3 and 4 built** (v0.6.016). A new appliance type whose
content is a set of uploaded files — a support dump, a log tarball, an encrypted
diagnostic blob — staged, expanded, ingested, and then investigated with the
same lead/worker machinery every other appliance type already uses.

What shipped, and where it differs from the design below:

- `bundle_store.go` — line-sliced encrypted storage plus the per-file index.
  `bundle_format.go` — format detection, timestamp/severity/host parsing.
  `bundle_ingest.go` — expansion and the single-pass ingest.
  `bundle_upload.go` — the streaming upload and the ingest trigger.
  `bundle_tools.go` — the five worker tools. `bundle_prompts.go` — the
  investigator / worker / lead / consolidation prompts.
- **Upload is one request per file, and staging is separate from ingest.** The
  design implied a batch POST. Per-file requests are what make progress and
  retry per file real, and ingesting after each one would start N passes over a
  half-staged tree, each wiping the one before it. So `/api/bundle/upload`
  stages and `/api/bundle/ingest` runs once at the end — which doubles as the
  retry path after a failed ingest, with no re-upload.
- **Resumable chunked upload was NOT built.** A large upload streams in one
  request; if the connection drops, that file starts over. The per-file retry
  button limits the damage to one file rather than the batch.
- **xz, 7z, zst and encrypted blobs are recognized but not opened.** Go's
  standard library covers gzip, bzip2, tar and zip; the rest need slice 2's
  operator-supplied command, which is a permission question rather than a
  decompression one. Such a file is stored with `Format: "archive"` and counted
  in `BundleUnopened`, so the listing and the summary both say the contents are
  not searchable rather than the bundle looking thin for no stated reason.
- **A year-less timestamp (classic syslog) takes its year from the staged
  file's mtime**, and the file is marked `YearInferred`. The caveat is rendered
  everywhere the span is, because a span silently off by a year is worse than
  no span. A line whose format supplies no year at all does not parse rather
  than being dated by guess.
- **A line with no parseable timestamp survives a time window.** The
  continuation lines of a stack trace carry no time of their own, and filtering
  them out would sever a trace from the message that introduced it.
- **`purgeAppliance` now drops the bulk content stores** — both the bundle
  store and, pre-existing, the repo store, which a deleted repo appliance had
  been leaving behind in encrypted storage that nothing pointed at.
- **`UploadPanel` is a new generic `core/ui` component**, not a servitor
  surface: files, an endpoint, and a status field to poll. Servitor mounts it
  from its own `web_assets.html` via `uiMountComponent`.

Slice 2 (operator-supplied transform commands behind the per-(agent, appliance)
grant), slice 5 (bundle as a workspace member) and slice 6 (embedding the
derived summaries) remain open.

## The idea

Servitor can reach a live system over SSH, run a local command, search an
ingested repo, and draw on a curated knowledge collection. What it cannot do is
take a file from you.

The question this is for:

> "Here is the diagnostic bundle from the customer's cluster. Why did the
> scheduler stop picking up jobs on the 14th?"

Answering it means joining four things: the log lines in the bundle, the code in
the repo that emits them, the runbook in a collection that says what the service
is supposed to do, and — often — a live lab box you can reproduce against.
Servitor already joins the last three (`apps/servitor/web.go:3773`). The bundle
is the missing leg.

## What already exists

**The appliance record is the right chassis.** `apps/servitor/web.go:105`
defines four types (`ssh`, `command`, `repo`, `workspace`), and every record
already carries `Collections` (`web.go:157`) and `LinkedRepos` (`web.go:135`).
A fifth type inherits sharing, knowledge linkage, repo linkage, and workspace
membership without any of those being rebuilt.

**The repo backend is the storage pattern to copy.**
`apps/servitor/repo_backend.go` clones into tmpfs, ingests text files into the
hardware-locked-encrypted `RepoFilesDB`, discards the plaintext clone, and
serves `search_code` / `read_file` / `list_dir` by decrypting in memory. That is
the shape a bundle store wants, with one difference noted under slice 3.

**A gated local-exec path exists for the transform stage.** `exec_local_ctx`
(`apps/servitor/sysprobe.go:1312`) runs a command on the gohort host with a
working directory and env. Every run already gets a private scratch directory
where writes and deletes are ungated and teardown runs through the raw exec path
so the gate cannot refuse it (`apps/servitor/scratch.go`). Permission to run
anything riskier is expressed per (agent, appliance) with most-specific-wins
semantics in `apps/servitor/command_grants.go`.

**Log-shaped read tools exist, pointed the wrong way.** `read_log`,
`search_logs`, `count_lines`, and `read_range` (`web.go:2850`, `:2896`,
`:3362`, `:3385`) are exactly the right verbs, but each one builds a shell
command and runs it over SSH against a remote host. None of them can read a file
servitor is holding.

**Extraction and RAG ingest exist.** `ExtractDocument`
(`core/media/document_extract.go:64`) handles PDF, DOCX, DOC, HTML, plain text,
and audio. `handleCollectionUpload` (`apps/orchestrate/collections.go:470`)
takes base64 JSON, extracts, chunks, embeds, and makes the result reachable via
`search_knowledge`.

**Generic workspace and file primitives exist in core, deliberately out of
reach.** `core/workspace.go:68` and `:102` give per-user sandbox roots with
traversal and symlink validation; `tools/workspace`, `tools/files`,
`tools/localexec`, and `core/sandbox_exec.go` give a bwrap-sandboxed shell and
file verbs. Servitor never sets `WorkspaceDir`, and `tool_guard.go:23` excludes
all of them, because the worker tier is pinned local and every tool it can call
must be proven not to reach a third party. Bundle tools have to be built to that
same standard rather than borrowed from orchestrate.

## What is missing

1. **No inbound file path into servitor at all.** No multipart endpoint, no
   store, no UI. The nearest affordance anywhere in the framework is the chat
   paperclip (`core/ui/assets/runtime/20_chat_panel.js:202`), which accepts one
   plain-text file and inlines it into the outgoing message as a code fence.
2. **No evidence-bundle concept.** Nothing to attach to a session, share with
   another user, or drop into a workspace as a member.
3. **No transform stage.** Nothing binds *uploaded artifact* to *decrypt or
   unpack command* to *ingested content*.
4. **Every size ceiling is wrong for a dump.** 20 MiB chat attachment
   (`core/chat_attachments.go:39`), 64 MB `maxUploadBytes`
   (`core/upload_source.go:18`), 1 MiB per ingested repo file
   (`repo_backend.go:27`), 512 KB techwriter import. Support bundles run to
   hundreds of megabytes.
5. **Log-shaped retrieval does not exist.** Repo search is substring over whole
   file bodies; collection retrieval is prose-shaped chunking. Logs need time
   windows, line addressing, grep with context, first/last-seen, and a merged
   cross-file timeline. Embedding a million log lines is the wrong instrument
   and would bury the vector store.
6. **No archive support** in `ExtractDocument` — no gz, zip, tar, bz2, or xz.

## The build

### Slice 1 — the bundle appliance and upload

A fifth `Type: "bundle"` in the picker (`apps/servitor/page.go:22`), with its
own `ShowWhen` fields. Bundle-only record fields: source filenames, ingest
state, file and byte counts, and the observed time span of the contents.

`POST /api/appliance/{id}/upload`, multipart, streamed to a per-user staging
directory. Resumable in chunks above a threshold, because a 400 MB upload that
dies at 95% and restarts from zero is a feature nobody uses twice.

Staging lives on local SSD, configured separately from the data directory. The
production kvlite store is on NFS and staging a multi-gigabyte extract there
would be miserable.

**One new generic `core/ui` primitive is required: an `UploadPanel`** — multiple
files, per-file progress, per-file status, retry on one failed file without
re-sending the rest. The existing `PipelineField{Type: "file"}`
(`core/ui/components.go:1877`) is the wrong shape: it POSTs one file to an
extractor endpoint and back-fills a text field for the user to review. Per
`CLAUDE.md`, the panel goes into `core/ui/` only in fully domain-agnostic form —
it knows about files, progress, and an endpoint, and nothing about servitor,
appliances, or logs.

### Slice 2 — the transform hook (the decrypt step)

The record carries an ordered `BundleTransform` list. Each step is a matcher
(glob or content magic) plus an action:

- **Built-in actions** — gunzip, bunzip2, unxz, untar, unzip. No grant needed;
  they are pure decompression with no operator-supplied command string.
- **Command actions** — a template run through `exec_local_ctx` in the run's
  scratch directory, with `{in}` and `{out}` substituted. This is where
  "decrypt this dump with our tool" lives.

Command actions route through the existing risk classifier and are grantable per
(agent, appliance) exactly like any other command, so the operator approves the
decrypt once and it stops asking.

Key material comes from a secured credential injected as an environment
variable, never interpolated into the command string, so it does not land in the
transcript, the audit line, or the process table.

Guards, all of which have to be there from the first commit and not added after
the first bad tarball:

- archive members are rejected if they contain `..` or resolve outside the
  staging root;
- total extracted bytes, file count, and nesting depth are capped, so a zip bomb
  fails loudly instead of filling the disk;
- extraction happens in the scratch directory, which `scratch_teardown` already
  removes on every exit path including cancellation.

### Slice 3 — the bundle store

`BundleFilesDB`, mirroring `repoFileStore` (`repo_backend.go:38`): a hardware-
locked encrypted store, keyed per (user, appliance), plaintext staging discarded
once ingest completes.

The one real departure from the repo store: repo files are capped at 1 MiB and
stored whole, but a single log file is routinely far larger. Bundle files are
stored as line-addressable slices so a range read on an 800 MB log decrypts a
window rather than the whole file.

Ingest also derives a per-file index, and this is the part that makes questions
cheap to answer:

- line count and byte size;
- detected format (syslog, JSON lines, Java stack traces, access log, unknown);
- first and last timestamp, and the parse pattern that produced them;
- emitting host, where the format carries one;
- a severity histogram.

Without the index, every question starts by scanning the bundle. With it, the
lead can say "the scheduler log covers the 11th through the 16th and has 4,200
ERROR lines, 90% of them after 03:14 on the 14th" before reading a single line
of content.

### Slice 4 — the tools

All local reads against the encrypted store, so each one extends
`servitorWorkerToolAllowList` (`tool_guard.go:23`) with the same justification
comment the existing entries carry.

| Tool | What it does |
|---|---|
| `list_bundle` | file tree with sizes, formats, and time spans |
| `read_bundle_file` | a line range from one file |
| `search_bundle` | regex, with context lines, file glob, and time window |
| `bundle_timeline` | merged, time-ordered slice across several files |
| `bundle_summary` | what is in here, what period it covers, what is noisy |

Registered from a new `appliance.Type == "bundle"` branch at `web.go:3751`,
alongside the shared recording, fact, and guide tools that every type gets.

### Slice 5 — the join

`wsMember.Kind()` (`apps/servitor/workspace.go:76`) learns a `bundle` kind:
scoutable from its index and summary, drillable by its own investigator. A
workspace can then hold the customer's dump, the lab box, the repo, and the
runbook collection, and answer one question across all four.

Because a bundle is an appliance, `LinkedRepos` works on it for free — a stack
frame found by `search_bundle` goes straight to `search_code` on the linked
repo. Whether that chain needs a dedicated `trace_to_code` tool or just a prompt
block telling the worker to do it is worth measuring before building; the LLM
can already chain the two.

### Slice 6 — selective RAG

Do not embed logs. Embed the derived artifacts — the bundle summary, the
per-file profiles, and any findings the investigator records — into a per-bundle
collection. A question then lands on the summary and drills with the exact tools
from slice 4, which is both cheaper and more accurate than semantic search over
raw log text.

## Sequencing

Ship **1 + 3 + 4 with built-in decompression only**. That alone gets you
"upload a tar.gz of logs, ask questions, join against the linked repo," which is
most of the value.

Slice 2's custom command action lands second, because it is where the grant
machinery matters and it deserves its own review pass.

Slice 5 is the payoff and is mostly wiring. Slice 6 is optional and should wait
until there is a real bundle to measure it against.

## Open decisions

- **Retention.** Bundles are large and they are evidence. A TTL alone risks
  deleting the thing an investigation is still about; explicit delete alone
  fills the disk. Proposal: TTL with a pin, and the pin is visible in the
  appliance list.
- **Transform trust model.** Per-(agent, appliance) grant, consistent with
  `command_grants.go`, rather than a single "this user may define transforms"
  capability. A transform is a command running on the gohort host; it should be
  governed like every other command running on the gohort host.
- **Bundle as appliance vs. attachment to an existing appliance.** Spec assumes
  appliance type. Attachment is cheaper to build and gets none of sharing,
  collections, linked repos, or workspace membership.
