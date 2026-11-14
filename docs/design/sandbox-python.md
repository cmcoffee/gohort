# The sandbox's Python: chosen, not inherited

Status: built (2026-11-13). On this host the choice is Python 3.12
(/usr/bin/python3.12, found by the search); a sandboxed script, and a Word,
Excel and PowerPoint export through the sandbox, run on it. macOS needed no
Seatbelt change: its profile allows process-exec everywhere.

## Why

Every Python gohort runs is whatever `python3` resolves to on the host's
PATH. On this deployment's host that is **Python 3.6.8**, end of life since
December 2021, while 3.11 and 3.12 are installed beside it in `/usr/bin`.
RHEL 8 and its relatives ship 3.6 as `python3` and keep it there for the OS's
own tools, so any deployment on that family is in the same place.

What 3.6 costs today:

- **Builder writes against a 3.6 note.** `SandboxPythonAuthoringNote`
  (`core/deps/python_version.go`) tells it to avoid `capture_output=`,
  `text=`, `dataclasses`, the walrus operator and more. Models write modern
  Python by habit, so this is a standing source of failed first runs.
- **Packages stop at 3.6.** Current releases of many libraries no longer
  install on it (pandas 2 needs 3.9). The packages already provisioned are
  old ones: `python_docx-0.8.11-py3.6`.
- **No security fixes.** The sandbox contains what a script can reach, but
  the interpreter running model-written code is unmaintained.

Python stays the language. The interpreter becomes a deliberate choice.

## Where Python runs today (verified in code)

| Where | What it runs |
|---|---|
| `core/deps/python_version.go` | `python3 --version`, for the authoring note |
| `core/deps/pydeps.go` | `python3 -m pip install --target <dir>`, host side, for the packages flows and exports declare |
| `core/sandbox_hook.go`, `core/sandbox/{curl,git,relay}_shim.go` | The shims in the sandbox bin dir (`fetch_url`, `fetch_via`, `browse_page`, `describe_file`, `curl`, `wget`, `git`): each starts `#!/usr/bin/env python3` |
| `core/event_monitor.go` | `RunSandboxedScript(ctx, "python3", ...)` for a monitor's format script |
| `core/flows/jobs.go` | Steps written as `python3 gen.py {in} {out}` (Word, Excel, PowerPoint) |
| `apps/orchestrate/app_patch_script.go` | `python3 -m py_compile`, host side, the syntax check on an app script |
| App, tool and pipeline scripts | Run in the sandbox shell, which finds `python3` on its PATH |
| `core/sandbox/container.go` | The container backend runs its image's own `python3`, which the image decides |

Two facts that shape the design:

- **The sandbox bin dir is first on the sandbox's PATH** (`sandboxShimBinDir`,
  `GohortBinMountPath`). A `python3` there wins for every script, shim and
  command inside, without touching any of them.
- **Installed packages are tied to one Python version.** `lxml`, which
  `python-docx` needs, is in the package directory as
  `*.cpython-36m-x86_64-linux-gnu.so`, and 3.12 cannot load it. Packages
  cannot be shared across versions.

And one about the newer interpreters here: **3.11 and 3.12 have no pip**
(`python3.12-pip` is not installed), but both carry `ensurepip` (pip 23.2.1)
and `venv`, so a pip can be made from what is already on disk, offline.

## The design

### 1. One chosen interpreter

At startup gohort picks the interpreter once, and everything that runs
Python asks for it (`deps.Python()`), host side and in the sandbox alike.

- **Default:** the newest `python3.N` on PATH with N >= 9, found by name
  (`python3.13`, `python3.12`, ... `python3.9`), then `python3` itself if it
  is 3.9 or newer. 3.9 is the floor because it is the oldest still common in
  distributions and what current packages commonly require.
- **Override:** `python = ` in gohort.ini's `[paths]`, a path or a name. It
  wins over the search, so an operator can pin a version or point at their
  own build.
- **Nothing 3.9 or newer, and no override:** gohort falls back to `python3`
  as today, says so once in the log and on System Status, and keeps the
  authoring note for that version. Nothing that works today stops working.

### 2. The sandbox's `python3` is the chosen one

The sandbox bin dir gets a `python3` (and `python`) entry pointing at the
chosen interpreter. Every shim's `#!/usr/bin/env python3`, every script, and
every `python3 ...` a tool or flow step runs then gets it, with no change to
any of them. `/usr` is already bound read-only into the sandbox, so an
interpreter under `/usr/bin` is reachable there as it is.

`RunSandboxedScript` callers and the host-side syntax check take
`deps.Python()` instead of the literal `"python3"`.

### 3. pip from the chosen interpreter, owned by gohort

pydeps installs with a pip that belongs to the chosen interpreter:

- When the chosen interpreter has pip, that one.
- When it does not (the 3.12 here), gohort makes a private virtual
  environment under its data directory
  (`<data_dir>/python/<version>/venv`, `python3.12 -m venv`, which
  bootstraps pip from `ensurepip` with no network) and runs that pip.
  Nothing is installed system-wide or into anyone's home directory.

Packages still install with `--target` into a directory the sandbox binds
read-only, with wheels only, as today. Only the pip running the install
changes.

### 4. One package directory per Python version

The package directory becomes per version: `_gohort_pydeps/py3.12/`, with
its own record of what is installed. Switching interpreters starts from an
empty directory, so the first run that needs a package installs it again
(python-docx for a Word export, say), host side, the way a fresh install
does today. The old 3.6 directory is left alone, so switching back costs
nothing; deleting it is the operator's choice.

### 5. Builder is told the real version

The authoring note follows the chosen interpreter, so on 3.9 or newer it is
empty and Builder writes ordinary modern Python. A note for an older
fallback stays as it is.

### 6. Seen where an operator looks

System Status gains a "Script Python" row: the version, its path, where it
came from (search, the ini, or the fallback), and a warning when it is the
fallback below 3.9.

## What does not change

- Python stays the scripting language, `bash` stays beside it, and an app's
  `language` field keeps its two values.
- The container backend keeps its image's Python: the image is the choice.
- The sandbox boundaries are as they are: no network, a read-only `/usr`,
  the gohort hook for everything outside.

## Risks

- **A script that relied on 3.6 behaviour.** Unlikely, since the note pushed
  scripts toward the common subset, but possible. The ini override (`python
  = python3`) puts things back at once.
- **The first package install after the switch** takes as long as a fresh
  install: python-docx and lxml are a few MB of wheels.
- **macOS:** Homebrew's `python3` is current, so the search usually picks it.
  The Seatbelt profile allows process-exec everywhere and reads filesystem
  wide, so an interpreter under `/opt/homebrew` runs as one under `/usr`
  does.

## Testing

- The search picks the newest `python3.N` with N >= 9 from a fake PATH;
  takes the ini's `python` over it; falls back to `python3` with a warning
  when nothing qualifies.
- The sandbox bin dir's `python3` resolves to the chosen interpreter, and a
  sandboxed `python3 -c 'import sys; print(sys.version_info[:2])'` prints it
  (bwrap, on the CI host).
- pydeps installs into the per-version directory with the chosen
  interpreter's pip, through the private venv when the interpreter has
  none, and never touches the system or a home directory.
- The authoring note is empty for 3.9 and newer.
- Then a live check on this host: a Word export (python-docx) and an app
  data source both run on 3.12.

## Steps

1. The chosen interpreter: the search, the ini override, `deps.Python()`,
   and every host-side caller on it.
2. `python3` in the sandbox bin dir.
3. pip from the chosen interpreter, the private venv, the per-version
   package directory.
4. The authoring note and the System Status row.
5. The macOS Seatbelt check, and the live check here.
