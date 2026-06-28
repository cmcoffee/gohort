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
// empty); this tool translates that into a ui.Page, marshals it with
// ConfigJSON, and stores the bytes via core.SaveAppSpec. customapps serves the
// stored page + a generic per-app record store (the form writes records, the
// table lists them) with no per-app Go code.
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
				"sections": {
					Type:        "array",
					Description: "(create/update) Ordered sections, each an object with a `kind` plus kind-specific fields. Every section may set `title` and `subtitle`.\n\nkind=\"form\" — a create form. Fields: `fields` (array of {field, label, type, placeholder, rows, help}; type is text|textarea|number|select|toggle|tags|password, default text; select needs `options`:[{value,label}]), `submit_label` (button text, default \"Add\"), `modal` (boolean — when true the form opens from a \"New\" button in a dialog; the signature structured-create pattern). The form saves a record to the app's store.\n\nkind=\"table\" — a list of the app's records. Fields: `columns` (array of {field, label, flex, mute}), `empty_text` (shown when there are no records — ALWAYS set this), `deletable` (boolean — adds a Delete button per row), `auto_refresh_ms` (poll interval; 2000 keeps the list live as records are added).\n\nkind=\"display\" — a read-only labeled-value panel over the records endpoint. Fields: `pairs` (array of {label, field}).\n\nkind=\"empty\" — a centered empty-state placeholder (for a 'nothing selected' panel). Fields: `icon` (an emoji), `title`, `hint`.\n\nMinimal good app = a form (modal=true, submit_label) + a table (empty_text, deletable, auto_refresh_ms) over the same records.",
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

Section kinds: form (create form; set modal=true + submit_label for the structured-create look) | table (record list; always set empty_text; deletable + auto_refresh_ms keep it live) | display (read-only pairs) | empty (centered placeholder).

Minimal good app = a form + a table over the same records. The form's saves and the table's source both point at the app's per-record store automatically — you don't wire endpoints.`

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
		existing, ok := LoadAppSpec(t.udb, key)
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
		if _, exists := LoadAppSpec(t.udb, slug); exists {
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
	} else if !isUpdate {
		return "", errors.New("sections is required to create an app")
	}

	saved := SaveAppSpec(t.udb, spec)
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
			Source:        "records",
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
		sec.Body = ui.DisplayPanel{Source: "records", Pairs: appDisplayPairs(m["pairs"])}
	case "empty":
		sec.Body = ui.EmptyState{
			Icon:  mapStr(m, "icon"),
			Title: firstNonEmptyStr(mapStr(m, "title"), "Nothing selected"),
			Hint:  mapStr(m, "hint"),
		}
		// EmptyState carries its own title; avoid a duplicate section heading.
		sec.Title, sec.Subtitle = "", ""
	default:
		return ui.Section{}, fmt.Errorf("unknown section kind %q — use form | table | display | empty", kind)
	}
	return sec, nil
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
	specs := ListAppSpecs(t.udb)
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
	spec, ok := LoadAppSpec(t.udb, key)
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
	spec, ok := LoadAppSpec(t.udb, key)
	if !ok {
		return "", errors.New("no matching app to delete")
	}
	DeleteAppSpec(t.udb, spec.Slug)
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
