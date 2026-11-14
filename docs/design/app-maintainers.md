# App maintainers

Status: built (2026-11-13), phase 1. Decided with the owner: start narrow.
A maintainer changes an app's sections and wording; its scripts and page
code stay the owner's.

## Why

Agents have maintainers (`agent_maintainers.go`): people who keep somebody
else's agent working without owning it. Apps had nothing like it. An app
shared with a team could be used by the team, and only its owner could fix
a column, a label or a form.

## The rule

A maintainer edits the owner's app, in the owner's store, the one copy there
is. Every save keeps the version it replaced, named for the maintainer, so
the owner can see it and revert it.

What a maintainer may change is a LIST (`applyAppMaintainerEdit`), not a list
of what they may not. Every field off it is copied from the owner's record,
so a field added to apps later is the owner's until somebody decides
otherwise.

| A maintainer changes | The owner keeps |
|---|---|
| Sections: add, change, remove, reorder | Data-source and action scripts, shared Python modules |
| The app's name, description and notes | Any page code: an `html` or `card` section's HTML, and any script or styling in the page |
| Layout (full width, narrow) | Who uses it and who maintains it, Everyone, the public link |
| | The bound agent and pipeline, including a section's `pipeline_id` |
| | Settings and their values (a setting can hold a token), shared collections, the record key, the data settings, spending caps |
| | Disable and delete |

**Why scripts and page code are the owner's.** An app's data-source and
action scripts run in the owner's sandbox, with the owner's credentials. A
maintainer editing one would be running their own code as the owner. Page
code runs in every viewer's browser. Phase 1 keeps both with the owner;
phase 2 (below) is how a maintainer could propose a change to them.

A section can only name scripts the app already has (`source_script` is the
name of an existing data source), so a section edit cannot reach code the
owner did not write.

**Data.** A maintainer is a user of the app like any other: they keep their
own copy of the records, and never get the owner's.

## Naming a maintainer

On the app's Share dialog, below "Shared with", a "Maintainers" list: people
by name. Naming someone a maintainer also shares the app with them, since
maintaining an app you cannot open makes no sense. Unsharing a person also
drops them as a maintainer. Taking someone off the maintainers leaves them
able to use the app. Teams are left for later.

The owner's index of maintained apps (`maintained_apps`, maintainer to owner
and slug) is written whenever an app is saved and dropped when it is deleted,
the same as `maintained_agents`. Every read re-checks the app's own list, so
the record, not the index, decides.

## Editing, through Builder

Apps have no field editor; they are edited by Builder through `app_def`. For
an app the person maintains rather than owns:

| Action | |
|---|---|
| `list` | Lists it, marked with whose it is |
| `get` | Reads it, scripts included: a maintainer needs to understand the app |
| `update_section`, `add_section`, `remove_section` | Allowed, held to the rule above |
| `update` | Allowed with name, description, notes, sections and layout only; any other key is refused by name |
| Everything else (`create`, `delete`, `patch_html`, `replace_function`, `revert`, `checkout`, `publish`, `run`, `pack`, `unpack`, assets, `verify`, `test`) | Refused, naming the owner |

The checks a save runs (the page in a browser, fields left without a home)
run against the owner's app and the owner's records.

## Phase 2 (not built)

A maintainer's change to a script or page code is held for the owner: the
app keeps running the old version until the owner approves a diff on a
review card. Needs a pending-change store and the review UI. Built when a
maintainer actually needs it.

Also later: teams as maintainers, `revert` for a maintainer's own revisions,
and the folder workflow (`checkout` / `publish`).
