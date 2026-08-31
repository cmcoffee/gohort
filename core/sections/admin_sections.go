package sections

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

// AdminSectionEntry is a settings surface an app contributes to the admin page:
// a section (rendered under its own group tab) plus an optional ExtraHeadHTML
// fragment carrying any client actions the section's controls need. It lets an
// app whose configuration is FRAMEWORK tuning rather than agent behavior — e.g.
// the prompt-block editor — live inside the admin UI WITHOUT admin importing the
// app. The app self-registers here (init, like RegisterApp / RegisterAdminAgent)
// and the admin page reads the registry when it assembles its tabs. The section
// carries its own Group (the tab it lands under) and Wide flag.
type AdminSectionEntry struct {
	Section ui.Section
	Head    string // ExtraHeadHTML fragment (client-action registrations, etc.); may be empty
	// App names the app this section configures, as its WebPath. Empty means a
	// section that belongs to no single app.
	//
	// Group and App answer different questions and both are needed: Group is
	// the TAB this lands on, App is whose settings these are. A section can
	// have one, the other, or both — the prompt-block editor lands under
	// Extensions and belongs to the prompts app.
	App string
}

// AdminSectionSource contributes admin sections that vary at RUNTIME, the way
// DashboardCardSource contributes dashboard tiles that do.
//
// RegisterAdminSection takes a section VALUE at init, which is the right shape
// for a surface an app knows about when it is compiled — a prompt-block editor
// exists whether or not anybody has used it. It is the wrong shape for a
// surface whose ROWS are records people write while the server is running:
// custom apps are authored, renamed and deleted at runtime, and a list of them
// fixed at startup is a list that is wrong by lunchtime.
//
// Called on EVERY admin render, so what it returns tracks what exists. Per-
// request access checks are the source's responsibility — the framework does
// not apply them, exactly as it does not for dashboard cards.
type AdminSectionSource func(r *http.Request) []AdminSectionEntry

var (
	adminSections       []AdminSectionEntry
	adminSectionSources []AdminSectionSource
)

// RegisterAdminSection adds an app-contributed section to the admin page. Call
// once at startup (typically in the app's init), same pattern as the other
// self-registration hooks.
func RegisterAdminSection(e AdminSectionEntry) { adminSections = append(adminSections, e) }

// RegisterAdminSectionSource adds a runtime source of admin sections. Same
// self-registration pattern as the rest; see AdminSectionSource for when to
// reach for it instead of RegisterAdminSection.
func RegisterAdminSectionSource(fn AdminSectionSource) {
	if fn != nil {
		adminSectionSources = append(adminSectionSources, fn)
	}
}

// AdminSectionEntries returns the statically registered admin sections in
// registration order.
//
// Deprecated for new callers: use AdminSectionEntriesFor, which also asks the
// runtime sources. Kept because it is the honest answer to the question it
// asks, and a caller with no request in hand cannot ask the other one.
func AdminSectionEntries() []AdminSectionEntry { return adminSections }

// AdminSectionEntriesForApp returns the sections an app has CLAIMED, for one
// request. Asks the runtime sources too, so a section that only exists for
// some requests is found the same way it is anywhere else.
func AdminSectionEntriesForApp(r *http.Request, appPath string) []AdminSectionEntry {
	if appPath == "" {
		return nil
	}
	var out []AdminSectionEntry
	for _, e := range AdminSectionEntriesFor(r) {
		if e.App == appPath {
			out = append(out, e)
		}
	}
	return out
}

// AdminSectionEntriesFor returns every admin section for one request: the
// statically registered ones first, then whatever the runtime sources report.
//
// Static first so a registration order that has been stable for releases stays
// stable, and a source's rows land after it rather than interleaved.
func AdminSectionEntriesFor(r *http.Request) []AdminSectionEntry {
	out := make([]AdminSectionEntry, 0, len(adminSections))
	out = append(out, adminSections...)
	for _, src := range adminSectionSources {
		if src == nil {
			continue
		}
		out = append(out, src(r)...)
	}
	return out
}
