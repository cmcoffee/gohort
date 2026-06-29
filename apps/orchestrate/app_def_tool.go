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
// empty/chat); this tool translates that into a ui.Page, marshals it with
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
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func (t *chatTurn) appDefToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "app_def",
			Description: "Author and manage data-driven gohort APPS — real in-dashboard surfaces (NOT standalone HTML files) composed from ui primitives and served at /custom/<slug>/. This is how you build a gohort app: describe it declaratively as a list of sections, and the framework renders it + gives it a generic per-app record store (a form section saves records, a table section lists them) with no hand-written HTML/CSS/JS.\n\nUse this when the user asks for \"an app\", \"a page where I can…\", \"a tool to track/manage X\", or any persistent multi-panel surface inside gohort. Do NOT produce a standalone downloadable HTML file for these requests — that's not a gohort app.\n\nActions: create (author a new app), update (revise one), list (see the user's apps), get (read one's section definition), delete.\n\nGOOD DEFAULTS (reach for these so the app feels considered): a list/table section should always carry empty_text for its empty state; a creation form should use submit_label (a deliberate \"Add\" button) and modal=true so \"new\" opens a structured dialog rather than an always-visible form; pair a create FORM with a TABLE over the same records so new entries appear in the list. A standalone EMPTY section gives a \"nothing selected yet\" middle panel.",
			Parameters: map[string]ToolParam{
				"action":      {Type: "string", Description: "One of: create | update | list | get | delete | help."},
				"name":        {Type: "string", Description: "App name (shown in the dashboard). Required for create."},
				"slug":        {Type: "string", Description: "(create) URL slug, e.g. 'reading-list' → /custom/reading-list/. Optional — derived from the name when omitted. Lowercase letters, digits, hyphens."},
				"id":          {Type: "string", Description: "(update/get/delete) The app's slug, identifying which app to act on."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the app is for (shown on the Custom Apps index)."},
				"record_key":  {Type: "string", Description: "(create/update) The primary-key field of each record. Default 'id' — the host allocates one on save. Only override if the records have a natural key."},
				"agent_id":    {Type: "string", Description: "(create/update) Optional name or id of an agent that powers this app (reserved for the chat surface). Stored on the app; not required."},
				"data_sources": {
					Type:        "array",
					Description: "(create/update) Optional script-backed data endpoints — the way to give an app real LOGIC instead of plain stored-record CRUD. Each is {name, script, language?, capabilities?}. The script (python by default; set language:\"bash\" for shell) COMPUTES the JSON a table/display renders: it receives the app's stored records as the env var `records` (a JSON string — json.loads it) plus each request query param as its own env var, and must PRINT a JSON value to stdout (a JSON array for a table, a JSON object for a display). To pull external data, declare capabilities and call the gohort hook from the script: `from gohort import fetch, log` then `fetch(url)` (capabilities:[\"fetch\",\"log\"]) — the host performs the fetch (the sandbox itself has no raw network). A table/display section then sets source_script:\"<name>\" to read from it instead of the record store. Use this for apps that fetch/aggregate/transform (a dashboard over an API, a computed report) rather than just collecting form entries. Owner-only today.",
					Items:       &ToolParam{Type: "object"},
				},
				"actions": {
					Type:        "array",
					Description: "(create/update) Optional script-backed action buttons — the WRITE side of the logic seam (data_sources is the read side). Each is {name, label?, desc?, script, language?, capabilities?, confirm?}. A button labeled `label` runs the script when clicked; the script receives the app's stored records (env var `records`, JSON) + any params, and PRINTS a JSON OBJECT {message?: string, records?: [...]}. The FRAMEWORK upserts any returned records into the app's store (so they appear in the tables — your script does NOT write the store itself) and shows the message. Use for app verbs like \"Sync now\", \"Generate\", \"Refresh from API\". Surface the buttons with an `actions` section. Set confirm for destructive ones. capabilities work the same as data_sources (e.g. [\"fetch\"] for `from gohort import fetch`).",
					Items:       &ToolParam{Type: "object"},
				},
				"sections": {
					Type:        "array",
					Description: "(create/update) Ordered sections, each an object with a `kind` plus kind-specific fields. Every section may set `title` and `subtitle`.\n\nkind=\"form\" — a create form. Fields: `fields` (array of {field, label, type, placeholder, rows, help}; type is text|textarea|number|select|toggle|tags|password, default text; select needs `options`:[{value,label}]), `submit_label` (button text, default \"Add\"), `modal` (boolean — when true the form opens from a \"New\" button in a dialog; the signature structured-create pattern). The form saves a record to the app's store.\n\nkind=\"table\" — a list of the app's records. Fields: `columns` (array of {field, label, flex, mute}), `empty_text` (shown when there are no records — ALWAYS set this), `deletable` (boolean — adds a Delete button per row), `auto_refresh_ms` (poll interval; 2000 keeps the list live as records are added), `source_script` (name of a data_sources entry — when set, the table's rows come from that SCRIPT instead of the record store; the script must print a JSON array).\n\nkind=\"display\" — a read-only labeled-value panel. Fields: `pairs` (array of {label, field}), `source_script` (name of a data_sources entry whose script prints a JSON object; defaults to the record store when omitted).\n\nkind=\"actions\" — a row of script-backed action buttons (one per entry in the app's top-level `actions`). Clicking a button runs its script and the framework persists what it returns + refreshes the tables. No fields needed; declare the scripts in `actions` (see the actions parameter). Use for app verbs (Sync, Generate, Refresh).\n\nkind=\"empty\" — a centered empty-state placeholder (for a 'nothing selected' panel). Fields: `icon` (an emoji), `title`, `hint`.\n\nkind=\"chat\" — a live chat panel bound to the app's agent (REQUIRES agent_id on the app). Sessions + streaming reply are wired automatically to the bound agent; the user talks to it right inside the app. Fields: `list_title`, `empty_text`, `placeholder`. This is how you build a one-app assistant surface (e.g. sessions list + a viewer + a chat that drafts content) instead of sending the user off to a separate /chat URL.\n\nkind=\"workbench\" — the THREE-COLUMN document workbench: an item list (left), a rendered document VIEWER of the selected item (center), and a chat bound to the app's agent (right). REQUIRES agent_id. This is the right shape for 'a list of docs/guides/notes, a formatted reader in the middle, and an AI assistant that helps write them' — clicking a list item shows it; the chat drafts content; each chat reply has an 'Add to document' button that appends it into the open item, and the viewer re-renders. ONE workbench section IS the whole app (don't add other sections). Fields: `item_label` (record field for the list label, default title), `body_field` (the markdown field shown + appended-to in the viewer, default content), `item_noun` (e.g. 'guide' — used in the New button + 'Add to <noun>' label), `new_fields` (form fields for creating an item; defaults to a single title field), `list_title`, `empty_title`, `empty_hint`, `empty_icon`.\n\nThe document body is MARKDOWN, rendered as a formatted HTML-like document — '## Section' and '### Sub-section' headings, lists, code blocks, etc. The DATA LAYER IS THE APP. The workbench AUTOMATICALLY gives the bound agent an 'add_section(section_title, markdown)' tool that writes a section straight into the OPEN document's record (the store the viewer renders) — so 'add a section about hooks' appears in the guide with no button. You do NOT build that tool; it's provided. So a workbench agent should be told to call add_section to commit content, and must NOT be given its OWN storage tools (no file/python/JSON, no custom save) — those write to its workspace, never reaching the viewer. (A manual 'Add to document' button on each reply is also available as a fallback.)\n\nMinimal good app = a form (modal=true, submit_label) + a table (empty_text, deletable, auto_refresh_ms) over the same records. For an assistant app, add agent_id + a chat section. For a 'sessions | viewer | chat' three-panel app, use ONE workbench section.",
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
			case "delete":
				return t.appDefDelete(args)
			case "help", "":
				return appDefHelpText, nil
			default:
				return "", fmt.Errorf("unknown action %q — use create | update | list | get | delete | help", action)
			}
		},
	}
}

const appDefHelpText = `app_def actions:
- create {name, slug?, description?, record_key?, sections:[…]} — author a data-driven app, served at /custom/<slug>/.
- update {id(slug), …, sections:[…]} — revise an app in place.
- list — your apps: [{slug, name, desc}].
- get  {id(slug)} — one app's full section definition.
- delete {id(slug)}.

Section kinds: form (create form; set modal=true + submit_label for the structured-create look) | table (record list; always set empty_text; deletable + auto_refresh_ms keep it live) | display (read-only pairs) | empty (centered placeholder) | chat (live chat bound to the app's agent — requires agent_id) | workbench (three-column list|viewer|chat — the whole app; requires agent_id).

Minimal good app = a form + a table over the same records. The form's saves and the table's source both point at the app's per-record store automatically — you don't wire endpoints. For an assistant app, set agent_id and add a chat section so the LLM lives inside the app. For a 'list | document viewer | chat' three-panel app, use ONE workbench section (it IS the whole app).

For LOGIC (fetch/aggregate/transform instead of plain CRUD): add data_sources:[{name, script, capabilities?}] — a python script that receives the app's records (env var 'records', a JSON string) + query params and PRINTS JSON; reach external data with 'from gohort import fetch' (capabilities:["fetch"]). Then a table/display sets source_script:"<name>" to render the script's output. Served at /custom/<slug>/data/<name>. Owner-only.

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
	return fmt.Sprintf("%s app %q at /custom/%s/ — open it in the dashboard under Custom Apps. Records save to the app's own store; the table lists them. Revise with app_def(action=\"update\", id=%q, …).",
		verb, saved.Name, saved.Slug, saved.Slug), nil
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
	page := ui.Page{
		Title:     spec.Name,
		ShowTitle: true,
		BackURL:   "/custom/",
		MaxWidth:  "900px",
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return ui.Page{}, fmt.Errorf("section %d must be an object", i+1)
		}
		sec, err := buildAppSection(spec, m)
		if err != nil {
			return ui.Page{}, fmt.Errorf("section %d: %w", i+1, err)
		}
		page.Sections = append(page.Sections, sec)
	}
	return page, nil
}

func buildAppSection(spec AppSpec, m map[string]any) (ui.Section, error) {
	kind := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
	sec := ui.Section{Title: mapStr(m, "title"), Subtitle: mapStr(m, "subtitle")}
	switch kind {
	case "form":
		form := ui.FormPanel{
			PostURL:     "records",
			SubmitLabel: firstNonEmptyStr(mapStr(m, "submit_label"), "Add"),
			Fields:      appFormFields(m["fields"]),
			// New records should show up in a sibling table without a reload.
			Invalidate: []string{"records"},
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
		if boolArg(m, "deletable") {
			tbl.RowActions = []ui.RowAction{{
				Type: "button", Label: "Delete", Method: "DELETE",
				PostTo: "record?id={" + spec.RecordKey + "}", Confirm: "Delete this item?",
			}}
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
			Source:     "actions",
			DescField:  "desc",
			PostTo:     "action/{name}",
			Invalidate: []string{"records"},
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
	default:
		return ui.Section{}, fmt.Errorf("unknown section kind %q — use form | table | display | empty | chat | workbench | actions", kind)
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
		})
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
