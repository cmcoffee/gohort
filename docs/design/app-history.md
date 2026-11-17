# One history per app, for Builder and Anvil

Status: proposed (2026-11-17). Nothing built. Step 1 below (Anvil saying
when an install leaves the app broken) shipped first, on its own, in 0.9.510.

Two stages. **Stage 1** gives every app a repository that records each save,
and has Anvil work in it; the app as stored stays what is served. **Stage 2**
makes the repository what the app is: it is served from the repository's
live branch, and the store keeps only what is not code. Stage 1 is built and
lived with first; nothing in it is thrown away by stage 2, which needs the
same repository and history, and what stage 1 measures (repository sizes,
the time a commit takes) decides stage 2's details.

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

**Every save of an app is a commit in that app's own repository**, whoever
made it and however.

- The repository is the app's folder (the format `WriteAppFolder` writes:
  app.json, page.html, data/, actions/, lib/, assets/, NOTES.md,
  checks.json), at `<owner's workspaces>/<owner>/.apps/<slug>/`, outside any
  chat workspace.
- `SaveAppSpecAs` is the one place every save passes through (Builder's
  edits, an install, a revert, a maintainer, settings, sharing), so the
  commit is made there: the saved app is written into the repository and
  committed when its files changed. A change that does not touch the files
  (sharing, who maintains it) makes no commit.
- The message is the save's change note, written as a commit message since
  0.9.498. Saves with none get one from what changed ("Update data source
  now").
- The author is the person (`user <user@users.gohort.invalid>`, as Anvil's
  commits are now), with a `Changed-by: Builder` or `Changed-by: Anvil`
  trailer when an agent made the change, and the maintainer as author for a
  maintainer's save.
- The live app stays what is served. The repository is the record of saves,
  written after each one; nothing reads the app from it. That keeps every
  existing path, check and revision as it is, and nothing is migrated: an
  app's history begins at its next save.

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
- An existing Anvil app project keeps its own history and is offered "Use
  the app's history": its uncommitted work is kept, and the checkout is
  re-made from the app's repository.

**Later, separately:** Builder's folder workflow (`checkout`) uses the same
repository, and the revisions list is read from git. Neither is needed for
the above; stage 2 does both.

## Stage 2: the app is its repository

The app runs from its repository. Stage 1's repository becomes the source,
and the store keeps the app's record.

**What is where.**

| In the repository (the app's code) | In the store (the app's record) |
|---|---|
| app.json, page.html, data/, actions/, lib/, assets/, NOTES.md, checks.json | owner, slug, sharing (people, groups, public), maintainers, managers |
| | an administrator's approvals (publishing, store access, outbound) |
| | verify verdicts, settings' values, private_db, schedules' state |
| | the records people save, and their files |

**Serving.** The app is built from the `live` branch (the folder reader
`BuildAppFolder` already does this), cached by commit, so a request reads a
built app from memory and a new commit on `live` replaces it.

**Changing.** Builder and Anvil work in the repository, each on its own
branch or working copy, so their changes meet in git. Builder's in-place
edits (patch_html, replace_function) become an edit to the file and a
commit, on Builder's branch.

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
- A maintainer's change can be a branch the owner merges, instead of a save
  into the owner's app.
- Bundles (`.gohortapp`) can become git bundles of the repository, carrying
  its history.

**Moving apps over.** No bulk migration: an app moves the first time it is
saved or opened after stage 2 is on. Its current version is written into the
repository (already there since stage 1 for any app saved since), `live`
points at it, and its code fields in the store are cleared once the
repository serves it. An app not yet moved is served from the store exactly
as now, so the two kinds run side by side until every app has been touched.
This is a migration, and is said as one: the code of every app ends up
somewhere new.

**What stage 2 costs on top of stage 1.**

- **git is required for every app**, not only Anvil, on every deployment
  (Windows included). Without it the store keeps serving (an app is moved
  only where git is present), and that has to be said on the admin page. A
  Go git library instead is a new dependency, vetted before it is chosen.
- **Every path that changes code moves to git**: app_def's edits, revisions,
  revert, maintainers' saves, bundles. The save gates do not change; what
  they guard does.
- **A request must not wait on git.** The cache by commit is required, not
  an optimisation.

### Stage 2 questions

5. **git library or the git binary?** The binary is what Anvil uses now;
   a library removes the dependency at the cost of vetting one.
6. **Builder's branch.** One per app (proposed), or one per Builder turn?
7. **Maintainers.** Keep saving into the owner's live app (as now), or
   propose a branch for the owner to merge?

## What it costs

- **Disk.** One repository per app. Assets (images, sounds, now scripts and
  WebAssembly) are stored at each version that changes them. An app whose
  repository passes a size worth noticing (say 200 MB) is said on its row;
  pruning old history is a separate decision.
- **Time per save.** Writing the folder and a commit: tens of milliseconds
  for a typical app, run after the save and never blocking it (a failed
  commit is logged and the save stands).
- **Shared apps.** The repository is the owner's. A maintainer's save is
  committed in it under the maintainer's name; a maintainer opening the app
  in Anvil clones the owner's repository and installs through the same
  maintainer rules as today.
- **Privacy.** The repository holds what the app already holds, under the
  owner's workspace, which only the owner's sessions reach.

## Questions to settle before building

1. **Where the repository lives.** Under the owner's workspaces root
   (proposed), or beside the app's assets?
2. **Anvil's conflict.** Merge automatically when the changes do not
   overlap (proposed), or always ask?
3. **Existing Anvil app projects.** Offer "Use the app's history"
   (proposed), or switch them over automatically on next open?
4. **Builder's in-place edits** (patch_html, replace_function) commit like
   any save (proposed). Is a commit per small edit too many? Grouping one
   Builder turn's edits into one commit is possible, at the cost of a
   commit that lands when the turn ends rather than with the edit.

## Steps

Stage 1:

1. Anvil says when an install leaves the app broken. **Done (0.9.510).**
2. The app repository and a commit on every save, behind the save
   (`core`, with the folder writer from orchestrate).
3. Anvil's app projects open the app's repository; Install fast-forwards;
   another save since is fetched and merged, or shown as a conflict.
4. "Use the app's history" for existing app projects.
5. Measure: repository sizes and commit times across real apps.

Stage 2:

6. Serve from `live`, cached by commit, for an app that has moved; the store
   serves the rest.
7. Installs merge into `live` through the save gates; Builder's edits commit
   on its branch.
8. Revisions, revert and Preview from git; bundles as git bundles.
9. Move apps on first save or open; clear their code fields once served
   from the repository.

Each stage on its own branch: stage 1 changes how Anvil opens an app and
adds a repository per app; stage 2 moves where every app's code lives.
