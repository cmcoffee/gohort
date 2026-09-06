// New admin page — framework-rendered with the simple sections migrated
// to core/ui's declarative components. Sections that need new framework
// primitives (DB Browser tree, Cost Rates split chart, Routing
// per-row select+number table, Watchers/Tools/Tasks edit dialogs) still
// live at /admin/legacy until each gets ported.

package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

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
	for _, s := range AllTunableSpecs() {
		if _, seen := byCat[s.Category]; !seen {
			order = append(order, s.Category)
		}
		// Bool knobs render as a real toggle, not a 0/1 number field.
		if s.Kind == KindBool {
			byCat[s.Category] = append(byCat[s.Category], ui.FormField{
				Field: s.Key,
				Label: toggleLabel(s.Label),
				Type:  "toggle",
				Help:  s.Help,
			})
			continue
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
		byCat[s.Category] = append(byCat[s.Category], ui.FormField{
			Field:    s.Key,
			Label:    s.Label + unit,
			Type:     "number",
			Help:     s.Help,
			Min:      int(s.Min),
			Max:      int(s.Max),
			Decimals: s.Decimals,
		})
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
				ResetURL:     "api/settings/reset-tunables?category=" + url.QueryEscape(cat),
				ResetLabel:   "Revert to defaults",
				ResetConfirm: "Revert the " + cat + " settings to their built-in defaults?",
				Fields:       byCat[cat],
			},
		})
	}
	return out
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

func (a *AdminApp) serveNewAdminPage(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
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
			JS(artifactDownloadHelper).
			JS(artifactExportControls).
			JS(artifactImportPreviewJS).
			JS(peerKeyShowOnceJS).
			ClientAction("admin_reset_password", adminResetPasswordAction).
			ClientAction("artifacts_import_preview", artifactsImportPreviewAction).
			ClientAction("connectors_export", connectorsExportAction).
			ClientAction("connectors_export_all", connectorsExportAllAction).
			ClientAction("connector_edit_spec", connectorEditSpecAction).
			ClientAction("add_image_backend", addImageBackendAction).
			ClientAction("configure_backend", configureBackendAction).
			ClientAction("configure_backend_pick", configureBackendPickAction).
			ClientAction("add_tool_from_template", addToolFromTemplateAction).
			ClientAction("template_add", templateAddAction).
			ClientAction("add_extension", addExtensionAction).
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
		Tabbed:     true,     // category tab bar across the top (the multiple menus); sections grouped below
		SectionNav: true,     // within each tab, a left-rail sub-nav of its sections (one at a time)
	}
	// The page body, one builder per tab area (page_<area>.go). Appended in
	// this order; within a tab, sections keep the order their builder lists.
	for _, build := range []func() []ui.Section{
		a.systemSections,
		a.llmSections,
		a.costSections,
		a.capabilitiesSections,
		a.maintenanceSections,
		a.credentialsSections,
		a.governanceSections,
		a.extensionsSections,
		a.sourceHooksSections,
		a.toolsSections,
		a.skillsSections,
	} {
		page.Sections = append(page.Sections, build()...)
	}
	// Category for each section's top tab, and which sections span the
	// full grid width (tables, the cost chart, multi-pane Stacks, the DB
	// browser) vs the narrow config forms that pack two-up. Kept here in
	// one place so the section literals above stay uncluttered and the
	// layout reads at a glance. Tab order follows first appearance in the
	// Sections slice, so the order of these groups is set by section order.
	sectionGroup := map[string]string{
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

		"Extensions": "Extensions",

		// Pluggable integrations you ADD — grouped under Extensions (vs Capabilities,
		// which are configured features like Image Generation / STT).
		"Templates":       "Extensions",
		"API Credentials": "Extensions", "MCP Servers": "Extensions", "Connectors": "Extensions",
		"Source Hooks": "Extensions", "Persistent Tools (Pending)": "Extensions",
		"Global Tools": "Extensions", "Agent-Scoped Tools": "Extensions", "Orphaned Tools": "Extensions",
		"Tool Groups": "Extensions",
		"Skills":      "Extensions", "Pipelines": "Extensions", "Catalog": "Extensions",

		"Agent Capabilities — Outward & Spending": "Agents",

		"Scheduled Tasks": "Maintenance", "Maintenance": "Maintenance",
		"Migrations": "Maintenance", "Vector Index": "Maintenance",
		"Database Browser": "Maintenance",
	}
	wideSections := map[string]bool{
		"System Status": true, "Users": true, "LLM Routing": true,
		"Cost History (Last 30 Days)": true, "Cost by source": true, "Scheduled Tasks": true,
		"API Credentials": true, "MCP Servers": true, "Connectors": true, "Source Hooks": true,
		"Persistent Tools (Pending)": true, "Global Tools": true,
		"Agent-Scoped Tools": true, "Orphaned Tools": true,
		"Tool Groups": true, "Skills": true, "Pipelines": true, "App Groups": true,
		"Extensions": true,
		"Templates":  true,
		"Catalog":    true,
		"Migrations": true, "Database Browser": true,
		"Agent Capabilities — Outward & Spending": true,
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
	page.Sections = append(page.Sections, buildTunableSections()...)
	// Resource sharing — lending this instance's capabilities to a peer
	// instance. Lands under Capabilities via sectionGroup, next to the
	// Embeddings and Image Generation settings it shares.
	page.Sections = append(page.Sections, peerSharingSections()...)
	// The Apps tab: one row per compiled app. Custom apps land on the SAME tab
	// through the runtime section source below, which is why this is appended
	// first — compiled apps, then whatever people have authored.
	page.Sections = append(page.Sections, a.appsTabSections()...)
	// App-contributed admin sections — framework tuning that belongs in admin
	// (e.g. the prompt-block editor), self-registered via core so admin doesn't
	// import the app. Each carries its own Group/Wide; its Head brings any
	// client actions the section's controls need.
	for _, e := range AdminSectionEntriesFor(r) {
		page.Sections = append(page.Sections, e.Section)
		page.ExtraHeadHTML += e.Head
	}
	for i := range page.Sections {
		t := page.Sections[i].Title
		// The map keys on TITLE, and an Apps row's title is an app's NAME —
		// which nobody here chose and which could one day be "Catalog" or
		// "Skills". A collision would yank that app's row onto another tab,
		// where it would read as the app having vanished. Sections that have
		// already declared this group keep it.
		if g, ok := sectionGroup[t]; ok && page.Sections[i].Group != AppsTabGroup {
			page.Sections[i].Group = g
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
	groupRank := map[string]int{"System": 0, "Costs": 1, "LLMs": 2, "Capabilities": 3, "Agents": 4, "Extensions": 5, "Tools": 6, "Tuning": 7, "Prompts": 8, "Maintenance": 9}
	sort.SliceStable(page.Sections, func(i, j int) bool {
		return groupRank[page.Sections[i].Group] < groupRank[page.Sections[j].Group]
	})
	page.ServeHTTP(w, r)
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

// templateAddAction (Templates catalog row "Add") → open the generic renderer for
// the chosen template, resolving connector vs tool by the row's target.
var templateAddAction = `function(ctx){
  if(!window.uiTemplateForm){` + connectorFormDef + `}
  var r=(ctx&&ctx.record)||{}; var target=r.target||'connector'; var name=r.name;
  if(!name){ window.uiAlert && window.uiAlert('No template selected.'); return; }
  var base=(target==='tool')?'api/tool-template':'api/connector-template';
  fetch(base+'?name='+encodeURIComponent(name),{cache:'no-store',credentials:'same-origin'})
    .then(function(res){ if(!res.ok) return res.text().then(function(t){throw new Error(t||('HTTP '+res.status));}); return res.json(); })
    .then(function(sc){ window.uiTemplateForm(sc, function(){ location.reload(); }); })
    .catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
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
    if(!img.length){ window.uiAlert && window.uiAlert('No image backend yet — add one first.'); return; }
    if(img.length===1){ window.uiConfigureBackend(img[0].name, function(){ location.reload(); }); return; }
    var names=img.map(function(x){return x.name;});
    var pick=window.prompt('Configure which backend?\n'+names.join(', '), names[0]);
    if(pick){ window.uiConfigureBackend(pick.trim(), function(){ location.reload(); }); }
  }).catch(function(e){ window.uiAlert && window.uiAlert((e&&e.message)||(''+e)); });
}`

// (credentialScopeManageAction removed — the per-agent credential scope pill is
// gone: tier-1 "which users" is the credential's Access button, and tier-2
// per-agent scope moved to the agent editor's "Credentials this agent may use".)
