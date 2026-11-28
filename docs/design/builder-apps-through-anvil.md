# Builder builds apps through Anvil

Status: not taken (2026-11-27). Measure found the two even (below), so
Builder keeps its own tools and gains Anvil's pieces instead.

## Why

Two agents edit gohort apps in two different ways:

- **Builder** changes the stored app through `app_def` actions (`create`,
  `update`, `patch_html`, `replace_function`), each saved through the app's
  gates: refusals, script checks, a load in a real browser that rolls a bad
  edit back, drafts until committed.
- **Anvil** edits the app's folder as files, in the app's own history repo,
  with a coding agent's tools, and installs through the same `app_def` save
  path (`InstallAppFolder`).

Every improvement to how an agent works lands on one of them. On 2026-11-27
alone Anvil gained tolerant edit matching, recorded and revertable changes
from `run`, `try_page` (the agent looks at its own page), long commands
moving to the background, and `tool_script` (a chain of reads in one round);
Builder got none of them. Asked to build the same call center app that day
on the same model:

- Anvil wrote it in files, ran its 7 checks, fixed what failed (a check it
  had written wrong, a search that needed records seeded, the page's
  layout), looked at the page with `try_page`, and finished at 7 of 7 in
  about 22 minutes.
- Builder created the app in one `app_def create`, then spent its next
  rounds on a quoting bug and an action called by the wrong name; one
  `patch_html` broke the page and was rolled back by the browser check.
  It then fixed a crashing action that `verify` found and looked at its
  page's screenshot, and stalled: three rounds of three to five minutes'
  thinking returned no text and no tool call, and it never reported back
  (stopped at 24 minutes, 20 tool calls).

One way of doing the work means one place to improve it.

## What exists

| Piece | Where |
|---|---|
| Each app's history repo, the folder both agents read | `apps/orchestrate/app_history.go` (`appRepoDir`), `<workspaces>/<owner>/.apps/<slug>` |
| An Anvil project for an app, opened on that repo | `apps/anvil/app_project.go` (`Source: sourceApp`, `AppSlug`) |
| Installing a folder through every gate Builder's saves pass | `orchestrate.InstallAppFolder`, `CheckAppFolder` |
| Anvil's loop run with no page open | `apps/anvil/eval.go` (`runEvalAgent`) |
| Builder's app mode, and app jobs sent to it | `builder_app_mode.go` (intake App; `builderJobIsApp`) |
| A measure of both on the same app tasks | Measure: "Worker, with the check" and "Builder, app tasks" |

## The proposal

### Builder hands app work to Anvil

When a Builder turn is about an app (intake App, or a sent job judged an app
job), its app work runs as an **Anvil task**:

1. Builder works out what is wanted, as now (intake, questions, which app).
2. It calls one tool, `build_app` (name to settle), with the app (or a name
   for a new one) and the request in its own words.
3. That opens the app's **Builder project** in Anvil: an Anvil project on the
   app's history repo, marked as Builder's and kept out of the person's
   project list. A new app starts from `start_app` in a fresh one.
4. Anvil's loop runs the request there, with its own tools and checks, as an
   Anvil turn would: in the background, with progress on Builder's card.
5. When it ends, the folder is installed through `InstallAppFolder` as a
   **draft** (Builder's change stays a draft until committed, as today), and
   Builder gets back what Anvil said, what it changed, how the app's checks
   went, and a link to open the project in Anvil to see the diffs or revert
   an edit.

Builder's own `app_def` edit actions retire for apps once this is on; its
save and check code stays, since Anvil's installs go through it.

### What stays Builder's

Everything that is not an app: agents, tools, skills, pipelines, machines,
connections and credentials, schedules. And an app's parts that are not its
folder: the agents and pipelines it owns, its file stores, its sharing.

### Tools: not now

A tool is usually one script and its definition, and carries the strictest
gates (credentials, approval, admin-only commands at `tool_def`'s save).
Anvil's strengths (many files, tests, revert) pay off less. Revisit once
apps have moved.

## Measure first

Before this is on, Measure runs the three app tasks both ways on the worker:
"Worker, with the check" (Anvil) and "Builder, app tasks" (Builder), each
more than once. It is switched on only if Anvil passes at least as many and
takes no longer in total.

**First runs (2026-11-27, throwaway instance, worker =
ThinkingCap-Qwen3.8-27B abliterated NVFP4 W4A4, unassisted):**

| setting | run | passed | rounds | steps | time |
|---|---|---|---|---|---|
| Worker, with the check, app tasks (Anvil) | 1 | 3 of 3 | 50 | 70 | 4m34s |
| | 2 | 2 of 3 | 70 | 79 | 9m06s |
| Builder, app tasks | 1 | 2 of 3 | 57 | 65 | 6m45s |
| | 2 | 3 of 3 | 59 | 69 | 7m32s |

Even: 5 of 6 each, 13m40s against 14m17s. Each failed "Add a field to an
app" once, the same way: the new field made required, which broke the app's
existing "adding a plant saves it" check.

**Second runs, with a from-scratch task added** ("Build an app from
scratch": the call log, from `start_app`'s bare folder, six hidden checks
including the page):

| task | Anvil 1 | Builder 1 | Anvil 2 | Builder 2 |
|---|---|---|---|---|
| Fix an app's numbers | pass 1m47s | pass 2m20s | pass 1m37s | pass 2m57s |
| Build an app from scratch | pass 11m44s | pass 15m31s (at the cap) | pass 11m01s | pass 3m01s |
| Add a field to an app | fail 1m19s | pass 1m54s | pass 1m27s | fail 2m07s |
| Add a data source to an app | pass 1m04s | pass 1m33s | pass 2m14s | pass 1m43s |
| total | 3 of 4, 15m54s | 4 of 4, 21m19s | 4 of 4, 16m19s | 3 of 4, 9m49s |

Both rounds together: **12 of 14 each**, Anvil 45m53s and Builder 45m25s.
From scratch, both built the call log both times: Anvil steadily (11 to 12
minutes), Builder unevenly (once working at the 15-minute cap, once in 3).

**Result: the gate is not met, and there is no performance case.** The
call center difference did not hold up unassisted and scored the same way.
What is left for this proposal is maintenance (one way of building apps),
which is a choice on its own, not a measured win. Taken instead: Anvil's
pieces come to Builder's tools, starting with its edit matcher, now shared
as `core/editmatch` and used by `patch_html` and script patches.

## Stored changes

- An Anvil project gains one field marking it as Builder's (kept out of the
  person's project list, its sessions Builder's). Additive; nothing to
  migrate: the app repos and folders already exist.

## Not now

- Tools through Anvil.
- Anvil's tools inside Builder's own chat (the other way to do this: one
  conversation instead of a hand-off, but Anvil's cards and page would have
  to come into Builder's chat).
- Removing `app_def`'s edit actions from the code (they retire for apps when
  this is on; deleting them is later).

## To settle

1. **Hand-off or tools in Builder's chat.** Proposed: a hand-off to a hidden
   Anvil project, with a link to open it; the alternative is Anvil's tools
   inside Builder's own turn.
2. **The tool's name** (`build_app`, `anvil`, `change_app`).
3. **Installs.** Proposed: as a draft, committed as Builder's are today; the
   alternative is leaving it ready to install for the person.
4. **Which model runs it.** Proposed: the Anvil project's Reasoning (worker
   by default, a project setting the person can change); the alternative is
   Builder's own tier settings.
5. **The switch.** Proposed: on the branch until Measure passes, then on for
   every app job, with no setting.
