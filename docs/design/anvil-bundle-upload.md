# Anvil: a user brings a repository as a git bundle

Status: **stages 1 and 2 built** (2026-11-16), with the recommended answers
to the open questions: an administrator setting, off by default; 500 MB and
5 GB; a patch out; newer versions later; built before the move to `apps/`.
Stage 3 is not built.

Upgrade note needed: yes. A new way to add a project, new project fields, and
two or three administrator settings. **No migration**: every new field
defaults to empty, existing projects read as they do now, and nothing changes
for users until the administrator turns it on (Q1).

## Why a bundle, when users may not add repositories

`anvil-development.md` settled that bringing anything from outside gohort in
is an administrator's: a folder on this host, or a repository from any
address. The reason it gives is that **the server's own files and its network**
are at stake. A folder path reaches the server's files; an address makes the
server reach out, from inside its own network, with its own keys.

A bundle is neither. It is a file the user already has, made on their own
machine with `git bundle create`. The server reaches nowhere to get it and
opens no path the user chose. What is left is the content itself: untrusted
code and git data, which is what every checkout already holds, and what the
sandbox is for.

The case it serves: code on a machine gohort cannot reach (a laptop, a
private network, an employer's git host) that the user wants to work on with
gohort's models, and then carry the changes back.

**That needs a way out as well as a way in.** Bundle in, patch out. Without
the way out, an upload is half a feature.

## The way in

1. **New project → Upload a bundle.** The dialog says how to make one
   (`git bundle create project.bundle --all`, or a branch name instead of
   `--all`) and shows the size limit before anything is sent.
2. **The upload** uses `ui.UploadPanel`, the framework's upload surface (an
   XHR with byte progress, so a large file shows how far along it is). It
   streams to the user's own Anvil folder (`<workspaces>/<user>/.anvil/`),
   never to a temporary directory nobody sized, with a byte cap enforced
   while streaming (the way the file store and Servitor's evidence upload
   refuse an oversize request, saying what the limit is).
3. **The header is read before anything else.** A bundle starts with a
   signature line (`# v2 git bundle` or `# v3 git bundle`), then the refs it
   carries, then a pack. Anvil reads only the header:
   - not a bundle: refused, saying so;
   - **prerequisites** (lines starting with `-`, an incremental bundle that
     needs commits the server does not have): refused for a new project,
     saying to make it with `--all` or a branch name;
   - the refs become the branch picker, defaulting to the branch `HEAD`
     names.
4. **The clone runs in the sandbox**, reaching only the uploaded file and
   the new checkout, with every transport but git's file one refused (so it
   reaches no network whether or not the deployment has closed the
   sandbox's):
   `git clone --single-branch --branch <b> -- <bundle> <checkout>`, with
   Anvil's `gitSafety` settings, `protocol.allow=never` plus
   `protocol.file.allow=always` (a bundle is read over git's file
   transport; the https-only transport a user's clone uses refuses it,
   checked), and no host git config (`GIT_CONFIG_GLOBAL=/dev/null`,
   `GIT_CONFIG_NOSYSTEM=1`), so a filter the server's own git config names
   never runs. git-lfs is the one that matters: its smudge filter reads an
   address from the repository's own `.lfsconfig` and fetches from it.
5. **After the clone**: the `origin` remote is removed (it would name the
   upload's path), and the uploaded file is deleted. The project is a
   checkout like any other: `Kind` clone, a new `Source: "bundle"`, the
   bundle's file name, the branch, the upload's time and size. It has its
   full history, so "fetch full history" is not offered.

**It survives the page.** The project carries its state (uploading,
unpacking, ready, failed, with the reason) and the project card shows it with
the house spinner and a size count ("1.2 GB unpacked, 40s"). A page arriving
mid-unpack rejoins it, and one arriving after a failure says what failed.

## The way out: Download changes

A **Download changes** button on the project's changes panel. It downloads
one patch file, `<project>-<date>.patch`: everything that differs from the
last commit, as `git diff --binary HEAD` produces it, with untracked files as
new-file diffs (the panel already renders those). At home: `git apply
<project>-<date>.patch`.

A patch, not a bundle, because Anvil does not commit (deliberately: commits
are the user's). A patch is exactly "what differs", which is what the user
wants back.

It works for every project that is a git repository, not only uploaded ones:
an administrator's checkout and a directory project get it too. It is useful
on its own, so it is built first.

## Bringing a newer version (later stage)

**Upload a newer bundle** on an existing bundle project: a full bundle, or an
incremental one whose prerequisites the checkout has. It is fetched in the
sandbox into `refs/anvil/incoming`, then the checkout is moved **forward
only**: when the new commit descends from the checkout's, and the files it
changes are not ones with changes of the user's. Otherwise it is refused,
saying to download the changes first and start a new project from the newer
bundle. No merge, no conflict markers.

## Limits

| Limit | Default (Q2) | Enforced |
|---|---|---|
| Upload size | 500 MB | while streaming; refused with the limit named |
| A user's Anvil disk (all their checkouts) | 5 GB | before the upload is taken (what is left is shown), and while unpacking |
| Unpack time | 30 minutes, as a clone today | the clone's timeout |

**While unpacking**, because a small bundle can unpack large: the checkout's
size is measured on the progress tick (every two seconds), and the clone is
stopped and removed when it passes what is left of the user's budget. The
same tick gives the spinner its "1.2 GB unpacked".

## What could go wrong, and what stops it

- **A bundle runs nothing by itself.** It holds refs and a pack: no config,
  no hooks. What runs is git's own handling of hostile data (unpacking,
  checkout), and that runs in the sandbox, over git's file transport alone,
  reaching only the upload and the checkout.
- **Content that points out of the checkout.** A symlink in it is not
  followed out of a checkout Anvil made: `resolvePath` already refuses it,
  and the changes panel reads the path it checked (fixed 2026-11-16).
- **Submodules** are not cloned (no `--recurse-submodules`); a
  `.gitmodules` file does nothing.
- **Filters** from the server's git config do not run (no host config, step
  4). The repository's own `.gitattributes` names filters, but a filter does
  nothing without config to define it.
- **Disk**: the limits above.
- **Afterwards**, the project is an ordinary checkout. The run tool and the
  check command stay sandboxed. Project commands (unsandboxed) stay an
  administrator's to define, as now.

## What it touches

Anvil only, plus nothing new in `core/`: `ui.UploadPanel` exists, the
sandbox runner exists (`runGitSandboxed`), and the size refusal follows the
file store's. New in Anvil: the upload and finalize handlers, the header
reader, the sandboxed bundle clone, the project fields and state, the
Download changes route, the dialog's third option, and the settings.

## Build stages

1. **Download changes**, for every git project. Useful alone.
2. **Upload a bundle**: the upload, the header, the sandboxed clone, the
   state on the card, the limits, the administrator's switch.
3. **A newer version**, forward only.

## Open questions

**Q1. Who gets it, on or off by default.** Proposal: an administrator
setting, "Users may bring a repository as a bundle", **off by default**,
since it changes the level table settled in `anvil-development.md`. The
argument for turning it on is above: neither reason for the rule applies to a
file the user already holds.

**Q2. The limits.** 500 MB per upload and 5 GB per user, both administrator
settings?

**Q3. The way out.** A patch (proposed), or also a bundle, which would mean
Anvil makes a commit, something it withholds today?

**Q4. Newer versions** in the first build, or later (proposed: later, stage 3).

**Q5. Timing.** Building it in private now does not touch the agent's tools,
which are what the move to `apps/` waits on to settle. Build now, or after
the move?
