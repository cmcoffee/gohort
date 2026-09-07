package orchestrate

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

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
	// Normalize once, up front: every scan below keys off `kind`, so a section
	// that only implies its kind has to be resolved before the first look, not
	// at build time.
	secs := make([]map[string]any, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return ui.Page{}, fmt.Errorf("section %d must be an object", i+1)
		}
		secs = append(secs, normalizeSection(m))
	}
	// A workbench is a whole-page shape (three full-height columns), so when one
	// is present it owns the page: full width, single no-chrome section.
	for _, m := range secs {
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "workbench") {
			wb, err := buildWorkbench(spec, m)
			if err != nil {
				return ui.Page{}, err
			}
			return ui.Page{
				Title:     spec.Name,
				ShowTitle: true,
				BackURL:   "/apps/",
				MaxWidth:  "100%",
				Sections:  []ui.Section{{NoChrome: true, Body: wb}},
			}, nil
		}
	}
	// A pipeline panel is a two-column shape too — run history beside a stage
	// transcript — so it takes the full width the same way a workbench does,
	// without the author having to know to ask. It does NOT own the page: a
	// pipeline section can sit under a display panel or beside a table.
	for _, m := range secs {
		if k := strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))); k == "pipeline" || k == "run" {
			spec.FullWidth = true
			break
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
		BackURL:   "/apps/",
		MaxWidth:  maxWidth,
	}
	// The first form section's fields are the natural default for an editable
	// table's edit dialog — same labels/types/selects the record was created
	// with. Scanned up front so section order doesn't matter.
	var createFields []ui.FormField
	for _, m := range secs {
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "form") {
			if fields := appFormFields(m["fields"]); len(fields) > 0 {
				createFields = fields
				break
			}
		}
	}
	for i, m := range secs {
		sec, err := buildAppSection(spec, m, createFields)
		if err != nil {
			return ui.Page{}, fmt.Errorf("section %d: %w", i+1, err)
		}
		page.Sections = append(page.Sections, sec)
	}
	return page, nil
}

// normalizeSection makes a section object parseable when the author sent a
// near-miss instead of the documented {kind, …} shape. Two arrive constantly:
// a section read back from a RENDERED page (fields nested under `body`, kind
// carried as the body's component `type`), and a section whose kind is simply
// implied by the field that was set (an `html` blob, a `columns` list). Both
// state the intent unambiguously, so infer rather than reject — a hard error
// here reads as "the app can't be edited" and the author re-writes it blind.
// sectionKeys is what each section kind actually READS, so a key that is not
// listed here is a key the framework threw away. Kept beside the builder it
// mirrors: if a kind learns a field, it belongs in both places, and the cost of
// forgetting is one spurious note, never a refused save.
var sectionKeys = map[string][]string{
	"":          {"kind", "title", "subtitle", "group", "collapsed"}, // every kind
	"form":      {"fields", "submit_label", "modal"},
	"table":     {"columns", "empty_text", "editable", "edit_fields", "deletable", "auto_refresh_ms", "source_script"},
	"display":   {"pairs", "source_script"},
	"chart":     {"chart_type", "labels", "series", "source_script", "stacked", "legend", "height", "auto_refresh_ms"},
	"actions":   {"empty_text"},
	"empty":     {"icon", "hint"},
	"chat":      {"list_title", "empty_text", "placeholder"},
	"pipeline":  {"fields", "submit_label", "empty_text", "input_label", "placeholder", "pipeline_id", "toolbar", "suggest_script", "suggest_label", "suggest_target", "meta"},
	"run":       {"fields", "submit_label", "empty_text", "input_label", "placeholder", "pipeline_id", "toolbar", "suggest_script", "suggest_label", "suggest_target", "meta"},
	"workbench": {"item_label", "body_field", "item_noun", "new_fields", "new_label", "new_title", "list_title", "list_empty", "empty_title", "empty_hint", "empty_icon", "chat_empty", "placeholder"},
	"html":      {"html", "height"},
	"card":      {"html", "height"},
}

// topLevelAppKeys are app_def parameters that sit BESIDE sections, not inside
// one. A section carrying one of these isn't using a key that doesn't exist —
// it put a real key one level too deep, which is a different mistake with a
// different fix. pipeline_id is absent deliberately: it is valid in both places.
var topLevelAppKeys = map[string]bool{
	"actions": true, "data_sources": true, "agent_id": true,
	"full_width": true, "private_db": true, "record_key": true,
	"name": true, "description": true, "slug": true,
}

// unknownSectionKeyNotes reports keys a section carried that its kind does not
// read.
//
// A dropped key used to be perfectly silent: the save succeeded, the app came
// back missing the behavior the key was supposed to add, and the only way to
// find out was to guess. Guessing is what actually happened — an invented
// pipeline_label and a borrowed source_script were both accepted without
// comment, so the author concluded the SECTION KIND was broken and rewrote a
// working app by hand.
//
// A note, not an error: the section is otherwise valid and saving it is right.
// The note just makes the difference between "what I sent" and "what was
// stored" visible in the same breath as the save.
func unknownSectionKeyNotes(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var notes []string
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		kind := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
		known, ok := sectionKeys[kind]
		if !ok {
			continue // an unknown kind is buildAppSection's error to raise, not ours
		}
		allowed := map[string]bool{}
		for _, k := range append(append([]string{}, sectionKeys[""]...), known...) {
			allowed[k] = true
		}
		var stray []string
		for k := range m {
			if !allowed[strings.ToLower(strings.TrimSpace(k))] {
				stray = append(stray, k)
			}
		}
		if len(stray) == 0 {
			continue
		}
		sort.Strings(stray) // map order is not an order
		note := fmt.Sprintf("section %d (kind %q): ignored %s — this kind reads: %s. Nothing you sent under those keys was stored.",
			i+1, kind, strings.Join(stray, ", "), strings.Join(append(append([]string{}, known...), sectionKeys[""]...), ", "))
		// Where it DOES belong, when the key is a real parameter one level up.
		// Listing what the kind reads answers "why was this dropped" and not
		// "where does it go", and the difference is rounds: an actions array
		// nested in an actions SECTION was re-sent unchanged, then moved on a
		// guess, because the note never said the word "top-level".
		var misplaced []string
		for _, k := range stray {
			if topLevelAppKeys[strings.ToLower(strings.TrimSpace(k))] {
				misplaced = append(misplaced, k)
			}
		}
		if len(misplaced) > 0 {
			note += fmt.Sprintf(" %s is a TOP-LEVEL app_def parameter — move it out of the section, beside \"sections\".",
				strings.Join(misplaced, " and "))
		}
		notes = append(notes, note)
	}
	return notes
}

// appShapeNotes reports section COMBINATIONS that parse cleanly and then don't
// do what the author is about to promise. Every one of these produced a working
// page and a false claim to the user.
func appShapeNotes(raw any, boundPipeline bool) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var notes []string
	pipelineAt := -1
	var recordViews []string
	var clientButtonAt []int
	htmlSection := false
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "pipeline", "run":
			if pipelineAt < 0 {
				pipelineAt = i
			}
			if appToolbarUsesClient(m["toolbar"]) {
				clientButtonAt = append(clientButtonAt, i+1)
			}
			// Extra submit fields are the pipeline's PARAMETERS: each arrives as
			// {name} in every stage's prompt. Nothing to warn about — the note
			// that used to live here said they went nowhere, which was true of
			// the run surface before it carried them.
			_ = appPipelineFields(m["fields"])
		case "table", "display":
			// A record-backed view; a source_script one computes its own rows.
			if strings.TrimSpace(mapStr(m, "source_script")) == "" {
				recordViews = append(recordViews, strconv.Itoa(i+1))
			}
		case "html", "card":
			htmlSection = true
		}
	}
	// A client-method button dispatches by NAME to a handler the app has to
	// register itself, and an html section's inline script is the only place a
	// declarative app can do that. With no html section anywhere the button
	// renders perfectly and toasts "No handler for client action" on click.
	//
	// Anywhere, not before: the runtime looks a client handler up at CLICK
	// time, unlike a block renderer, which the panel snapshots at mount. Two
	// registries, two different rules, and asserting the stricter one here
	// would send an author reordering a page that was already correct.
	if len(clientButtonAt) > 0 && !htmlSection {
		notes = append(notes, fmt.Sprintf("section(s) %s have a toolbar button with method \"client\", but the app has no html section — nothing registers the handler, so the button renders and does nothing but toast an error when clicked. Add an html section whose script calls window.uiRegisterClientAction(\"<the button's url>\", fn).",
			strings.Join(intsToStrings(clientButtonAt), ", ")))
	}
	// A pipeline bound with nothing to run it. The app carries a pipeline_id,
	// so the author means to run it — and without the section there is no
	// button, no transcript, and no history anywhere on the page. It is the
	// exact end state of an app that grew a form, a table and a script-backed
	// "run" button instead: everything parses, verify passes, and the thing the
	// app is for cannot be started.
	//
	// Only when a binding exists: an app with no pipeline_id is simply not a
	// pipeline app, and has nothing to be missing.
	if pipelineAt < 0 && boundPipeline {
		notes = append(notes, "this app binds a pipeline (pipeline_id) but has NO section of kind \"pipeline\" — nothing on the page can start a run, show its stages, or list past ones. Add {kind:\"pipeline\"}. An action script cannot run a pipeline; the section is the only surface that does.")
	}
	if pipelineAt >= 0 && len(recordViews) > 0 {
		notes = append(notes, fmt.Sprintf("section(s) %s read the app's RECORD store, which a pipeline never writes to — a run's history lives in the pipeline panel's own sidebar (section %d). Those sections will stay on their empty state forever unless an action script writes records, so do not tell the user past runs will appear there.",
			strings.Join(recordViews, ", "), pipelineAt+1))
	}
	return notes
}

// pipelineFieldsNameTheInput reports whether the form explicitly names its
// input field, in which case the first field carries no special meaning.
func pipelineFieldsNameTheInput(fields []ui.PipelineField) bool {
	for _, f := range fields {
		if strings.EqualFold(f.Name, "input") || strings.EqualFold(f.Name, "topic") {
			return true
		}
	}
	return false
}

// sectionPipelineRef finds a pipeline_id written on a pipeline SECTION rather
// than at the app level — the same binding, one object down. First one wins:
// an app has a single pipeline binding, so two different values is a conflict
// no guess resolves, and taking the first keeps the behavior predictable
// (the stray one is reported by unknownSectionKeyNotes... it isn't, since
// pipeline_id IS a key this kind reads — which is the point: it is honored).
func sectionPipelineRef(raw any) string {
	arr, ok := raw.([]any)
	if !ok {
		return ""
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "pipeline", "run":
			if p := strings.TrimSpace(mapStr(m, "pipeline_id")); p != "" {
				return p
			}
		}
	}
	return ""
}

func normalizeSection(m map[string]any) map[string]any {
	if strings.TrimSpace(mapStr(m, "kind")) != "" {
		return m
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	// Rendered shape: {title, body:{type:"card", html:…}} — lift the body's
	// fields up and translate its component type to the authoring kind.
	if body, ok := m["body"].(map[string]any); ok {
		delete(out, "body")
		for k, v := range body {
			if k == "type" {
				continue
			}
			if _, taken := out[k]; !taken {
				out[k] = v
			}
		}
		if kind, ok := bodyTypeToKind[strings.TrimSpace(mapStr(body, "type"))]; ok {
			out["kind"] = kind
			return out
		}
	}
	// Kind implied by the defining field of exactly one kind.
	switch {
	case strings.TrimSpace(mapStr(out, "html")) != "":
		out["kind"] = "html"
	case out["fields"] != nil:
		out["kind"] = "form"
	case out["columns"] != nil:
		out["kind"] = "table"
	case out["pairs"] != nil:
		out["kind"] = "display"
	case out["series"] != nil || out["chart_type"] != nil:
		out["kind"] = "chart"
	}
	return out
}

func buildAppSection(spec AppSpec, m map[string]any, createFields []ui.FormField) (ui.Section, error) {
	m = normalizeSection(m)
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
			return ui.Section{}, entryListError("form", "field", "fields", m["fields"])
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
			return ui.Section{}, entryListError("table", "column", "columns", m["columns"])
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
			InjectURL:    "chat/inject",
			CancelURL:    "chat/cancel",
			ListTitle:    firstNonEmptyStr(mapStr(m, "list_title"), "Sessions"),
			NewLabel:     "New",
			ListPosition: "top",
			Markdown:     true,
			EmptyText:    firstNonEmptyStr(mapStr(m, "empty_text"), "Ask the assistant to get started."),
			Placeholder:  firstNonEmptyStr(mapStr(m, "placeholder"), "Ask anything…"),
		}
	case "pipeline", "run":
		// The RUN surface: submit form on top, live stage-by-stage transcript
		// below, past runs in the sidebar. Bound to the app's pipeline_id;
		// customapps serves the SSE + session endpoints under pipeline/*
		// (handlePipeline → orchestrate's PublicHandlePipeline), so the URLs are
		// relative to the app mount exactly like chat/* and the record store.
		//
		// This is what makes a multi-stage recipe an APP rather than a tool an
		// agent happens to own: the user gets a page to launch it, watch it
		// work, and read what it produced last week.
		if strings.TrimSpace(spec.PipelineID) == "" {
			return ui.Section{}, errors.New("a pipeline section needs the app to have a pipeline_id (the stored pipeline this app runs) — author the pipeline first with the `pipeline` tool, then pass its name or id as pipeline_id")
		}
		sec.NoChrome = true // the panel manages its own layout
		fields := appPipelineFields(m["fields"])
		if len(fields) == 0 {
			// The default is the one field every pipeline takes: its input. Named
			// "topic" because the stream endpoint accepts input|topic and the
			// panel titles a run from it.
			fields = []ui.PipelineField{{
				Name: "topic", Type: "textarea", Required: true, Rows: 3,
				Label:       firstNonEmptyStr(mapStr(m, "input_label"), "What should this run?"),
				Placeholder: mapStr(m, "placeholder"),
			}}
		}
		panel := ui.PipelinePanel{
			SessionsListURL:  "pipeline/sessions",
			SessionLoadURL:   "pipeline/sessions/{id}",
			SessionDeleteURL: "pipeline/sessions/{id}",
			SubmitURL:        "pipeline/stream",
			// A run outlives the request that started it, so the panel gets
			// both halves of that: a Cancel button while it is going, and a
			// way back into one still running when the page is reopened.
			CancelURL:    "pipeline/cancel",
			ReconnectURL: "pipeline/reconnect/{id}",
			SubmitLabel:  firstNonEmptyStr(mapStr(m, "submit_label"), "Start"),
			Fields:       fields,
			// Name the deep-link param, because an app page's own ?id= is
			// whatever THAT app means by it — a record in a table section,
			// usually — and the panel's generic fallback would read it as a
			// run to open.
			DeepLinkParam: "session",
			// A stage transcript is prose — headings, lists, citations — so it
			// renders as markdown, and past runs get checkboxes because a run
			// history is something you prune in batches.
			Markdown:   true,
			BulkSelect: true,
			EmptyText:  firstNonEmptyStr(mapStr(m, "empty_text"), "Start a run to see it here."),
		}
		// The furniture: a per-run toolbar and a Suggest button. Both are
		// OPTIONAL and both refuse rather than render when they are pointed at
		// something this app cannot serve.
		toolbar, err := appPipelineToolbar(spec, m["toolbar"])
		if err != nil {
			return ui.Section{}, err
		}
		panel.Actions = toolbar
		if err := appPipelinePrefill(spec, m, fields, &panel); err != nil {
			return ui.Section{}, err
		}
		metaFields, err := appPipelineMetaFields(m["meta"])
		if err != nil {
			return ui.Section{}, err
		}
		panel.SessionMetaFields = metaFields
		sec.Body = panel
	case "chart":
		// A chart is either STATIC (inline labels + series) or COMPUTED by
		// a data source that prints {labels, series[, chart_type, title,
		// options]} — the source-script path is the useful one for a data
		// app (a chart of the records). The section title is the heading;
		// the SVG carries no duplicate title.
		cp := ui.ChartPanel{
			ChartType:     firstNonEmptyStr(strings.ToLower(strings.TrimSpace(mapStr(m, "chart_type"))), "bar"),
			Labels:        appChartLabels(m["labels"]),
			Series:        appChartSeries(m["series"]),
			Options:       appChartOptions(m),
			AutoRefreshMS: intFromArgs(m, "auto_refresh_ms"),
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
			return ui.Section{}, errors.New("an html section needs an `html` field (the raw HTML to render) — pass the markup itself, not a nested object")
		}
		// A COMPLETE document gets its own frame; a fragment is inlined. An
		// author writing a game or an animation writes a whole document
		// (doctype, <head>, a `* { margin: 0 }` reset, `body { … 100vh }`),
		// and inlining that leaks its reset and body rules into the host page
		// while its 100vh layout measures the browser window instead of its
		// own box. Framing it keeps both cascades to themselves — same origin
		// either way, so relative data-source fetches still work.
		if isFullHTMLDocument(html) {
			sec.Body = ui.Frame{HTML: html, Height: mapStr(m, "height")}
		} else {
			sec.Body = ui.Card{HTML: html}
		}
	default:
		if kind == "" {
			return ui.Section{}, errors.New("this section has no `kind` and none could be inferred from its fields — every section needs kind: form | table | display | chart | empty | chat | workbench | actions | html. Call action=get to read the app's current sections in editable form (or action=help for each kind's fields)")
		}
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
		InjectURL:    "chat/inject",
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

// entryListError explains why a fields/columns list came out EMPTY.
//
// "a form section needs at least one field" was true of the parsed result and
// false of what the author sent: three fields arrived and all three were
// dropped for lacking the key the parser reads. The author, reading a message
// that contradicted the payload in front of them, re-sent the same shape six
// times and then simplified to a single field to isolate it — which produced
// the same sentence, because the count was never the problem.
//
// So: distinguish "you sent none" from "every one was discarded, here is what
// yours carry instead."
func entryListError(section, item, plural string, raw any) error {
	arr, isArr := raw.([]any)
	switch {
	case raw == nil, !isArr && raw == nil:
		return fmt.Errorf("a %s section needs at least one %s — pass %s:[{%q:…, \"label\":…}]", section, item, plural, "field")
	case !isArr:
		return fmt.Errorf("a %s section's %s must be an ARRAY of objects, got %T", section, plural, raw)
	case len(arr) == 0:
		return fmt.Errorf("a %s section needs at least one %s — %s was empty", section, item, plural)
	}
	// Non-empty in, nothing out: every entry lacked the key. Report the keys
	// they DO carry, which is the fastest possible route to the fix.
	keys := map[string]bool{}
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			for k := range m {
				keys[k] = true
			}
		}
	}
	have := make([]string, 0, len(keys))
	for k := range keys {
		have = append(have, k)
	}
	sort.Strings(have)
	msg := fmt.Sprintf("a %s section got %d %s but every one was DROPPED: each needs a \"field\" key naming the record field it reads or writes.",
		section, len(arr), plural)
	if len(have) > 0 {
		msg += " Yours carry: " + strings.Join(have, ", ") + "."
	}
	for _, alias := range []string{"key", "id", "column", "value"} {
		if keys[alias] {
			msg += fmt.Sprintf(" Rename %q to \"field\".", alias)
			break
		}
	}
	return errors.New(msg)
}
