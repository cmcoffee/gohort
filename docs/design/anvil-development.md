# Anvil: who works on what, a page built around the project, and an agent that checks its work

Status: proposed (2026-11-15). Not built. Builds on
`apps-as-repositories.md`.

## Why

Anvil works, but three things hold it back:

- **It cannot reliably check its own work.** The run tool's sandbox has no
  network (the deployment default), and nothing keeps a package cache. On a
  fresh checkout `go build`, `npm install` or `pip install` cannot download
  anything, so the agent changes code it cannot build or test. A coding
  agent is only as good as the loop it gets: change, build, read the
  failure, fix.
- **We cannot tell whether a change to it helps.** There is no set of tasks
  to score it on, so every prompt or tool change is a guess.
- **The page is built around a chat, not a project.** Adding a project sits
  in an overflow menu; the project is a field beside the message box; what
  the agent is doing shows as it finishes rather than as it goes.

## Who works on what

Two levels, by what a person may bring into gohort:

| | A user | An administrator |
|---|---|---|
| An app they own or maintain (its repository) | yes | yes |
| A folder on this host | no | yes |
| An outside repository (GitHub, GitLab, any https) | no | yes |
| A new, empty repository of their own | yes | yes |
| Commit their work in a checkout Anvil made | yes | yes |
| Publish (push) a repository they started or brought as a bundle, to a public https address | yes, unless an administrator turns it off | yes, any address |
| Porting an outside project into an app | no | yes |
| The project's check command (sandboxed) | yes, on their app | yes |
| Project commands (unsandboxed, on the host) | no | yes |
| A project an administrator added, shared with them | read-only, as today | yes |

A new repository brings nothing in, so anyone may start one. Publishing
sends one out: only the person presses Push (the assistant never commits or
pushes), and the push runs from a bare repository the server makes from a
bundle of the history, never in the checkout, whose git config the run tool
can write. A user's address is held to a public host as a clone's is.

A user maintains apps that live in gohort. Bringing anything from outside
gohort in (a folder on this machine, a repository from elsewhere, a port) is
an administrator's, because that is where the server's own files, its
network and its trust are at stake. A maintainer's limits on an app (no
scripts, page code, sharing or settings) still hold at install, whatever
Anvil lets them edit.

**What this changes today.** A user can add an outside repository now.
Under this, they no longer can. Their existing checkouts keep opening as
they are (read at load, no migration) and can be removed; no new ones are
offered. Until apps have repositories (`apps-as-repositories.md`, steps 1 to
3) a user has nothing to open, so Anvil is an administrator's page until
then.

## The page, built around the project

- **Home is the projects.** Opening Anvil lists what you can work on: your
  apps, and for an administrator the folders and repositories added. Each
  shows its branch, how many files differ from the last commit, and its
  last session. **New project** is the page's main button, not an entry
  in a site menu, and its own menu offers what your level allows: a new
  repository, a git bundle, one of your apps; for an administrator also a
  repository by its address or a folder on this host.
- **Opening a project is the workspace.** The conversation in the middle;
  the project's sessions beside it (sessions already belong to a project);
  the changes (what differs from the last commit, and every edit the agent
  made) in a panel that stays open and updates as it goes; the project's
  check and commands in reach. The project is chosen before the
  conversation, never a field in it.
- **Settings, notes and permissions** belong to the project and open from
  it, not from a menu.

## Seeing the work as it happens

What the agent is doing should be readable while it does it, the way a
terminal coding agent shows it:

- **Each step is one line**: "Read core/api.go (120 lines)", "Searched for
  parseConfig, 14 hits", "Edited core/api.go +3 -1", "Ran go test ./...,
  2 failed". A line opens to show what it covers: the lines read, the hits,
  the output.
- **An edit shows as a diff** the moment it lands, in the conversation and
  in the changes panel.
- **A step that runs shows it is alive**: a spinner, the elapsed time, the
  output so far, and Stop (the house rule for anything that takes time).
- **Asking first is a card in the conversation** saying exactly what will
  run and where, with Allow once, Allow for this project (by the command's
  verb, as the standing approvals already work), and Deny. A run waiting on
  an answer says so, rather than looking stuck.
- **A plan for multi-file work**: a short checklist the agent writes before
  a change across several files, ticked off as it goes. A wrong approach is
  caught before ten edits land.
- **A status line**: project, branch, files changed, model tier, how much of
  the context is used, and Stop.

The step lines, the diff block, the approval card and the plan are generic:
any agent app with tools gains them. They go into `core/ui`'s agent panel as
domain-agnostic pieces; what is Anvil's (the changes panel, the check) is
plugged in from Anvil's package through the registries.

## 1. Checking its own work

- **Downloads through gohort.** The run tool turns on the sandbox's relay:
  `go`, `npm`, `pip` and `git` reach the public internet through gohort's
  audited fetch, public addresses only, never the host's network. The
  sandbox stays closed otherwise.
- **A package cache per project**, kept outside the checkout and given to
  the run as a writable folder, with the toolchains pointed at it
  (`GOMODCACHE`, `GOCACHE`, `npm_config_cache`, `PIP_CACHE_DIR`,
  `CARGO_HOME`). Dependencies download once. Per project, so one project
  cannot plant something another one builds with.
- **A check command per project**: the build and tests, for example
  `go build ./... && go test ./...`. Unlike project commands it runs in the
  sandbox, so anyone who may edit the project may set it. Anvil suggests
  one from the tree (`go.mod`, `package.json` scripts, `pyproject.toml`)
  for the person to confirm.
- **Run automatically.** After a batch of edits, before the agent reports,
  Anvil runs the check and hands a failure back to the agent to fix. The
  agent cannot say it is done while the check is red; if it gives up, it
  says what is still failing. The page shows the check as a step, alive,
  with its time and output, and it can be stopped.
- **The run tool runs on the turn's context**, so a private turn's privacy
  and the agent's network ceiling apply to it. Today it runs on a blank
  context; harmless while the shell network is closed, wrong if a
  deployment opens it.

## 2. Measuring

- **A task set**: about ten small scratch repositories in the tree's test
  data, each with a request and a hidden check that decides pass or fail
  and that the agent never sees (a test file added after the run, or a
  command). A spread of work: fix a bug a failing test shows, add a flag,
  rename across several files, write a missing test, fix a broken build,
  a small feature, in Go, Python and JavaScript.
- **Run headless through the same agent loop** Anvil uses, with the real
  model and the real tools, each task in a fresh copy.
- **Scored per run**: pass or fail, steps taken, time, tokens, and whether
  the agent's own report matched the outcome (said it passed when it did
  not is its own failure).
- **Compared across settings**: Worker against the lead, and a change
  against the version before it. The report says which tasks moved.
- **Launched by an administrator** from Anvil's Measure page, in the
  server: the database has one writer, so a command-line run could not
  share it with the running server. It spends real model time, so it never
  runs on a schedule or on its own; one run at a time, with progress and
  Stop.

## Stored changes

- `Project.CheckCommand`, optional. Unset is today's behaviour.
- A cache folder per project on disk, created on first use.
- A user's existing outside-repository projects: read at load as today, no
  new ones. No migration.

## To settle

1. **The levels** above: in particular whether an administrator may share a
   folder or repository project with a user for editing, not only reading.
   Today sharing is read-only, and editing would let that user's run tool
   write the shared tree.
2. **Anvil before app repositories exist**: administrators only until then
   (above), or users keep adding outside repositories until their apps can
   be opened.
3. **The check's place**: always automatic after edits, or automatic with a
   per-project switch for a project whose tests are slow.

## Build order

1. **The task set**, and its first run as the baseline, so what follows
   is measured.
2. **The check**: relay, per-project cache, check command, run after edits,
   run tool on the turn's context. Then the task set again.
3. **Levels**: who may add what, enforced on save and on open.
4. **The page**: projects as home, the workspace, New project.
5. **Seeing the work**: step lines, live diffs, approval cards, the plan,
   the status line, in the shared agent panel.
