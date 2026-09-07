package orchestrate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// appSpecSections reads the stored AUTHORING sections off a spec — the shape
// update accepts, which is what a comparison against an incoming update needs.
// Empty for a spec written before that field existed; the guard then has
// nothing to compare and stays quiet, which is the right way to be wrong.
func appSpecSections(spec AppSpec) []map[string]any {
	if len(spec.Sections) == 0 {
		return nil
	}
	var raw []any
	if json.Unmarshal(spec.Sections, &raw) != nil {
		return nil
	}
	return appProposedSections(raw)
}

// appProposedSections normalizes an incoming sections array for comparison.
func appProposedSections(raw any) []map[string]any {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, normalizeSection(m))
		}
	}
	return out
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
		// `name` is accepted as an alias for `field`. The two spellings mean the
		// same thing to an author, the pipeline section already took `name`, and
		// refusing it here made ONE key behave differently in three sections —
		// which cost six round-trips of "a form section needs at least one
		// field" against a payload that plainly carried three.
		field := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
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
		// Same alias as appFormFields — one spelling, one meaning, everywhere.
		field := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
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
// notes reports back anything the author must know that the parse changed or
// dropped — a slugified name means every reference (source_script, an html
// section's fetch path) must use the NEW spelling, and a silently skipped entry
// reads as "saved" when it wasn't.
func appDataSources(raw any) (out []AppDataSource, notes []string) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			notes = append(notes, fmt.Sprintf("data_sources entry %d IGNORED — not an object", i+1))
			continue
		}
		given := strings.TrimSpace(mapStr(m, "name"))
		name := slugify(given)
		script := mapStr(m, "script")
		if name == "" || strings.TrimSpace(script) == "" {
			notes = append(notes, fmt.Sprintf("data_sources entry %d IGNORED — needs both a name and a script", i+1))
			continue
		}
		if name != given {
			notes = append(notes, fmt.Sprintf("data source %q is registered as %q (names are slugified: lowercase, non-alphanumerics → \"-\") — reference it by the slugified name in source_script and in any fetch of data/%s", given, name, name))
		}
		out = append(out, AppDataSource{
			Name:         name,
			Language:     strings.ToLower(strings.TrimSpace(mapStr(m, "language"))),
			Script:       script,
			Capabilities: appStringList(m["capabilities"]),
		})
	}
	return out, notes
}

// appActionDefs parses the declarative actions array into AppAction records.
// notes mirrors appDataSources: renames and dropped entries are reported, not
// swallowed.
func appActionDefs(raw any) (out []AppAction, notes []string) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			notes = append(notes, fmt.Sprintf("actions entry %d IGNORED — not an object", i+1))
			continue
		}
		given := strings.TrimSpace(mapStr(m, "name"))
		name := slugify(given)
		script := mapStr(m, "script")
		if name == "" || strings.TrimSpace(script) == "" {
			notes = append(notes, fmt.Sprintf("actions entry %d IGNORED — needs both a name and a script", i+1))
			continue
		}
		if name != given {
			notes = append(notes, fmt.Sprintf("action %q is registered as %q (names are slugified: lowercase, non-alphanumerics → \"-\") — its endpoint is action/%s", given, name, name))
		}
		act := AppAction{
			Name:         name,
			Label:        strings.TrimSpace(mapStr(m, "label")),
			Desc:         strings.TrimSpace(mapStr(m, "desc")),
			Language:     strings.ToLower(strings.TrimSpace(mapStr(m, "language"))),
			Script:       script,
			Capabilities: appStringList(m["capabilities"]),
			Confirm:      strings.TrimSpace(mapStr(m, "confirm")),
		}
		sch, snotes := appSchedule(m["schedule"], name)
		act.Schedule = sch
		notes = append(notes, snotes...)
		out = append(out, act)
	}
	return out, notes
}

// appSchedule parses an action's optional `schedule` object into an *AppSchedule
// (the self-update cadence). Returns nil when there's no schedule or it names no
// cadence. Notes report a floored interval, a cron/interval clash, or a schedule
// object that would do nothing — the same "report, don't swallow" contract the
// rest of app_def parsing follows.
func appSchedule(raw any, action string) (*AppSchedule, []string) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	var notes []string
	sch := &AppSchedule{Cron: strings.TrimSpace(mapStr(m, "cron"))}
	if f, ok := floatVal(m["interval_seconds"]); ok && f > 0 {
		sch.IntervalSeconds = int(f)
	}
	if f, ok := floatVal(m["max_idle_days"]); ok && f > 0 {
		sch.MaxIdleDays = int(f)
	}
	// Cron and interval are mutually exclusive at the engine (cron wins); make the
	// stored spec unambiguous and say so.
	if sch.Cron != "" && sch.IntervalSeconds > 0 {
		notes = append(notes, fmt.Sprintf("action %q schedule sets both cron and interval_seconds — using cron, ignoring the interval", action))
		sch.IntervalSeconds = 0
	}
	if sch.IntervalSeconds > 0 && sch.IntervalSeconds < MinAppScheduleSeconds {
		notes = append(notes, fmt.Sprintf("action %q schedule interval_seconds %d is below the %d-second minimum for unattended updates — it will run every %d seconds", action, sch.IntervalSeconds, MinAppScheduleSeconds, MinAppScheduleSeconds))
		sch.IntervalSeconds = MinAppScheduleSeconds
	}
	if !sch.Scheduled() {
		notes = append(notes, fmt.Sprintf("action %q has a schedule object with no cron or interval_seconds — it will NOT self-update (add interval_seconds or cron)", action))
		return nil, notes
	}
	return sch, notes
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
