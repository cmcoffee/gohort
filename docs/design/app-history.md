# One history per app, for Builder and Anvil, and apps that hold everything they run on

Status: agreed (2026-11-17), stage 1 next. The seven questions are settled
(Decisions, at the end). Step 1 (Anvil saying when an install leaves the app
broken) shipped first, on its own, in 0.9.510.

Three stages. **Stage 1** gives every app a repository that records its
work, and has Anvil work in it; the app as stored stays what is served.
**Stage 2** makes an app self-contained: it owns the agents, pipelines,
machines and tools it runs on, changed only through Builder and Anvil, so
its history holds all of it. **Stage 3** makes the repository what the app
is: it is served from the repository's live branch, and the store keeps
only what is not code. Each stage is built and lived with before the next;
none throws away the one before, and what stage 1 measures (repository
sizes, the time a commit takes) decides the later stages' details.

## Why

An app is changed from two places, and they do not see each other.

- **Builder** changes the live app directly (`app_def` update, patch_html,
  replace_function, revert), or through a project folder in its chat
  workspace (`checkout` / `publish`). Its history is the app's revisions:
  the previous version kept on each save, with the change note.
- **Anvil** keeps the app's files in a git checkout of its own
  (`.anvil/<project>`), made once when the app is opened, and Install
  commits there. Builder cannot reach that checkout.

Both save through the same code (`appDefCreateOrUpdate`, then
`SaveAppSpecAs`), so every check and gate is shared. What is not shared is
the history:

- Builder's changes are never commits. Someone reading an app's git history
  in Anvil sees only what was installed from Anvil.
- When Builder changes an app that is open in Anvil, Anvil's next Install
  is refused ("the live app was changed since this folder was checked
  out"), and nothing helps bring the two together: the checkout has to be
  thrown away or edited by hand to match.
- A maintainer's edits, a revert and an app's settings land in revisions
  only.

## What it would be

**Every app has its own repository, and a finished, tested piece of work on
it is a commit there**, whoever made it.

- The repository is the app's folder (the format `WriteAppFolder` writes:
  app.json, page.html, data/, actions/, lib/, assets/, NOTES.md,
  checks.json), at `<workspaces>/<owner>/.apps/<slug>/`, outside any chat
  workspace.
- **Every save updates its files.** `SaveAppSpecAs` is the one place every
  save passes through (Builder's edits, an install, a revert, a maintainer,
  settings), so the saved app is written into the repository's working
  files there. A change that does not touch the files (sharing, who
  maintains it) changes nothing in it.
- **A commit is a finished piece of work, as Claude Code commits.** Saves
  are not committed one by one: between commits the live app is ahead of
  its history, and the repository shows those saves as uncommitted
  changes. Builder commits when a piece of work is done and tested (verify
  passed on the revision serving now, and the app's checks pass), with a
  message it writes; a commit before that is refused, saying why, and a
  turn that ends verified with changes uncommitted is sent back to commit
  them. Anvil already works this way (`commit` once its check passes;
  Install commits with the message the assistant wrote). A change a person
  makes themselves (a revert, a setting, a maintainer's edit on the app's
  page) is committed at once: their action is the finished piece.
- **A message** has a subject under 72 characters saying what changed, a
  blank line, and why.
- **The author is the person** (`user <user@users.gohort.invalid>`, as
  Anvil's commits are now), the maintainer for a maintainer's change, and
  the agent is credited as Claude Code credits itself:
  `Co-Authored-By: Builder` or `Co-Authored-By: Anvil`.
- The live app stays what is served. The repository is the record of the
  work; nothing reads the app from it. That keeps every existing path,
  check and revision as it is, and nothing is migrated: an app's history
  begins at its next save, with a commit of the version it starts from.

**Without git** (a machine where it is not installed), apps do not change:
they save, run and keep revisions exactly as now, and only the history in
git is missing. The admin page says so ("git is not installed: app history
is kept as revisions only"). Once git is there, an app's history begins at
its next save.

**Anvil opens the app's own repository** instead of making a checkout of
its own.

- Opening an app in Anvil clones its repository (a local clone, so the
  app's history is there and the work is still in a checkout Anvil owns).
- Install saves as it does now; the save commits in the app's repository,
  and Anvil's checkout takes that commit (fast-forward).
- When Builder or anyone else saved the app since, Anvil fetches it: if the
  work in the checkout does not touch what changed, it is merged and Install
  goes ahead; if it does, the project shows what conflicts, as git does, and
  the assistant or the person resolves it. That replaces today's refusal.
- An Anvil app project made before this keeps its own history and keeps
  working; the feature is days old, so nothing moves it over. Removing it
  and opening the app again gives it the app's history.

**Later, separately:** Builder's folder workflow (`checkout`) uses the same
repository, and the revisions list is read from git. Neither is needed for
the above; stage 3 does both.

## Stage 2: an app holds everything it runs on

Today an app points at things that are not its own. It names an agent
(`agent_id`), and that agent is a record of its own, edited on the Agents
page, used by other agents and apps; its tools sit in the person's tool pool
or on the agent's record; a pipeline (`pipeline_id`) and its machines are
the same. Changing any of them changes the app, and none of it is in the
app's history, its export (which carries the bound agent and the
credentials its scripts name, and nothing else) or its checks. An app that
copies them is whole: its history is all of it, its export carries all of
it, and deleting it leaves nothing behind.

**What an app owns.** Its agents, pipelines and machines, and its own tool
space. Each is in the app's folder and history (`agents/<name>.json`,
`pipelines/<name>.json`, `machines/<name>.json`, `tools/<name>.json`),
exported and deleted with it. Builder, finding an agent that would do, makes
the app a copy of it rather than pointing at it; each copy notes what it was
copied from. One of them belongs to one app: another app that wants it makes
its own copy.

**What it does not own**, because it is the deployment's, not anything
built on it: gohort's own tools (web_search, fetch_url, browse_page,
calculate, image and the rest), the deployment's MCP servers (an app names
a server's tools; the server is not copied), and credentials and the
keystore (an app names the owner's credential, as its scripts do now; a
copy would be a second copy of a secret).

**Its own tool space.** Tools today live in three places: gohort's own; a
person's pool, which all their agents may use; and a single agent's record
(admin's "Agent-Scoped Tools"). All but gohort's are named per person, so
an app's copy of `get_weather` could not be its own: an import matched it to
the pool's tool of that name, or set it aside under Orphaned tools. An
app's tools are kept in a space of the app's own, named by the app as well
as the tool, so its `get_weather` and the pool's are two tools that never
collide. The app's agents see that space and gohort's own tools, and not the
person's pool: a pool tool an app needs is copied in. Nothing stored moves:
the pool and agent-scoped tools stay as they are, and an app's space is new
storage that starts empty.

**Only Builder and Anvil change it.** An app's agents, pipelines, machines
and tools change through Builder or Anvil, and nothing else, so every change
goes through the app's checks, a shared app's approval and its history,
with no second path to keep in step. Elsewhere they are shown and marked:
the Agents page, the pipeline and machine editors and admin's tools list
show them as "Part of <app>: change it with Builder or Anvil". An
administrator can still switch one off or remove it (a tool misbehaving),
which stops it and is recorded in the app's history; that is a stop, not an
edit.

**A shared app's approval covers all of it.** A shared app is taken out of
sharing until an administrator approves its new version when its page or
scripts change (`appServesDifferently`); a change to an agent, pipeline,
machine or tool it owns changes what it does just as much, and counts the
same. (A change to an agent a shared app is bound to today does not, which
self-contained apps close.)

**Approval by reach, one review per space.** Every tool an imported agent
brings waits under Drafted tools for an administrator, one by one, so an
agent with five tools is five approvals before it works. In an app's space:

- A tool that only runs sandboxed and reaches the public web through gohort,
  no more than gohort's own tools reach, needs no administrator: the person
  importing or building the app chose it.
- A tool that reaches a credential, a store's commands, raw network or the
  host needs an administrator, and the review is one card for the whole
  space, listing each such tool and what it reaches, approved at once. It is
  asked again only when one of those tools changes.
- A copy made within the deployment (a pool tool copied into an app) keeps
  its approval while it is unchanged, and is reviewed again once edited.

**An app records blueprints, never what it gathers or learns.** Knowledge
and memory are built up while an app is used; what the app holds is how to
build them, and what they come to hold is data, like the records people
keep in the app.

- **Knowledge.** The app records a collection's blueprint, in its folder
  (`knowledge/<name>.json`) and history: its name and settings, what it
  gathers from (pages, searches, the app's own files, a store), how often it
  refreshes (collections refresh on a schedule already), and its **seed
  files**, the documents someone uploaded by hand (a manual, a rulebook),
  kept in the app's folder as its assets are, since nothing could gather
  them again. Changing the blueprint is changing the app: Builder or Anvil,
  the app's history, a shared app's approval. The collection's documents
  are built from the blueprint and are data: added to and pruned from
  anywhere (the Knowledge page, an agent's ingest tools, the app), kept in
  the knowledge store, never in git. A refresh adds to what is there and
  keeps those edits; a rebuild from nothing (an import on another
  deployment) does not, so a document that must always be there belongs in
  the blueprint as a seed file. A rebuild is not identical either: what it
  gathers from changes over time.
- **Memory.** The app records what its agents start out knowing: facts
  written in on purpose ("prices are in euros") and their standing operating
  notes, part of each agent's blueprint and changed the same way. What an
  agent learns in use (remember and recall, what it picks up about the
  people using it) is data: in the memory store, managed from anywhere as
  now, never in git and never in an export. That is privacy as much as
  tidiness: a shared app's agent learns about each person who uses it, and
  an app that carried that would hand whoever received it what it had
  learned about everyone else. What it learned about a person stays that
  person's, as now, and changing or copying the app does not move it. An
  imported agent already works this way: it carries the recipe, and its
  memory and identity stay behind.

An exported app is therefore small and carries nobody's data: its blueprints
and seed files, from which the deployment receiving it builds the rest.

**An app's collections.** An app's collection is an ordinary knowledge
collection (the same ingest, search, refresh and curation), marked as the
app's (`owning_app`, as an app's agents and pipelines are). The mark decides
who touches it:

- **Only the app's own parts write to it**: its agents' ingest tools, its
  scripts (an action, or a scheduled one that keeps it current: a nightly
  fetch of the latest release notes), and the people using the app through
  its page. An agent outside the app cannot push into it; to do that it
  would have to be part of the app. The app's agents read it; others do not
  see it. Scripts need a call to add to it: `knowledge_add` in the gohort
  module, held to the app's own collections (new).
- **Curation is wired by the app.** The blueprint says how the collection
  curates itself: which of the app's agents does it, and on what schedule.
  The app's Settings page offers **Auto-curation: on / off** (a setting's
  value), with no picker of agents: the app chose, and only its own agents
  could.
- **Sources and curation rules are recorded into the blueprint when they are
  changed**, from anywhere. Where it gathers from and what it keeps, drops
  and prunes are the two parts of an app's blueprint that can be changed
  outside Builder and Anvil, on the Knowledge page or the app's Settings
  page. Such a change is a change to the app: saved through the app's own
  save (the one Builder's changes take), so it is checked, committed at once
  as the person's change, carried by an export, and takes a shared app out
  of sharing until an administrator approves it (what a shared app gathers
  is what its users are shown). Everything else in a collection's blueprint
  (which agent curates, how the app wires it, its seed files) changes
  through Builder or Anvil only.
- **A setting's value is how this copy runs, and does not travel.** An app's
  settings carry their names and defaults; the values chosen (auto-curation
  on or off, a refresh time) stay on the deployment, as every app setting's
  value does now. What should go wherever the app goes belongs in the
  blueprint.

**Export is not a backup.** An exported app carries its blueprints, never
what it holds: not its settings' values, its collections' documents, what
its agents learned, or the records people keep. Re-imported, it builds its
collections again from their sources and seed files and starts from its
defaults. Moving one's own app with its data (another machine, a
reinstall) is a backup, a separate feature, not this.

**Existing apps copy on their first change.** An app that points at an
agent, pipeline or tools of the person's copies them into itself the first
time Builder or Anvil changes it, and from then on owns them. An app nobody
changes keeps working as it is. This is a migration, and is said as one: an
app's agent becomes a copy the app owns, and the agent it was copied from
no longer changes it.

## Stage 3: the app is its repository

The app runs from its repository. Stage 1's repository becomes the source,
and the store keeps the app's record.

**What is where.**

| In the repository (the app's code) | In the store (the app's record) |
|---|---|
| app.json, page.html, data/, actions/, lib/, assets/, NOTES.md, checks.json | owner, slug, sharing (people, groups, public), maintainers, managers |
| | an administrator's approvals (publishing, store access, outbound) |
| | verify verdicts, settings' values, private_db, schedules' state |
| | the records people save, and their files |

**Serving.** The `live` branch is kept checked out as a plain folder, and
the app is built from those files (the folder reader `BuildAppFolder`
already does this), cached by the folder's state, so a request reads a built
app from memory. Serving never runs git.

**Changing.** Builder and Anvil work in the repository, so their changes
meet in git. Builder has one branch per app (`builder`), kept across its
conversations, so work one conversation left unfinished is there in the
next. Builder's in-place edits (patch_html, replace_function) change files
on that branch, committed when the work is done and tested, as in stage 1.

**Going live is the one gated step.** A change reaches `live` only through
the install path, which runs everything a save runs now: the validators,
the app's checks, the page loaded in a browser, an administrator's approval
for a shared app. A merge that fails any of them does not move `live`. An
agent editing files in the repository changes its own branch and nothing
else; it cannot make anything live by writing files.

**What follows from it.**

- A revision is a commit on `live`, revert is `git revert` there, and the
  revisions list is the log.
- Preview is serving a branch to its author (what Anvil's Preview builds
  today, from a branch instead of a working folder).
- A maintainer's change goes live as now, through the same gates, committed
  under the maintainer's name. (A branch for the owner to merge is possible
  later; it is not built.)
- Bundles (`.gohortapp`) can become git bundles of the repository, carrying
  its history.

**Moving apps over.** No bulk migration: an app moves the first time it is
saved or opened after stage 3 is on. Its current version is written into the
repository (already there since stage 1 for any app saved since), `live`
points at it, and its code fields in the store are cleared once the
repository serves it. An app not yet moved is served from the store exactly
as now, so the two kinds run side by side until every app has been touched.
This is a migration, and is said as one: the code of every app ends up
somewhere new.

**What stage 3 costs on top of stage 1.**

- **git becomes required for history, not for apps.** Serving reads the
  live folder, so without git (never installed, or gone since) an app still
  runs and saves: a save writes the live folder directly through the same
  gates, revisions are kept as now, and history resumes when git is back. An
  app not yet moved is served from the store. The git program is used (what
  Anvil uses now); no Go library, so nothing new to vet.
- **Every path that changes code moves to git**: app_def's edits, revisions,
  revert, maintainers' saves, bundles. The save gates do not change; what
  they guard does.
- **A request must not wait on git.** The cache by commit is required, not
  an optimisation.

## What it costs

- **Disk.** One repository per app. Assets (images, sounds, now scripts and
  WebAssembly) are stored at each version that changes them. An app whose
  repository passes a size worth noticing (say 200 MB) is said on its row;
  pruning old history is a separate decision.
- **Time per save.** Writing the folder's files: milliseconds for a typical
  app, after the save and never blocking it; the commit comes at the end of
  a piece of work. A failed write or commit is logged and the save stands.
- **Shared apps.** The repository is the owner's. A maintainer's save is
  committed in it under the maintainer's name; a maintainer opening the app
  in Anvil clones the owner's repository and installs through the same
  maintainer rules as today.
- **Privacy.** The repository holds what the app already holds, under the
  owner's workspace, which only the owner's sessions reach.

## Decisions (owner, 2026-11-17)

1. **Where the repository lives:** `<workspaces>/<owner>/.apps/<slug>/`.
2. **Another save since Anvil started:** merged when it does not overlap,
   and Install goes ahead; only a real overlap stops it, shown as a
   conflict.
3. **Existing Anvil app projects:** nothing special; the feature is days
   old.
4. **Commits:** as Claude Code commits. A finished piece of work, tested
   (verify and checks passing), with a message the agent writes and the
   agent credited as co-author; not a commit per save. A person's own
   change commits at once.
5. **git:** the git program, with a fallback: without git, apps run and
   save as now and only the history in git is missing (stage 3 serves from
   a plain live folder, so it never needs git to serve).
6. **Builder's branch (stage 3):** one per app.
7. **Maintainers (stage 3):** save directly, as now.
8. **Self-contained apps (stage 2):** an app owns copies of its agents,
   pipelines, machines and tools, in a tool space of its own; only Builder
   and Anvil change them, shown read-only elsewhere with an
   administrator's stop; tools approved by reach, one review per space; an
   app records blueprints, never what it gathers or learns (a collection's
   sources, settings, refresh and seed files; an agent's starting memory),
   its documents and learned memory being data; an app's collection is an
   ordinary one with the app's mark, written only by the app's own parts,
   curation wired by the app with an on/off setting, and its sources and
   curation rules recorded into the blueprint when changed from anywhere;
   existing apps copy what they use on their first change.

## Steps

Stage 1:

1. Anvil says when an install leaves the app broken. **Done (0.9.510).**
2. The app repository, its files written on every save (`core`, with the
   folder writer from orchestrate), and the no-git fallback with its admin
   notice.
3. Commits: Builder's commit (refused until verify and checks pass, a turn
   ending verified and uncommitted sent back), a person's change committed
   at once, the Co-Authored-By trailer.
4. Anvil's app projects open the app's repository; Install fast-forwards;
   another save since is fetched and merged, or shown as a conflict.
5. Measure: repository sizes and commit times across real apps.

Stage 2 (an app holds everything it runs on):

6. An app's own tool space: tools named by the app, its agents seeing that
   space and gohort's own tools; approval by reach, one review per space; a
   copy keeping its approval while unchanged.
7. An app owns its agents, pipelines and machines: Builder copies rather
   than points, each in the app's folder and history, exported and deleted
   with it; shown read-only elsewhere, with an administrator's stop.
8. A shared app's approval covers what it owns.
9. Blueprints: a collection's blueprint and seed files, and each agent's
   starting memory, in the app; documents and learned memory kept as data,
   out of git and exports. An app's collections: the app's mark, only its
   own parts writing (knowledge_add for its scripts), curation wired by the
   app with an on/off setting, sources and rules recorded into the
   blueprint when changed from anywhere. Existing apps copying what they
   use on their first change.

Stage 3 (the app is its repository):

10. Serve from the live folder, cached, for an app that has moved; the store
    serves the rest.
11. Installs merge into `live` through the save gates; Builder works on its
    branch, one per app; without git, saves write the live folder directly.
12. Revisions, revert and Preview from git; bundles as git bundles.
13. Move apps on first save or open; clear their code fields once served
    from the repository.

Each stage on its own branch: stage 1 changes how Anvil opens an app and
adds a repository per app; stage 2 changes what an app owns and how tools
are kept and approved; stage 3 moves where every app's code lives.
