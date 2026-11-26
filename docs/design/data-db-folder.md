# Databases in their own folder, and an app's own file by default

Status: **built (0.9.707)**, see "As built" at the end. Part 1 **is a
migration**: database files are moved (renamed) once at startup. Nothing
inside any file is rewritten. Part 2 needs none.

## Where it stands

Everything gohort keeps lives under `data_dir` (default `<binary>/data`). The
database files sit at its top level, mixed in with the folders:

```
data/
  gohort.db                            the deployment: users, agents, settings, credentials
  gohort_vectors.db                    embeddings (vector_dir, default data_dir)
  gohort_repos.db                      cloned repo files (repo_dir, default data_dir)
  gohort_bundles.db                    evidence bundles (bundle_dir, default data_dir)
  gohort_customapp_<slug>_<hash>.db    one per app that opted into its own file
  app_assets/ app_record_files/ browser/ geocode/ images/ models/ tls/ workspaces/ ...
```

- Every path is built in `config.go`: `mainDBFile`, the three
  `loadPath(..., data_dir)` stores, and the private-file opener handed to
  core (`SetPrivateDBOpener`, `data_dir/gohort_<name>.db`).
- Only custom apps use the private-file opener today (`AppSpec.PrivateDB`,
  `OpenCustomAppDB`). No native app implements `PrivateDBApp`.
- An app gets its own file only when Builder passes `private_db=true` at
  create. The default is the shared custom-apps store in `gohort.db`.
- Deleting a private-file app drops its tables but leaves its file on disk.

Part 2 below turns one file per app into the usual case, so the top of
`data/` would keep filling up with `.db` files.

## Part 1: a `db` folder

```
data/
  db/
    gohort.db
    gohort_vectors.db      unless vector_dir is set
    gohort_repos.db        unless repo_dir is set
    gohort_bundles.db      unless bundle_dir is set
    apps/
      gohort_customapp_<slug>_<hash>.db
  app_assets/ browser/ images/ staging/ workspaces/ ...
```

- **`db`, not `appdata`.** `appdata` describes all of `data/` and is also
  the name of Windows' AppData. `db` says what is in it: kvlite files and
  nothing else.
- **The deployment's files go in too.** The folder is meant to be the one
  place to back up, put on another disk, or keep away from a NAS indexer,
  and `gohort.db` matters most for all three.
- **`apps/` under it.** Per-app files will number in the dozens; the
  deployment's handful stay easy to see, and cleaning up after deleted apps
  never touches them.
- **The three relocatable stores keep their settings.** `vector_dir`,
  `repo_dir` and `bundle_dir`, when set in `gohort.ini`, win as today, and a
  file there is not moved. Unset, they now default to `data/db/` instead of
  `data/`.
- **Bundle staging stays out of `db/`.** It is a transient extract, not a
  database: `bundle_dir/staging` when `bundle_dir` is set, else
  `data/staging` (where it is today).
- **No new setting.** `data_dir` already relocates everything, and the
  three store settings cover the files that want a fast local disk.

### The move, once, at startup

In `init_database`, before any database is opened:

1. For each file the old layout would have opened (`gohort.db`, the three
   stores whose setting is unset, and every `gohort_customapp_*.db`), if it
   is a regular file at its old path and nothing is at its new one, rename
   it there.
2. Leave a link at the old path pointing to the new one (relative, so a
   moved `data_dir` keeps working).
3. Log each move: `Moved data/gohort.db to data/db/gohort.db (a link stays at the old path).`

Edge cases:

- **Same disk, so the rename is atomic** and no data is copied. If the
  rename fails across devices (someone mounted a separate disk at
  `data/db`), gohort refuses to start and says so. It never copies a
  database.
- **The encryption lock moves with the file.** The padlock comes from the
  machine and the config file, never the file's path (`planPadlocks`).
- **Both present** (a file at the new path and a regular file at the old
  one): open the new one, leave the old one alone, and warn, naming both.
  This only happens after a rollback where no link could be made (below).
- **Idempotent.** On a later start everything is already in `db/` and the
  old paths are links, so nothing happens.

### Going back to an older version

An older binary opens `data/gohort.db`. Through the link it reaches the
moved file and works as before. Without the link it would find nothing and
start as a fresh, empty install, asking for setup. That is the risk this
design is built around.

- **Links stay for a few releases**, then a later version removes them.
  Proposed: until 0.10.
- **Where a link cannot be made** (Windows without the privilege), the move
  is skipped, the old layout is kept, and the log says why. A desktop
  install on Windows simply stays as it is.

### What else reads these paths

- **Servitor's data guard** (`gohort_data_guard.go`) refuses commands that
  name the data folder or the file name `gohort.db`. Both still match.
- **Backups and NAS jobs you run yourself** that name `data/gohort.db` keep
  working through the link, and should be pointed at `data/db/` before the
  links go.
- **`update_gohort.sh` and `publish.sh`** name no
  database file.

## Part 2: a new app gets its own file

- **At create**, when Builder does not pass `private_db`, the app gets its
  own file (`PrivateDB` true). An app made by import or copy is a new app
  here and gets one too. `private_db=false` still asks for the shared
  store.
- **Existing apps are untouched.** An app in the shared store stays there.
  Moving one onto its own file would mean copying its records, which is a
  migration this design leaves out. If wanted later, it is its own step: a
  "Move to its own file" on the app's ⚙, copying records, then switching.
- **Deleting an app removes its file**: close the cached handle, delete the
  file and the link at its old path. Today only its tables are dropped.
  This needs a way to close a handle in core without growing its export
  count (core is at its ceiling): a method on an existing type, or the
  opener's injected side.
- **Builder's guidance** (`app_def_tool.go`'s `private_db` description and
  the create path) is updated: own file by default, shared store on
  request.

### Cost

Each file is a bbolt database, opened on first use and kept open for the
life of the process (`OpenAppDB`), with its own memory map and file lock.
Dozens are fine. A deployment with many users each making many apps could
reach hundreds; if that shows up, idle handles can be closed and reopened
on use. Not built until it is measured.

## Stored-shape changes

- Part 1: file locations only. No record changes shape.
- Part 2: `AppSpec.PrivateDB` defaults to true for new apps. The field
  already exists; nothing stored is rewritten.

## Order

1. Part 1: the move, the links, the new paths, and tests for each edge case
   above, in a throwaway data folder.
2. Part 2: the default, delete removing the file, and Builder's guidance.

## Decided

1. **The links stay until 0.10.** A release before then removes nothing.
2. **Windows without the link privilege keeps the old layout.** The move
   is skipped and the log says why.
3. **Existing shared-store apps stay where they are.** No "Move to its own
   file" step.

## As built (0.9.707)

- **`db_folder.go`** (main package, so core's file count is unchanged):
  `dbRelocation.run` plans the moves (`gohort.db`, each store whose
  `[paths]` setting is unset, every other `gohort_*.db` into `db/apps/`),
  tries one link in `db/` first, then renames each file and links its old
  path relatively (`db/gohort.db`, `db/apps/<file>`). The layout it returns
  is what `init_database` opens.
- **If the link check fails** and something would move, nothing moves and
  the old layout is opened. A fresh folder needs no link and always uses
  `db/`.
- **A rename that fails stops the start** with the file still at its old
  path. A link that fails after a rename moves the file back and stops the
  start.
- **A regular file at both paths:** the one in `db/` is opened, and both
  are named in a warning on each start until one is removed.
- **Staging** stays at `bundle_dir/staging`, else `data/staging`.
- **`SetPrivateDBOpener` takes a second func**, the delete. Core's
  `removeAppDB` closes the cached handle, drops it from the cache and calls
  it. The delete removes the file in `db/apps/` and the link at its old
  path. This adds no new exported core symbol.
- **`AppSpec.OwnFileByDefault` and `AppSpec.RemovePrivateDB`** (methods,
  which the export ceiling does not count) check every account's apps for
  one stored with its records in the same file. A new app sharing a file
  with an app moved to another account stays in the shared store. A file
  any stored app uses, the deleted app included until its spec is gone, is
  never removed. With no account list, nothing is put on a file and nothing
  is removed.
- **The callers:**
  - Builder's create (and folder install, which goes through it), and a
    bundle import of a new slug, call `OwnFileByDefault`.
  - `deleteApp` calls `RemovePrivateDB` last. That covers the delete
    button, an account purge and the old side of a move. A move to the
    same slug keeps the file, because the moved app uses it.
  - An update from an app's origin keeps the live app's `PrivateDB` and
    `DataOwner`, both when it is merged and when it is taken whole.
- **Deleting a shared app on its own file** also deletes the copies its
  readers kept in that file. In the shared store those copies were left
  behind but could not be reached once the app was gone.
- **Tests:**
  - `db_folder_test.go`: fresh, move and link, idempotent, an older
    binary through the link, no links, both copies, a failed rename, a
    failed link, removing a file.
  - `TestAnAppsOwnFileIsMadeAndRemovedOnlyWhenNoOtherAppUsesIt` (core).
  - `TestAnAppsOwnFileMovesWithItAndGoesWhenItIsDeleted` (customapps,
    real files).
  - `TestANewAppGetsItsOwnFileUnlessAskedForTheSharedStore`.
  - Import and both update paths.
- **Mutants:** 23 of 23 caught.
