# One history per app, for Builder and Anvil, and apps that hold everything they run on

Status: agreed (2026-11-17). Stage 1 is built on the `app-history` branch,
not yet merged: steps 2 to 4 done, step 5 (measuring) waits on running it. The seven questions are settled
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
with no second path to keep in step. An app's agents live inside it, as a
built-in app's hidden agents do (the guide writer, the publisher): no
picker, Fleet list, dashboard, key or other agent outside the app reaches
them, and only the app's own agents may hand them work. What they ran and
cost shows in Fleet under the app's name. An administrator still sees them,
marked "part of <app>", and can switch one off or remove it (a tool
misbehaving), which stops it and is recorded in the app's history; that is
a stop, not an edit.

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

**Serving.** The `live` branch is kept checked out as a plain folder, where
no agent's workspace reaches and only the install path writes (further
decision 2), and the app is built from those files (the folder reader `BuildAppFolder`
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

## Further decisions

Raised after the questions, and agreed (owner, 2026-11-17). Each names the
stage it belongs to.

1. **A commit is its author's work (stage 1).** Saves wait to be committed
   until a piece of work is finished, so two people changing one app (an
   owner and a maintainer) would have one author's commit sweep in the
   other's saves. A commit takes what its author's turn changed; another
   person's saves waiting in the repository are committed first, under that
   person's name, with the change notes their saves carried. The saves a
   turn made are known from the revisions it pushed.
2. **The live folder cannot be changed around its gates (stage 3).** In
   stage 1 the repository is a record: writing into it lies about the
   history and changes nothing that runs. In stage 3 it is what is served,
   so a file written into it would change a live app without its checks or
   approval. The live folder sits where no agent's workspace reaches (not
   under the owner's workspaces root, which sessions' folders sit inside),
   is written only by the install path, and is served only while it matches
   its last commit on `live`; a folder that does not match is refused,
   logged, and the last commit served instead.
3. **Agent checks.** "Tested" means the app's checks and verify pass, and
   neither looks at what an app's agent says: a prompt change could pass
   both and make the agent worse. An app's checks gain a third kind: a
   prompt given to one of the app's agents, and what its reply must and
   must not contain (`{"agent": "<name>", "ask": "...", "expect": [...]}`),
   run like a script check, against scratch records, with writes refused.
   Builder commits an agent's change once its agent checks pass. Built so
   the agent answers without its tools (what a script's gohort.ask
   reaches), which tests what it says and can never write; testing its use
   of tools, with them held to scratch records, is a follow-up.
4. **Model tiers, not model names (stage 2).** An agent copied or imported
   into an app carries its model choice, and model names differ between
   deployments. An app's agents name a tier (Worker, Lead, the Offload
   model); a particular model is a setting's value on this copy, and does
   not travel.
5. **What does not travel is asked for on import (stage 2).** The blueprint
   names what it needs that only a deployment has: a credential, the channel
   an agent answers on (iMessage, Teams), an MCP server. An imported app
   lists each and asks the person to pick theirs, and stays switched off
   until every one is chosen (imports already land switched off; this gives
   the reason a list).
6. **Schedules (stage 2).** An app's agents' schedules and its collections'
   refreshes are defined in the blueprint; when one next runs, and how its
   last run went, are data.
7. **Copying keeps what the agent learned about this app's people
   (stage 2).** When an existing app copies the agent it used on its first
   change, the copy starts without what that agent learned, and the app
   would seem to forget. The first-change copy also copies what the agent
   learned about the people using this app, once, and says so in the
   change's note. An export still never carries learned memory.
8. **Updates for imported apps (after stage 3).** With history, someone who
   imported an app could take the author's later improvements, merged with
   the changes they made since. Not built; named as what the history makes
   possible.
9. **Deleting an app deletes what it owns (stage 2).** A self-contained
   app's deletion takes its agents, its tool space and its collections'
   documents. The delete confirmation lists what goes, with counts.

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
   at once, the Co-Authored-By trailer, and each commit holding only its
   author's work (another person's waiting saves committed first, as
   theirs). Agent checks, so an agent's change can be tested.
4. Anvil's app projects open the app's repository; Install fast-forwards;
   another save since is fetched and merged, or shown as a conflict.
5. Measure: repository sizes and commit times across real apps. Each
   finished commit logs both (`[app-history] ... committed in ...; its
   history is ...`), so running the branch is the measurement.

Stage 2 (an app holds everything it runs on):

6. An app's own tool space: tools named by the app, its agents seeing that
   space and gohort's own tools; approval by reach, one review per space; a
   copy keeping its approval while unchanged. **Built (piece B, 0.9.527),
   except the copy keeping its approval (with piece C).** Decided (owner,
   2026-11-18):
   - **A store of its own** (`core/apptools`, table `app_tools`, keyed by
     owner and app). The pool and everything that reads it never see an
     app's tool; nothing moved, and a space started empty.
   - **The pool is cut at piece D.** Until an app copies what it uses on its
     first change, its agents load its space beside the pool, the app's tool
     winning a name they share; a held tool of the space is not stood in
     for by the pool's.
   - **Every app tool that reaches further waits**, Builder's and Anvil's as
     well as an import's, stricter than the pool. Reach is a credential
     (`credential`, `secret:`, `fetch_via:`) or raw network; a store's
     commands and the host are reached by no stored tool (an admin's
     mapping is never stored, and no tool mode runs on the host). The
     review is one request per app in the approvals queue ("App's tools"),
     naming each such tool and what it reaches; approving it approves what
     it named, each by a fingerprint of its definition, so one changed
     since asks again. The flags that narrow a tool (confirm in chat, never
     unattended) do not count as a change.
   - **In the folder** as `tools/<name>.json`, the script beside it as
     `tools/<name>.py` (the extension of its script_name), so Anvil edits a
     real script and the history diffs read as code.
   - Builder reaches it with tool_def's `app` argument (create, get,
     update, test, delete, list), wired for Builder's session only. Admin's
     Tools page lists every app's tools ("Apps' own tools") with what each
     reaches and whether it loads, and switches one off: a stop kept on the
     space's review record, never a change to the tool.
   - Not yet: an export carrying what an app owns (agents, pipelines,
     machines and tools together).
7. An app owns its agents, pipelines and machines: Builder copies rather
   than points, each in the app's folder and history, exported and deleted
   with it; shown read-only elsewhere, with an administrator's stop.
   **Built (piece A, 0.9.523 to 0.9.526; piece C, 0.9.528), except the
   export.** Piece C, decided (owner, 2026-11-18):
   - **The copy is made at binding**, in app_def's save: an agent_id or
     pipeline_id naming something that is not the app's own (the person's,
     or another app's) binds a copy made for the app; the original is left
     as it is. Only a new binding copies (a new app, or one now naming
     something else): an app that keeps pointing keeps pointing until its
     first change (piece D). Anvil's install passes through the same save.
   - **What the copy runs comes with it**, each the app's own: an agent's
     sub-agents and the person's tools it names (its agent-scoped tools and
     the pool tools in its allowed_tools; one allowed every tool copies
     none), into the app's tool space, an unchanged copy keeping its
     approval; a pipeline's stage agents and machines; a machine's step
     agents, pipelines and machines. Only the person's own (or the
     framework's agents) are copied; one shared with them is refused.
   - **A copy keeps its original's name**, and records what it was copied
     from (`copied_from`: id, name, when). Inside the app a name means the
     app's own: a save of an app's pipeline or machine by Builder or Anvil
     stores each agent, pipeline and machine it names as the id of the
     app's own of that name, and an app's run_agent and run_pipeline look
     there first. Anywhere else, where an original and an app's copy tie,
     the name means the one that is no app's.
   - A copy does not take who its original is shared with.

   **Piece D, existing apps copying on their first change (0.9.530), a
   migration made one app at a time, with no pass at startup.** Decided
   (owner, 2026-11-18):
   - **Only a change copies**: the first save of an app by Builder or Anvil
     makes what it is bound to its own, as piece C does a new binding. An
     app nobody changes keeps working as it is. Admin's list of apps marks
     each one that runs an agent or pipeline and is "not self-contained
     yet".
   - **The copy of what an app was already bound to takes what its
     original learned**, once: its facts (whoever said them, the app's
     users included), its working notes and its learned findings (indexed
     again after the save). Nothing records which app a conversation came
     through, so it is all of it; it stays in the owner's store, and an
     export never carries it. A new binding (piece C) starts fresh.
   - **Tools taken from the deployment's catalog are copied in** as the
     person's own pool tools are (the release they took), so the app
     travels whole; the author's later fixes no longer reach it.
   - **The app then holds its own tools** (`AppSpec.OwnTools`): its agents
     stop loading their owner's pool and load its own space, gohort's tools
     and the deployment's MCP servers. **Held while one of its agents is
     allowed every tool** (no list of its own, or "*"): nothing says which
     of the owner's tools it needs, so the app keeps loading the pool, and
     each save says so and asks for that agent's tools to be named; the
     next save copies them and makes the cut. New apps hold their own tools
     from their first save on the same terms.
8. A shared app's approval covers what it owns.
9. Model tiers in an app's agents; what does not travel asked for on import;
   schedules defined in the blueprint; the first-change copy keeping what
   the agent learned about this app's people; deleting an app listing what
   it owns.

   **Piece F (owner, 2026-11-18).**
   - **Model tiers: already so.** An agent names a tier (`lead_model`:
     worker or lead), a pipeline stage and a machine step `"worker"` or
     `"lead"`; nothing in an app names a model. Which model a tier is
     stays the deployment's (routing). An agent's TTS voice is the one
     name that is the deployment's; it is asked for on import.
   - **Schedules: already so.** An app's scheduled work is its actions'
     schedules (in app.json) and its collections' refresh (their
     blueprints, piece E2); when each next runs and how the last run went
     are the scheduler's, data. An agent's recurring updates belong to a
     conversation, not to an app.
   - **Export carries what the app owns (F1, 0.9.537).** An app's recipe
     carries its folder's owned files (agents, pipelines, machines, tools
     with their scripts, collections' blueprints and seed files) under
     "owned", its own agent and pipeline named by name; nothing it owns
     rides beside it as a dependency. An import saves the app switched off
     and applies them as an install applies a folder, credited to the
     import, binding the app to its copies. Older bundles import as they
     did; their bound agent becomes the app's own on its first change.
   - **What does not travel is asked for on import (F2, 0.9.538 and
     0.9.539).** On import the app lists what it names that only a
     deployment has: credentials its scripts and tools reach, tools it
     calls that are neither gohort's nor its own (an MCP server's
     included), its agents' voices, and the file stores it was granted
     where it was exported (the recipe carries those). One found here is
     mapped to itself. Each other is answered by the owner: select one of
     theirs, create a new one (then select it), or don't map (it goes
     without). A dialog opens after the import for the ones that did not
     match; the same form is the app's Settings page's Connections, where
     any answer can be changed later, and the owner reaches it while the
     app is switched off. The app cannot be switched on until every need
     is answered.
   - The answers are aliases the app keeps on this deployment
     (`core/applocal`, never exported): wherever the app reaches one (its
     scripts' and tools' credentials, the sandbox hook still checking the
     name the app declared; the tools its scripts call and its agents may
     use; its agents' voices; its scripts' stores) the name means what it
     is mapped to here, and one it goes without is refused saying where to
     map it. A store mapped to one of the owner's asks an administrator
     for it, as an app asks for a store.
10. Blueprints: a collection's blueprint and seed files, and each agent's
   starting memory, in the app; documents and learned memory kept as data,
   out of git and exports. An app's collections: the app's mark, only its
   own parts writing (knowledge_add for its scripts), curation wired by the
   app with an on/off setting, sources and rules recorded into the
   blueprint when changed from anywhere. Existing apps copying what they
   use on their first change (agents, pipelines, machines and tools: built,
   piece D; collections with the blueprints).

   **Piece E1, an app's memory and what its agents know from the start
   (0.9.531).** Decided (owner, 2026-11-18), replacing "each agent's
   starting memory" and "what an agent learns in use" above for an app's
   agents:
   - **Memory is the app's, and an option.** `app.json` `memory: "on"`
     (Builder's app_def memory; off unless set) gives the app a memory of
     its own, one per person, kept in that person's store and shared by
     every agent of the app: remember and recall as usual, deleted with
     the app, never exported. Copying or replacing an agent loses nothing.
     Off, the app's agents remember nobody. An app not saved since keeps
     its agents' own memories until its first change, which switches the
     app's on and copies what they learned into it (the owner's and each
     other person's, each in their own store).
   - **What the agents know from the start is the app's**: `starting_facts`
     in `app.json`, given to every agent of the app before anything
     learned, and changed only with the app. An agent's `seed_notes` stay
     its starting working notes.
   - **The ⚙ in the app's bar** opens its Settings page, which holds what is
     the person's own about the app: its settings; what it remembers about
     them (the starting facts read-only, their own facts each to forget,
     Forget everything, and "Remember me in this app" to stop being
     remembered, which keeps what is kept until they forget it); and, for
     its owner and maintainers, Change with Builder (moved off the bar).
     Each person sees only their own: what it remembers about anyone else
     is in that person's store.

   **Piece E2, an app's collections, step by step.** Seed files travel as
   their text, rebuilt from what was indexed (owner, 2026-11-18).
   - **Step 1, the mark and the blueprint (0.9.532).** A collection marked
     `owning_app` is in the app's folder as `knowledge/<name>.json`: name,
     description, filter rules, search terms, documents per run, articles,
     refresh, submitters (by agent name) and the suggestion switch; never
     its documents or what it has fetched. An install applies the folder's,
     a new file making a new collection of the app's; deleting the app
     deletes them, keeping one an agent outside the app reads. A change to
     the blueprint from anywhere (the Knowledge page, its refresh) is the
     app's change, recorded in its history as its maker's: Builder's or
     Anvil's when they made it, else the person's own. Builder makes one
     with collections create owning_app.
   - **Step 2, seed files (0.9.533).** The documents added to an app's
     collection by hand (an upload, a paste) are in its folder as
     `knowledge/<collection>/<title>.txt`, their text rebuilt from what was
     indexed, the title on the first line. An install indexes a new or
     changed one, replacing the document of its title, and writes the
     folder again as indexed; a document whose file is left out is kept and
     said. What a curation run gathered is not a seed: the run gathers it
     again.
   - **Step 3, who reaches it (0.9.534).** Only the app's own parts: its
     agents read it and add to it (Builder too, building the app), its
     scripts add to it with `gohort.knowledge_add(collection, title, text)`
     (capability "knowledge_add", which reaches the app's own collections
     only), and people through the app's page and the Knowledge page. An
     agent outside the app does not see it in its searches or its
     collections list, and is refused adding to it. What an agent or a
     script adds is the app's data, indexed under names of its own, never
     taken for a seed file. What it is (its name, description and
     submitters) changes with Builder, as the app does; its plan from the
     Knowledge page as well.
   - **Step 5, keeping them current is the app's setting (0.9.535).** How
     often an app's collection refreshes is its blueprint, kept on it
     (`Collection.Refresh`), changed with the app or from the Knowledge
     page (recorded in the app's history). Whether it refreshes on this
     copy of the app is the owner's switch on the app's Settings page,
     "Keep its collections current" (`AppCurationOff`, on unless turned
     off): off stops their schedules and never changes the blueprint;
     back on, each refreshes as its blueprint says.
   - **Step 6, what its agents read becomes the app's (0.9.536).** A
     collection of the person's that one of the app's agents reads is
     copied into the app on a save by Builder or Anvil (the first change of
     an existing app, a new binding, or an agent of the app pointed at one
     later): its blueprint, and its documents as they are indexed (their
     passages and embeddings, nothing indexed again), its submitters the
     app's copies of them where the app has those. The agent then reads the
     copy; the original and the person's agents reading it are left as
     they are. One that is not the person's own (the deployment's, or a
     colleague's shared with them) is the deployment's, as an MCP server
     is, and is not copied.

Stage 3 (the app is its repository):

11. Serve from the live folder, cached, for an app that has moved; the store
    serves the rest. The live folder outside every workspace, written only
    by the install path, served only while it matches its last commit.
12. Installs merge into `live` through the save gates; Builder works on its
    branch, one per app; without git, saves write the live folder directly.
13. Revisions, revert and Preview from git; bundles as git bundles.
14. Move apps on first save or open; clear their code fields once served
    from the repository.

After stage 3: updates for imported apps, merged with the changes made
since.

Each stage on its own branch: stage 1 changes how Anvil opens an app and
adds a repository per app; stage 2 changes what an app owns and how tools
are kept and approved; stage 3 moves where every app's code lives.
