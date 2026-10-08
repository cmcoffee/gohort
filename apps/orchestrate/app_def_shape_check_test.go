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
	chart := map[string]any{"labels": []any{"a", "b"}, "series": []any{map[string]any{"data": []any{1.0, 2.0}}}}
	if probs := appChartShape("chart", "s", chart); len(probs) != 1 || !strings.Contains(probs[0], `"data"`) {
		t.Fatalf("data instead of points: %q", probs)
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
