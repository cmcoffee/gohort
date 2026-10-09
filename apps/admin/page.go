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

// sectionGroup is the top tab for each of admin's own sections, by TITLE.
// Tab order follows first appearance in the Sections slice, so the order of
// these groups is set by section order. Read through sectionTab.
var sectionGroup = map[string]string{
	"System Status": "System", "Site Settings": "System",
	"Users": "System", "Add account": "System", "Default Apps": "System",
	"App Groups": "System",

	"Cost History (Last 30 Days)": "Costs", "Cost by source": "Costs", "Prices": "Costs",

	"Worker LLM": "LLMs", "Lead LLM": "LLMs", "LLM Routing": "LLMs", "Model Privacy": "LLMs",
	"Ollama Proxy": "LLMs", "Agent Loop Tuning": "LLMs",
	"Local Model Scheduler": "LLMs",

	"Embeddings":                "Capabilities",
	"Audio Transcription (STT)": "Capabilities", "Image Generation": "Capabilities",
	"Resource Sharing": "Capabilities", "Peers": "Capabilities", "Shared With": "Capabilities",
	"Web Search": "Capabilities", "Mail (SMTP)": "System",
	"Network Timeouts": "Tuning",

	"Templates": "Extensions", "Import and export": "Extensions",

	// Pluggable integrations you ADD — grouped under Extensions (vs Capabilities,
	// which are configured features like Image Generation / STT).
	"API Credentials": "Extensions", "MCP Servers": "Extensions", "Connectors": "Extensions",
	"Source Hooks": "Extensions", "Persistent Tools (Pending)": "Extensions",
	"Global Tools": "Extensions", "Agent-Scoped Tools": "Extensions", "Orphaned Tools": "Extensions",
	"Tool Groups": "Extensions",
	"Skills":      "Extensions", "Pipelines": "Extensions",

	"Agent Capabilities: Outward & Spending": "Agents",

	"Scheduled Tasks": "Maintenance", "Maintenance": "Maintenance",
	"Migrations": "Maintenance", "Vector Index": "Maintenance",
	"Database Browser": "Maintenance",
	// The three maintenance groups. They were absent from this map, so
	// they kept the empty Group that means "General" and sat on a tab of
	// their own away from the Maintenance ones — which also made their
	// being collapsed read as arbitrary rather than as "the rarely-used
	// ones are closed", which is the rule they were written under.
	"Reclaim space": "Maintenance", "Reports": "Maintenance",
	"Housekeeping": "Maintenance",
	// Also unmapped, and landing in General for the same reason.
	"Channel Wake Rules": "System", "Feature Access": "System",
	"System Dependencies": "Capabilities", "Page Rendering (Browser)": "Capabilities",
	"MCP Tools (exposed to external clients)": "Extensions", "Bridges": "Extensions",
	"Categories": "Extensions",

	// Who owns what, across every kind of owned thing. Six sections that
	// are one subject, which is a tab rather than six strays in General.
	"Rules":                  "Governance",
	"User-owned credentials": "Governance", "Global tools": "Governance",
	"User-owned agents": "Governance", "User-owned pipelines": "Governance",
	"User-owned machines": "Governance", "Pending promotions": "Governance",
}

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
	// tab as a compact left-rail side-index of areas (Network Timeouts, Limits,
	// …) with only the selected category's fields shown — instead of one long
	// stack that grows every time a knob is registered. This replaces the app's
	// own embedded NavShell rail with the shared menu system, so Tuning navigates
	// exactly like Extensions. Each category still saves as you edit and carries
	// its own Revert.
	out := make([]ui.Section, 0, len(order))
	for _, cat := range order {
		out = append(out, ui.Section{
			Title:    cat,
			Group:    "Tuning",
			Wide:     true, // each category pane wants full width, not the two-up config column
			Subtitle: "Saved automatically as you edit.",
			Body: ui.FormPanel{
				Source:       "api/settings",
				Method:       settingsSaveMethod,
				ResetURL:     "api/settings/reset-tunables?category=" + url.QueryEscape(cat),
				ResetLabel:   "Revert to defaults",
				ResetConfirm: "Revert the " + cat + " settings to their built-in defaults?",
				Fields:       byCat[cat],
				RefreshOn:    paneSources[cat],
			},
		})
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

// sourceHookFormTemplates turns the built-in SourceHookTemplates into
// FormPanel presets for the "Start from template" dropdown — so an
// operator can pick PubMed / OpenAlex / Westlaw / etc. and have the
// endpoint + field mappings filled in, then just add the API key.
// sourceHookFormFields is the editable field set for a source hook, shared
// by the "Add source" modal (empty create form) and the per-row "Edit"
// expand (pre-filled from api/source-hooks?name={name}). Field names match
// the SourceHook json tags so the GET-one response pre-fills directly.
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
			ClientAction("connector_webhook", connectorWebhookAction).
			ClientAction("add_image_backend", addImageBackendAction).
			ClientAction("configure_backend", configureBackendAction).
			ClientAction("configure_backend_pick", configureBackendPickAction).
			ClientAction("add_tool_from_template", addToolFromTemplateAction).
			ClientAction("add_extension", addExtensionAction).
			ClientAction("template_install", templateInstallAction).
			ClientAction("template_export", templateExportAction).
			ClientAction("template_import", templateImportAction).
			ClientAction("configure_tool", configureToolAction).
			ClientAction("tools_export", toolsExportAction).
			ClientAction("tools_export_all", toolsExportAllAction).
			ClientAction("tool_promote_global", toolPromoteGlobalAction).
			ClientAction("pipeline_scope_manage", pipelineScopeManageAction).
			ClientAction("category_scope_manage", categoryScopeManageAction).
			ClientAction("credentials_export", credentialsExportAction).
			ClientAction("credentials_export_all", credentialsExportAllAction).
			ClientAction("skills_export", skillsExportAction).
			ClientAction("skills_export_all", skillsExportAllAction).
			ClientAction("peer_key_issued", peerKeyIssuedAction).
			ClientAction("peer_key_reissue", peerKeyReissueAction).
			ClientAction("artifacts_export_all", artifactsExportAllAction),
		MaxWidth:   "1200px", // desktop admin: wide enough for full-width tables in a single column
		Grid:       false,    // single column: sections stack vertically within each tab (Wide flags become no-ops)
		SectionNav: true,     // a left-rail sub-nav of the tab's sections (one at a time); the tabs are Nav links, one page each
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
		{"system", a.systemSections},
		{"llm", a.llmSections},
		{"cost", a.costSections},
		{"capabilities", a.capabilitiesSections},
		{"maintenance", a.maintenanceSections},
		{"import-export", a.importExportSections},
		{"credentials", a.credentialsSections},
		{"governance", a.governanceSections},
		{"extensions", a.extensionsSections},
		{"source-hooks", a.sourceHooksSections},
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
		"System Status": true, "Users": true, "LLM Routing": true,
		"Cost History (Last 30 Days)": true, "Cost by source": true, "Scheduled Tasks": true,
		"API Credentials": true, "MCP Servers": true, "Connectors": true, "Source Hooks": true,
		"Persistent Tools (Pending)": true, "Global Tools": true,
		"Agent-Scoped Tools": true, "Orphaned Tools": true,
		"Tool Groups": true, "Skills": true, "Pipelines": true, "App Groups": true,
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
	// Embeddings and Image Generation settings it shares.
	page.Sections = append(page.Sections, timed("peer-sharing", peerSharingSections)...)
	// The Apps tab: one row per compiled app. Custom apps land on the SAME tab
	// through the runtime section source below, which is why this is appended
	// first — compiled apps, then whatever people have authored.
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
		page.Sections = append(page.Sections, e.Section)
		page.ExtraHeadHTML += e.Head
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
	// Tab order (and clustering of each group's sections) — a stable sort
	// by this rank so the tabs read in a sensible order regardless of the
	// section authoring order above; sections keep their relative order
	// within each group.
	// An unranked group sorts to 0 and ties with System, so every name used
	// above has to appear here. "Tools" is deliberately absent now: the tool
	// sections all live under Extensions, and a rank for a tab nothing lands on
	// is a tab that never appears.
	groupRank := map[string]int{"System": 0, "Costs": 1, "LLMs": 2, "Capabilities": 3, "Agents": 4, "Governance": 5, "Extensions": 6, "Apps": 7, "Tuning": 8, "Prompts": 9, "Maintenance": 10}
	sort.SliceStable(page.Sections, func(i, j int) bool {
		gi, gj := groupRank[page.Sections[i].Group], groupRank[page.Sections[j].Group]
		if gi != gj {
			return gi < gj
		}
		return order[page.Sections[i].Title] < order[page.Sections[j].Title]
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
	keep := page.Sections[:0:0]
	for _, s := range page.Sections {
		if ui.SectionSlug(s.Group) == tabs[cur].slug {
			keep = append(keep, s)
		}
	}
	page.Sections = keep
	for i, t := range tabs {
		page.Nav = append(page.Nav, ui.NavLink{Label: t.name, URL: t.url, Active: i == cur})
	}
	page.ExtraHeadHTML += adminTabRedirectScript(tabs, tabs[cur].slug)
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
// tab is on. Links into the administrator pages name a tab and a section
// in the hash (ui.SectionOnTab), as they did when every tab was one page,
// and a hash is not sent to the server: so the page the link lands on
// reads it, and if it names another tab, goes there with the hash kept,
// where the rail opens the section. Also on a hash change, for a link
// clicked on the page itself (an app's "On tab" links).
func adminTabRedirectScript(tabs []adminTab, cur string) string {
	urls := map[string]string{}
	for _, t := range tabs {
		urls[t.slug] = t.url
	}
	b, _ := json.Marshal(urls)
	c, _ := json.Marshal(cur)
	return "<script>(function(){var tabs=" + string(b) + ",cur=" + string(c) + ";" +
		"function go(){var h=location.hash.replace(/^#/,'');var i=h.indexOf('/');if(i<0)return;" +
		"var t=h.slice(0,i);if(t===cur||!tabs[t])return;location.replace(tabs[t]+location.hash);}" +
		"window.addEventListener('hashchange',go);go();})();</script>"
}

// addImageBackendAction (Image Generation toolbar) → Add flow for image templates.
var addImageBackendAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  window.uiAddBackend('Image generation', function(){ location.reload(); });
}`

// configureBackendAction (Connectors row "Configure") → Configure the row's backend.
var configureBackendAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  var name = ctx && ctx.record && ctx.record.name;
  if(!name){ window.uiAlert && window.uiAlert('No connector selected.'); return; }
  window.uiConfigureBackend(name, (ctx && ctx.reload) || function(){ location.reload(); });
}`

// addToolFromTemplateAction (Global Tools toolbar) → Add a tool from a tool
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

// configureToolAction (Global Tools row "Configure") → re-open a template-authored
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

// configureBackendPickAction (Image Generation toolbar) → pick an image backend to configure.
var configureBackendPickAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  fetch('api/connectors',{cache:'no-store',credentials:'same-origin'}).then(function(r){return r.json();}).then(function(rows){
    var img=(rows||[]).filter(function(x){ return x.is_image; });
    if(!img.length){ window.uiAlert && window.uiAlert('No image backend yet: add one first.'); return; }
    if(img.length===1){ window.uiConfigureBackend(img[0].name, function(){ location.reload(); }); return; }
    var names=img.map(function(x){return x.name;});
    var pick=window.prompt('Configure which backend?\n'+names.join(', '), names[0]);
    if(pick){ window.uiConfigureBackend(pick.trim(), function(){ location.reload(); }); }
  }).catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
}`

// (credentialScopeManageAction removed — the per-agent credential scope pill is
// gone: tier-1 "which users" is the credential's Access button, and tier-2
// per-agent scope moved to the agent editor's "External credentials".)
