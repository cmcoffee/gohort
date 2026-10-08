package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

func shapeSpec(t *testing.T, sections string) AppSpec {
	t.Helper()
	if !json.Valid([]byte(sections)) {
		t.Fatalf("bad sections: %s", sections)
	}
	return AppSpec{Slug: "wx", Sections: json.RawMessage(sections)}
}

// The incident: a weather source printed labelled strings, the chart wanted
// {labels, series} and the display named fields it never printed. Four checks
// passed it while the page showed nothing.
func TestSectionsAreCheckedAgainstWhatTheirSourcePrints(t *testing.T) {
	spec := shapeSpec(t, `[
	  {"kind":"display","title":"Now","source_script":"weather_data_source","pairs":[{"field":"temperature"},{"field":"Humidity"}]},
	  {"kind":"chart","title":"Week","source_script":"weather_data_source"},
	  {"kind":"form","title":"Where"}]`)
	out := map[string]any{"Current Weather": "18C", "Humidity": "65%", "Day 1 Forecast": "Min 15"}
	probs := appSectionShapeProblems(spec, "weather-data-source", out)
	if len(probs) != 2 {
		t.Fatalf("want a display and a chart problem, got %q", probs)
	}
	if !strings.Contains(probs[0], "temperature") || strings.Contains(probs[0], "Humidity,") || !strings.Contains(probs[0], "Current Weather") {
		t.Errorf("display: %s", probs[0])
	}
	if !strings.Contains(probs[1], "no series") || !strings.Contains(probs[1], `"points"`) {
		t.Errorf("chart: %s", probs[1])
	}

	good := map[string]any{"temperature": "18C", "Humidity": "65%", "labels": []any{"Mon", "Tue"},
		"series": []any{map[string]any{"name": "High", "points": []any{21.0, 19.0}}}}
	if probs := appSectionShapeProblems(spec, "weather-data-source", good); len(probs) != 0 {
		t.Fatalf("a matching output still flagged: %q", probs)
	}
	// Nothing printed yet (no location saved) is not a shape problem.
	if probs := appSectionShapeProblems(spec, "weather-data-source", map[string]any{}); len(probs) != 0 {
		t.Fatalf("empty output flagged: %q", probs)
	}
}

func TestChartAndTableShapes(t *testing.T) {
	spec := shapeSpec(t, `[
	  {"kind":"chart","title":"C","source_script":"s"},
	  {"kind":"table","title":"T","source_script":"s","columns":[{"field":"city"},{"field":"temp"}]}]`)
	arr := []any{map[string]any{"city": "Reno"}}
	probs := appSectionShapeProblems(spec, "s", arr)
	if len(probs) != 2 || !strings.Contains(probs[0], "printed an array") || !strings.Contains(probs[1], "temp") {
		t.Fatalf("%q", probs)
	}
	// The rounds the weather build lost: "data" for "points" in the OUTPUT,
	// fixed by editing the section's own series as if it were a template.
	sec := map[string]any{"series": []any{map[string]any{"data": "{series[0].data}"}}, "labels": "{labels}"}
	chart := map[string]any{"labels": []any{"a", "b"}, "series": []any{map[string]any{"name": "Low", "data": []any{1.0, 2.0}}}}
	probs = appChartShape("chart", "s", sec, chart)
	if len(probs) != 1 || !strings.Contains(probs[0], "rename the key in the script's output") || !strings.Contains(probs[0], "labels and series is the literal text") {
		t.Fatalf("data instead of points: %q", probs)
	}
	nested := map[string]any{"Humidity": "58%", "chart_data": map[string]any{"labels": []any{"a"}, "series": []any{}}}
	if probs := appChartShape("chart", "s", map[string]any{}, nested); len(probs) != 1 || !strings.Contains(probs[0], `inside "chart_data"`) || !strings.Contains(probs[0], "TOP level") {
		t.Fatalf("nested: %q", probs)
	}
	unnamed := map[string]any{"labels": []any{"a"}, "series": []any{map[string]any{"label": "Low", "points": []any{1.0}}}}
	if probs := appChartShape("chart", "s", map[string]any{}, unnamed); len(probs) != 1 || !strings.Contains(probs[0], `"Series 1"`) {
		t.Fatalf("label for name: %q", probs)
	}
	named := map[string]any{"labels": []any{"a"}, "series": []any{map[string]any{"name": "Low", "points": []any{1.0}}}}
	if probs := appChartShape("chart", "s", map[string]any{"series": []any{map[string]any{"name": "Low"}}}, named); len(probs) != 0 {
		t.Fatalf("a good chart flagged: %q", probs)
	}
}

// Scripts read records[-1] as the newest; records came in key order and keys
// are random, so the forecast followed whichever city sorted last.
func TestRecordsReachScriptsOldestFirst(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	tbl := appscript.RecordsTable("wx")
	db.Set(tbl, "ffff", map[string]any{"id": "ffff", "city": "Reno", "created": "2026-10-08T07:00:00Z"})
	db.Set(tbl, "0000", map[string]any{"id": "0000", "city": "Boise", "created": "2026-10-08T09:00:00Z"})
	db.Set(tbl, "8888", map[string]any{"id": "8888", "city": "Old"})
	recs := appscript.ReadRecords(db, "wx")
	if len(recs) != 3 || recs[0]["city"] != "Old" || recs[1]["city"] != "Reno" || recs[2]["city"] != "Boise" {
		t.Fatalf("order = %v", recs)
	}
}
