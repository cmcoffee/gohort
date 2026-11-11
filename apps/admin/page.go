// New admin page — framework-rendered with the simple sections migrated
// to core/ui's declarative components. Sections that need new framework
// primitives (DB Browser tree, Cost Rates split chart, Routing
// per-row select+number table, Watchers/Tools/Tasks edit dialogs) still
// live at /admin/legacy until each gets ported.

package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// adminLayout is the administrator's tabs in order, each with the sections on
// it in order, by TITLE. It is the one place that says where a section goes:
// the tab, the tab's position, and the section's position on it all read from
// here, so they cannot disagree.
//
// Tabs are by the administrator's job, not by the kind of thing configured:
// who gets in (People), what answers (Models), what gohort can do
// (Services, each beside the connectors behind it), what it is wired to
// (Integrations), what agents may do (Oversight) and how well they did it
// (Quality). A subject held both ways, a deployment's own and its users', has
// the two side by side on one tab rather than one on each of two.
//
// A section another app contributes is placed here by title like admin's
// own, ahead of the Group it declared (see sectionTab). Apps and Tuning are
// filled by sections that declare their tab, and list none here.
var adminLayout = []struct {
	tab      string
	sections []string
}{
	// What this deployment is, how it is doing, and what it costs. Prices sit
	// with the chart they feed.
	{"Overview", []string{"System Status", "Site", "Cost History (Last 30 Days)", "Cost by source", "Prices"}},
	{"People", []string{
		"Sign-in", "Add account", "Users", "People outside gohort", "Teams", "Default Apps",
		"Feature Access", "Model access limits", "Personal integrations", "Activity Log",
	}},
	{"Models", []string{"Worker LLM", "Lead LLM", "Decision Offloading", "Model Privacy", "LLM Routing", "Local Model Scheduler"}},
	// System Hooks first: the settings below it are what some of its rows
	// point to. The Keystore next: the keys every page below names.
	{"Services", []string{
		"System Hooks", "Keystore", "Embeddings", "System One", "Audio Transcription (STT)", "Speech (TTS)", "Media Generation",
		"Web Search", "Page Rendering (Browser)", "Mail (SMTP)", "Research Sources",
		"Connectors", "Names no longer offered", "Templates",
	}},
	{"Integrations", []string{
		"API Credentials", "User-owned credentials", "File stores",
		"MCP Servers", "Personal MCP servers", "MCP Tools (exposed to external clients)",
		"Bridges", "Personal Teams bots", "Other integrations", "Ollama Proxy",
		"Resource Sharing", "Peers", "Shared With", "Import and export",
	}},
	{"Tools & Skills", []string{
		"Drafted tools waiting for review", "Users' tools", "Agent-Scoped Tools", "Orphaned Tools",
		"Categories", "Shared tools", "Skills",
	}},
	// What every agent is held to, and what reaches the whole deployment:
	// the rules, the security ceiling, the requests to share and what is
	// shared, and what users have built.
	{"Oversight", []string{
		"Rules", "Channel Wake Rules", "Agent security across the deployment",
		"Agent Capabilities: Outward & Spending", "Sites agents may send to",
		"Requests to share", "Shared with everyone",
		"User-owned agents", "User-owned pipelines", "User-owned machines",
	}},
	// How the checks on agents' replies are doing: the queues to review
	// first, then the records.
	{"Quality", []string{
		"Flagged replies", "Drafted guards", "Correction checks", "Overturned firings",
		"Recent firings", "Build outcomes",
	}},
	{AppsTabGroup, nil},
	{"Tuning", nil},
	{"Maintenance", []string{
		"Scheduled Tasks", "Reclaim space", "Reports", "Housekeeping", "Migrations",
		"Vector Index", "Database Browser",
	}},
}

// sectionGroup is the tab of each section adminLayout places, by title;
// tabRank is each tab's position and sectionRank each section's on its tab.
// Read through sectionTab.
var sectionGroup, tabRank, sectionRank = func() (map[string]string, map[string]int, map[string]int) {
	groups, tabs, secs := map[string]string{}, map[string]int{}, map[string]int{}
	for i, t := range adminLayout {
		tabs[t.tab] = i
		for j, title := range t.sections {
			groups[title] = t.tab
			secs[title] = j
		}
	}
	return groups, tabs, secs
}()

// sectionTab is the tab a section lands on: its entry in sectionGroup, else
// the Group it declared. Empty stays empty, which the page renders as General.
//
// The map keys on TITLE, and an Apps row's title is an app's NAME — which
// nobody here chose and which could one day be "Catalog" or "Skills". A
// collision would yank that app's row onto another tab, where it would read as
// the app having vanished. Sections that have already declared the Apps group
// keep it.
func sectionTab(title, declared string) string {
	if declared == AppsTabGroup {
		return declared
	}
	if g, ok := sectionGroup[title]; ok {
		return g
	}
	return declared
}

// boolOffSuffixRE matches a trailing "(0 = off)"-style parenthetical. On a bool
// tunable that renders as a toggle, the 0/1 convention is redundant noise, so the
// admin UI strips it from the label. Left intact on number knobs, where "0 = off"
// is a meaningful sentinel the operator needs to know.
var boolOffSuffixRE = regexp.MustCompile(`(?i)\s*\(\s*0\s*=\s*off\s*\)\s*$`)

// toggleLabel strips the redundant 0=off suffix from a bool tunable's label.
func toggleLabel(label string) string { return boolOffSuffixRE.ReplaceAllString(label, "") }

// buildTunableSections renders the operator tunable registry as one admin
// FormPanel section per category, each with a per-category "Revert to
// defaults". Field type, bounds, and help come straight from each TunableSpec,
// so the admin UI never grows by hand as knobs are registered.
func buildTunableSections() []ui.Section {
	var order []string
	byCat := map[string][]ui.FormField{}
	// The app panes showing knobs from each category: those forms write the
	// same keys, so this category's form reloads when one of them saves.
	paneSources := map[string][]string{}
	seenPane := map[string]bool{}
	for _, s := range AllTunableSpecs() {
		if _, seen := byCat[s.Category]; !seen {
			order = append(order, s.Category)
		}
		byCat[s.Category] = append(byCat[s.Category], tunableField(s))
		if s.App != "" && isListableApp(s.App) && !seenPane[s.Category+"\x00"+s.App] {
			seenPane[s.Category+"\x00"+s.App] = true
			paneSources[s.Category] = append(paneSources[s.Category], tuningSourceForApp(s.App))
		}
	}
	// One category per SECTION, so the framework's SectionNav renders the Tuning
	// tab as a compact left-rail side-index of areas (Model calls, Images,
	// …) with only the selected category's fields shown — instead of one long
	// stack that grows every time a knob is registered. This replaces the app's
	// own embedded NavShell rail with the shared menu system, so Tuning navigates
	// exactly like Extensions. Each category still saves as you edit and carries
	// its own Revert.
	out := make([]ui.Section, 0, len(order))
	for _, cat := range order {
		var body ui.Component = ui.FormPanel{
			Assist:       true,
			Source:       "api/settings",
			Method:       settingsSaveMethod,
			ResetURL:     "api/settings/reset-tunables?category=" + url.QueryEscape(cat),
			ResetLabel:   "Revert to defaults",
			ResetConfirm: "Revert the " + cat + " settings to their built-in defaults?",
			Fields:       append(byCat[cat], tuningExtraFields[cat]...),
			RefreshOn:    paneSources[cat],
		}
		if panes := tuningExtraPanes[cat]; len(panes) > 0 {
			body = ui.Stack{Children: append([]ui.Component{body}, panes...)}
		}
		out = append(out, ui.Section{
			Title:    cat,
			Group:    "Tuning",
			Wide:     true, // each category pane wants full width, not the two-up config column
			Subtitle: "Saved automatically as you edit.",
			Body:     body,
		})
	}
	return out
}

// tuningExtraFields are settings on the shared record that are not tunables
// but are about a category's subject, so they sit in its form. Revert to
// defaults covers tunables only, and says nothing of these.
var tuningExtraFields = map[string][]ui.FormField{
	"Storage & retention": {
		{Field: "fetch_cache_quota_mb", Label: "Fetch cache quota (MB)", Type: "number",
			Min: 0, Max: 10240, Help: "Disk budget for the URL fetch cache. 0 disables caching. Not covered by Revert to defaults."},
	},
}

// tuningExtraPanes are settings kept in a store of their own that are about a
// category's subject. Each was a one-form section on another tab; here it is
// beneath the knobs it sits beside, still saving to where it always did.
var tuningExtraPanes = map[string][]ui.Component{
	"Turns & context": {ui.FormPanel{
		Assist: true,
		Source: "api/agent-loop-tuning",
		Fields: []ui.FormField{
			{Type: "header", Label: "Agent loop history"},
			{Field: "history_budget_percent", Label: "History budget (% of context window)",
				Type: "number", Min: 25, Max: 90, Placeholder: "50",
				Help:   "Steady-state cap on per-round history, as a percent of the LLM's context window. Not covered by Revert to defaults.",
				Detail: "Default 50, so a 200K-window worker targets about 100K of history. Lower means faster prefill and more aggressive elision of old tool results; higher means more retained context and slower prefill. Clamped to 25-90.\n\nLower it when long sessions push prefill latency or thrash the LLM's prompt cache; raise it when the model needs to remember more context across rounds."},
		},
	}},
	"Integrations": {ui.FormPanel{
		Source: "api/network",
		Fields: []ui.FormField{
			{Type: "header", Label: "Research sources and search APIs"},
			{Field: "connect_timeout_seconds", Label: "Connect timeout (seconds)",
				Type: "number", Min: 1, Max: 120, Placeholder: "10",
				Help: "TCP + TLS connection timeout. Default 10. Not covered by Revert to defaults."},
			{Field: "request_timeout_seconds", Label: "Request timeout (seconds)",
				Type: "number", Min: 1, Max: 300, Placeholder: "15",
				Help:   "Per-read I/O timeout for HTTP response bodies. Default 15. Not covered by Revert to defaults.",
				Detail: "Raise them when working against slow upstreams; lower them to fail fast in a tight loop."},
		},
	}},
}

// tunableFieldsFor is the named knobs as form fields, for a section on another
// tab that shows a few of the Tuning tab's knobs beside its own settings. A key
// that is not registered is left out (page_system_test pins the ones in use).
func tunableFieldsFor(keys ...string) []ui.FormField {
	var out []ui.FormField
	for _, k := range keys {
		if s, ok := LookupTunable(k); ok {
			out = append(out, tunableField(s))
		}
	}
	return out
}

// tunableField is one knob as a form field: a toggle for a bool, otherwise a
// bounded number labelled with its unit. Shared by the Tuning tab and an app's
// pane on the Apps tab, so the two show the same control for the same key.
func tunableField(s TunableSpec) ui.FormField {
	// Bool knobs render as a real toggle, not a 0/1 number field.
	if s.Kind == KindBool {
		return ui.FormField{
			Field:  s.Key,
			Label:  toggleLabel(s.Label),
			Type:   "toggle",
			Help:   s.Help,
			Detail: s.Detail,
		}
	}
	unit := ""
	switch s.Kind {
	case KindSeconds:
		unit = " (seconds)"
	case KindMinutes:
		unit = " (minutes)"
	case KindHours:
		unit = " (hours)"
	case KindDays:
		unit = " (days)"
	}
	return ui.FormField{
		Field:    s.Key,
		Label:    s.Label + unit,
		Type:     "number",
		Help:     s.Help,
		Detail:   s.Detail,
		Min:      int(s.Min),
		Max:      int(s.Max),
		Decimals: s.Decimals,
	}
}

// tuningSourceForApp is the source of one app's tuning form. Named so the form
// and every RefreshOn naming it agree on the spelling: RefreshOn matches
// exactly, and a source off by one character is a form that never refreshes.
func tuningSourceForApp(app string) string {
	return "api/settings?app=" + app
}

// appTuningForm is the knobs one app has claimed, as a form over the same keys
// the Tuning tab writes. Nil when the app has claimed none.
//
// Headed by category because an app's knobs still span subjects (orchestrate
// has limits, timeouts and memory knobs), and the category is what each one is
// called on the Tuning tab, which is the other place an operator will look.
func appTuningForm(app string) *ui.FormPanel {
	specs := TunablesForApp(app)
	if len(specs) == 0 {
		return nil
	}
	var order []string
	byCat := map[string][]ui.FormField{}
	for _, s := range specs {
		if _, seen := byCat[s.Category]; !seen {
			order = append(order, s.Category)
		}
		byCat[s.Category] = append(byCat[s.Category], tunableField(s))
	}
	var fields []ui.FormField
	for _, cat := range order {
		fields = append(fields, ui.FormField{Type: "header", Label: cat})
		fields = append(fields, byCat[cat]...)
	}
	// Saves go to the app's own source rather than api/settings: a save
	// announces its target, and this form listens for api/settings, so posting
	// there would have it reload after every one of its own edits. The server
	// takes only this app's knobs at that address.
	return &ui.FormPanel{
		Assist:       true,
		Source:       tuningSourceForApp(app),
		Method:       settingsSaveMethod,
		ResetURL:     "api/settings/reset-tunables?app=" + url.QueryEscape(app),
		ResetLabel:   "Revert to defaults",
		ResetConfirm: "Revert this app's tuning to its built-in defaults? Its routing is not affected.",
		Fields:       fields,
		// The Tuning tab's category forms save to api/settings.
		RefreshOn: []string{"api/settings"},
	}
}

// The cost surfaces, named once because two things have to agree about them: a
// chart's Source and the Invalidate list of every form that changes what the
// chart counts. Invalidation matches on the EXACT source string, so a days=30
// written in one place and days=60 in the other fails silently, leaving a stale
// graph and nothing to notice it by. Sharing the constant makes the coupling a
// compile error instead of a bug report.
const (
	costHistorySource  = "api/cost-history?days=30"
	costBySourceSource = "api/cost-by-source?days=30"
)

// costSources is what a form that edits a per-call cost must refresh: editing
// one changes both the chart total and the per-source breakdown.
var costSources = []string{costHistorySource, costBySourceSource}

// serveNewAdminPage serves one tab of the administrator pages: tab is its
// slug ("costs"), or empty for the first.
//
// The administrator UI was one page with every tab on it, and it took a few
// seconds to arrive: the server built every section, then the browser
// mounted all of them and fetched every table behind every tab at once,
// seventy requests through the six connections it has, with the slow ones
// (a version probe of each external binary, a summary per app) holding up
// the ones the open tab needed. One page per tab builds and fetches only
// what is on screen, and a change made on one tab is on the next because
// the next is rendered fresh when it is opened.
func (a *AdminApp) serveNewAdminPage(w http.ResponseWriter, r *http.Request, tab string) {
	a.serveAdminPage(w, r, tab, nil)
}

// serveAdminPage serves an administrator page with tab marked in the menu.
// With open set, the page holds what open returns in place of the tab's own
// sections (one app's page, under Apps), and is a 404 when open returns nil.
func (a *AdminApp) serveAdminPage(w http.ResponseWriter, r *http.Request, tab string, open func(*panes) *appPage) {
	if !a.requireAdmin(w, r) {
		return
	}
	started := time.Now()
	page := ui.Page{
		Title:     "Administrator",
		ShowTitle: true,
		BackURL:   "/",
		// Head registers the per-row "Reset password" modal client action.
		// Migrated off a hand-written <script> blob onto the typed ui.Head
		// builder — the framework assembles the <script>, the register call,
		// and the readiness guard.
		Head: ui.NewHead().
			CSS(adminUsersCSS).
			JS(adminUsersModalJS).
			JS(ArtifactClientJS).
			JS(artifactDownloadHelper).
			JS(artifactExportControls).
			JS(artifactImportPreviewJS).
			JS(peerKeyShowOnceJS).
			ClientAction("admin_reset_password", adminResetPasswordAction).
			ClientAction("artifacts_import_preview", artifactsImportPreviewAction).
			ClientAction("connectors_export", connectorsExportAction).
			ClientAction("connectors_export_all", connectorsExportAllAction).
			ClientAction("connector_edit_spec", connectorEditSpecAction).
			ClientAction("system_deps_test", systemDepsTestAction).
			ClientAction("connector_webhook", connectorWebhookAction).
			ClientAction("configure_backend", configureBackendAction).
			ClientAction("add_tool_from_template", addToolFromTemplateAction).
			ClientAction("add_extension", addExtensionAction).
			ClientAction("template_install", templateInstallAction).
			ClientAction("connector_add_another", connectorAddAnotherAction).
			ClientAction("template_export", templateExportAction).
			ClientAction("template_import", templateImportAction).
			ClientAction("configure_tool", configureToolAction).
			ClientAction("tools_export", toolsExportAction).
			ClientAction("tools_export_all", toolsExportAllAction).
			ClientAction("category_scope_manage", categoryScopeManageAction).
			ClientAction("credentials_export", credentialsExportAction).
			ClientAction("credentials_export_all", credentialsExportAllAction).
			ClientAction("skills_export", skillsExportAction).
			ClientAction("skills_export_all", skillsExportAllAction).
			ClientAction("peer_key_issued", peerKeyIssuedAction).
			ClientAction("peer_key_reissue", peerKeyReissueAction).
			ClientAction("artifacts_export_all", artifactsExportAllAction),
		MaxWidth: "1200px", // desktop admin: wide enough for full-width tables in a single column
		Grid:     false,    // single column: sections stack vertically within each tab (Wide flags become no-ops)
		// The tabs are Nav links, one page each, across the header; the ones
		// that do not fit fold into its More menu. A tab's sections are a rail
		// down its left, one shown at a time.
		SectionNav: true,
	}
	// The page body, one builder per tab area (page_<area>.go). Appended in
	// this order; within a tab, sections keep the order their builder lists.
	// Each builder is timed, so a slow page can name the one that was slow
	// (see the log line at the end) rather than only how long they all took.
	var slowest []string
	timed := func(name string, build func() []ui.Section) []ui.Section {
		t0 := time.Now()
		secs := build()
		if d := time.Since(t0); d > 50*time.Millisecond {
			slowest = append(slowest, name+" "+d.Round(time.Millisecond).String())
		}
		return secs
	}
	for _, b := range []struct {
		name  string
		build func() []ui.Section
	}{
		{"system", a.systemSections}, // the activity log among them
		{"llm", a.llmSections},
		{"cost", a.costSections},
		{"capabilities", a.capabilitiesSections},
		{"maintenance", a.maintenanceSections},
		{"import-export", a.importExportSections},
		{"credentials", a.credentialsSections},
		{"governance", a.governanceSections},
		{"extensions", a.extensionsSections},
		{"offered-choices", func() []ui.Section { return []ui.Section{offeredChoicesSection()} }},
		{"research-sources", a.researchSourcesSections},
		{"keystore", a.keystoreSections},
		{"tools", a.toolsSections},
		{"skills", a.skillsSections},
	} {
		page.Sections = append(page.Sections, timed(b.name, b.build)...)
	}
	// Which sections span the full grid width (tables, the cost chart,
	// multi-pane Stacks, the DB browser) vs the narrow config forms that pack
	// two-up. The tab each section lands on is sectionTab, at package level
	// because the Apps tab names those tabs in its links.
	wideSections := map[string]bool{
		"System Status": true, "Users": true, "LLM Routing": true, "Activity Log": true,
		"Cost History (Last 30 Days)": true, "Cost by source": true, "Scheduled Tasks": true,
		"API Credentials": true, "MCP Servers": true, "Connectors": true, "Names no longer offered": true, "Other integrations": true, "Research Sources": true,
		"Keystore": true, "Drafted tools waiting for review": true, "Users' tools": true, "System Hooks": true,
		"Requests to share": true, "Shared tools": true,
		"Agent-Scoped Tools": true, "Orphaned Tools": true,
		"Skills": true, "Teams": true, "People outside gohort": true,
		"Templates":  true,
		"Migrations": true, "Database Browser": true,
		"Agent Capabilities: Outward & Spending": true,
	}
	// Wide sections that still shouldn't run the width of a large monitor.
	// The cost chart needs more than one grid column, but past ~900px the
	// bars stop being a chart and become wallpaper.
	sectionMaxWidth := map[string]string{
		"Cost History (Last 30 Days)": "900px",
	}
	// Generated tunable sections — one FormPanel per registered category, built
	// from core's tunable registry so a newly-registered knob appears here with
	// no admin edit. Pre-grouped under the "Tuning" tab; the loop below skips
	// them (their titles aren't in sectionGroup, so their Group is preserved).
	page.Sections = append(page.Sections, timed("tunables", buildTunableSections)...)
	// Resource sharing — lending this instance's capabilities to a peer
	// instance. Lands under Capabilities via sectionGroup, next to the
	// Embeddings and Media Generation settings it shares.
	page.Sections = append(page.Sections, timed("peer-sharing", peerSharingSections)...)
	// The Apps tab: one table of apps (apps_tab.go).
	builtOwn := time.Since(started)
	panes := newPanes(r)
	page.Sections = append(page.Sections, timed("apps", panes.sections)...)
	builtSources := time.Since(started) - builtOwn
	// App-contributed admin sections — framework tuning that belongs in admin
	// (e.g. the prompt-block editor), self-registered via core so admin doesn't
	// import the app. Each carries its own Group/Wide; its Head brings any
	// client actions the section's controls need. Read once, with the Apps
	// tab, which asks the same sources about every app.
	order := map[string]int{} // section title -> its Order within its tab
	for _, e := range panes.entries {
		page.ExtraHeadHTML += e.Head
		// An app's section filed under Apps is on that app's own page.
		if e.App != "" && sectionTab(e.Section.Title, e.Section.Group) == AppsTabGroup {
			continue
		}
		page.Sections = append(page.Sections, e.Section)
		if e.Order != 0 {
			order[e.Section.Title] = e.Order
		}
	}
	// A builder can hand back a placeholder: no title, no body. Each one
	// made a tab (its empty group reads as General) with nothing on it, which
	// loaded as a blank page. Nothing to show is no section.
	filled := page.Sections[:0:0]
	for _, s := range page.Sections {
		if s.Title != "" || s.Body != nil {
			filled = append(filled, s)
		}
	}
	page.Sections = filled
	for i := range page.Sections {
		t := page.Sections[i].Title
		// The map keys on TITLE, and an Apps row's title is an app's NAME —
		// which nobody here chose and which could one day be "Catalog" or
		// "Skills". A collision would yank that app's row onto another tab,
		// where it would read as the app having vanished. Sections that have
		// already declared this group keep it.
		page.Sections[i].Group = sectionTab(t, page.Sections[i].Group)
		if page.Sections[i].Group == "" {
			page.Sections[i].Group = "General"
		}
		if wideSections[t] {
			page.Sections[i].Wide = true
		}
		if w, ok := sectionMaxWidth[t]; ok {
			page.Sections[i].MaxWidth = w
		}
	}
	// Tab order, then each section's place on its tab, both from adminLayout.
	// A section the layout does not list (one another app contributes, or a
	// Tuning category) follows the listed ones, in its own declared Order and
	// then the order it was built in. A tab the layout does not name sorts
	// last, where it is visible rather than ahead of Overview.
	rankOf := func(s ui.Section) (int, int) {
		tr, ok := tabRank[s.Group]
		if !ok {
			tr = len(adminLayout)
		}
		if n, ok := sectionRank[s.Title]; ok && sectionGroup[s.Title] == s.Group {
			return tr, n
		}
		return tr, 1000 + order[s.Title]
	}
	sort.SliceStable(page.Sections, func(i, j int) bool {
		ti, si := rankOf(page.Sections[i])
		tj, sj := rankOf(page.Sections[j])
		if ti != tj {
			return ti < tj
		}
		return si < sj
	})
	tabs := adminTabs(a.WebPath(), page.Sections)
	cur := -1
	for i, t := range tabs {
		if t.slug == tab || (tab == "" && i == 0) {
			cur = i
		}
	}
	if cur < 0 {
		http.NotFound(w, r)
		return
	}
	// Where every section is, by slug, before the page keeps only its own:
	// a link names a section's tab as it was when the link was written, and
	// the section is what it means.
	where := map[string]string{}
	for _, s := range page.Sections {
		if k := ui.SectionSlug(s.Title); k != "" && where[k] == "" {
			where[k] = ui.SectionSlug(s.Group)
		}
	}
	keep := page.Sections[:0:0]
	for _, s := range page.Sections {
		if ui.SectionSlug(s.Group) == tabs[cur].slug {
			keep = append(keep, s)
		}
	}
	page.Sections = keep
	if open != nil {
		pg := open(panes)
		if pg == nil {
			http.NotFound(w, r)
			return
		}
		page.Title, page.BackURL = pg.title, tabs[cur].url
		page.Sections = pg.sections
		page.ExtraHeadHTML += pg.head
	}
	for i, t := range tabs {
		page.Nav = append(page.Nav, ui.NavLink{Label: t.name, URL: t.url, Active: i == cur})
	}
	page.ExtraHeadHTML += adminTabRedirectScript(tabs, where, tabs[cur].slug)
	page.ExtraHeadHTML += deleteUserScript // the user table's Delete (delete_user.go)
	page.ServeHTTP(w, r)
	// A slow page says where its time went, in the log an operator already
	// reads: the admin's own sections, the sections other apps contribute
	// (every source runs for each page), or the page itself.
	if total := time.Since(started); total > 300*time.Millisecond {
		Log("[admin] the %s page took %s to build: own sections %s, contributed sections %s, render %s; slow builders: %s",
			tabs[cur].name, total.Round(time.Millisecond), builtOwn.Round(time.Millisecond),
			builtSources.Round(time.Millisecond), (total - builtOwn - builtSources).Round(time.Millisecond),
			strings.Join(slowest, ", "))
	}
}

// adminTab is one of the administrator pages: a tab's name, its slug and
// the address it is served at.
type adminTab struct {
	name, slug, url string
}

// adminTabs is every tab the sections fill, in the sections' order: the
// first at the app's root, the rest at their slug.
func adminTabs(base string, sections []ui.Section) []adminTab {
	var tabs []adminTab
	seen := map[string]bool{}
	for _, s := range sections {
		if seen[s.Group] {
			continue
		}
		seen[s.Group] = true
		url := base + "/" + ui.SectionSlug(s.Group)
		if len(tabs) == 0 {
			url = base + "/"
		}
		tabs = append(tabs, adminTab{name: s.Group, slug: ui.SectionSlug(s.Group), url: url})
	}
	return tabs
}

// adminTabRedirectScript sends a "#<tab>/<section>" address to the page that
// section is on. Links into the administrator pages name a tab and a section
// in the hash (ui.SectionOnTab), and a hash is not sent to the server: so the
// page the link lands on reads it and goes where the SECTION is, with the
// hash rewritten to name that tab, where the rail opens it. A link written
// before a section moved tabs still arrives. A hash naming a tab and no known
// section goes to that tab. Also on a hash change, for a link clicked on the
// page itself (an app's "On tab" links).
func adminTabRedirectScript(tabs []adminTab, where map[string]string, cur string) string {
	urls := map[string]string{}
	for _, t := range tabs {
		urls[t.slug] = t.url
	}
	b, _ := json.Marshal(urls)
	w, _ := json.Marshal(where)
	c, _ := json.Marshal(cur)
	return "<script>(function(){var tabs=" + string(b) + ",where=" + string(w) + ",cur=" + string(c) + ";" +
		"function go(){var h=location.hash.replace(/^#/,'');var i=h.indexOf('/');if(i<0)return;" +
		"var t=h.slice(0,i),s=h.slice(i+1),at=s&&where[s];" +
		"if(at&&tabs[at]){if(at!==t||at!==cur)location.replace(tabs[at]+'#'+at+'/'+s);return;}" +
		"if(t===cur||!tabs[t])return;location.replace(tabs[t]+location.hash);}" +
		"window.addEventListener('hashchange',go);go();})();</script>"
}

// adminSectionLink is the address of one of the administrator's sections, on
// whichever tab adminLayout puts it, relative to the admin's root: the tab's
// own page with the section in the hash, so it opens there with no hop.
func adminSectionLink(title string) string {
	tab := sectionGroup[title]
	return ui.SectionSlug(tab) + ui.SectionOnTab(tab, title)
}

// retiredTabs are tab addresses from an earlier layout, and the tab that took
// most of each one's sections. A bookmark to one arrives there, and its hash,
// if it names a section, then goes on to wherever that section is.
var retiredTabs = map[string]string{
	"system": "", "costs": "", "llms": "models", "capabilities": "services",
	"agents": "quality", "governance": "oversight", "sharing": "oversight",
	"extensions": "integrations", "files": "integrations",
}

// configureBackendAction (Connectors row "Configure") → Configure the row's backend.
var configureBackendAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  var name = ctx && ctx.record && ctx.record.name;
  if(!name){ window.uiAlert && window.uiAlert('No connector selected.'); return; }
  window.uiConfigureBackend(name, (ctx && ctx.reload) || function(){ location.reload(); });
}`

// addToolFromTemplateAction (Users' tools toolbar) → Add a tool from a tool
// template (same generic renderer, tool target → persists a TempTool).
var addToolFromTemplateAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  window.uiAddTool(function(){ if(window.uiInvalidate) window.uiInvalidate(['api/persistent-tools']); else location.reload(); });
}`

// addExtensionAction (Extensions catalog row "Add") → open the generic template
// form for the row's template. The row carries its Target, so one action serves
// both connectors and tools; the schema endpoint stamps `target` and the renderer
// routes the save (api/connector-config vs api/tool-config) itself.
var addExtensionAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  var r=(ctx&&ctx.record)||{};
  if(!r.name){ window.uiAlert && window.uiAlert('No extension selected.'); return; }
  var reload=(ctx&&ctx.reload)||function(){ location.reload(); };
  fetch('api/extension-template?target='+encodeURIComponent(r.target||'')+'&name='+encodeURIComponent(r.name),{cache:'no-store',credentials:'same-origin'})
    .then(function(res){ if(!res.ok) return res.text().then(function(t){throw new Error(t||('HTTP '+res.status));}); return res.json(); })
    .then(function(sc){ window.uiTemplateForm(sc, reload); })
    .catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
}`

// configureToolAction (Users' tools row "Configure") → re-open a template-authored
// tool in the generic form, prefilled from its provenance. Mirrors
// configureBackendAction on the connector side; the shared uiTemplateForm routes
// the save to api/tool-config (edit mode preserves share/ACL state).
var configureToolAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  var r=(ctx&&ctx.record)||{};
  var name=r.tool&&r.tool.name;
  if(!name){ window.uiAlert && window.uiAlert('No tool selected.'); return; }
  var reload=(ctx&&ctx.reload)||function(){ if(window.uiInvalidate) window.uiInvalidate(['api/persistent-tools']); else location.reload(); };
  fetch('api/tool-config?tool='+encodeURIComponent(name)+'&owner='+encodeURIComponent(r.owner||''),{cache:'no-store',credentials:'same-origin'})
    .then(function(res){ if(!res.ok) return res.text().then(function(t){throw new Error(t||('HTTP '+res.status));}); return res.json(); })
    .then(function(sc){ window.uiTemplateForm(sc, reload); })
    .catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
}`

// (credentialScopeManageAction removed — the per-agent credential scope pill is
// gone: tier-1 "which users" is the credential's Access button, and tier-2
// per-agent scope moved to the agent editor's "External credentials".)
