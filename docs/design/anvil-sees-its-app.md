# Anvil sees the app it is building

Status: built at desktop width (2026-11-27). Where the build differs from
the plan below:

- **`try_page` takes `steps` and `look_for`, no `width` yet** (desktop only,
  as settled).
- **Screenshots on cards are kept in memory**, the newest 40, served by
  Anvil (`api/shot/{id}`) to the person they were taken for. Nothing is
  stored; after a restart the card says the picture is gone.
- **The preview for one load is `appdraft.CheckPreview`**, set by the
  custom-apps host, so orchestrate and Anvil reach it the same way.

## Why

Anvil's assistant changes an app's page and never sees it. `preview` opens
the working copy in a pane for the person; the assistant gets the address
and nothing else. So a page that throws on load, a section that renders
empty, a Save button that does nothing, or a layout that breaks at phone
width is found by the person, or later by someone using the installed app
(`app_runs`), and reported back. The loop a coding agent needs (change,
look, fix) is missing its middle for exactly the part of an app people use.

The page checks in `checks.json` do not close it either: on a working copy
`app_checks` skips them ("page checks run once the version is saved"), so
the assistant writes page checks it cannot run until someone installs.

## What exists (verified in code)

| Piece | Where | What it gives |
|---|---|---|
| A headless page load as a given user | `CheckPageAsUser` (`core/page_check.go`), `CheckPage` (`tools/browser/pagecheck.go`) | console errors, uncaught exceptions, failed and pending requests, body and frame text, a probe's result, a full-page JPEG |
| Steps a person would take | `appcheck.Step` (`fill`/`with`, `click`, `choose`/`option`, `see`, `not_see`, `in`, `rows`), run from Go in every frame (`tools/browser/check_steps.go`) | driving a flow by what is on screen, sandboxed html sections included |
| A preview of a working copy | `customapps.StartAppPreview` (`apps/customapps/preview.go`) | the built folder served at the app's address to one person, on a copy of their records |
| Check rules on a run | `appscript.BeginCheckScratch`, the check cookie | no outbound writes, call_tool, run_agent, run_pipeline or notifications |
| Builder's draft checks | `chatTurn.appCheckCookies` (`apps/orchestrate/app_draft.go`) | page checks run on an unsaved draft through its preview |
| Putting a picture in front of the model | `ToolSession.AppendViewImageAs` (as `workspace view_image show_me` does) | a vision model sees it on its next step; a model that reads no images gets a description through the image gate |

Almost everything is built. What is missing is the door from an Anvil turn
to it.

## The proposal

### 1. `app_checks` runs page checks on the working copy

When the assistant calls `app_checks`, page checks run too, against the
built working copy, as Builder already runs them on a draft. Each runs on
its own scratch records (its `records`, as at install), never the person's.

The check that runs by itself as the turn ends stays as it is (install
check and script checks): a page check is 5 to 20 seconds, and every turn
paying that is the wrong default. The assistant runs them when it has
changed the page.

### 2. A new tool that looks at the app: `try_page`

```
try_page(steps?, width?, look_for?)
```

- Builds the working copy and loads its page, as the person, in the
  headless browser.
- `steps` (optional): the same steps a page check takes, run in order. The
  first that does not hold stops the run and says what the page showed.
- `width` (optional): `desktop` (default) or `phone` (390 px).
- `look_for` (optional): a question about the screenshot ("is the table
  empty", "does the header overlap the form").

It returns, as text: errors (uncaught, console, failed requests), what each
section shows (trimmed), how the steps went, and whether the page looked
blank. The screenshot goes two places: a card in the conversation, so the
person sees what the assistant saw, and in front of the assistant on its
next step (`AppendViewImageAs`). For a model that reads no images, the
image gate turns it into a description, and `look_for` is the question that
description answers.

Records: each call runs on a fresh copy of the person's records, thrown
away when the load ends, held to check rules. A Delete the assistant clicks
deletes nothing real, whichever records the person's own preview uses. This
is deliberate: the assistant should see the app as the person sees it, with
their data, without being able to change it.

Each call is one load. A flow is one call with steps, not a tab kept open
between calls. See "Not now".

### 3. A one-shot preview for the assistant

Today a person has one preview per app (`startAppPreview` keys by editor
and slug): starting a second one updates or ENDS the first. Builder's
draft checks go through that, which is fine because the draft is the same
build the person previews. For Anvil it is not fine: if the person chose
real records for their preview, a check from the assistant would end it
and replace it with a scratch one.

So customapps gains a preview made for one load: its own token, a fresh
copy of records, not counted as the person's preview, ended when the load
returns. Something like:

```go
// in apps/customapps/preview.go
func CheckPreview(editor string, spec AppSpec) (cookie PageCheckCookie, end func(), err error)
```

Both `app_checks` and `try_page` use it.

### Where the code goes

- `apps/customapps`: `CheckPreview`.
- `apps/orchestrate`: a `CheckAppFolder` option to run page checks through
  it; a small `TryAppFolder(ctx, user, dir, steps, width)` returning the
  report.
- `core/page_check.go` and `tools/browser`: a viewport for `phone`. This is
  the one change to a shared signature: `BrowserCheckPage` takes a
  position-only argument list today, so it grows an options struct.
  `CheckPageAsUser`'s existing callers keep their behaviour.
- `apps/anvil`: the `try_page` tool, its card, the `app_checks` change.

Nothing goes into `core/ui`. The card uses a block type that already exists
if there is one for an image; otherwise Anvil registers a block renderer
from its own package.

### Asking, plan mode, progress

- No approval card: nothing real changes (scratch records, check rules),
  the same reason `app_checks` asks nothing.
- Allowed in plan mode, since it only looks: the assistant can look at the
  app as it stands before proposing a change.
- A load is 5 to 20 seconds, so the card is alive while it runs (the
  check's card already is), and stopping the turn ends it.

## Stored changes

None. No new records, no new fields, no migration. One new tool (with
`steps`, `width` and `look_for`) and one tool's behaviour
(`app_checks` running page checks).

## Not now

- **A browser tab kept between calls** (opencode's browser plugin: tabs,
  click by element ref, evaluate JS, network, traces, heap). Each call
  being one load keeps cleanup, the shared Chromium, and concurrent turns
  simple, and steps by visible label already drive a flow. Revisit if
  one-shot loads with steps prove too coarse in use.
- **Arbitrary JS from the assistant.** Steps and the fixed probe cover
  looking and doing. A script would run in the page as the person.
- **Other projects than apps.** A web project in a checkout has no
  gohort-served address to load; that needs a dev server (opencode's item 5,
  background commands) first.
- **An eval task for apps.** Measure has only code tasks. Worth adding once
  this exists, so a change to it can be scored.

## Settled (2026-11-27)

1. **The name** is `try_page`.
2. **Page checks run only when `app_checks` is called.** The turn's end
   still checks only that the app would install.
3. **`try_page` uses a fresh copy of the person's records**, thrown away
   after the load.
4. **Desktop first.** Phone width, and the `BrowserCheckPage` options struct
   it needs, come later.

## Build order

1. `CheckPreview` and `app_checks` running page checks on the working copy.
   Smallest, and it makes the existing `checks.json` useful in Anvil.
2. `try_page` at desktop width: report, card, screenshot to the assistant.
3. Phone width.
