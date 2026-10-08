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
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
			out = append(out, appChartShape(where, source, v)...)
		case "display":
			out = append(out, appDisplayShape(where, source, sec, v)...)
		case "table":
			out = append(out, appTableShape(where, source, sec, v)...)
		}
	}
	return out
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

func appChartShape(where, source string, v any) []string {
	want := `print {"labels": ["Mon", "Tue"], "series": [{"name": "High", "points": [21, 19]}]} (a pie: one series whose points are the slices). If another section needs a different shape, give the chart its own data source.`
	obj, ok := v.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s reads data/%s, which printed an array; a chart renders nothing from that: %s", where, source, want)}
	}
	labels, _ := obj["labels"].([]any)
	series, _ := obj["series"].([]any)
	if len(series) == 0 {
		return []string{fmt.Sprintf("%s reads data/%s, which printed no series (its keys: %s), so the chart is empty: %s", where, source, appKeys(obj), want)}
	}
	var out []string
	for i, s := range series {
		m, _ := s.(map[string]any)
		pts, _ := m["points"].([]any)
		if len(pts) == 0 {
			if _, alt := m["data"]; alt {
				out = append(out, fmt.Sprintf("%s: series %d carries its numbers in \"data\"; the chart reads \"points\"", where, i+1))
			} else {
				out = append(out, fmt.Sprintf("%s: series %d has no points: %s", where, i+1, want))
			}
			continue
		}
		if len(labels) > 0 && len(pts) != len(labels) {
			out = append(out, fmt.Sprintf("%s: series %d has %d points for %d labels; one point per label", where, i+1, len(pts), len(labels)))
		}
	}
	return out
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
