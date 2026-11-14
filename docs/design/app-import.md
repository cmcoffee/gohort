# App import: absorbing an existing app into gohort primitives

Status: proposed (2026-11-13). Nothing built.

## Why

People already have small apps made elsewhere: a React page with a Node
backend written by Claude Desktop, a Flask tool, a static HTML
calculator. Running them beside gohort means another server, another
login and keys in a `.env`. Rebuilding them by hand from a description
loses what the original already settled.

Import takes the source of an existing app and rebuilds it as a gohort
custom app. Its screens become the toolkit's typed sections, its routes
become the app's record store and sandboxed scripts, and its secrets
become gohort credentials. What does not fit is either kept in an
isolated `html` section or reported as not carried over. The result looks
and behaves like the rest of gohort, and the owner gets users, sharing,
review and versioning for free.

It is generic. Nothing in it is specific to React, Node or Claude
artifacts; those are just the first case it is tried on.

## The shape

The work splits in two: **understanding** the source and **building**
the app.

| Who | Does | Tools |
|---|---|---|
| **App Importer** (new, built-in, hidden) | Reads the source, inventories it, maps every piece to a gohort target, returns the plan | `app_source` (new, read-only), read access to the source |
| **Builder** (as today) | Shows the plan, waits for approval or changes, builds from it, verifies, reports | Its existing authoring tools: `app_def`, `tool_def`, drafted credentials |

The reasons for the split:

- **Builder's brain is unchanged.** It gains an intake option and a short
  paragraph. Attaching a machine to Builder would replace its prompt for
  every message and need a routing phase. That is avoided here.
- **No new authoring power.** Authoring tools reach Builder only through
  an identity check (`builderInternalTools`, `agentCanAuthor`). The
  importer never builds, so that check does not widen.
- **Builder's context stays small.** Reading a whole source tree is the
  bulk of the work. It happens in the importer's context; Builder
  receives a compact plan.
- **No built-in machine loader.** The importer's job is two straight
  steps with no stop between them (the stop is in Builder). A prompt is
  enough. If it drifts, it can become a machine later without touching
  Builder.

Precedent: Research (`seed-research`) is a hidden built-in that
Collections and Scribe run by id through `RunAgentSync`. The importer is
the same kind of thing, run by Builder.

## The flow

1. **Intake.** Builder's intake form gets a fourth action, **Import**,
   beside Create, Change and Fix (the form is data in `builder.md`'s
   frontmatter). It asks where the source is (see Sources) and,
   optionally, what to call the new app.
2. **Analysis.** Builder calls `app_import(action="analyze", source=...)`.
   That tool runs the importer by its fixed id and returns its plan. It
   is a dedicated tool rather than a hole in `agents(action="run")`: that
   refusal (`builderRunRefusal`) stays exactly as it is, and the only new
   path reaches one agent.
3. **Inventory** (inside the importer). `app_source(action="inventory")`
   walks the source and returns a structured map (below). The importer
   then reads only the files the map points at.
4. **Mapping** (inside the importer). Each inventoried item gets a
   target, following Builder's existing reach order (below). The result
   is the plan.
5. **Review** (back in Builder). Builder shows the plan as a card and
   stops. The owner approves it, or says what to change ("keep the
   drag-and-drop board", "drop the admin screen"). A change goes back to
   the importer with the owner's note, or Builder adjusts the plan
   itself when the change does not need the source re-read.
6. **Build.** Builder works through the approved plan, one item at a
   time, with its existing tools. Every save passes the existing review
   gates for changes.
7. **Verify.** `app_def(action="verify")`, plus one new check: every plan
   item marked as carried over appears in the `STORED:` line. Anything
   missing goes back to step 6. The final report cannot claim a piece
   that was not built.
8. **Report.** What was carried over and as what, which handlers were
   rewritten from Node into Python (so the owner knows where to look),
   which secrets need entering, and what was left out and why.

## Sources

The source is untrusted code. Nothing in this flow runs it, installs its
dependencies or executes its build scripts. The importer reads files as
text.

| Source | Status |
|---|---|
| A file store folder | Exists (admin-registered folders, `apps/filestore`). Works today for anyone with access to a store. |
| Code pasted into the conversation | Exists. Fine for a single-file artifact. |
| An uploaded `.zip` | New. Unpacked into the importer's own workspace (`EnsureAgentWorkspaceDir`). The unpacking refuses: paths that leave the folder (zip slip), symlinks, more than a set number of files, and more than a set total size, checked as it goes rather than trusting the archive's own header. `node_modules`, `.git` and build output folders are skipped. |

A git URL is left for later. It overlaps with the repo browser being
built.

## The inventory tool: `app_source`

Read-only and deterministic: the same source gives the same map. It
recognises the stack from manifest files and then looks for the shapes
that matter. Unknown stacks still get a file tree and the generic
findings (secrets, hosts, data files).

| Finds | How |
|---|---|
| Stack | `package.json`, `requirements.txt` / `pyproject.toml`, `Gemfile`, `go.mod`, a bare `index.html` |
| Screens and components | React components and routes, HTML pages, templates |
| Routes | Express (`app.get`, `router.post`), Flask (`@app.route`), FastAPI (`@app.get`), plus a plain list of files that look like handlers when no pattern matches |
| Data | SQLite files, JSON data files, schema definitions (Prisma, SQL `CREATE TABLE`, ORM models) |
| Secrets and settings | `process.env.X`, `os.environ`, `.env` keys (names only; values are never read into the plan) |
| Outside calls | Hosts in `fetch`, `axios`, `requests` calls |
| Live updates | Websockets, server-sent events, polling loops |
| Dependencies | The declared packages, each marked when the mapping will need to account for it |

Each finding carries its file and line, so the importer reads exactly
those spots.

## Mapping: the reach order

Builder's prompt already sets this order for apps. The importer follows
it, so an import builds what Builder would have built from a description.

| Original | Target, first that fits |
|---|---|
| A list, a form, a detail view, a chart, a document with a chat beside it | A typed section: `table`, `form`, `display`, `chart`, `workbench` (an app that needs a brain also gets its agent, as Builder already does) |
| Routes that create, read, update and delete records | Nothing to write: the app's record store and its built-in endpoints |
| Routes with logic (computing, calling out, transforming) | A data-source or action script, **rewritten into Python** |
| A key in `.env` or a hardcoded token | A drafted gohort credential the owner enters; the script references it |
| An outside API the app polls or calls often | A connector, when one fits; otherwise a credential plus a script |
| The app's own login and users | Dropped: gohort users and app sharing replace it |
| A multi-stage job (several passes, review rounds) | A pipeline plus one pipeline section, as Builder already does |
| Something no typed section expresses (a canvas, a game, drag-and-drop) | One `html` section with the original component's behaviour, isolated as every app's HTML is |
| Something that cannot be carried (realtime collaboration between users, a native module, a background daemon) | "Not carried over", with the reason |

**Node logic is rewritten into Python** in this phase. App scripts run
`python` or `bash` in the owner's sandbox, and adding Node would mean
sandbox work and carrying npm dependencies. The rewrite is the least
reliable step, so the plan marks every rewritten handler, and the report
lists them.

## The plan

The importer returns it, Builder shows it and builds from it. One entry
per piece of the original:

| Field | Holds |
|---|---|
| `source` | The original piece, with file and line (`server.js:42 POST /api/tasks`) |
| `kind` | screen, route, data, secret, outside call, live update, dependency |
| `target` | What it becomes: a section type, "record store", a script name, a credential name, `html`, dropped, not carried over |
| `how` | One line on the mapping ("rewritten into Python: same validation, same response shape") |
| `why` | For dropped or not carried over: the reason |
| `rewritten` | true for a Node handler rewritten into Python |

Plus a header: the stack found, the app's name, the files read, and any
part of the source the importer could not read.

## Data

`app_def` has no action that seeds records, and the import that exists
(`records_artifact.go`) reads gohort's own bundles, not a foreign
database. So **data is not imported in this phase**. The plan maps the
original's tables to record fields, and the report says the data stayed
behind. Importing rows (SQLite or JSON into the new app's empty tables,
never over data already there) is the first thing in Later.

## What is new and what is reused

| New | Reused |
|---|---|
| The hidden built-in importer (a file in `builtin/`) | Builder's intake form, review card and authoring tools |
| `app_source` (inventory) | `app_def` and its verification, the `html` section's isolation |
| `app_import` (Builder runs the importer through it) | `RunAgentSync`, the Research precedent |
| Import in the intake form | File stores, agent workspaces |
| The plan format and the "every item stored" check | The change review gates, credential drafts |
| Zip unpacking into the workspace, with its limits | |

## Testing

- `app_source` against small fixture apps under `testdata`: a React +
  Express app, a Flask app, a static HTML page, an unknown stack. The
  maps are checked exactly.
- The zip unpacker against hostile archives: a path that escapes, a
  symlink, too many files, a size bomb. Each is refused and nothing is
  written outside the workspace.
- The importer's plan, with the scripted LLM fake: the plan parses, every
  inventoried item has an entry, and a dropped item has a reason.
- The verify check: a plan item missing from `STORED:` fails the verify;
  a complete one passes.
- `builderRunRefusal` still refuses everything it refused before.
- Then a live trial on one of the owner's Claude Desktop apps.

## Steps

1. `app_source` and its fixtures.
2. The importer agent and `app_import`, with the plan format.
3. Import in Builder's intake and the paragraph in its prompt; the plan
   card.
4. The "every item stored" verify check.
5. Zip upload into the workspace, with its limits.
6. The live trial.

## Later

- **Data import**: rows from SQLite or JSON into the new app's empty
  tables.
- **Node as a script language**, if rewritten handlers prove unreliable.
- **A git URL as a source**, alongside the repo browser.
- **Claude Desktop sending an artifact directly** through gohort's MCP
  server, so "save this to gohort" starts an import.
- **The importer as a machine**, if its two steps drift as a prompt.
