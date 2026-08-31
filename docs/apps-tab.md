# The Apps Tab — admin organised by subject, not only by mechanism

Status: **design / target** (not built).

Admin is organised by MECHANISM. Everything configurable about techwriter is
spread across three tabs: its tier in **LLMs** (a route stage), its knobs in
**Tuning** (tunables), its prompt-block editor in **Extensions** (a contributed
section). Nothing anywhere answers the question an operator actually arrives
with, which is "what can I change about techwriter?"

Both organisations are legitimate. "Show me all routing at once" is exactly what
you want when you are chasing a bill, and that view has to stay. This adds the
other axis: one tab listing every app, each showing its own controls, gathered
from the registries that already hold them.

## The finding that shapes the whole thing

**The association does not exist as data.** An operator can see that
`app.techwriter` is probably techwriter's, but nothing in the tree says so.

Route stage keys carry two conventions and neither is declared:

```
app.techwriter            app.orchestrate.suggest      app.servitor.orchestrator
blogger.editor            debate.contestability_audit  admin.tool_groups.suggest
```

Tunables carry none at all — every key is `tune_<something>` and the Category is
cross-cutting (Retrieval, Timeouts, Limits, Cache). Some are plainly app-scoped
by name (`tune_bridge_reply_budget`, `tune_autofill_max_docs`) and nothing but
the name says so. Contributed admin sections declare a `Group`, which names the
TAB they land on, not the app they belong to.

So the work here is not the tab. It is that three registries need to be able to
say who a control belongs to.

### Do not infer it from the key

The prefix is nearly a convention, and nearly is the problem. `app.techwriter`
and `blogger.editor` are the same relationship written two ways, and
`admin.tool_groups.suggest` is admin's, not a "tool_groups" app's. A heuristic
that is right eighty percent of the time puts somebody else's dial on your app's
page, where moving it looks safe and is not.

**Declared or absent.** A control with no declared app appears on its mechanism
tab exactly as it does today and does not appear under any app. That makes the
migration incremental — nothing moves until an app claims its own dials — and
it makes a missing claim a visible gap rather than a wrong attribution.

## The enabling change

One optional field on three registrations:

```go
type RouteStage struct {
	// App names the app this call site belongs to, as its WebPath
	// ("/techwriter"), or empty for framework-level routing that belongs to no
	// single app.
	//
	// Declared and never inferred from Key. The key prefixes look like a
	// convention and are two conventions plus exceptions, and a wrong guess
	// here does not read as a guess: it reads as a dial that is yours.
	App string
	// … existing fields unchanged
}

type TunableSpec struct {
	App string // same contract
	// … existing fields unchanged
}

type AdminSectionEntry struct {
	App string // same contract; Group still names the tab it also lands on
	// … existing fields unchanged
}
```

Empty is the default and means what happens today. No key changes, no storage
changes: a route override is stored under its Key and a tunable under its Key,
and renaming either would orphan every value an operator has set.

## The list

Two kinds of app, one list:

```
RegisteredWebApps()  → compiled apps, minus /admin and anything WebHidden()
+ every custom app   → AuthListUsers × ListAppSpecs
```

Rendered the way the Custom Apps tab already renders: one `ui.Section` per app
sharing a `Group`, with the page's `SectionNav` drawing the rail. Compiled apps
first, then custom apps grouped by owner, because an operator scanning for
"servitor" and one scanning for "whatever Alice built" are doing different jobs.

`appadmin.App` widens to carry both kinds:

```go
type App struct {
	Kind string // "app" (compiled) | "custom"
	Path string // "/techwriter", or "/custom/<slug>"
	Name string
	// Custom apps only; empty for compiled ones.
	Owner, Slug string
	Shared, Disabled bool
	PublicToken, PipelineID string
}
```

Controls already decline by returning nil, so the custom-app controls need no
guard beyond checking `Kind` — which is the property that made the registry
worth having.

## What is on a pane

**Identity**, for every app: its path, its description, and whether it is
reachable — a compiled app has no owner and no share state, so this is short and
honest rather than a table of empty fields.

**Access**: who can open it, which is the same grant the Users picker edits, read
from the app end. For a custom app this sits beside the operator allowlist that
already exists there, with the copy that already explains the conjunction.

**Its route stages**, from the registry, filtered to this app. Each shows the
same control the LLMs tab shows, writing the same key.

**Its tunables**, same.

**Its contributed section**, if it registered one. This is the one that gets
interesting: `filestore`, `publish` and `prompts` each contribute a section
today, and those are precisely "settings for this app" wearing a tab name.

**Nothing else.** An app with no declared controls renders identity and access
and stops. That is a true statement about it, and a truer one than a page of
empty groups implying there is something to set.

## Both axes, one storage

The mechanism tabs keep listing everything, including controls that also appear
under an app. That is deliberate duplication of PRESENTATION, and it is fine:
the two views answer different questions and a control that appeared in only one
of them would make the other lie by omission.

What must not be duplicated is storage. Both views read the same registries and
write the same keys, so a tier set on the Apps tab is the same value the LLMs
tab shows a second later. Any design where the app view has its own store is
wrong on arrival.

## What this does to the Custom Apps tab

It folds in. Custom apps become rows in this list rather than a tab of their
own, and everything built for them — the control registry, the operator state,
the tier dials, review — is already app-agnostic enough to come along. The tab
called "Custom Apps" disappears when its rows have somewhere better to live.

And only then does the tab get called **Apps**. Naming it that while it holds
only custom apps is a promise it cannot keep; naming it that once it lists
techwriter, servitor and filestore is just what it is. The user-row labels were
already changed to "App access" and "App groups", so the noun is free.

## Risks

**A control appearing under the wrong app** is the failure mode that matters,
because a dial on your app's page reads as safe to change. Declared-only
attribution is the answer, and the test below pins it.

**A registry growing a field it does not need.** `App` is optional on all three
and every existing registration keeps compiling unchanged; a registry that never
declares one behaves exactly as it does now.

**An app claiming a control that is not its own.** Registration is in-process Go,
so this is not an attack, it is a mistake — and it shows up as a dial on two
apps' pages, which the duplicate-claim test catches.

## Tests

- an app that declares nothing renders identity and access only, and no empty
  control groups
- a route stage declaring `App: "/techwriter"` appears on techwriter's pane AND
  still on the LLMs tab; setting it from either writes the same key
- a route stage declaring nothing appears ONLY on the LLMs tab — the check that
  keeps attribution declared rather than inferred
- a stage whose `App` names no registered app is reported at startup rather than
  silently dropped, because a typo there is a control that vanishes
- two apps cannot claim the same control key
- a custom app's controls do not render for a compiled app, and vice versa
- a hidden app (`WebHidden()`) and `/admin` are absent from the list

## Rollout

1. The `App` field on the three registries. No UI change, nothing declares it
   yet, everything behaves as it does today.
2. The Apps tab: the list, identity, access. No controls gathered yet.
3. Backfill `App` on a few apps — techwriter and filestore are the clearest,
   one route stage and one contributed section — and their controls start
   appearing under them without moving.
4. Fold the Custom Apps rows in and retire that tab.
5. Rename to **Apps** once step 4 lands, and not before.

Bump `version.txt` on every commit (no trailing newline).
