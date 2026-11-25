# Apps that feel like gohort's own

Status: **items 1, 2, 3 and 6 built (0.9.688, 0.9.689)**; 4 and 5 not built. Nothing here changes stored data or needs a
migration, except the last item, which applies to new apps only.

## The goal

An app made with Builder, Anvil or an import should work like an app compiled
into gohort: the same header, settings in the same place, remembering where
you were without asking, and opening as fast. Today it feels different, and
the difference comes from what surrounds the app's page, not from the page
itself.

What has to stay is the containment. An app's page HTML is its author's and is
shown to whoever opens the app, administrators included. On the gohort origin
its script could act as the viewer against every endpoint the viewer can
reach, so it runs in a sandboxed frame with no origin (`isolateAppHTML`, the
runtime's `isolatedFrame`). Everything below works within that, and none of it
gives the frame more reach.

## Two kinds of app page

The custom app tier (`custom-app-tier.md`) has two shapes, and they differ here:

- **Composed from primitives** (`Table`, `FormPanel`, `WorkbenchPanel`, ...).
  These render in the real page with the shared runtime, the same way a
  native app's do. They already look and load like gohort's own.
- **Written as HTML** (a Card or Frame holding the app's own page). This is
  what runs in the isolated frame, and it is where almost every difference
  below comes from.

So the first answer to "make apps feel native" is the existing rule: compose
from primitives wherever a primitive fits, and write HTML only for a shape
none of them can express.

## Side by side

| | Native app | Custom app (HTML page) |
|---|---|---|
| Where the page runs | The gohort page itself | A sandboxed frame with no origin |
| Header | `ui.Page` title and nav | The same `ui.Page` header, plus links the host adds |
| Settings | Each app's own page, wherever it put it | The ⚙ beside the app's name, to `_settings` |
| Why the ⚙ shows | (no ⚙) | The app has settings, **or the person has a saved view** |
| Remembering where you were | Browser localStorage, this device only | Server-kept stand-in for localStorage, every device (`view_state.go`) |
| Clearing that | Never needed; it holds layout choices read defensively | **Reset view**, on the ⚙ Settings page |
| Its scripts and styles | Shared runtime, cached by the browser | Inlined into the page on every open (`asset_inline.go`), never cached |
| A code-split build | Works | Does not: one file per script |
| Its data | Its own bucket in the shared store, or its own file | Its own namespace in the shared custom-apps store, or its own file (`PrivateDB`) |
| A data source | Go code in the process | A fresh sandboxed script run on every call (`tools/appscript`), no cache |

## What to change

### 1. The ⚙ is for settings

The ⚙ appears when the app has something to set, and not because the person
has a saved view. Today an app with no settings at all grows a ⚙ the first
time its page saves anything, and the page behind it is a Reset view and
nothing else. That is the moment it reads as foreign.

Later, native apps that keep per-person settings take the same ⚙ beside their
name, so the place to look is the same in both.

### 2. Reset view when it is needed

Saving where the person was stays as it is, quiet, on every device. That is
better than native today, and native pages may adopt it later.

Reset view leaves the standing UI. It is offered by gohort, in its own bar
above the frame, when the app's page fails while loading: "This page did not
load. Reset your view of it?" The app's page helper (`window.app`) already
catches an error the page hits and files it as a page problem
(`handlePageProblem`), through the shim the page around the frame relays. The
same catch, in the first seconds of a load, also tells the page around the
frame, which shows the offer. It is drawn by gohort, not by the app, so a
broken app cannot hide it.

A page can also be stuck without an error, for example a wizard saved on a
step that no longer exists. For that, the confirmation page at `_view/reset`
stays, and the ⚙ Settings page keeps a quiet "Trouble with this page? Reset
your view" line at its foot when the app has settings. See open question 1
for an app without them.

### 3. The app's scripts and styles load like files

An app's own `.js`, `.mjs` and `.css` are inlined because a request the frame
makes by itself carries no session, and the browser fetches `<script src>` and
stylesheets while parsing, before the runtime's shim can relay them (measured:
a classic script and a stylesheet blocked by ORB, a module script by CORS).

Instead, serve them at a URL that needs no session:

    /apps/<slug>/_files/<token>/<name>

- `<token>` is derived on the server from the app's owner, slug and a hash of
  its current files, so it is unguessable, names one version of one app's
  files, and changes whenever any of them change.
- The response is `Cache-Control: public, max-age=31536000, immutable`, with
  `Access-Control-Allow-Origin: *` so module scripts load into the frame's
  null origin.
- It serves the app's **assets only**: never records, settings, view state or
  anything a person stored. Those keep going through the shim and the session.
- Because the token is in the path, a module's relative `import` resolves under
  it, so a code-split build works and the one-file-per-script rule goes.

What this lends: anyone holding the URL reads that version of the app's code
and styles, which everyone who can open the app already receives today. An
app that puts something private in an asset file would expose it to a holder
of the link; see open question 2.

### 4. Data sources answer from a cache when nothing changed

**Measure first.** A data source is a fresh sandboxed process on every call,
so a page with three runs three on every open. Before building anything,
time one slow app from its run log, or with the desktop app's timing, to see
whether this or the inlined assets is the cost.

If it is the scripts: keep a data source's last output in memory, keyed by the
app, its name, the person and its arguments, and drop it when the app's
records change (the host already signals a change, `recordsInvalidationBridge`)
or after a short time for a source that reads outside data. A source declares
whether it may be cached, off by default, since only the author knows whether
it reads the clock or the web.

### 5. A new app gets its own database file

`PrivateDB` becomes the default for apps created from now on: its own
encrypted file, removed with the app, with writes that do not queue behind
other apps. Existing apps stay where they are. Moving one would be a
migration, offered per app, by its owner, if ever.

### 6. Fix what the assistant is told about the bar

`apps/anvil/context.go` tells Anvil's assistant that the bar above the page
holds the app's name, Reset view and the link to change it. Reset view has
moved to the ⚙ Settings page, and with item 2 it moves again. The text follows
whatever ships.

## Stored-shape changes

None for items 1 to 4 and 6: the token is derived on each request, the cache
is in memory, and the view-state table is unchanged. Item 5 changes the
default of an existing field for new apps only. **No migration.**

## Order

1. Items 1, 2 and 6 together: the ⚙ and Reset view, which is what feels
   different today.
2. Measure a slow app (item 4's first step).
3. Item 3, then item 4 if the measurement points at the scripts.
4. Item 5, at any point.

## Open questions

1. **An app with no settings and a page stuck without an error.** With item 2
   it has no ⚙ and no error prompt, so its Reset view is reachable only by its
   address. Recommended: accept that, and have "Report a problem" (shown to
   everyone the app is shared with) mention Reset view in its dialog.
2. **Assets at a link that needs no session.** Recommended: acceptable, since
   it is the code every viewer already receives. The folder guide and
   Builder's instructions would say that an asset is readable by anyone who
   can open the app, so data stays in records.
3. **Should native apps take the server-kept view?** Recommended: later, per
   app, starting with the agents page's tab and sidebar choices, if remembering
   across devices proves wanted.

## As built: items 1, 2 and 6 (0.9.688)

- **The ⚙** shows when the person has a Settings page (`hasSettingsPage`):
  settings to set, memory the app keeps about them, or they own or maintain
  it. A saved view alone no longer brings it.
- **The load-error offer.** The page helper in each isolated frame posts
  `{gohortApp: "load-error"}` to the page around it on the first uncaught
  error or rejected promise within 8 seconds of the frame's load, once. The
  app's page, for a person with a saved view, carries `viewTroubleJS`, which
  accepts the message only from one of its own frames and shows, once, below
  the header: "This page hit an error while loading..." with Reset your view,
  linking to the confirmation page. A page that sends the message falsely
  can show the offer and nothing more.
- **Reset view on the Settings page** moved to the foot, as "Trouble with this
  page?", and no longer hides "Nothing to set". Someone with no Settings page
  reaches it by its address.
- **Open question 1, as built:** "Report a problem" tells someone with a saved
  view that resetting it may fix a stuck page, with the link. Owners and
  maintainers always have a Settings page.
- **Anvil's assistant** is told the bar holds the app's name, its gear and
  the link to change it, and that Reset view is the framework's, offered on a
  load error and at the foot of the settings.

## As built: item 3 (0.9.689)

- **The path** is `/apps/_files/<app>/<version>/<name>`, not under the app's
  slug: an unauthenticated request carries no person to resolve a slug for,
  so `<app>` names the owner and slug (base64url). It is answered at the top
  of the app host's catch-all route, ahead of the sign-in check, because a
  mux pattern of its own overlaps the apps' `/{slug}/...` patterns, which
  the router refuses.
- **The version** is the first 32 hex digits of a SHA-256 of the owner, the
  slug and every `.js`, `.mjs` and `.css` asset's name, length and bytes. No
  server secret: it cannot be made without the files it serves. A request
  for any version but the current one, any other type, an app that is gone
  or switched off, is a 404.
- **Headers:** the file's type, `Cache-Control: public, max-age=31536000,
  immutable`, `Access-Control-Allow-Origin: *` (a module from the frame's
  null origin), and `Content-Security-Policy: sandbox; default-src 'none'`,
  so a file opened as a page of its own has no origin.
- **Checked in Chromium**: a module, the chunk it imports, a classic script
  and a stylesheet load inside a frame sandboxed like an app's
  (`TestABrowserLoadsAnAppsFilesInItsSandboxedFrame`, skipped without a
  Chromium, as on CI).
- **The guard** `TestNoAnonymousSurfaceIsRegistered` now reads every source
  file of the package and allows exactly this one public path and this one
  early answer ahead of sign-in; anything else still fails it.
- **The folder guide** says split builds work with their chunks flat in
  `assets/`, and that scripts and stylesheets are readable by anyone who can
  open the app (open question 2, as recommended).
- **Not served here:** images, fonts and sounds, which still go through the
  shim. A stylesheet's `url(...)` to one of the app's own images or fonts
  resolves inside the files folder and is a 404; serving those types here
  too is the follow-up if an app needs it.

## Work that keeps going

A native app's work runs on the server and its page only watches, so leaving
and coming back finds the work where it was. An app's actions already ran
as jobs, but the page could not find them again, and an ask from the page
ran on the page's own request. In order:

1. **The page finds its work again** (built 0.9.690).
2. **An ask is a job** (built 0.9.690).
3. **A notice when work ends with nobody watching**, linking back to the
   app; an app that sends its own (`gohort.notify`) is not sent a second
   (built 0.9.691).
4. **Chained work on the server**: Builder and Anvil put steps in one
   action, and an app check warns when a page chains actions itself
   (built 0.9.691).

None of it changes stored data: running and ended jobs stay in memory for
30 minutes, as actions' already did.

### As built: 1 and 2 (0.9.690)

- **Joining.** `startOrJoinJob` hands back the running job when the same
  call comes again: the same person, app and action (or ask), and the same
  inputs, which are only what the caller sent (query and body), not the
  records or settings the host adds. Other inputs start their own job, so a
  page that runs one action per item still runs them all. A call after the
  run has ended runs again.
- **`work` and `work/<job>`**, for the person's own jobs in the app only:
  each `{job, kind, name, running, progress, done, outcome, started}` plus
  an action's `message, saved, records, result` or an ask's `result.text`.
  The frame may fetch `work`.
- **`window.app.work()`** lists them, newest first, and
  **`window.app.follow(job, opts)`** resolves as `action` or `ask` would.
  The page API reference says to call `work()` as the page loads.
- **`window.app.ask`** follows its job when the answer is slow, as `action`
  already did. On the server the ask runs on a context of its own, in the
  live pill, and an answer that fails is still filed as a page problem,
  decided while the request was open.
- **Co-author proposals** (`propose`) still run in the request.

### As built: 3 and 4 (0.9.691)

- **Watching.** A job counts as watched while a request waits on it, and
  for ten seconds after a page last asked how it was going (`work/<job>`,
  `action-progress/<name>`); a following page asks every two seconds.
- **The notice.** A job started from the page (`appWork.Tell`: a button's
  action or an ask; not an app's agent running an action, which waits on
  it, and not a check's run) that ends unwatched files a notice through
  `appscript.TellPerson`, the same path `gohort.notify` takes: in the
  person's notifications, forwarded as they asked when it is the first of
  its title, a repeat folded into a count. "Extract finished" with the
  action's message, "Extract failed" with why, "Your answer from Notes is
  ready" without the answer in it (a notice can be forwarded by email or
  to a phone). None when the run sent its own (`Job.OnNotify`), and none
  when the person stopped it.
- **Builder and Anvil** are told once, in `apppage.Work`, carried by
  Builder's page help, the folder guide and Anvil's context: work keeps
  going when the page closes, so call `app.work()` on load and follow what
  runs, and do work of several steps in one action script.
- **Verify's code check** adds two notes, not failures: a page that starts
  actions or asks and never calls `app.work()`, and one that runs actions
  one after another itself (a `.then`, two awaits close together, an await
  in a loop), read from the source with its strings masked.

## Fresh starts (0.9.700)

Settled with the owner, 2026-11-25: an app is an independent runtime when it
comes to being opened. Every open starts fresh, reloads included, as with an
app of its own; to get back to where someone was, the app has to offer it.

- **localStorage lasts one visit.** Each isolated frame gets an empty,
  in-memory stand-in (`freshStorageScript`), and nothing is put back into it.
  Before this the page's localStorage was kept on the server and restored on
  every open, on every device, so an app always landed where it was left,
  whether it meant to or not.
- **`app.state` is how an app keeps something between visits**, on purpose:
  `get(key)`, `set(key, value)` (any JSON), `remove(key)`, `clear()`, per
  person and per app, on every device, capped at 256 KB. It is the old saved
  view's table and route (`app_view_state`, `view-state`), so a view saved
  before this reads back through `get` (as its text). The app decides on
  load what to do with it ("Continue where you left off?").
- **Reset view is gone** (item 2 above, and its load-error offer): with
  nothing restored unasked, a page has no saved view to get stuck on. Its
  place on the Settings page is "What it saved for you", with a Clear, when
  the app has saved something with `app.state`; that also brings the ⚙.
  Report a problem no longer mentions it.
- **Builder, the page API reference and Anvil** say each open is fresh and to
  offer a resume with `app.state`, where they used to say localStorage
  follows the person.
- **No migration.** What was saved stays where it was; nothing restores it
  but an app asking.
