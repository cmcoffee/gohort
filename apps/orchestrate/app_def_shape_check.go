package orchestrate

// Does each section find what its data source prints?
//
// A script check used to ask one question of a data source's output: is it
// JSON, and an array or an object. A weather app passed it four times while
// its page showed nothing: its source printed {"Current Weather": "...",
// "Day 1 Forecast": "..."}, its chart section wanted {labels, series} and its
// display asked for fields the source never printed, and every check said OK.
// The section is what the user sees, so the check now reads the output the
// way each section that consumes it will.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// appSectionShapeProblems lists, for every section that reads data source
// `source`, why it would render empty or wrong from output v. An empty
// output is not judged: there is nothing yet to show either way.
func appSectionShapeProblems(spec AppSpec, source string, v any) []string {
	if appOutputEmpty(v) {
		return nil
	}
	var sections []map[string]any
	if json.Unmarshal(spec.Sections, &sections) != nil {
		return nil
	}
	var out []string
	for i, sec := range sections {
		if slugify(mapStr(sec, "source_script")) != source {
			continue
		}
		name := firstNonEmptyStr(mapStr(sec, "title"), mapStr(sec, "id"), fmt.Sprintf("#%d", i+1))
		kind := strings.ToLower(strings.TrimSpace(mapStr(sec, "kind")))
		where := fmt.Sprintf("%s section %q", kind, name)
		switch kind {
		case "chart":
			out = append(out, appChartShape(where, source, sec, v)...)
		case "display":
			out = append(out, appDisplayShape(where, source, sec, v)...)
		case "table":
			out = append(out, appTableShape(where, source, sec, v)...)
		case "html", "card":
			out = append(out, appHTMLShape(where, source, v)...)
		}
	}
	return out
}

// appHTMLShape: a sourced html section renders the "html" string of what the
// script prints (or the output itself, when that is a string).
func appHTMLShape(where, source string, v any) []string {
	switch t := v.(type) {
	case string:
		return nil
	case map[string]any:
		if _, ok := t["html"].(string); ok {
			return nil
		}
		return []string{fmt.Sprintf("%s renders the \"html\" key of what data/%s prints, and it has none (its keys: %s): print the markup as \"html\"", where, source, appKeys(t))}
	}
	return []string{fmt.Sprintf("%s reads data/%s, which printed an array; it renders {\"html\": \"<markup>\"}", where, source)}
}

// appHTMLPlaceholder is the name inside markup that is nothing but a
// "{name}" (or "{{name}}") placeholder, or "". An html section renders its
// markup verbatim, so such a section shows the braces.
func appHTMLPlaceholder(html string) string {
	t := strings.TrimSpace(html)
	for strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	if t == "" || t == strings.TrimSpace(html) || strings.ContainsAny(t, "<>{} \n\t\"'") {
		return ""
	}
	return t
}

func appOutputEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func appChartShape(where, source string, sec map[string]any, v any) []string {
	want := `print {"labels": ["Mon", "Tue"], "series": [{"name": "High", "points": [21, 19]}]} (a pie: one series whose points are the slices). If another section needs a different shape, give the chart its own data source.`
	fix := fmt.Sprintf("Change what the data/%s script prints; the section's own labels and series are fixed values for a chart with no script, not templates", source)
	if appChartHasTemplates(sec) {
		fix += fmt.Sprintf(" (this section's %s is the literal text, not a reference into the output: remove it)", appChartTemplateFields(sec))
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s reads data/%s, which printed an array; a chart renders nothing from that: %s %s.", where, source, want, fix)}
	}
	series, _ := obj["series"].([]any)
	if _, has := obj["series"]; has && len(series) == 0 {
		return []string{fmt.Sprintf("%s reads data/%s, which printed \"series\" with nothing in it, so the chart is blank: fill it, %s", where, source, want)}
	}
	if len(series) == 0 {
		// The likeliest near miss: the right object, one level down.
		for k, inner := range obj {
			if m, isMap := inner.(map[string]any); isMap && m["series"] != nil {
				return []string{fmt.Sprintf("%s: data/%s prints its labels and series inside %q, and the chart reads them at the TOP level of the output: print \"labels\" and \"series\" as keys of the object itself. %s.", where, source, k, fix)}
			}
		}
		return []string{fmt.Sprintf("%s reads data/%s, which printed no series (its keys: %s), so the chart is empty: %s %s.", where, source, appKeys(obj), want, fix)}
	}
	labels, _ := obj["labels"].([]any)
	var out []string
	for i, s := range series {
		m, _ := s.(map[string]any)
		pts, _ := m["points"].([]any)
		switch {
		case len(pts) == 0 && m["data"] != nil:
			out = append(out, fmt.Sprintf("%s: series %d in what data/%s prints carries its numbers in \"data\", and the chart reads \"points\": rename the key in the script's output. %s.", where, i+1, source, fix))
		case len(pts) == 0:
			out = append(out, fmt.Sprintf("%s: series %d in what data/%s prints has no points: %s", where, i+1, source, want))
		case len(labels) > 0 && len(pts) != len(labels):
			out = append(out, fmt.Sprintf("%s: series %d has %d points for %d labels; one point per label", where, i+1, len(pts), len(labels)))
		}
		if _, named := m["name"]; !named && m["label"] != nil {
			out = append(out, fmt.Sprintf("%s: series %d in what data/%s prints is titled with \"label\"; the legend reads \"name\", so it shows \"Series %d\". Rename the key in the script's output.", where, i+1, source, i+1))
		}
	}
	return out
}

// appChartHasTemplates reports a chart section whose own labels or series
// hold "{...}" strings: an author reaching into the source's output from the
// section, which the chart reads as literal text.
func appChartHasTemplates(sec map[string]any) bool { return appChartTemplateFields(sec) != "" }

func appChartTemplateFields(sec map[string]any) string {
	var hit []string
	for _, k := range []string{"labels", "series"} {
		b, _ := json.Marshal(sec[k])
		if strings.Contains(string(b), "{") && strings.Contains(string(b), "}") && sec[k] != nil {
			if k == "series" {
				// A series object is braces by nature; only a "{...}" STRING counts.
				if !strings.Contains(string(b), `"{`) {
					continue
				}
			}
			hit = append(hit, k)
		}
	}
	return strings.Join(hit, " and ")
}

func appDisplayShape(where, source string, sec map[string]any, v any) []string {
	obj, ok := v.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s reads data/%s, which printed an array; a display shows one object's fields", where, source)}
	}
	pairs := appDisplayPairs(sec["pairs"])
	if len(pairs) == 0 {
		return []string{fmt.Sprintf("%s declares no pairs, so it shows nothing; list pairs:[{field, label}] naming keys data/%s prints: %s", where, source, appKeys(obj))}
	}
	var missing []string
	for _, p := range pairs {
		if _, has := obj[p.Field]; !has {
			missing = append(missing, p.Field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s shows %s, which data/%s does not print, so those rows are blank. It prints: %s. Name the pairs after those keys, or print the keys the pairs name", where, strings.Join(missing, ", "), source, appKeys(obj))}
}

func appTableShape(where, source string, sec map[string]any, v any) []string {
	rows, ok := v.([]any)
	if !ok {
		return []string{fmt.Sprintf("%s reads data/%s, which printed an object; a table needs an array of rows", where, source)}
	}
	first, _ := rows[0].(map[string]any)
	cols, _ := sec["columns"].([]any)
	if first == nil || len(cols) == 0 {
		return nil
	}
	var missing []string
	for _, c := range cols {
		m, _ := c.(map[string]any)
		f := strings.TrimSpace(mapStr(m, "field"))
		if f == "" {
			continue
		}
		if _, has := first[f]; !has {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s has columns %s that the rows of data/%s do not carry, so they are blank. A row has: %s", where, strings.Join(missing, ", "), source, appKeys(first))}
}

// appKeys is an object's keys, sorted, for a message.
func appKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 12 {
		keys = append(keys[:12], "…")
	}
	return strings.Join(keys, ", ")
}

var callToolRE = regexp.MustCompile(`call_tool\(\s*["']([A-Za-z0-9_.-]+)["']`)

// appScriptBody is the source of the data source or action named name.
func appScriptBody(spec AppSpec, kind, name string) string {
	if kind == "action" {
		for _, a := range spec.Actions {
			if a.Name == name {
				return a.Script
			}
		}
		return ""
	}
	for _, ds := range spec.DataSources {
		if ds.Name == name {
			return ds.Script
		}
	}
	return ""
}

// appToolCapNotes checks each "tool:<name>" a script declares against the rule
// the call itself meets, at save time: a tool the owner does not have, or one
// that would stop to ask, fails on every page load otherwise, far from here.
func appToolCapNotes(user string, spec AppSpec) []string {
	var notes []string
	check := func(kind, script string, caps []string) {
		declared := map[string]bool{}
		for _, c := range caps {
			declared[c] = true
		}
		body := appScriptBody(spec, kind, script)
		for _, m := range callToolRE.FindAllStringSubmatch(body, -1) {
			if !declared["tool:"+m[1]] {
				notes = append(notes, fmt.Sprintf("%s %q calls call_tool(%q) but does not declare tool:%s in its capabilities, so the call will be refused: add it", kind, script, m[1], m[1]))
				declared["tool:"+m[1]] = true
			}
		}
		for _, c := range caps {
			name, ok := strings.CutPrefix(c, "tool:")
			if !ok {
				continue
			}
			if _, err := temptool.ScriptCallableTool(RootDB, user, name); err != nil {
				notes = append(notes, fmt.Sprintf("%s %q declares tool:%s, but the call will be refused: %v", kind, script, name, err))
			}
		}
	}
	for _, ds := range spec.DataSources {
		check("data source", ds.Name, ds.Capabilities)
	}
	for _, act := range spec.Actions {
		check("action", act.Name, act.Capabilities)
	}
	return notes
}
