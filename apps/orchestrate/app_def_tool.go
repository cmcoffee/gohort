// `app_def` — grouped tool for authoring data-driven gohort APPS: real
// in-dashboard surfaces composed from ui primitives (FormPanel, Table,
// DisplayPanel, EmptyState), stored as an AppSpec and served by apps/customapps
// at /custom/<slug>/. This is the tool that lets Builder answer "build me an
// app" with an ACTUAL gohort app instead of a standalone HTML file.
//
//	create / update — author an app (name, sections[]).
//	list            — see the user's apps.
//	get             — read one app's definition.
//	delete          — remove an app.
//
// The LLM describes the app declaratively (sections of kind form/table/display/
// chart/empty/chat/workbench/actions, plus an html raw-HTML escape hatch); this
// tool translates that into a ui.Page, marshals it with
// ConfigJSON, and stores the bytes via core.SaveAppSpec. customapps serves the
// stored page + a generic per-app record store (the form writes records, the
// table lists them) with no per-app Go code. A chat section binds the app's
// agent (agent_id) to a live chat panel served under /custom/<slug>/chat/*.
//
// Specs are stored owner-keyed in the SHARED deployment root (core/appspec.go),
// NOT this app's DB bucket — otherwise a spec written here would be invisible to
// the customapps host, which holds a different bucket.
//
// Builder-only, same as the pipeline tool — authoring apps is Builder's job.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/tools/appscript"
)

func (t *chatTurn) appDefToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "app_def",
			Description: "Author and manage data-driven gohort APPS — real in-dashboard surfaces (NOT standalone HTML files) composed from ui primitives and served at /custom/<slug>/. This is how you build a gohort app: describe it declaratively as a list of sections, and the framework renders it + gives it a generic per-app record store (a form section saves records, a table section lists them) with no hand-written HTML/CSS/JS.\n\nUse this when the user asks for \"an app\", \"a page where I can…\", \"a tool to track/manage X\", or any persistent multi-panel surface inside gohort. Do NOT produce a standalone downloadable HTML file for these requests — that's not a gohort app.\n\nActions: create (author a new app), update (revise one), list (see the user's apps), get (read one's section definition), delete.\n\nGOOD DEFAULTS (reach for these so the app feels considered): a list/table section should always carry empty_text for its empty state; a creation form should use submit_label (a deliberate \"Add\" button) and modal=true so \"new\" opens a structured dialog rather than an always-visible form; pair a create FORM with a TABLE over the same records so new entries appear in the list, and mark that table editable so entries can be fixed in place. A standalone EMPTY section gives a \"nothing selected yet\" middle panel.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "One of: create | update | test | list | get | delete | help. After authoring an app with script-backed data_sources or actions, run test to EXECUTE each script and see its real output/errors before telling the user the app is ready. Pass sample=[{...}] to test the full form→data-source→output chain with example form data even before any records exist."},
				"sample": {
					Type:        "array",
					Description: "(test) Example form submissions to run the data sources/actions against, standing in for the live record store. Each item is an object keyed by the FORM's field names — exactly what a record looks like after the user submits the form (e.g. [{\"city\":\"Santa Cruz, CA\"}]). Use this to test end-to-end before the app has any real records: the scripts receive these as the `records` env var, so you see whether 'add a location → forecast' actually produces output. If a data source returns [] against a sample that clearly should match, the script isn't reading the records env var (or has the wrong field name).",
					Items:       &ToolParam{Type: "object"},
				},
				"params": {
					Type:        "object",
					Description: "(test) Optional query-param inputs to simulate alongside sample, handed to each script as env vars (for filter-style data sources that read a param). Most form-driven apps don't need this — inputs come from sample/records, not params.",
				},
				"name":        {Type: "string", Description: "App name (shown in the dashboard). Required for create."},
				"slug":        {Type: "string", Description: "(create) URL slug, e.g. 'reading-list' → /custom/reading-list/. Optional — derived from the name when omitted. Lowercase letters, digits, hyphens."},
				"id":          {Type: "string", Description: "(update/get/delete) The app's slug, identifying which app to act on."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the app is for (shown on the Custom Apps index)."},
				"record_key":  {Type: "string", Description: "(create/update) The primary-key field of each record. Default 'id' — the host allocates one on save. Only override if the records have a natural key."},
				"full_width":  {Type: "boolean", Description: "(create/update) Render the page EDGE-TO-EDGE instead of the default centered ~900px column. Set true for DATA-HEAVY surfaces — a dashboard, a wide table with many columns, a live monitor — where the extra horizontal space helps. Leave false (default) for forms and simple lists, which read better in a narrow column. A workbench app is always full-width regardless of this flag."},
				"private_db":  {Type: "boolean", Description: "(create/update) Give this app its OWN dedicated database file instead of sharing the common custom-apps store. Set true for a data-heavy app (many records, or data you want isolated / independently disposable) — its records live in a separate hardware-encrypted file, and deleting the app removes that data cleanly. Leave false (default) for ordinary small apps; the shared store is fine. Opt-in only: flipping this on an EXISTING app starts a fresh empty store and does NOT migrate records already saved in the shared store, so choose it at create time."},
				"agent_id":    {Type: "string", Description: "(create/update) Optional name or id of an agent that powers this app (reserved for the chat surface). Stored on the app; not required."},
				"data_sources": {
					Type:        "array",
					Description: "(create/update) Optional script-backed data endpoints — the way to give an app real LOGIC instead of plain stored-record CRUD. Each is {name, script, language?, capabilities?}. The script (python by default; set language:\"bash\" for shell) COMPUTES the JSON a table/display renders: the app's stored records arrive as an ENVIRONMENT VARIABLE named records holding a JSON string, and each request query param arrives as its own env var; the script must PRINT a JSON value to stdout (a JSON array for a table, a JSON object for a display). Read the records like this (python): `import os, json` then `records = json.loads(os.environ.get('records', '[]'))` — do NOT write `json.loads(\"records\")` (that parses the literal word, not the data) or `os.environ['records']` without a default. Bash: `echo \"$records\"`. INPUTS COME FROM records, NOT query params: when the app has a FORM that saves entries (a city, a name, an item), the user's typed value was saved as a RECORD — read it from `records`, e.g. `recs = json.loads(os.environ.get('records','[]')); city = recs[-1].get('city')` (match the form field's name). Nothing passes form fields as query-param env vars, so `os.environ.get('city')` is ALWAYS empty and the panel shows nothing — this is the #1 reason a form-driven app looks 'disconnected' (you add a location but no forecast appears). A data source reads the SAVED RECORDS; query params are only for filters you wire yourself. CRITICAL: the section fetches this data source the moment the page LOADS, with NO query params set yet — so read EVERY param defensively (`os.environ.get('city', '')`, never `os.environ['city']`) and return valid JSON even when params are empty, or the app errors on open. To pull external data, call the gohort hook (fetch is granted by default — you need NOT declare capabilities for a public URL): `from gohort import fetch_url` then `r = fetch_url(url)` — the host performs the fetch (the sandbox has no raw network); the result is a dict `{status, headers, body}`. fetch_url RAISES on a transport failure (bad/blocked host, timeout), so wrap it: `try: r = fetch_url(url)` / `except Exception as e:` and still PRINT valid JSON (e.g. `[]` or `{\"error\": str(e)}`) so the panel renders instead of 500ing. Then check `r['status']` and `json.loads(r['body'])`. URL-ENCODE every value you interpolate into a URL: `from urllib.parse import quote` then `url = f\"...?name={quote(city)}\"` — an unencoded space or comma (e.g. a city like 'Santa Cruz, CA') makes the fetch REFUSE the URL. Network HTTP libs (requests, urllib.request, curl, http.client) are BLOCKED — only fetch_url reaches the network — but urllib.parse (quote/urlencode) is fine and is the right tool for building URLs. A table/display section then sets source_script:\"<name>\" to read from it instead of the record store. Run app_def action=test after authoring to confirm the script prints valid JSON. Use this for apps that fetch/aggregate/transform (a dashboard over an API, a computed report) rather than just collecting form entries. Owner-only today.",
					Items:       &ToolParam{Type: "object"},
				},
				"actions": {
					Type:        "array",
					Description: "(create/update) Optional script-backed action buttons — the WRITE side of the logic seam (data_sources is the read side). Each is {name, label?, desc?, script, language?, capabilities?, confirm?}. A button labeled `label` runs the script when clicked; the script receives the app's stored records as an env var named records (a JSON string — read it with `json.loads(os.environ.get('records', '[]'))`, NOT `json.loads(\"records\")`) plus any params, and PRINTS a JSON OBJECT {message?: string, records?: [...]}. The FRAMEWORK upserts any returned records into the app's store (so they appear in the tables — your script does NOT write the store itself) and shows the message. Use for app verbs like \"Sync now\", \"Generate\", \"Refresh from API\". Surface the buttons with an `actions` section. Set confirm for destructive ones. capabilities work the same as data_sources (e.g. [\"fetch\"] for `from gohort import fetch`).",
					Items:       &ToolParam{Type: "object"},
				},
				"sections": {
					Type:        "array",
					Description: "(create/update) Ordered sections, each an object with a `kind` plus kind-specific fields. Every section may set `title` and `subtitle`.\n\nkind=\"form\" — a create form. Fields: `fields` (array of {field, label, type, placeholder, rows, help}; type is text|textarea|number|select|toggle|tags|password, default text; select needs `options`:[{value,label}]), `submit_label` (button text, default \"Add\"), `modal` (boolean — when true the form opens from a \"New\" button in a dialog; the signature structured-create pattern). The form saves a record to the app's store.\n\nkind=\"table\" — a list of the app's records. Fields: `columns` (array of {field, label, flex, mute, link}; set `link` to the name of another field holding a URL to render THIS cell as a clickable link — e.g. a story row {title, url} uses column {field:\"title\", link:\"url\"}. NEVER put raw <a>…</a> HTML in a cell value; cells are escaped and it shows as literal markup — use the link field instead), `empty_text` (shown when there are no records — ALWAYS set this), `editable` (boolean — adds an Edit button per row that opens the record in a PREFILLED dialog; the user fixes a typo or updates a value and saves in place. Fields default to the create form's fields (same types/labels/selects), or pass `edit_fields` (same shape as form fields) to edit a different subset. Set this on any record-store table paired with a create form — records the user typed are records the user will want to fix. NOT for source_script tables: computed rows aren't stored records), `deletable` (boolean — adds a Delete button per row), `auto_refresh_ms` (poll interval; 2000 keeps the list live as records are added), `source_script` (name of a data_sources entry — when set, the table's rows come from that SCRIPT instead of the record store; the script must print a JSON array).\n\nkind=\"display\" — a read-only labeled-value panel. Fields: `pairs` (array of {label, field}), `source_script` (name of a data_sources entry whose script prints a JSON object; defaults to the record store when omitted).\n\nkind=\"chart\" — a bar / line / area / pie chart. Set `chart_type` (bar|line|area|pie; default bar). Data is EITHER inline — `labels`:[...] + `series`:[{name, points:[numbers]}] (one point per label; for pie use `series`:[{name, value}]) — OR computed: set `source_script` to a data_sources entry whose script PRINTS a JSON object {\"labels\":[...], \"series\":[...]} (optionally chart_type/title/options), i.e. a chart OF the app's records. Options (flat on the section): `stacked` (bars), `height`. The section title is the heading; the chart draws no duplicate title. Use this to VISUALIZE what a table lists — e.g. a form logging {day, amount} + a data source that buckets them, rendered as a bar chart.\n\nkind=\"actions\" — a row of script-backed action buttons (one per entry in the app's top-level `actions`). Clicking a button runs its script and the framework persists what it returns + refreshes the tables. No fields needed; declare the scripts in `actions` (see the actions parameter). Use for app verbs (Sync, Generate, Refresh).\n\nkind=\"empty\" — a centered empty-state placeholder (for a 'nothing selected' panel). Fields: `icon` (an emoji), `title`, `hint`.\n\nkind=\"html\" — a raw-HTML escape hatch. Field: `html` (the markup, rendered VERBATIM and unescaped; inline <script> runs). This is the ONLY way to put hand-written HTML/CSS/JS into a custom app, and it is a LAST RESORT — reach for a typed section (form/table/display/chart) first, because those give you the record store, editing, refresh, and styling for free. Use html only for a bespoke widget or layout the typed primitives genuinely can't express. The blob is trusted (owner-authored, owner-served), so it is not sanitized — do not interpolate untrusted data into it.\n\nkind=\"chat\" — a live chat panel bound to the app's agent (REQUIRES agent_id on the app). Sessions + streaming reply are wired automatically to the bound agent; the user talks to it right inside the app. Fields: `list_title`, `empty_text`, `placeholder`. This is how you build a one-app assistant surface (e.g. sessions list + a viewer + a chat that drafts content) instead of sending the user off to a separate /chat URL.\n\nkind=\"workbench\" — the THREE-COLUMN document workbench: an item list (left), a rendered document VIEWER of the selected item (center), and a chat bound to the app's agent (right). REQUIRES agent_id. This is the right shape for 'a list of docs/guides/notes, a formatted reader in the middle, and an AI assistant that helps write them' — clicking a list item shows it; the chat drafts content; each chat reply has an 'Add to document' button that appends it into the open item, and the viewer re-renders. ONE workbench section IS the whole app (don't add other sections). Fields: `item_label` (record field for the list label, default title), `body_field` (the markdown field shown + appended-to in the viewer, default content), `item_noun` (e.g. 'guide' — used in the New button + 'Add to <noun>' label), `new_fields` (form fields for creating an item; defaults to a single title field), `list_title`, `empty_title`, `empty_hint`, `empty_icon`.\n\nThe document body is MARKDOWN, rendered as a formatted HTML-like document — '## Section' and '### Sub-section' headings, lists, code blocks, etc. The DATA LAYER IS THE APP. The workbench AUTOMATICALLY gives the bound agent an 'add_section(section_title, markdown)' tool that writes a section straight into the OPEN document's record (the store the viewer renders) — so 'add a section about hooks' appears in the guide with no button. You do NOT build that tool; it's provided. So a workbench agent should be told to call add_section to commit content, and must NOT be given its OWN storage tools (no file/python/JSON, no custom save) — those write to its workspace, never reaching the viewer. (A manual 'Add to document' button on each reply is also available as a fallback.)\n\nMinimal good app = a form (modal=true, submit_label) + a table (empty_text, deletable, auto_refresh_ms) over the same records. For an assistant app, add agent_id + a chat section. For a 'sessions | viewer | chat' three-panel app, use ONE workbench section.",
					Items:       &ToolParam{Type: "object"},
				},
			},
			Required: []string{"action"},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
			switch action {
			case "create", "update":
				return t.appDefCreateOrUpdate(args, action == "update")
			case "list":
				return t.appDefList()
			case "get":
				return t.appDefGet(args)
			case "test":
				return t.appDefTest(args)
			case "delete":
				return t.appDefDelete(args)
			case "help", "":
				return appDefHelpText, nil
			default:
				return "", fmt.Errorf("unknown action %q — use create | update | test | list | get | delete | help", action)
			}
		},
	}
}

const appDefHelpText = `app_def actions:
- create {name, slug?, description?, record_key?, sections:[…]} — author a data-driven app, served at /custom/<slug>/.
- update {id(slug), …, sections:[…]} — revise an app in place.
- list — your apps: [{slug, name, desc}].
- get  {id(slug)} — one app's full section definition.
- test {id(slug), sample?:[{...}], params?:{...}} — RUN every data_source + action script and report each one's output/errors (catches broken scripts before the user opens the app). Run this after authoring any app with scripts. Pass sample=[{field:value,...}] (example form submissions, keyed by the form's field names) to exercise the full form→record→data-source→output chain even before any real records exist — e.g. test that adding {"city":"Santa Cruz, CA"} actually yields a forecast.
- delete {id(slug)}.

Section kinds: form (create form; set modal=true + submit_label for the structured-create look) | table (record list; always set empty_text; editable adds a per-row Edit dialog prefilled from the record, deletable + auto_refresh_ms keep it live) | display (read-only pairs) | chart (bar/line/area/pie from inline data or a source_script that prints {labels, series}) | empty (centered placeholder) | chat (live chat bound to the app's agent — requires agent_id) | workbench (three-column list|viewer|chat — the whole app; requires agent_id) | html (raw-HTML escape hatch — set the html field; last resort, prefer typed sections).

Minimal good app = a form (modal=true) + a table (editable, deletable) over the same records. The form's saves and the table's source both point at the app's per-record store automatically — you don't wire endpoints. For an assistant app, set agent_id and add a chat section so the LLM lives inside the app. For a 'list | document viewer | chat' three-panel app, use ONE workbench section (it IS the whole app).

For LOGIC (fetch/aggregate/transform instead of plain CRUD): add data_sources:[{name, script, capabilities?}] — a python script that reads the app's records with 'records = json.loads(os.environ.get("records", "[]"))' (the records env var is a JSON STRING; never json.loads("records")) + query params, and PRINTS JSON; reach external data with 'from gohort import fetch_url; r = fetch_url(url)' (granted by default; r is {status,headers,body}; it RAISES on transport failure so wrap it in try/except and still print JSON). Then a table/display sets source_script:"<name>" to render the script's output. Served at /custom/<slug>/data/<name>. Run app_def action=test to execute the scripts and see their output/errors before telling the user it's ready. Owner-only.

For ACTION BUTTONS (the write side): add actions:[{name, label, script, capabilities?, confirm?}] — a script that gets the records + params and PRINTS {message?, records?}; the framework upserts the returned records (so they reach the tables) and shows the message. Surface them with an "actions" section. Served at /custom/<slug>/action/<name>.`

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a URL slug from a name: lowercase, non-alphanumerics → single
// hyphen, trimmed. "Reading List!" → "reading-list".
func slugify(name string) string {
	s := slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}

func (t *chatTurn) appDefCreateOrUpdate(args map[string]any, isUpdate bool) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	slug := slugify(stringArg(args, "slug"))

	var spec AppSpec
	if isUpdate {
		key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), name))
		existing, ok := LoadAppSpec(t.user, key)
		if !ok {
			return "", errors.New("no matching app to update — check the slug (app_def action=list)")
		}
		spec = existing
		if name != "" {
			spec.Name = name
		}
	} else {
		if name == "" {
			return "", errors.New("name is required to create an app")
		}
		if slug == "" {
			slug = slugify(name)
		}
		if slug == "" {
			return "", errors.New("could not derive a slug from the name — pass an explicit slug")
		}
		if _, exists := LoadAppSpec(t.user, slug); exists {
			return "", fmt.Errorf("an app with slug %q already exists — use action=update, or pick a different name/slug", slug)
		}
		spec = AppSpec{Slug: slug, Name: name, Owner: t.user}
	}

	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		spec.Desc = d
	}
	if rk := strings.TrimSpace(stringArg(args, "record_key")); rk != "" {
		spec.RecordKey = rk
	}
	if spec.RecordKey == "" {
		spec.RecordKey = "id"
	}
	if a := strings.TrimSpace(stringArg(args, "agent_id")); a != "" {
		if ag, ok := findAgentByNameOrID(t.udb, t.user, a); ok {
			spec.AgentID = ag.ID
		} else {
			spec.AgentID = a // store as given; resolution is the chat surface's problem (step 2)
		}
	}
	// full_width: opt the app's page into edge-to-edge layout. Only honored when
	// the key is present so an update without it keeps the existing choice.
	if _, ok := args["full_width"]; ok {
		spec.FullWidth = boolArg(args, "full_width")
	}
	// private_db: opt the app into its own dedicated database file. Only honored
	// when the key is present so an update without it keeps the existing choice.
	// No migration — records already in the shared store stay there.
	if _, ok := args["private_db"]; ok {
		spec.PrivateDB = boolArg(args, "private_db")
	}
	// Script-backed data sources (the "logic" seam): a table/display section can
	// be backed by a python script instead of the record store. Passed wholesale
	// replaces the stored set on update (omit to keep existing).
	if raw, ok := args["data_sources"]; ok && raw != nil {
		spec.DataSources = appDataSources(raw)
	}
	// Script-backed actions (the write-side logic seam): buttons that run a
	// script which returns records the framework persists.
	if raw, ok := args["actions"]; ok && raw != nil {
		spec.Actions = appActionDefs(raw)
	}

	// Build the Page from the declarative sections. On update with no sections
	// passed, keep the existing page.
	if raw, ok := args["sections"]; ok && raw != nil {
		page, err := buildAppPage(spec, raw)
		if err != nil {
			return "", err
		}
		blob, err := page.ConfigJSON()
		if err != nil {
			return "", fmt.Errorf("render app page: %w", err)
		}
		spec.Page = blob
		// Record the workbench body field on the spec so the co-author tool +
		// viewer agree on which field is the document body.
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				if mm, ok := item.(map[string]any); ok && strings.EqualFold(strings.TrimSpace(mapStr(mm, "kind")), "workbench") {
					spec.BodyField = firstNonEmptyStr(mapStr(mm, "body_field"), "content")
				}
			}
		}
	} else if !isUpdate {
		return "", errors.New("sections is required to create an app")
	}

	saved := SaveAppSpec(spec)
	verb := "Created"
	if isUpdate {
		verb = "Updated"
	}
	msg := fmt.Sprintf("%s app %q at /custom/%s/ — open it in the dashboard under Custom Apps. Records save to the app's own store; the table lists them. Revise with app_def(action=\"update\", id=%q, …).",
		verb, saved.Name, saved.Slug, saved.Slug)

	// Auto-verify the data sources: they fire when the page first opens (a table or
	// display fetches them), so a script that crashes is exactly the "errors on
	// load" footgun. Run them here — read-only by design, safe to execute — and on
	// failure return an error-shaped result so the author fixes the script before
	// telling the user it's ready, rather than the user hitting the 500. Actions
	// (the write side; a fetch cap can reach an external API) are NOT auto-run —
	// they wait for an explicit app_def action=test.
	if len(saved.DataSources) > 0 {
		report, _, _, fail := t.checkScripts(saved, false, nil, nil)
		if fail > 0 {
			return fmt.Sprintf("%s app %q, BUT a data source FAILED to run — the app will error on load until this is fixed:\n\n%s\nFix the script with app_def(action=\"update\", id=%q, …) (it re-checks on save). Do NOT tell the user the app is ready yet.",
				verb, saved.Name, strings.TrimSpace(report), saved.Slug), nil
		}
		msg += "\n\nData source check — all passed:\n" + strings.TrimSpace(report)
		msg += "\nTip: run app_def(action=\"test\", id=\"" + saved.Slug + "\", sample=[{…example form entry…}]) to confirm the full form→data-source→output chain produces real output before telling the user it's ready."
	}
	return msg, nil
}

// buildAppPage translates the declarative sections array into a ui.Page scoped
// to the app's mount. Endpoints are fixed and relative ("records" / "record")
// so a spec cannot point a binding outside its own app.
func buildAppPage(spec AppSpec, raw any) (ui.Page, error) {
	arr, ok := raw.([]any)
	if !ok {
		return ui.Page{}, errors.New("sections must be an array of section objects")
	}
	if len(arr) == 0 {
		return ui.Page{}, errors.New("an app needs at least one section")
	}
	// A workbench is a whole-page shape (three full-height columns), so when one
	// is present it owns the page: full width, single no-chrome section.
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "workbench") {
			wb, err := buildWorkbench(spec, m)
			if err != nil {
				return ui.Page{}, err
			}
			return ui.Page{
				Title:     spec.Name,
				ShowTitle: true,
				BackURL:   "/custom/",
				MaxWidth:  "100%",
				Sections:  []ui.Section{{NoChrome: true, Body: wb}},
			}, nil
		}
	}
	// Default to a centered ~900px column; the author opts into full width for
	// data-heavy surfaces (wide tables / dashboards).
	maxWidth := "900px"
	if spec.FullWidth {
		maxWidth = "100%"
	}
	page := ui.Page{
		Title:     spec.Name,
		ShowTitle: true,
		BackURL:   "/custom/",
		MaxWidth:  maxWidth,
	}
	// The first form section's fields are the natural default for an editable
	// table's edit dialog — same labels/types/selects the record was created
	// with. Scanned up front so section order doesn't matter.
	var createFields []ui.FormField
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "form") {
			if fields := appFormFields(m["fields"]); len(fields) > 0 {
				createFields = fields
				break
			}
		}
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return ui.Page{}, fmt.Errorf("section %d must be an object", i+1)
		}
		sec, err := buildAppSection(spec, m, createFields)
		if err != nil {
			return ui.Page{}, fmt.Errorf("section %d: %w", i+1, err)
		}
		page.Sections = append(page.Sections, sec)
	}
	return page, nil
}

func buildAppSection(spec AppSpec, m map[string]any, createFields []ui.FormField) (ui.Section, error) {
	kind := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
	sec := ui.Section{Title: mapStr(m, "title"), Subtitle: mapStr(m, "subtitle")}
	switch kind {
	case "form":
		form := ui.FormPanel{
			PostURL:     "records",
			SubmitLabel: firstNonEmptyStr(mapStr(m, "submit_label"), "Add"),
			Fields:      appFormFields(m["fields"]),
			// New records should show up without a reload — refresh the plain record
			// lists ("records") AND every script-backed panel, since a data source's
			// output is computed FROM the records (the "set a city → see its weather"
			// wiring). Without the data/<name> sources here a form and a source_script
			// display stay disconnected: the record saves but the computed panel never
			// re-fetches.
			Invalidate: appRecordWriteInvalidations(spec),
		}
		if len(form.Fields) == 0 {
			return ui.Section{}, errors.New("a form section needs at least one field")
		}
		if boolArg(m, "modal") {
			// Structured-create: the form opens from a "New" button in a dialog —
			// the signature pattern instead of an always-visible form.
			sec.Body = ui.ModalButton{
				Label:    firstNonEmptyStr(mapStr(m, "submit_label"), "New"),
				Title:    firstNonEmptyStr(sec.Title, "New"),
				Subtitle: sec.Subtitle,
				Body:     form,
			}
			// The modal carries its own title; clear the section chrome so it reads
			// as a single action button.
			sec.Title, sec.Subtitle = "", ""
		} else {
			sec.Body = form
		}
	case "table":
		tbl := ui.Table{
			Source:        appSectionSource(m),
			RowKey:        spec.RecordKey,
			Columns:       appTableCols(m["columns"]),
			EmptyText:     firstNonEmptyStr(mapStr(m, "empty_text"), "Nothing here yet."),
			AutoRefreshMS: intFromArgs(m, "auto_refresh_ms"),
		}
		if len(tbl.Columns) == 0 {
			return ui.Section{}, errors.New("a table section needs at least one column")
		}
		// editable: an Edit button per row opens the record in a prefilled
		// dialog (FormPanel: Source GETs the row, submit upserts it back to
		// the records store, invalidations refresh the table + any computed
		// panels). Field precedence: explicit edit_fields → the create form's
		// fields → a plain text input per column. Record-store tables only —
		// a source_script table's rows are computed, not stored records.
		if boolArg(m, "editable") {
			fields := appFormFields(m["edit_fields"])
			if len(fields) == 0 {
				fields = createFields
			}
			if len(fields) == 0 {
				for _, c := range tbl.Columns {
					if c.Field == spec.RecordKey || c.Field == "created" {
						continue
					}
					fields = append(fields, ui.FormField{
						Field: c.Field,
						Label: firstNonEmptyStr(c.Label, c.Field),
						Type:  "text",
					})
				}
			}
			if len(fields) > 0 {
				tbl.RowActions = append(tbl.RowActions, ui.ModalAction("Edit", ui.FormPanel{
					Source:      "record?id={" + spec.RecordKey + "}",
					PostURL:     "records",
					SubmitLabel: "Save",
					Fields:      fields,
					Invalidate:  appRecordWriteInvalidations(spec),
				}))
			}
		}
		if boolArg(m, "deletable") {
			tbl.RowActions = append(tbl.RowActions, ui.RowAction{
				Type: "button", Label: "Delete", Method: "DELETE",
				PostTo: "record?id={" + spec.RecordKey + "}", Confirm: "Delete this item?",
			})
		}
		sec.Body = tbl
	case "display":
		sec.Body = ui.DisplayPanel{Source: appSectionSource(m), Pairs: appDisplayPairs(m["pairs"])}
	case "actions":
		// A row of buttons, one per declared action (the app's `actions`). Each
		// button POSTs to action/<name>; the framework runs the script, persists
		// any returned records, and refreshes the records table. Button labels +
		// per-action confirm ride on the items (see handleActionsList).
		sec.Body = ui.ActionList{
			Source:    "actions",
			DescField: "desc",
			PostTo:    "action/{name}",
			// An action upserts records, so refresh the record lists AND every
			// script-backed panel computed from them (same as a form save).
			Invalidate: appRecordWriteInvalidations(spec),
			EmptyText:  firstNonEmptyStr(mapStr(m, "empty_text"), "No actions."),
		}
	case "empty":
		sec.Body = ui.EmptyState{
			Icon:  mapStr(m, "icon"),
			Title: firstNonEmptyStr(mapStr(m, "title"), "Nothing selected"),
			Hint:  mapStr(m, "hint"),
		}
		// EmptyState carries its own title; avoid a duplicate section heading.
		sec.Title, sec.Subtitle = "", ""
	case "chat":
		// The chat panel binds to the app's agent (agent_id). customapps serves
		// the SSE + session endpoints under chat/* (handleChat → orchestrate's
		// PublicHandle*), so the URLs are relative to the app mount, same as the
		// records store. Requires an agent_id on the app.
		if strings.TrimSpace(spec.AgentID) == "" {
			return ui.Section{}, errors.New("a chat section needs the app to have an agent_id (the agent that powers the chat)")
		}
		sec.NoChrome = true // the panel manages its own layout
		sec.Body = ui.AgentLoopPanel{
			ListURL:      "chat/sessions",
			LoadURL:      "chat/sessions/{id}",
			DeleteURL:    "chat/sessions/{id}",
			SendURL:      "chat/send",
			CancelURL:    "chat/cancel",
			ListTitle:    firstNonEmptyStr(mapStr(m, "list_title"), "Sessions"),
			NewLabel:     "New",
			ListPosition: "top",
			Markdown:     true,
			EmptyText:    firstNonEmptyStr(mapStr(m, "empty_text"), "Ask the assistant to get started."),
			Placeholder:  firstNonEmptyStr(mapStr(m, "placeholder"), "Ask anything…"),
		}
	case "chart":
		// A chart is either STATIC (inline labels + series) or COMPUTED by
		// a data source that prints {labels, series[, chart_type, title,
		// options]} — the source-script path is the useful one for a data
		// app (a chart of the records). The section title is the heading;
		// the SVG carries no duplicate title.
		cp := ui.ChartPanel{
			ChartType: firstNonEmptyStr(strings.ToLower(strings.TrimSpace(mapStr(m, "chart_type"))), "bar"),
			Labels:    appChartLabels(m["labels"]),
			Series:    appChartSeries(m["series"]),
			Options:   appChartOptions(m),
		}
		if name := slugify(mapStr(m, "source_script")); name != "" {
			cp.Source = "data/" + name
		}
		if cp.Source == "" && len(cp.Series) == 0 {
			return ui.Section{}, errors.New("a chart section needs a source_script (computed data) or inline series")
		}
		sec.Body = cp
	case "html", "card":
		// Raw-HTML escape hatch (ui.Card): render an author-supplied HTML blob
		// verbatim, for the rare surface the typed primitives don't model — a
		// bespoke layout, an embedded widget. The HTML is rendered UNescaped and
		// any inline <script> runs, so this is trusted input: same owner-only
		// trust level as the python data_sources (which run arbitrary code
		// server-side). Reach for a typed section first; this is a last resort.
		html := mapStr(m, "html")
		if strings.TrimSpace(html) == "" {
			return ui.Section{}, errors.New("an html section needs an `html` field (the raw HTML to render)")
		}
		sec.Body = ui.Card{HTML: html}
	default:
		return ui.Section{}, fmt.Errorf("unknown section kind %q — use form | table | display | chart | empty | chat | workbench | actions | html", kind)
	}
	return sec, nil
}

// buildWorkbench assembles the three-column WorkbenchPanel from a workbench
// section spec: a list + viewer over the app's records, a New modal to create an
// item, and a chat bound to the app's agent. Requires agent_id.
func buildWorkbench(spec AppSpec, m map[string]any) (ui.WorkbenchPanel, error) {
	if strings.TrimSpace(spec.AgentID) == "" {
		return ui.WorkbenchPanel{}, errors.New("a workbench needs the app to have an agent_id (the agent that powers the chat)")
	}
	itemLabel := firstNonEmptyStr(mapStr(m, "item_label"), "title")
	bodyField := firstNonEmptyStr(mapStr(m, "body_field"), "content")

	// The New form: the fields the LLM gave, or a sensible default (a title + the
	// body field) so creating an item always works. Posts to the records store
	// and invalidates it so the list refreshes.
	newFields := appFormFields(m["new_fields"])
	if len(newFields) == 0 {
		newFields = []ui.FormField{
			{Field: itemLabel, Label: "Title", Type: "text", Placeholder: "Name this " + firstNonEmptyStr(mapStr(m, "item_noun"), "item")},
		}
	}
	newButton := ui.ModalButton{
		Label: firstNonEmptyStr(mapStr(m, "new_label"), "New"),
		Title: firstNonEmptyStr(mapStr(m, "new_title"), "Create"),
		Body: ui.FormPanel{
			PostURL:     "records",
			SubmitLabel: firstNonEmptyStr(mapStr(m, "new_label"), "Create"),
			Fields:      newFields,
			Invalidate:  []string{"records"},
		},
	}

	// AgentLoopPanel in no-list mode: one chat window, NO sessions rail (we omit
	// list/load/delete URLs) and NO activity pane (LockActivity). The workbench's
	// own document list is the app nav, so a second session list is redundant.
	// MUST be AgentLoopPanel (not ChatPanel): chat/send emits the AgentLoopPanel
	// SSE format (sse.Send) — ChatPanel's parser ignores those frames, so its
	// replies never render. See sseWriter.SendChatEvent vs Send.
	chat := ui.AgentLoopPanel{
		SendURL:      "chat/send",
		CancelURL:    "chat/cancel",
		Markdown:     true,
		LockActivity: true,
		EmptyText:    firstNonEmptyStr(mapStr(m, "chat_empty"), "Ask the assistant to draft or add a section."),
		Placeholder:  firstNonEmptyStr(mapStr(m, "placeholder"), "Ask the assistant…"),
	}

	noun := firstNonEmptyStr(mapStr(m, "item_noun"), "document")
	return ui.WorkbenchPanel{
		ListURL:          "records",
		ItemKey:          spec.RecordKey,
		ItemLabel:        itemLabel,
		ListTitle:        firstNonEmptyStr(mapStr(m, "list_title"), "Items"),
		ListEmpty:        firstNonEmptyStr(mapStr(m, "list_empty"), "Nothing yet — create one."),
		NewButton:        newButton,
		DeleteURL:        "record?id={id}",
		RecordURL:        "record?id={id}",
		BodyField:        bodyField,
		ViewerTitleField: itemLabel,
		EmptyIcon:        firstNonEmptyStr(mapStr(m, "empty_icon"), "📄"),
		EmptyTitle:       firstNonEmptyStr(mapStr(m, "empty_title"), "Nothing selected"),
		EmptyHint:        firstNonEmptyStr(mapStr(m, "empty_hint"), "Pick an item on the left, or create one."),
		RefreshOn:        []string{"records"},
		// Tell the server which document is open so the agent's add_section tool
		// writes into it; the viewer re-fetches when the chat round finishes.
		ActiveURL: "chat/active",
		// Co-author: each assistant reply gets an "Add to <noun>" button that
		// appends it to the open record (upsert to the records store).
		CoAuthor:     true,
		CoAuthorVerb: "Add to " + noun,
		SaveURL:      "records",
		Chat:         chat,
	}, nil
}

// appFormFields converts the declarative fields array into ui.FormField values.
func appFormFields(raw any) []ui.FormField {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.FormField
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		field := strings.TrimSpace(mapStr(m, "field"))
		if field == "" {
			continue
		}
		ff := ui.FormField{
			Field:       field,
			Label:       firstNonEmptyStr(mapStr(m, "label"), field),
			Type:        firstNonEmptyStr(strings.ToLower(mapStr(m, "type")), "text"),
			Placeholder: mapStr(m, "placeholder"),
			Help:        mapStr(m, "help"),
			Rows:        intFromArgs(m, "rows"),
		}
		if opts := appSelectOptions(m["options"]); len(opts) > 0 {
			ff.Options = opts
		}
		out = append(out, ff)
	}
	return out
}

func appSelectOptions(raw any) []ui.SelectOption {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.SelectOption
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		v := strings.TrimSpace(mapStr(m, "value"))
		if v == "" {
			continue
		}
		out = append(out, ui.SelectOption{Value: v, Label: firstNonEmptyStr(mapStr(m, "label"), v)})
	}
	return out
}

func appTableCols(raw any) []ui.Col {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.Col
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		field := strings.TrimSpace(mapStr(m, "field"))
		if field == "" {
			continue
		}
		out = append(out, ui.Col{
			Field: field,
			Label: mapStr(m, "label"),
			Flex:  intFromArgs(m, "flex"),
			Mute:  boolArg(m, "mute"),
			Link:  strings.TrimSpace(mapStr(m, "link")),
		})
	}
	return out
}

// appRecordWriteInvalidations lists the data-source URLs a record write (a form
// save or an action) must refresh: the plain record lists ("records") PLUS every
// script-backed panel, because a data source computes its output FROM the stored
// records. Returning the data/<name> sources here is what connects a form/action
// (which changes records) to a source_script table/display (which renders a
// function of those records) — omit them and the computed panel silently goes
// stale after every save (the "set a city but the weather never updates" bug).
func appRecordWriteInvalidations(spec AppSpec) []string {
	out := []string{"records"}
	for _, ds := range spec.DataSources {
		out = append(out, "data/"+ds.Name)
	}
	return out
}

// appSectionSource resolves where a table/display reads its data: the generic
// record store ("records") by default, or a script-backed data source
// ("data/<name>") when the section names one via source_script.
func appSectionSource(m map[string]any) string {
	if name := slugify(mapStr(m, "source_script")); name != "" {
		return "data/" + name
	}
	return "records"
}

// appDataSources parses the declarative data_sources array into AppDataSource
// records. Each needs a name + script; language defaults to python at dispatch.
func appDataSources(raw any) []AppDataSource {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []AppDataSource
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := slugify(mapStr(m, "name"))
		script := mapStr(m, "script")
		if name == "" || strings.TrimSpace(script) == "" {
			continue
		}
		out = append(out, AppDataSource{
			Name:         name,
			Language:     strings.ToLower(strings.TrimSpace(mapStr(m, "language"))),
			Script:       script,
			Capabilities: appStringList(m["capabilities"]),
		})
	}
	return out
}

// appActionDefs parses the declarative actions array into AppAction records.
func appActionDefs(raw any) []AppAction {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []AppAction
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := slugify(mapStr(m, "name"))
		script := mapStr(m, "script")
		if name == "" || strings.TrimSpace(script) == "" {
			continue
		}
		out = append(out, AppAction{
			Name:         name,
			Label:        strings.TrimSpace(mapStr(m, "label")),
			Desc:         strings.TrimSpace(mapStr(m, "desc")),
			Language:     strings.ToLower(strings.TrimSpace(mapStr(m, "language"))),
			Script:       script,
			Capabilities: appStringList(m["capabilities"]),
			Confirm:      strings.TrimSpace(mapStr(m, "confirm")),
		})
	}
	return out
}

// appStringList coerces a declarative value to []string: a JSON array of
// strings, or a single string. Empty entries are dropped.
func appStringList(raw any) []string {
	var out []string
	switch v := raw.(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func appDisplayPairs(raw any) []ui.DisplayPair {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.DisplayPair
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		field := strings.TrimSpace(mapStr(m, "field"))
		if field == "" {
			continue
		}
		out = append(out, ui.DisplayPair{Label: firstNonEmptyStr(mapStr(m, "label"), field), Field: field})
	}
	return out
}

// appChartSeries parses the declarative series array into ui.ChartSeries.
// Each item is {name?, points?:[numbers]} for bar/line/area, or
// {name?, value?:number} for a pie slice.
func appChartSeries(raw any) []ui.ChartSeries {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.ChartSeries
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s := ui.ChartSeries{
			Name:   strings.TrimSpace(mapStr(m, "name")),
			Points: appFloatList(m["points"]),
		}
		if v, ok := floatVal(m["value"]); ok {
			s.Value = &v
		}
		if len(s.Points) == 0 && s.Value == nil && s.Name == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// appChartOptions reads the flat chart tweaks off a section map (height /
// width / stacked / legend). Returns nil when none are set so the
// renderer's defaults apply.
func appChartOptions(m map[string]any) *ui.ChartOptions {
	opt := ui.ChartOptions{
		Height:  intFromArgs(m, "height"),
		Width:   intFromArgs(m, "width"),
		Stacked: boolArg(m, "stacked"),
	}
	if lv, ok := m["legend"].(bool); ok {
		opt.Legend = &lv
	}
	if opt.Height == 0 && opt.Width == 0 && !opt.Stacked && opt.Legend == nil {
		return nil
	}
	return &opt
}

// appChartLabels coerces a chart's labels array to []string, keeping
// index alignment with the series points. Unlike appStringList it does
// NOT drop non-strings: a numeric label (2020, from a JSON number) is
// stringified rather than silently dropped, which would otherwise leave
// the axis blank / renumbered 0,1,2. A bare comma-string list falls back
// to the shared string parser.
func appChartLabels(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return appStringList(raw)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		out = append(out, labelString(e))
	}
	return out
}

// labelString renders one chart label value as display text: strings
// pass through, integer-valued numbers render without a trailing ".0"
// (2020, not 2020.0), other numbers use their shortest form, nil is "".
func labelString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	}
	if f, ok := floatVal(v); ok {
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// appFloatList coerces a JSON array to []float64, keeping index
// alignment (a non-numeric entry becomes 0 so a series stays aligned
// with its labels).
func appFloatList(raw any) []float64 {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		f, _ := floatVal(e)
		out = append(out, f)
	}
	return out
}

// floatVal coerces the common JSON-decoded numeric shapes (and a
// stringified number) to float64.
func floatVal(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

func (t *chatTurn) appDefList() (string, error) {
	specs := ListAppSpecs(t.user)
	if len(specs) == 0 {
		return "No apps yet. Author one with app_def(action=\"create\", name=…, sections=[…]).", nil
	}
	type row struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
		Desc string `json:"desc,omitempty"`
		URL  string `json:"url"`
	}
	out := make([]row, len(specs))
	for i, s := range specs {
		out[i] = row{Slug: s.Slug, Name: s.Name, Desc: s.Desc, URL: "/custom/" + s.Slug + "/"}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *chatTurn) appDefGet(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	out := map[string]any{
		"slug":       spec.Slug,
		"name":       spec.Name,
		"desc":       spec.Desc,
		"record_key": spec.RecordKey,
		"agent_id":   spec.AgentID,
		"full_width": spec.FullWidth,
		"url":        "/custom/" + spec.Slug + "/",
		"page":       json.RawMessage(spec.Page),
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *chatTurn) appDefDelete(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app to delete")
	}
	DeleteAppSpec(t.user, spec.Slug)
	return fmt.Sprintf("Deleted app %q (/custom/%s/).", spec.Name, spec.Slug), nil
}

// appDefTest executes every script-backed component of an app — each data source
// and each action — through the SAME runner the host uses at request time
// (appscript.Run), and reports per component: did it run, did it print the JSON
// shape its section expects, and the captured output/traceback when it didn't.
// This is the authoring-time feedback loop that catches script bugs (e.g.
// json.loads("records") instead of json.loads(os.environ['records'])) before the
// user ever opens the app.
func (t *chatTurn) appDefTest(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app to test — check the slug (app_def action=list)")
	}
	if len(spec.DataSources) == 0 && len(spec.Actions) == 0 {
		return fmt.Sprintf("App %q has no script-backed components (data_sources or actions) to test — a plain form/table app uses the built-in record store and needs no script test.", spec.Name), nil
	}
	// Optional example form data: run the chain against THESE records instead of
	// the (often empty) live store, so the full form→record→data-source→output
	// path is exercised with realistic input. `sample` is an array of objects
	// keyed by the form's field names; `params` simulates query-param inputs.
	sample := appSampleRecords(args["sample"])
	params := mapArg(args["params"])
	report, records, pass, fail := t.checkScripts(spec, true, sample, params)
	src := "stored"
	if sample != nil {
		src = "sample"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tested app %q with %d %s record(s).\n\n%s\n%d passed, %d failed.", spec.Name, records, src, report, pass, fail)
	if sample == nil && records == 0 {
		b.WriteString("\nNote: the store is empty, so data sources only saw []. Pass sample=[{...}] (example form submissions) to test the full form→data-source→output chain with real input.")
	}
	if fail > 0 {
		b.WriteString(" Fix the failing scripts with app_def action=update, then test again before telling the user the app is ready.")
	}
	return b.String(), nil
}

// mapArg coerces an arg to a map[string]any (the test action's `params`),
// returning nil when it isn't an object.
func mapArg(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok && len(m) > 0 {
		return m
	}
	return nil
}

// appSampleRecords parses the test action's `sample` argument — an array of
// example form submissions (objects keyed by form field name) — into records to
// stand in for the live store. Returns nil when absent so checkScripts falls back
// to the stored records.
func appSampleRecords(raw any) []map[string]any {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// checkScripts executes an app's script-backed components through the SAME runner
// the host uses at request time (appscript.Run) and returns a per-component
// report plus the stored-record count and pass/fail tallies. Each script gets the
// app's real stored records as the `records` env var but NO query params — which
// is exactly the state a table/display data source loads in when the page first
// opens, so a script that crashes on a missing param surfaces here.
//
// includeActions gates the WRITE side: data sources are read-only and are what
// the page fires on load, so they are always safe to run; actions can carry a
// fetch capability that hits an external API, so they run only on an explicit
// action=test, never as part of the automatic create/update check.
//
// sample, when non-nil, stands in for the app's stored records — letting the
// author exercise the full form→record→data-source→output chain with EXAMPLE
// form submissions before any real data exists (a fresh app's store is empty, so
// without this every data source just sees []). params are extra env vars handed
// to each script, simulating query-param inputs for filter-style sources.
func (t *chatTurn) checkScripts(spec AppSpec, includeActions bool, sample []map[string]any, params map[string]any) (report string, records, pass, fail int) {
	db := UserDB(RootDB, t.user)
	recs := sample
	if recs == nil {
		recs = []map[string]any{}
		if db != nil {
			tbl := "custom_records:" + spec.Slug
			for _, k := range db.Keys(tbl) {
				var rec map[string]any
				if db.Get(tbl, k, &rec) {
					recs = append(recs, rec)
				}
			}
		}
	}
	recJSON, _ := json.Marshal(recs)

	var b strings.Builder
	run := func(kind, name, lang, script string, caps []string) {
		label := fmt.Sprintf("%s %q", kind, name)
		scriptArgs := map[string]any{"records": string(recJSON)}
		for k, v := range params {
			scriptArgs[k] = fmt.Sprint(v)
		}
		out, err := appscript.Run(t.user, db, spec.Slug, kind, name, lang, script, caps, scriptArgs)
		if err != nil {
			fail++
			fmt.Fprintf(&b, "FAIL %s — could not run: %v\n", label, err)
			return
		}
		trimmed := strings.TrimSpace(out)
		if trimmed == "" {
			if kind == "action" { // an action may legitimately print nothing
				pass++
				fmt.Fprintf(&b, "OK   %s — ran, printed nothing (no message/records).\n", label)
				return
			}
			fail++
			fmt.Fprintf(&b, "FAIL %s — printed nothing; a data source must print JSON to stdout.\n", label)
			return
		}
		if !json.Valid([]byte(trimmed)) {
			fail++
			fmt.Fprintf(&b, "FAIL %s — did not print valid JSON. Output:\n%s\n", label, truncate(trimmed, 800))
			if strings.Contains(trimmed, `json.loads("records")`) || strings.Contains(trimmed, "json.loads('records')") {
				b.WriteString("     Hint: read records with json.loads(os.environ.get('records', '[]')) — json.loads(\"records\") parses the literal word, not the data.\n")
			} else if strings.Contains(trimmed, "KeyError") || strings.Contains(trimmed, "os.environ[") {
				b.WriteString("     Hint: a data source runs on page load with NO query params set — read every env var with a default, e.g. os.environ.get('city', ''), never os.environ['city'].\n")
			}
			return
		}
		var v any
		_ = json.Unmarshal([]byte(trimmed), &v)
		switch kind {
		case "data":
			if arr, isArr := v.([]any); isArr {
				pass++
				if len(arr) == 0 && len(recs) > 0 {
					// Valid JSON, but empty output while the app HAS records is the
					// signature of a script that reads a query param nothing supplies
					// (os.environ.get('city')) instead of pulling the saved entries
					// from the records env var — the "added a location, no forecast"
					// disconnect. Pass (it's valid) but flag it loudly.
					fmt.Fprintf(&b, "WARN %s — printed an EMPTY array though the app has %d saved record(s). The script is probably reading a query param (e.g. os.environ.get('city')) that is never set; read the saved entries from the `records` env var instead, e.g. recs = json.loads(os.environ.get('records','[]')).\n", label, len(recs))
				} else {
					fmt.Fprintf(&b, "OK   %s — printed a JSON array (%d item(s)); good for a table.\n", label, len(arr))
				}
			} else {
				pass++
				fmt.Fprintf(&b, "OK   %s — printed a JSON object; good for a display (a table section needs a JSON array).\n", label)
			}
		case "action":
			if _, isObj := v.(map[string]any); isObj {
				pass++
				fmt.Fprintf(&b, "OK   %s — printed a JSON object {message?, records?}.\n", label)
			} else {
				fail++
				fmt.Fprintf(&b, "FAIL %s — an action must print a JSON OBJECT {message?, records?}, got %T.\n", label, v)
			}
		}
	}

	for _, ds := range spec.DataSources {
		run("data", ds.Name, ds.Language, ds.Script, ds.Capabilities)
	}
	if includeActions {
		for _, act := range spec.Actions {
			run("action", act.Name, act.Language, act.Script, act.Capabilities)
		}
	}
	return b.String(), len(recs), pass, fail
}

// boolArg coerces a section-map field to bool: native bool, or the strings
// "true"/"1"/"yes" (LLMs sometimes stringify booleans).
func boolArg(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// firstNonEmptyStr returns the first trimmed-non-empty argument, or "".
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
