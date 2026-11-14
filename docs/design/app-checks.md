# App checks: tests that live with an app

Status: proposed (2026-11-13). Nothing built.

## Why

The goal is to use Builder on a gohort app the way one uses Claude Code on a
project: many small rounds of change, each one safe. What makes that safe in
a code project is its tests. They run after every change, and a change that
breaks something that used to work is caught by the change, not by the next
person to use it.

Builder already has the rest of the loop: small edits (`replace_function`,
`patch_html`, `update_section`), a folder workflow, a real browser loading
the page on every save, and revisions to go back to. What an app does not
have is a memory of what it is supposed to do. `verify` checks that the page
loads and the scripts print valid JSON. Nothing checks that "adding an item
puts it in the list" still holds after the fifth edit.

Checks are that memory: a short list stored on the app, written by Builder
as it adds each feature, run on every save.

## What exists to build on (verified in code)

| Piece | Where | Used for |
|---|---|---|
| A headless browser load as a given user | `CheckPageAsUser` (`core/page_check.go`), `BrowserCheckPage` (`tools/browser/pagecheck.go`) | Loading the page for a page check. It mints a session for the load and destroys it after. |
| A script run after the page settles | the `probeJS` argument; Rod's `Eval` awaits a returned promise | Driving the page: fill, click, wait, read |
| Page errors, console errors, failed requests | `PageCheckReport` | Failing a check on any of them |
| Running a data source or action | `app_def(action="test")`, `appscript` | Script checks |
| Revisions with a reason | `SaveAppSpecAs`, `revisions`, `revert` | Putting the previous version back when a save breaks a check |
| The outbound write gate | `OutboundWriteRefusal` (`core/authorizations.go`), the session's `ApproveWrite` | The hook a check run uses to refuse every write |

## Two kinds of check

### Script checks: the backend, without a browser

Run one data source or action with given records and inputs, and test what
it prints.

```json
{"name": "totals the week",
 "script": "weekly-total",
 "records": [{"day": "mon", "hours": 3}, {"day": "tue", "hours": 4}],
 "inputs": {"week": "2026-W46"},
 "expect": [{"path": "total", "equals": 7}, {"path": "days", "count": 2}]}
```

Fast (a sandboxed script run), and where most logic lives.

### Page checks: the app as a person uses it

A few steps in the browser, then what the page should show.

```json
{"name": "adding an item lists it",
 "steps": [
   {"fill": "Item", "with": "milk"},
   {"click": "Add"},
   {"see": "milk", "in": "Items"}
 ]}
```

| Step | Does |
|---|---|
| `fill: <label>, with: <text>` | Types into the field with that label (or placeholder) |
| `click: <text>` | Presses the button or link showing that text |
| `choose: <label>, option: <text>` | Picks an option in a select |
| `see: <text>` (optional `in: <section title>`) | Waits up to a few seconds for the text to appear |
| `not_see: <text>` | The text is absent |
| `count: <section title>, rows: <n>` | A table or list shows n rows |

Steps name what a person sees (labels, button text, section titles), never
CSS selectors or element ids, so a check survives a restyle and reads as a
description of the feature. Any page error, console error or failed request
during the steps fails the check.

The steps compile to one async script handed to the existing page load as its
probe, so nothing new is needed in the browser layer beyond waiting for text.

## Checks never touch real data

A check fills forms and presses buttons, which would write records. It runs
against **scratch records**: an empty store made for the run and dropped
after, seeded with the check's `records` when it gives some.

- The page load's session is marked as a check session. The app's record
  endpoints, and the scripts they run, read and write the scratch store for
  that session, never the owner's or anyone else's.
- **Every outbound write is refused**, a post, a send, a purchase. This is
  new, and stricter than an unattended run: today a write to a host the owner
  has already allowed goes out without asking, and a request through one of
  the owner's credentials is the credential's to govern and never reaches the
  write gate at all. A check run sets the session's `ApproveWrite` to refuse
  every write, and the credentialed dispatch refuses writes for a check run
  too. Reads go out as usual, so a check can exercise an app that looks
  something up. A check that needs a write fails, saying which, rather than
  doing it.
- A schedule never fires for a check.

## When checks run

- **`app_def(action="test")`** runs them all and reports each.
- **Every save that Builder (or a maintainer) makes** runs them after the
  existing page check:
  - A check that passed on the previous version and fails on this one
    **refuses the save**, naming the check and what it saw. The previous
    version keeps serving, as an in-place edit that breaks the page already
    does.
  - A check that was already failing is reported, not refused, so a fix can
    land.
  - A new check that fails on the save adding it is reported: it describes
    something not built yet, or a mistake in the check.
- A save with `confirm_rewrite` still runs them; refusing a confirmed rewrite
  for a broken check is the point.

Limits: 20 checks per app, 60 seconds for all of them on a save. Script
checks run in parallel; page checks run one after another on one browser.

## Where checks live

`AppSpec.Checks`, a new list on the app. No migration: an app without it has
no checks. In the app folder it is `checks.json`. Checks travel in an app
bundle (they are part of what the app is), and the records in them are test
data the author wrote, not anyone's records.

## Builder

- Its prompt gains one rule: **a feature is not done until it has a check**,
  and a bug fix adds a check that would have caught it. Builder writes the
  check in the same round as the feature.
- `get` shows the checks and their last result. `verify` includes them.
- A refused save is a normal step in the build, like a validator refusal:
  Builder reads what the check saw and fixes the change, not the check. It
  changes a check only when the feature itself changed on purpose, and says
  so.

**Maintainers** may add checks and change them. Removing one is the owner's,
so a maintainer cannot clear the check their change breaks.

## Testing

- Script checks: a passing and a failing `equals`, `count` and `contains`; a
  check naming a script the app does not have; records seeded and not leaked
  into the owner's store.
- Page checks on a fixture app: fill and click add a row in the scratch
  store and the check passes; the owner's records are unchanged after; a
  page error during the steps fails it.
- The save gate: a save breaking a passing check is refused and the previous
  version still serves; an already-failing check does not block; a new
  failing check is reported.
- Outbound writes are refused during a check: to an unknown host, to a host
  the owner always allows, and through a credential.

## Steps

1. `AppSpec.Checks`, script checks, `app_def(action="test")` running them.
2. Scratch records for check sessions.
3. Page checks: the step language compiled to the probe, waiting for text.
4. The save gate.
5. Builder's rule, `get` and `verify`, the folder's `checks.json`, bundles.

## Later

- A per-app record of what failed in real use (page errors from people's
  browsers, failed script runs), so "fix what has been failing" starts from
  evidence, and a failure there can become a check.
- The same idea for tools: a stored example call and what it returns, run on
  every change to the tool.
