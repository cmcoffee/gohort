# Code mode: one round that calls several tools

Status: built (2026-11-27), on for Anvil, waiting on the measure. Where the
build differs from the plan below:

- **A script is stopped after 2 minutes** rather than moving to the
  background: it only reads, so one still running is stuck.
- **The script runs in an empty folder of its own** under the person's
  workspace, with the network closed (`netgate.WithWorkspaceNetwork`), so it
  has no files and no network but the tools. That is what lets it say it
  only reads (`CapRead`), and so be offered in plan mode.
- **The hook gets a session made for the script**, holding only the way to
  its tools, never the turn's session.
- **The card is Anvil's step line** ("In a script: read 3 files, searched
  once"), read from the count of calls the result ends with. The script
  itself is in the step's arguments.
- **Measure counts rounds** now (`evalTaskResult.Rounds`, one additive field,
  zero on older runs), and has a setting without `tool_script`
  ("Worker, with the check, without tool_script") to compare against.

## Why

Each round Anvil's assistant takes is one call to the worker model. Gohort
already runs several tool calls from one round in parallel
(`core/agent_loop.go`, "Second pass: execute approved tool calls in
parallel"), so independent reads can already share a round. What a round
cannot do is a chain: search, then read what the search found, then read
the file that one imports. Each link is another round.

Measured on the live log (about 10 days, 2026-11-17 to 11-27, Anvil's file
tools as the marker):

| | |
|---|---|
| Rounds that called a tool | 132 |
| ... that only read (`read_file`, `list_files`, `search_code`, `git`) | 81 |
| ... with one call | 93 (70%) |
| Runs of 3 or more read-only rounds in a row | 10 (longest 10) |
| Time per round on the worker | median 4.2s, 90% under 25s |

So most of Anvil's rounds are looking around, mostly one call at a time,
and the long runs of them are chains. That is the part code mode goes at.
The gain is real but modest at today's volume: tens of rounds a week, a few
seconds to half a minute each, plus the context the intermediate output
takes up (a script can read seven files and return the twelve lines that
matter).

## What opencode does (read in its v2 tree)

- One extra tool, `execute`, taking a JavaScript program. On by default.
- The program runs in an in-process interpreter for a JS subset (no
  filesystem, timers or imports). The other tools are `tools.<name>(input)`
  functions, listed to the model as generated TypeScript signatures in the
  system prompt.
- An inner call goes through the normal tool path, permission prompts
  included, shown on the `execute` row.
- The model gets the program's return value and console output, not each
  inner result. The page shows one card with a row per inner call.
- Read, shell, edit and write stay direct tools by default. Only under an
  experimental flag do they move inside.

## The proposal

### One tool: `tool_script`

```
tool_script(code)
```

A short Python program, run in the same sandbox as `run` (bwrap on Linux,
Seatbelt on macOS: no network, the project folder only), with the
`gohort` helper the sandbox already gives app scripts. Inside it, the
allowed tools are `gohort.call_tool(name, **args)`, which already exists
(`core/sandbox_hook.go` `handleTool`). What the program prints is the
result, capped as `run`'s output is.

```python
hits = gohort.call_tool("search_code", pattern="func revertEdit", path_glob="*.go")
for path in sorted({h.split(":")[0] for h in hits.splitlines()})[:5]:
    print(path)
    print(gohort.call_tool("read_file", path=path, start_line=1, line_count=40))
```

Python and not an embedded JS interpreter: the sandbox, the hook, the helper
and `call_tool` are all built and in use by app scripts; an interpreter
would be a new third-party dependency to vet and a second way to run code.

### Only tools that read go inside

The tools a script may call are the turn's tools that never ask and change
nothing: for Anvil `read_file`, `list_files`, `search_code` and `git`
(read-only already). Edits, `run`, `try_page` and anything that asks stay direct calls
only.

This keeps the rule `ToolSession.CallTool` was written with: "a tool in a
chat cannot use the hook to call further tools behind its own
confirmation". A script has nobody to ask, and an edit inside a script
would lose its card, its Revert, and the halt-on-undo check. Writes are
also not where the rounds go (above).

The tools stay direct as well. A model that does not take to scripts loses
nothing, and a one-file read is still one plain call.

### Where it lives

- `core/toolscript` (core itself is at its file ceiling): `New(tools...)`
  builds the tool from a set of the turn's tools, and sets the session's `CallTool` to run them through
  the same handler a direct call reaches, checked against the set. The
  inner call is held to the turn's tool scope (plan mode, phase allowlist),
  as a direct call is.
- `apps/anvil`: turns it on with its read tools. Nothing else opts in until
  a measure says it helps.

### What the model is shown

The tool's description lists the tools it may call, each with its
arguments, in one line apiece (generated from their parameter schemas), and
says: print what you need, nothing carries over between calls, a failed
call raises and the message says why. About 400 tokens, which is the cost
of having it in every turn's schema.

### What comes back, and the card

- To the model: what the script printed, then a line per failed inner call.
  Not every inner result: that is the point.
- In the conversation: one card for the call, with the script behind a
  toggle and a row per inner call (tool, its main argument, how big its
  result was), using Anvil's existing step summary.

### Limits

- 2 minutes.
- At most 50 inner calls per script, and the printed output capped as
  `run`'s is. Over either, the script stops and says which limit it hit.

## Measuring it

Measure's Anvil tasks run with and without `tool_script` on the worker
model: rounds per task, time per task, pass rate. It stays on only if
rounds drop without the pass rate falling. A model that writes broken
scripts costs a round per failure, so this is not a given for a 27B model.

## Stored changes

None for the tool. Measure's per-task record gains `rounds` (additive,
omitted when zero); no migration.

## Not now

- Edits, `run` or anything that asks inside a script.
- Moving tools out of the direct list to save schema tokens (opencode's
  default). Revisit if the measure shows models use the script for reads.
- Other agents than Anvil. The core piece is generic; turning it on
  elsewhere is a separate decision with its own measure.
- `fetch` inside the script. App scripts have it; Anvil's sandbox has no
  network.

## Settled (2026-11-27)

1. **The name** is `tool_script`.
2. **Only tools that read go inside.** Edits, `run` and anything that asks
   stay direct.
3. **Built in core** as a generic piece, **turned on for Anvil only**.
4. **Kept only if the measure shows fewer rounds** at the same pass rate on
   the worker model.
