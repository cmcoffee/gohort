package orchestrate

import (
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// appPipelineFields converts the declarative fields array into the submit-form
// fields of a pipeline section. Accepts the same `field`/`name` spelling the
// form sections use, so an author who has written one section already doesn't
// have to learn a second key for the same idea.
//
// The names matter more here than in a form: the panel POSTs them as the run's
// JSON body, and the run surface reads `input` (or `topic`) as the pipeline's
// input. A field named anything else is carried but not consumed.
func appPipelineFields(raw any) []ui.PipelineField {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.PipelineField
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "name"), mapStr(m, "field")))
		if name == "" {
			continue
		}
		pf := ui.PipelineField{
			Name:        name,
			Label:       firstNonEmptyStr(mapStr(m, "label"), name),
			Type:        firstNonEmptyStr(strings.ToLower(strings.TrimSpace(mapStr(m, "type"))), "text"),
			Placeholder: mapStr(m, "placeholder"),
			Default:     mapStr(m, "default"),
			Required:    boolArg(m, "required"),
			Rows:        intFromArgs(m, "rows"),
		}
		for _, o := range appSelectOptions(m["options"]) {
			pf.Options = append(pf.Options, o.Value)
		}
		out = append(out, pf)
	}
	return out
}

// --- pipeline section: toolbar + suggest -------------------------------------
//
// The pipeline SECTION is a preset over ui.PipelinePanel, and for a long time
// it set four URLs and nothing else — so an app authored declaratively got the
// run surface but none of the furniture a compiled app hangs on it (a Copy
// Link button, a Suggest button, an export). These two knobs close most of
// that gap. What they deliberately do NOT expose is anything the declarative
// surface cannot actually serve; see appPipelineToolbarRefusals.

// appPipelineToolbarAllowed is what a toolbar button may DO here.
//
//	open   — navigate to the url in a new tab
//	copy   — copy the substituted url to the clipboard
//	post   — POST to one of the app's own action scripts, then refresh
//	client — call a browser-side handler the app registered itself
var appPipelineToolbarAllowed = map[string]bool{
	"open": true, "copy": true, "post": true, "client": true,
}

// appPipelineToolbarRefusals is the rest of what ui.PipelinePanel supports,
// with the reason each one cannot work on an app built out of sections.
//
// Refused at AUTHORING time rather than rendered, because every one of them
// fails at CLICK time instead: the button draws correctly, sits there looking
// finished, and breaks one user at a time long after the author declared the
// app done. An error here costs one retry; the alternative costs a bug report
// that starts in the wrong place.
var appPipelineToolbarRefusals = map[string]string{
	"stream": "it POSTs and expects an SSE transcript back, and the only endpoint " +
		"a custom app has that speaks SSE is pipeline/stream — the recipe itself, which " +
		"the Start button already runs. Pointed at anything else the transcript just empties",
	"modal": "it streams SSE into a dialog, and a custom app has no second streaming " +
		"endpoint to stream from (a data source answers once, in JSON, and the modal would " +
		"sit empty). Generate-a-report belongs in a second pipeline the app binds, not here",
	"related": "it fetches a list of RELATED runs keyed off fields on the session " +
		"summary, and a custom app's summary carries ID, Title and Date only",
	"load": "it jumps to another run named by a {FieldName} placeholder read off the " +
		"session summary, which here carries ID, Title and Date only — every other " +
		"placeholder renders empty and the button goes nowhere",
}

// appPipelineToolbar parses the pipeline section's toolbar: the row of buttons
// that appears above the transcript once a run is open.
func appPipelineToolbar(spec AppSpec, raw any) ([]ui.PipelineAction, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("a pipeline section's toolbar must be an ARRAY of buttons, got %T — pass toolbar:[{\"label\":\"Copy Link\", \"method\":\"copy\"}]", raw)
	}
	var out []ui.PipelineAction
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("toolbar entry %d is not an object — each button is {label, method, url?}", i+1)
		}
		label := strings.TrimSpace(mapStr(m, "label"))
		if label == "" {
			return nil, fmt.Errorf("toolbar entry %d needs a label (it is the button's text)", i+1)
		}
		method := strings.ToLower(strings.TrimSpace(firstNonEmptyStr(mapStr(m, "method"), "open")))
		if why, refused := appPipelineToolbarRefusals[method]; refused {
			return nil, fmt.Errorf("toolbar button %q asks for method %q, which a custom app cannot honor: %s", label, method, why)
		}
		if !appPipelineToolbarAllowed[method] {
			return nil, fmt.Errorf("toolbar button %q has method %q — use open (new tab), copy (to clipboard), post (run one of this app's action scripts) or client (a handler the app registered itself)", label, method)
		}
		url := strings.TrimSpace(mapStr(m, "url"))
		if url == "" {
			// The overwhelmingly common copy button is a link to the run being
			// looked at, and making the author spell that out is a chance to
			// get it subtly wrong for no gain.
			if method != "copy" {
				return nil, fmt.Errorf("toolbar button %q needs a url — for method %q that is %s", label, method, appPipelineToolbarURLHint(method))
			}
			url = "?session={id}"
		}
		if err := appPipelineToolbarTarget(spec, label, method, url); err != nil {
			return nil, err
		}
		out = append(out, ui.PipelineAction{
			Label:   label,
			URL:     url,
			Method:  method,
			Title:   strings.TrimSpace(mapStr(m, "title")),
			Variant: strings.ToLower(strings.TrimSpace(mapStr(m, "variant"))),
			Confirm: strings.TrimSpace(mapStr(m, "confirm")),
		})
	}
	return out, nil
}

func appPipelineToolbarURLHint(method string) string {
	switch method {
	case "post":
		return "\"action/<one of this app's actions>\" (add {id} as a query param to tell the script which run was open)"
	case "client":
		return "the NAME of a handler registered with window.uiRegisterClientAction, not a path"
	default:
		return "the address to open — an absolute URL, or one relative to this app like \"data/<source>\""
	}
}

// appPipelineToolbarTarget refuses a button pointed at something this app does
// not have.
//
// action/ and data/ names are slugified on the way in, so an action declared as
// "Save Run" is reachable at action/save-run and nowhere else. Getting that
// wrong produces a 404 the author meets by clicking their own finished app,
// which is late; the spec already knows every script it declares, so the check
// is free here.
func appPipelineToolbarTarget(spec AppSpec, label, method, url string) error {
	if method == "client" {
		// Not a path: the url field carries the registered handler's name.
		if strings.ContainsAny(url, "/?#") {
			return fmt.Errorf("toolbar button %q is method \"client\", so its url must be the NAME of a handler registered with window.uiRegisterClientAction (e.g. \"print_transcript\"), not the path %q", label, url)
		}
		return nil
	}
	path := url
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	switch {
	case strings.HasPrefix(path, "action/"):
		name := strings.TrimPrefix(path, "action/")
		if !appHasNamed(appActionNames(spec), name) {
			return fmt.Errorf("toolbar button %q points at action/%s, which this app does not declare%s", label, name, appDeclaredList("actions", appActionNames(spec)))
		}
		if method != "post" {
			return fmt.Errorf("toolbar button %q opens action/%s with method %q, but an action endpoint answers POST only — use method:\"post\"", label, name, method)
		}
	case strings.HasPrefix(path, "data/"):
		name := strings.TrimPrefix(path, "data/")
		if !appHasNamed(appDataSourceNames(spec), name) {
			return fmt.Errorf("toolbar button %q points at data/%s, which this app does not declare%s", label, name, appDeclaredList("data_sources", appDataSourceNames(spec)))
		}
	}
	return nil
}

// appPipelinePrefill wires the Suggest button to one of the app's own data
// sources: the script prints a JSON array (a popover of choices) or an object
// with a topic/text/suggestion key (dropped straight into the field), and the
// panel does the rest. It is the declarative form of the hand-written suggest
// endpoints the compiled apps carry.
func appPipelinePrefill(spec AppSpec, m map[string]any, fields []ui.PipelineField, panel *ui.PipelinePanel) error {
	given := strings.TrimSpace(mapStr(m, "suggest_script"))
	if given == "" {
		// A label or a target with nothing behind it renders no button at all,
		// so say which key is missing rather than quietly dropping both.
		if strings.TrimSpace(mapStr(m, "suggest_label")) != "" || strings.TrimSpace(mapStr(m, "suggest_target")) != "" {
			return errors.New("this pipeline section sets suggest_label/suggest_target but no suggest_script — the Suggest button is the data source, so without one there is nothing to render")
		}
		return nil
	}
	name := slugify(given)
	if !appHasNamed(appDataSourceNames(spec), name) {
		return fmt.Errorf("suggest_script names %q, which this app does not declare%s", name, appDeclaredList("data_sources", appDataSourceNames(spec)))
	}
	target := strings.TrimSpace(mapStr(m, "suggest_target"))
	if target == "" && len(fields) > 0 {
		target = fields[0].Name
	}
	// A target that names no field is the silent failure this check exists for:
	// the button fetches, the script runs, and the value lands nowhere.
	if !appHasPipelineField(fields, target) {
		return fmt.Errorf("suggest_target names the field %q, which this section's form does not have%s", target, appDeclaredList("fields", appPipelineFieldNames(fields)))
	}
	panel.PrefillURL = "data/" + name
	panel.PrefillLabel = firstNonEmptyStr(strings.TrimSpace(mapStr(m, "suggest_label")), "Suggest")
	panel.PrefillTarget = target
	return nil
}

func appActionNames(spec AppSpec) []string {
	out := make([]string, 0, len(spec.Actions))
	for _, a := range spec.Actions {
		out = append(out, a.Name)
	}
	return out
}

func appDataSourceNames(spec AppSpec) []string {
	out := make([]string, 0, len(spec.DataSources))
	for _, d := range spec.DataSources {
		out = append(out, slugify(d.Name))
	}
	return out
}

func appPipelineFieldNames(fields []ui.PipelineField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

func appHasNamed(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func appHasPipelineField(fields []ui.PipelineField, want string) bool {
	for _, f := range fields {
		if strings.EqualFold(f.Name, want) {
			return true
		}
	}
	return false
}

// appDeclaredList names what the app DOES have, so a typo is one read away
// from its fix instead of sending the author back to action=get.
func appDeclaredList(kind string, names []string) string {
	if len(names) == 0 {
		return " (it declares no " + kind + " at all)"
	}
	return " — its " + kind + ": " + strings.Join(names, ", ")
}

// appToolbarUsesClient reports whether any button in a toolbar dispatches to a
// browser-side handler, which is what makes the html-section ordering rule
// apply to this app (see appSectionNotes).
func appToolbarUsesClient(raw any) bool {
	arr, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if strings.EqualFold(strings.TrimSpace(mapStr(m, "method")), "client") {
				return true
			}
		}
	}
	return false
}

// appPipelineMetaStyles is what a sidebar row can render a promoted field as.
var appPipelineMetaStyles = map[string]bool{"text": true, "badge": true, "pill": true}

// appPipelineMetaFields parses the pipeline section's `meta`: the extra values
// shown under each sidebar row's title, so a run history can be SCANNED for
// its answer instead of opened one run at a time.
//
// The values themselves come from the pipeline, not from here — a def promotes
// declared stage output fields with session_meta, and this only says how to
// draw them.
func appPipelineMetaFields(raw any) ([]ui.SessionMetaField, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("a pipeline section's meta must be an ARRAY, got %T — pass meta:[{\"field\":\"winner\", \"style\":\"pill\"}]", raw)
	}
	var out []ui.SessionMetaField
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("meta entry %d is not an object — each is {field, label?, style?, variants?, truncate?}", i+1)
		}
		field := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
		if field == "" {
			return nil, fmt.Errorf("meta entry %d needs a field (the name the pipeline promotes it under)", i+1)
		}
		style := strings.ToLower(strings.TrimSpace(mapStr(m, "style")))
		if style != "" && !appPipelineMetaStyles[style] {
			return nil, fmt.Errorf("meta entry %d (%s) has style %q — use \"text\" (a line under the title), \"badge\" (a small neutral pill) or \"pill\" (colored by value, see variants)", i+1, field, style)
		}
		smf := ui.SessionMetaField{
			Field:    field,
			Label:    strings.TrimSpace(mapStr(m, "label")),
			Style:    style,
			Truncate: intFromArgs(m, "truncate"),
		}
		// variants colors a pill per VALUE ("for" green, "against" red). Only
		// a pill reads them, so a variants map on a text row is a instruction
		// that quietly does nothing.
		if v, ok := m["variants"].(map[string]any); ok && len(v) > 0 {
			if style != "pill" {
				return nil, fmt.Errorf("meta entry %d (%s) sets variants but its style is %q — only a pill is colored by value", i+1, field, firstNonEmptyStr(style, "text"))
			}
			smf.Variants = map[string]string{}
			for key, val := range v {
				smf.Variants[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(fmt.Sprint(val))
			}
		}
		out = append(out, smf)
	}
	return out, nil
}

// appSessionMetaNotes reports meta fields the bound pipeline does not promote.
//
// A NOTE and not a refusal, deliberately: the app and the pipeline are edited
// separately, so a field that resolves today can stop resolving tomorrow when
// somebody trims the def, and a check here can never be the guarantee. What it
// can do is catch the common case — the name was guessed — at the moment the
// author is looking, and say which names would have worked.
func appSessionMetaNotes(raw any, promoted []string) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	have := map[string]bool{}
	for _, ref := range promoted {
		if _, field, ok := strings.Cut(strings.TrimSpace(ref), "."); ok {
			have[field] = true
		}
	}
	var notes []string
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "pipeline", "run":
		default:
			continue
		}
		fields, err := appPipelineMetaFields(m["meta"])
		if err != nil || len(fields) == 0 {
			continue
		}
		var missing []string
		for _, f := range fields {
			if !have[f.Field] {
				missing = append(missing, f.Field)
			}
		}
		if len(missing) == 0 {
			continue
		}
		if len(have) == 0 {
			notes = append(notes, fmt.Sprintf("section %d shows meta field(s) %s, but the bound pipeline promotes NOTHING onto its run rows — those rows will render blank. Add session_meta:[\"<stage>.<field>\"] to the pipeline (the field must be one the stage declares in its output).",
				i+1, strings.Join(missing, ", ")))
			continue
		}
		notes = append(notes, fmt.Sprintf("section %d shows meta field(s) %s, which the bound pipeline does not promote — it promotes: %s. Those rows will render blank.",
			i+1, strings.Join(missing, ", "), strings.Join(promotedFieldNames(promoted), ", ")))
	}
	return notes
}

func promotedFieldNames(promoted []string) []string {
	out := make([]string, 0, len(promoted))
	for _, ref := range promoted {
		if _, field, ok := strings.Cut(strings.TrimSpace(ref), "."); ok {
			out = append(out, field)
		}
	}
	return out
}
