# Apps as repositories: Builder and Anvil on one history

Status: proposed (2026-11-15). Settled 2026-11-16: plain git folders in the
data directory, the git program, Anvil public, every app owner (sandboxed).
Step 1 found mostly built as Builder's app folder; the rest of it, and a
preview, built 2026-11-16 (below).

## Why

An app today is one stored record (`core.AppSpec`, table `app_specs` in its
owner's store): its sections and compiled page, its data sources, actions
and libraries, its settings, its checks. Builder changes it through the
`app_def` tool, and every save that changes what the app serves files the
replaced version in a short revision list (`PushAppRevision`).

That works for building an app by talking to Builder. It does not give an
app a real history, a way to work on it as code, or a way to keep it in
GitHub or GitLab. Anvil, gohort's coding surface (an agent working in a
git repository), could be that, but only if Builder and Anvil never edit
two different copies of the same app.

The owner's shape: Builder and Anvil complement each other. Every app has a
repository; Builder's changes are commits to it, and Anvil works on the same
repository directly. What runs is a checked install of one commit.

## The model

1. **Every app has a repository**, from the moment it is created. An app
   made before this gets one the first time it is changed or opened in
   Anvil, written from its current record, with one first commit ("as
   installed"). No migration pass.
2. **What runs is an install of one commit.** The stored record stays what
   the server serves: fast, transactional, and encrypted with the rest of
   the database. It records the commit it was installed from. The working
   files are never live: a half-made edit in Anvil changes nothing until it
   is committed and installed.
3. **Install is the one gate.** Installing a commit reads its files, builds
   the record, and runs everything a Builder save runs today: the app
   checks, the approval a shared app needs when what it serves changes (a
   non-admin owner's change unshares it until an administrator approves),
   and, for a maintainer, the allowed list of what a maintainer may change
   (`applyAppMaintainerEdit`), checked against what the commit changed.
   However a change arrives, it meets the same gate.
4. **Builder works through the repository.** `app_def` keeps its requests
   and its checks, but its write is a commit: Builder applies the change in
   a scratch working copy of the current commit, commits it (authored as
   Builder, on behalf of the person who asked, with what was asked as the
   message), and installs it. Builder never writes the record directly.
5. **Anvil opens the same repository.** An app's project in Anvil is a
   working copy of its repository. Editing, committing and Install are
   Anvil's; Install is built in (not an operator-written command) and is the
   same install as above.
6. **History is the repository's.** Revert is installing an earlier commit.
   Once this has run live, the revision list retires: it is a shorter,
   weaker copy of the same thing.

## What is in the repository

What the app IS, as files a person can read and diff. This is Builder's app
folder (`apps/orchestrate/app_folder.go`), which already existed when this
was written and is what the repository holds:

- `app.json`: name, description, the agent and pipeline it uses, its record
  key, layout flags, the settings it declares (names, kinds and help, not
  their values), its shared collections, and the sections, the authoring
  form of the page. The rendered page is compiled from them on install, so
  the two never disagree.
- `page.html` (an html section names its file), `data/<name>.py`,
  `actions/<name>.py`, `lib/<name>.py`: each part as its own file.
- `assets/`, `NOTES.md`, `checks.json`.
- `.live`: a fingerprint of the live app the folder was written from or last
  installed to; an install over a live app changed since is refused.

What it HOLDS, and what this instance decides about it, stays out: the
records people entered and files they added, setting values (keys among
them), who it is shared with and who maintains it, whether it is disabled,
its verification verdicts. Those belong to the instance, not to the app, and
a repository pushed to GitHub must not carry them.

The format is the `custom_app` export bundle unpacked into files, so export
and import keep working and a bundle and a repository say the same thing.

## Install and preview without a chat (built 2026-11-16)

Install was Builder's publish, which only runs in a chat. It is now a core
(`publishAppFolder`) with the chat's change approval passed in, and two
calls a person's own action makes with no chat (`app_folder_install.go`):

- `InstallAppFolder`: the same create or update Builder runs, so every check
  and gate holds; the person pressing Install is the approval.
- `BuildAppFolder`: the app as the folder says, compiled, saved nowhere.
- `WriteAppFolder`: an app into a folder, marked as holding the live app.

**Preview** (`apps/customapps/preview.go`). The person changing an app sees
the working copy at the app's own address; nobody else does. A preview is
started with a build, and its link sets a cookie on that app's path; a
banner leads back to the installed app. By default it reads and writes a
copy of the person's records and of the app's shared collections, held to a
page check's rules (no outbound writes, notifications, agents or
pipelines); real records is a choice made when it starts. Owner only for
now. For an app nobody else uses, Install on every save makes the running
app what is being edited; a shared app is previewed, then installed through
the approval its sharing needs.

## Builder and Anvil at the same time

Builder commits on top of the repository's current commit, in its own
scratch copy, so it never touches Anvil's working copy. If the current
commit moved while Builder worked, its commit goes on a branch
(`builder/<change>`) and is not installed; Builder says so, and the person
merges it in Anvil. If someone has uncommitted edits open in Anvil, those
stay theirs: Builder's commit lands on the repository, and Anvil shows the
repository moved, as any shared repository does.

## GitHub and GitLab

A repository can have a remote. Pushing uses the person's own connected
account (the credentials gohort already keeps per user), never a key typed
into a chat. Pulling from a remote is a change like any other: it is
committed and installed through the gate, never served straight from the
remote.

## Working from outside gohort

An app as plain files with a git history is something any coding agent can
work on, Claude Code among them. Three ways in, from least new surface to
most:

1. **Through GitHub or GitLab.** The owner pushes the app's repository to
   their remote; the agent works on a clone like any project; the owner
   pulls it back, and the pull is installed through the gate. Nothing beyond
   the remotes above.
2. **As local files.** Command-line actions to export an app to a folder,
   check a folder, and install a folder. The agent works on the folder; the
   install goes through the gate. The smallest piece, and useful before
   repositories exist.
3. **Straight from gohort over git.** gohort serves each app's repository
   over HTTPS, signed in with the person's own account key, so an agent
   clones from gohort and pushes back; a push installs through the gate, or
   waits for the owner to press Install. New surface: a git endpoint, and a
   key kind allowed to use it, checked against the external access rules.
   It serves a person their own apps, so it is not a peer key lending the
   instance to anybody.

1 and 2 first; 3 when it is wanted.

Whatever an outside agent writes is untrusted until installed: its scripts
run only in the app's sandbox, a shared app still needs an administrator's
approval when what it serves changes, and a maintainer's push is held to
the maintainer's list.

### What the agent reads

Each repository, and each exported folder, carries:

- **`AGENTS.md`**: the app format (what each file is), what install
  checks, what never goes in (records, setting values), and how to run the
  local check. The name is the convention several coding agents look for at
  a repository's root, so one file serves them all.
- **`CLAUDE.md`**: the single line `@AGENTS.md`, so Claude Code, which
  reads CLAUDE.md, imports the same guide with nothing duplicated.
- **`README.md`** (optional): for people.

gohort writes these from the format whenever it exports an app or creates
its repository, so they never drift from what install actually checks.

**A local check** runs the same checks install runs, against a folder, so
an agent tests its work before pushing instead of finding out at install.
**Install errors name the file and the field**, since an agent reads them
and fixes what they say.

## Porting a project

The same pieces bring an existing project into gohort and keep it there.
Importing, porting and maintaining are different jobs on the same surface:

1. **Import.** The existing project is added to Anvil as a repository, and
   the new app's repository links it as a read-only dependency. The agent
   reads its whole tree, runs its build and tests in the sandbox, and sees its
   history.
2. **Port.** The agent writes a gohort app in the format above (`app.json`,
   `sections.json`, `scripts/`) in the new app's repository, guided by the
   same `AGENTS.md`, runs the local check, and installs through the gate.
   The old project is never changed.
3. **Maintain.** Once installed it is an ordinary app with its own
   repository: Builder, Anvil and outside agents all work on it through
   that one history.

A gohort app is built from gohort's own parts: pages, records, scripts in
the app's sandbox, agents and pipelines. A project that is a long-running
service of its own, or needs host access gohort does not give apps, cannot
be ported whole. The useful answer there is the parts that fit as the app,
and the rest reached through a connector or a system hook. So before
writing anything the agent reports that split: what becomes the app, what
stays outside and how the app reaches it, and what has no place, so the
owner decides before the port starts rather than finding out after.

Nothing here is new machinery: the format, install, Anvil on an app and
linked projects (Anvil already has them). It needs steps 1 and 3 of the
build order, and the porting brief in `AGENTS.md`.

## Public and private

Anvil is a private app today. The repository itself (the file format,
install from a commit, Builder committing instead of writing, revert)
belongs in the public code, so every build gets versioned apps and Builder
works the same everywhere. Anvil is then an editor on top of it.

## Trust

Anvil runs operator-written commands on the host, unsandboxed; that is for
an administrator. For an app owner who is not one, Anvil on their app has
only sandboxed commands and the built-in Install. A repository's scripts
never run outside the app's own sandbox, whatever the repository says.

## To settle before building

1. **Where repositories live.** Plain git repositories under the data
   directory (simple, and what Anvil and git tools expect, but not
   encrypted at rest like the database), or git's objects kept inside the
   database (encrypted, one thing to back up, but a git library is needed
   and Anvil works through it).
2. **The git program or a library.** The program is what Anvil already
   uses and adds no dependency; a library would be a third-party dependency
   to vet and vendor first.
3. **Anvil public or private.** Whether Anvil itself becomes part of the
   public build, or stays a private editor on top of public repositories.
4. **Who may open an app in Anvil.** Every app owner (sandboxed commands
   only), or administrators only at first.

## Build order

1. **The format and install.** Write an app as files, and install a record
   from files through every gate. Test that installing what was written
   gives back the same app, and that each gate holds on install.
2. **A repository per app.** Created lazily; Builder's saves become commit
   and install; revert installs an earlier commit; the app's page lists its
   history.
3. **Anvil on an app.** Open its repository, edit, commit, Install;
   sandboxed for non-admins.
4. **Remotes.** Push and pull with the person's own credentials; a pull is
   installed through the gate.
5. **Porting.** A port brief in `AGENTS.md` (the split report first, then
   the app), and Anvil opening the new app's repository with the old
   project linked into it. Needs steps 1 and 3.
6. **Outside agents.** `AGENTS.md` and `CLAUDE.md` in every repository and
   export; export, check and install from the command line (these can come
   as early as step 1); direct git from gohort later, if wanted.

## Stored change

Two optional fields on the app record: that it is repository-backed (and
where), and the commit it was installed from. Apps without them are as
today until changed. No migration.
