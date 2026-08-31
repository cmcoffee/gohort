# Custom App Administration — one tab, one app per row

Status: **design / target** (not built).

Custom apps are the only apps an operator cannot administer. A compiled app
registers its dials at startup — a route stage, a tunable, an admin section —
and the admin page renders them. A custom app registers nothing, because it
does not exist at startup: it is a record somebody wrote at runtime, under
their own name, and the two registries it would need are keyed by constants
declared in Go.

So the operator questions have no home. Who may reach this app. What is its
pipeline allowed to cost. Its public link is live, revoke it. Somebody imported
an app carrying sandboxed scripts, show me them before they run. Each of those
is answerable today only by editing a record by hand or by not answering it.

This is one admin tab with the apps down the left and that app's operator
controls on the right.

## What it is NOT

**Not the app's settings.** There are two kinds of knob on an app and they have
different owners:

| | belongs to | lives on |
|---|---|---|
| what the app DOES — an endpoint, a threshold, a default | the author | the app's own page |
| what the app is ALLOWED — tier, spend, reach, enabled, public | the operator | this tab |

On a single-operator deployment that reads as ceremony, since one person is
both. The split still holds, for the same reason the rest of the framework is
multi-user: the moment a second person authors an app, an admin tab that edits
what their app DOES is a surface that silently rewrites someone else's work,
and the owner's own page becomes a lie about its own contents.

The test for whether a control belongs here: **would the author be surprised to
find it changed?** A revoked public link is an operator decision the author can
see the consequence of. A rewritten threshold is an edit to their app.

## Where it hangs

Two seams exist and one has to be added, and the shape of the addition is
already settled by precedent elsewhere in the framework.

**The tab.** `RegisterAdminSection` (core/sections/admin_sections.go) lets an
app contribute a section under its own tab without admin importing it.
`filestore`, `publish` and `prompts` do this today.

**The rail.** NOT `ui.NavShell`. The admin Tuning tab used to have an embedded
NavShell rail and it was deliberately replaced: one `ui.Section` per rail entry,
all sharing a `Group`, and the page's existing `SectionNav` renders them as a
compact left-rail side index. See the comment at apps/admin/page.go:112. That
is the pattern to follow, because it is the one the admin page already
navigates with everywhere else, and because a category rail assembled out of
plain sections gets the shared menu behaviour for free. `Section.Indent` nests
a control group under the app it belongs to.

So: **one section per custom app, `Group: "Custom Apps"`**, and the framework
draws the list on the left.

**The addition.** `RegisterAdminSection` takes a section VALUE at `init`, which
is exactly what a per-app list cannot be: the apps are runtime records, and
the set changes as people author and delete them. The framework already
answers this shape once, for the dashboard:

> `DashboardCardSource` is implemented by WebApps that contribute EXTRA
> dashboard tiles... The framework calls this on every dashboard render, so
> the returned list can change at runtime.

Mirror it exactly rather than inventing a second idea:

```go
// AdminSectionSource contributes admin sections that vary at RUNTIME, the way
// DashboardCardSource contributes dashboard tiles that do. Called on every
// admin render, so the returned set tracks what actually exists.
//
// Per-request access checks are the source's responsibility — the framework
// does not apply them, exactly as it does not for dashboard cards.
func RegisterAdminSectionSource(fn func(*http.Request) []AdminSectionEntry)
```

`apps/customapps` registers one of these. Admin never imports it, the static
registry keeps working untouched for the apps already using it, and there is
one new concept in the framework rather than two.

### Tenancy

A cross-user list is the part to get right before any of it is built.

1. **The list is admin-only**, gated the way the rest of the admin page is.
2. **An app's NAME and DESCRIPTION are user content.** They are shown, because
   a list of opaque slugs cannot be administered. That is a deliberate
   disclosure, and it is the same call `/api/live` makes about session labels,
   except that this surface is admin-gated where the ribbon is not.
3. **An app's SCRIPTS are not shown by default.** A data source is the owner's
   logic and can carry their intent, their endpoints, and the shape of their
   data. The review control (below) reveals them on an explicit click, which is
   logged. An operator who needs to read a script can; one who is scanning the
   list does not do it by accident.
4. **Nothing here edits the app's definition.** Every control writes to
   operator-owned storage keyed by `(owner, slug)`, never into the `AppSpec`
   the author edits — with the single exception of `Disabled`, which is
   discussed below because it is the one genuine collision.

## The controls, as a registry

The pane is assembled from a registry rather than written by hand:

```go
// CustomAppControl is one operator control rendered on an app's admin pane.
type CustomAppControl struct {
	Key      string // stable id; also the storage key under (owner, slug)
	Label    string
	Help     string
	Group    string // "Access", "Cost", "Exposure", "Review"
	// Render builds the control for one app. It receives the spec so a control
	// can render itself differently — or not at all — for an app it does not
	// apply to (a cost dial on an app with no pipeline is a dial for nothing).
	Render func(spec AppSpec) ui.Component
}

func RegisterCustomAppControl(c CustomAppControl)
```

The reason is the reason `RegisterTunable` and `RegisterAdminSection` exist: a
hand-assembled pane is a place where every future control has to be wired in by
hand, in a file that grows a case per feature. A registry means the app that
owns a concern registers the control for it — the publish surface registers
link revocation, orchestrate registers the tier dials — and admin stays
ignorant of all of them.

A `Render` that returns nil renders nothing. That is how a control that does
not apply disappears instead of appearing dead, which is the failure this
codebase keeps finding: a dial that is present and moves nothing.

### v1 controls

**Access.** Who may reach this app. Today this lives in the per-user grants
picker, where the axis is a user and the app is one checkbox among many.
Per-app is the natural axis for the question "who can get at this", and this is
the same underlying grant read from the other end — not a second mechanism.

**Exposure.** The public capability link: whether one is live, and a revoke
that clears the token and its index entry. The owner can already do this from
their own index; an operator needs it too, and unlike a setting it is not an
edit to what the app IS.

**Cost.** For an app bound to a pipeline, the tier of each stage (below). Plus
a per-app ceiling on public data-source runs, which today is a package constant
(`publicScriptsPerMinute`) shared by every published app.

**Review.** For an app carrying sandboxed data sources or actions: what
capabilities they declare, and a reveal-the-script control. The bundle import
gate already lands an imported app `Disabled` precisely so somebody looks
before anything runs, and today there is nowhere to look from.

**Enabled.** The one collision. `AppSpec.Disabled` is the owner's mute and the
import gate's hold, and an operator needs to stop an app too. Do NOT add a
second flag: two booleans that both mean "off" produce an app that is disabled
in one place and enabled in another, and a support conversation about which one
won. Keep one flag, record WHO set it, and show that on both surfaces: "off,
by the operator" reads correctly to an author, where a flag that silently
flipped back does not.

## The routing dial, which is the hard half

The layout is easy. The dial is where this can quietly become another control
that moves nothing, so it comes first.

Today a pipeline stage's tier comes from exactly one thing:

```go
func stageTier(stage PipelineStage) LLMTier {
	if strings.EqualFold(strings.TrimSpace(stage.Model), "lead") { return LEAD }
	return WORKER
}
```

### The trap

The obvious move is to register a route stage per pipeline stage and call
`RouteToLead(key)`. That is wrong in a way that is expensive and quiet.
`routeEffectiveVal` falls back to the registry's `Default` and then to the
empty string, and `RouteValueIsLead` treats everything outside the closed set
of worker values — the empty string included — as **lead**. A key that is not
registered, or is registered too late, therefore routes to the precision tier.
Pipeline stages default to worker today. Wiring it the obvious way silently
promotes every stage of every custom app to the expensive model, and nothing
fails: the runs just cost more and read a little better.

Registration is exactly the thing that cannot be relied on here, because these
keys are born at runtime and the registry is in memory. After a restart the
keys are gone until something re-registers them, which is the window where
every stage goes lead.

### The shape that works

Do not depend on registration for RESOLUTION. Read the override, fall back to
what the author wrote:

```go
// stageTierFor resolves a stage's tier: an operator override if one is set for
// this pipeline and stage, otherwise the tier the pipeline's author declared.
//
// The override is read DIRECTLY rather than through RouteToLead, because an
// unset route value means "lead" to that function and "the author decided" to
// this one. Wiring it the other way promotes every stage of every custom app
// to the precision tier the first time the process restarts.
func stageTierFor(pipelineID string, stage PipelineStage) LLMTier {
	if v := RouteOverride("pipeline." + pipelineID + "." + stage.Name); v != "" {
		return tierFromRouteValue(v)
	}
	return stageTier(stage)
}
```

`RouteOverride` is a small exported reader for the stored value alone (today
`LookupRouteFunc` is a package var and `routeEffectiveVal` folds in the
defaults this must not have).

The registry then has one job: populating the admin UI's list of dials. And it
does not need to be a registry at all — the admin pane enumerates the stages of
the app's bound pipeline when it renders, which is stored data that survives a
restart by construction. **The dials are derived from the definition; only the
overrides are stored.** A stage renamed in the pipeline drops its dial and its
override goes inert rather than applying to a stage that no longer exists.

Each dial's default position shows the authored value, labelled as such, so an
operator can see what they are overriding rather than only what they are
choosing.

## What is deferred, and why

**Spend ceilings per app.** A real want, and a different feature: it needs the
cost ledger to attribute a run to an app, and a decision about what happens
when a run hits the ceiling mid-way. Cutting a run off halfway is its own
design question and it does not belong in the same change as a settings pane.

**Editing the app's definition from admin.** Deliberately never. That is the
author's page.

**Per-app tunables in the `RegisterTunable` sense.** Those are deployment-wide
singletons with one value per key. A custom app is per-owner, so the key would
have to carry the owner, and at that point it is not a tunable — it is the
control registry above.

## Tests

- the section SOURCE is called per render, and a newly authored app appears
  without a restart — the whole reason it is a source and not a registration
- zero custom apps renders the empty-state section, not an empty rail
- an app with no bound pipeline renders no tier dials, and no empty Cost group
- a tier override changes the resolved tier; REMOVING it returns the stage to
  the tier its author declared, rather than to lead
- **no override anywhere means no behaviour change**: a stage with `model` unset
  still resolves to worker after a process restart, which is the regression the
  trap above would have caused
- an override naming a stage the pipeline no longer has is inert
- a non-admin request reaches none of it
- the scripts of an app are absent from the list payload, and present only in
  the review control's own response
- disabling from admin and from the owner's index write the SAME flag, and each
  surface reports who set it

## Rollout

1. `RouteOverride` + `stageTierFor`, with the no-override-no-change test. Ships
   alone and changes nothing visible.
2. `RegisterAdminSectionSource`, mirroring `DashboardCardSource`. Also ships
   alone: a source that returns nothing changes no page.
3. The control registry and the tab, with Access and Exposure only.
4. The tier dials, once (1) is proven in the wild.
5. Review, which is the one that wants a log line of its own.

Bump `version.txt` on every commit (no trailing newline).
