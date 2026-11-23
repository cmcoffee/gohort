# Apps

An app is a page of your own inside gohort, made by Builder from a
description: a tracker, a form and table, a dashboard, a chat over your
documents, a game. Each one is served at `/apps/<name>/`, keeps its own
records, and can run scripts, call its own agent, and run on a schedule. You
can share it with colleagues, who each get their own copy of its records.

You do not write HTML or code to make one. You say what it should do, Builder
builds it, and Builder changes it when you ask.

## Making one

Ask Builder, from any agent's page (**Build**) or in a conversation with
Builder:

> *"Make me an app to track customer escalations: a form to log one, a table
> of open ones, and a chart of how many came in each week."*

Builder writes the app, checks it renders, and opens it. To change it later,
use **Change** on its row in **My Apps**, or **Change it** on the app's
settings page, and say what should be different.

## What an app is made of

- **Sections**: the parts of the page. Builder picks from:
  - **form**: a way to add a record
  - **table**: the records, editable and deletable
  - **display**: read-only values
  - **chart**: bar, line, area or pie
  - **chat**: a conversation with an agent
  - **workbench**: a list, a document and a chat side by side
  - **actions**: a row of buttons that run scripts
  - **html**: a page Builder writes itself, for anything the others cannot
    express, such as a game, an animation or a custom visual
- **Records**: what the app keeps. Each person's records are their own. An
  app can also keep **shared collections** that every user reads in common
  (a leaderboard, a shared list), which only its scripts write.
- **Scripts**: Python that runs in the sandbox. A **data source** produces
  what a section shows (a chart's figures, a table from an API). An
  **action** runs when a button is pressed. A slow action keeps going with its
  button showing a spinner, the latest line it printed, and Stop, and you can
  leave the page and come back to it.
- **Its agent**: an app can have an agent of its own that answers its chat. In
  the app's chat, the agent can press the app's buttons and read what the page
  shows, as the person using it, and nothing more.
- **Schedules**: an action can run on a timer, so a tracker or dashboard stays
  fresh while nobody has it open. **Pause** and **Resume** on the app's row
  stop and restart it.
- **Settings**: values a person might change without rebuilding the app (an
  interval, a threshold, which source). They are set on the app's
  **Settings** page, and every script reads them.
- **Checks**: tests that live with the app, written as each feature is built.
  They run on every save, and a save that breaks a check that passed is
  refused.

## My Apps

**My Apps** lists the apps Builder made for you and the apps colleagues
shared with you. Each row shows whether the app is **Private**, **Shared**,
**Publish requested**, **Disabled** or **Not live yet**, and offers what
applies to it:

- **Open**, **Settings**, **Runs** (what its scripts did each time, and why
  one failed), and **Change**
- **Share** (see below), **Export** and **Delete**, for an app you own
- **Pause** / **Resume** for one that runs on a schedule

The gear on your dashboard's **My apps** section puts an app on your front
page.

### The app's settings page

The gear on an app's page opens its settings:

- **Agent**: which agent answers the app's chat.
- **Settings**: the app's own values.
- **What it remembers**: what the app's agents remember about you. Only you
  see this.
- **Change it**: ask Builder to change the app.
- **Your view**: **Reset view** clears your filters and layout choices.
- **Recent runs** (owner only): what the app's scripts did.

## Sharing an app

**Share** on the app's row has three lists:

- **Shared with**: people, teams you are on, or Everyone. Each person keeps
  their own records. Your data-source and action scripts run with your
  credentials for them, so the dialog says so, and lists any file stores the
  app reaches.
- **Maintainers**: people who can change the app's sections and wording
  through Builder. Its scripts, page code, sharing and settings stay yours,
  and every change they make is kept in its history under their name.
- **Managers**: people who run the app for its users. They see the actions and
  sections the app marks for managers.

Sharing with **Everyone** is a request an administrator approves; the row
shows **Publish requested** until then. People and teams take effect at once.

Someone using a shared app can **Report a problem** from the app's page; it
reaches the owner with what went wrong.

An app's owner can also offer it to the people it is shared with from an MCP
client such as Claude Desktop (**Offer to people it's shared with**, on the
app's settings page). There, a call does what their click would, on their own
records.

See [Sharing](sharing.md) for how sharing works across gohort.

## History, export and updates

Every change to an app is kept: what changed, who changed it, and how (Builder,
a maintainer, an import). Ask Builder to go back to an earlier version.

- **Export** saves the app as a file another gohort can import. Its records
  never travel, and neither do its settings' values.
- **Import...** on My Apps previews an exported app, then brings it in. An
  imported app lands **switched off and shared with nobody**: read its
  scripts, then **Enable** it. If it names things this deployment does not have
  (an agent, a collection), **Connect** maps each one to one of yours.
- **Update...** takes a newer export of an app you imported, from the same
  person's copy. It arrives as a draft, **Not live yet**: **Preview** it, then
  **Make live** or **Discard**.

Anvil can also open an app as a project, for work that is easier as files. See
[Getting started](GETTING_STARTED.md#7-work-on-code-with-anvil).

## What an app can reach

An app's scripts run in the sandbox. They reach what the app's owner can, and
a file store only when an administrator grants the app one (see [File
stores](file-stores.md#apps-that-use-a-store)). The owner pays for what the
app spends, so a shared app has daily caps on what its page may spend asking
its agent, in the whole app and per person.

An administrator sees every app, built-in and built by people, under
**Administrator › Apps**.
