package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The weather build, as the gap finder sees it: the saved city is never
// shown, a new user gets a blank page, icons point at files that do not
// exist by paths that cannot resolve, and the forecast code is a copy of the
// owner's own get_weather.
func TestGapsTheWeatherBuildLeftOut(t *testing.T) {
	saved, savedAssets := RootDB, AppAssetsDir()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	t.Cleanup(func() { RootDB = saved; SetAppAssetsDir(savedAssets) })
	if err := AdminPersistTempTool(RootDB, "owner", TempTool{Name: "get_weather", Description: "d", CommandTemplate: "python3 x.py",
		ScriptBody: `fetch_url("https://api.open-meteo.com/v1/forecast?x=1")`, HookCapabilities: []string{"fetch"}}); err != nil {
		t.Fatal(err)
	}
	spec := AppSpec{Owner: "owner", Slug: "wx",
		Sections: json.RawMessage(`[{"kind":"form","fields":[{"name":"city"}]},{"kind":"display","source_script":"weather","pairs":[{"field":"html"}]}]`),
		DataSources: []AppDataSource{{Name: "weather", Script: `records = os.environ.get("records")
fetch_url("https://api.open-meteo.com/v1/forecast?lat=1")
html += '<img src="/assets/weather-icons/sun.png">'`}}}
	in := appGapInput{
		outputs: map[string]string{"weather": `{"html":"<img src=\"assets/cloud.png\">"}`},
		empty:   map[string]string{"weather": `{}`},
	}
	got := appBuildGaps("owner", spec, in)
	for _, want := range []string{
		"WORTH ADDING",
		"never shown back",
		"first visit shows a blank page",
		"/assets/weather-icons/sun.png starts at the server's root",
		"assets/cloud.png is shown but the app has no such asset",
		"owner's tool get_weather already does: call it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// The complete version lists nothing.
	SaveAppAsset("owner", "wx", "cloud.png", []byte("\x89PNG\r\n\x1a\n...."))
	spec.Sections = json.RawMessage(`[{"kind":"form"},{"kind":"table"},{"kind":"display","source_script":"weather"}]`)
	spec.DataSources[0].Script = `records = os.environ.get("records"); out = call_tool("get_weather", city="Reno")`
	in.empty["weather"] = `{"html":"Add a city to see its forecast"}`
	if got := appBuildGaps("owner", spec, in); got != "" {
		t.Fatalf("a complete app still has gaps:\n%s", got)
	}
}

// The build that gave up: every version of its data source wrapped the work
// in a function and never called it, so the script printed nothing in 80ms.
func TestAnUncalledFunctionIsNamed(t *testing.T) {
	script := "import json\nimport sys\n\ndef current_weather_source_script():\n    print(json.dumps({\"t\": 25}))\n    sys.stdout.flush()"
	got := uncalledPythonFunctions(script)
	if len(got) != 1 || got[0] != "current_weather_source_script" {
		t.Fatalf("uncalled = %v", got)
	}
	if h := uncalledHint(got); !strings.Contains(h, "current_weather_source_script()") || !strings.Contains(h, "top level") {
		t.Fatalf("hint = %s", h)
	}
	called := script + "\n\ncurrent_weather_source_script()\n"
	if got := uncalledPythonFunctions(called); len(got) != 0 {
		t.Fatalf("a called function flagged: %v", got)
	}
	helper := "def fmt(x):\n    return str(x)\n\nprint(fmt(1))\n"
	if got := uncalledPythonFunctions(helper); len(got) != 0 {
		t.Fatalf("a used helper flagged: %v", got)
	}
}

// The same build's third round sent the script as one line with its
// newlines written as \n, and python stopped at line 1.
func TestADoublyEscapedScriptIsDecoded(t *testing.T) {
	sent := `\nimport os\nimport json\nrecords = json.loads(os.environ.get(\'records\', \'[]\'))\nprint(json.dumps({\"n\": len(records)}))\n`
	got, ok := appUnescapeScript(sent)
	if !ok || !strings.Contains(got, "\nimport json\n") || !strings.Contains(got, `os.environ.get('records', '[]')`) || !strings.Contains(got, `{"n": len(records)}`) {
		t.Fatalf("decoded %v:\n%s", ok, got)
	}
	if _, ok := appUnescapeScript("import os\nprint('a\\nb')\n"); ok {
		t.Fatal("a script with real newlines was decoded")
	}
	if _, ok := appUnescapeScript(`print("a\nb")`); ok {
		t.Fatal("a one-liner with one \\n in a string was decoded")
	}
	ds, notes := appDataSources([]any{map[string]any{"name": "w", "script": sent}})
	if len(ds) != 1 || !strings.Contains(ds[0].Script, "\nimport json\n") || len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "decoded") {
		t.Fatalf("parse: %+v %v", ds, notes)
	}
}

// The checklist for a complete app rides on the create result, where the next
// step is decided, and not on every update after it.
func TestTheCreateResultCarriesThePlan(t *testing.T) {
	pinRootDB(t)
	turn := &chatTurn{user: "u"}
	sections := []any{map[string]any{"kind": "form", "fields": []any{map[string]any{"name": "city"}}},
		map[string]any{"kind": "table", "empty_text": "Nothing yet.", "columns": []any{map[string]any{"field": "city"}}}}
	out, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Weather", "sections": sections}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COMPLETE IT BEFORE YOU REPLY", "call_tool", "first visit", "open your reply with one or two lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("create result lacks %q:\n%s", want, out)
		}
	}
	out, err = turn.appDefCreateOrUpdate(map[string]any{"id": "weather", "name": "Weather", "sections": sections}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "COMPLETE IT BEFORE YOU REPLY") {
		t.Errorf("an update repeats the plan:\n%s", out)
	}
}

// A page draws its own empty state, and a table over a source shows rows: the
// run-tracker build was told its first visit was blank and its entries never
// shown, though its page said "No runs yet" and its table listed them.
func TestGapsTrustAPageAndASourcedTable(t *testing.T) {
	pinRootDB(t)
	page := AppSpec{Owner: "u", Slug: "runs", Sections: json.RawMessage(`[{"kind":"html","html":"<!DOCTYPE html><p>No runs yet</p>"}]`),
		DataSources: []AppDataSource{{Name: "runs", Script: "records"}}}
	if got := appBuildGaps("u", page, appGapInput{empty: map[string]string{"runs": "[]"}}); strings.Contains(got, "first visit") {
		t.Fatalf("a page's own empty state flagged:\n%s", got)
	}
	typed := AppSpec{Owner: "u", Slug: "runs", Sections: json.RawMessage(`[{"kind":"form"},{"kind":"table","source_script":"runs"},{"kind":"chart","source_script":"runs"}]`)}
	if got := appGapSavedNotShown(typed); len(got) != 0 {
		t.Fatalf("a table over a source counted as not showing entries: %v", got)
	}
}
