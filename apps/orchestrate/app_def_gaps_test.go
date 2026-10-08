package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
		Sections: json.RawMessage(`[{"kind":"form","fields":[{"name":"city"}]},{"kind":"html","source_script":"weather"}]`),
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
	spec.Sections = json.RawMessage(`[{"kind":"form"},{"kind":"table"},{"kind":"html","source_script":"weather"}]`)
	spec.DataSources[0].Script = `records = os.environ.get("records"); out = call_tool("get_weather", city="Reno")`
	in.empty["weather"] = `{"html":"Add a city to see its forecast"}`
	if got := appBuildGaps("owner", spec, in); got != "" {
		t.Fatalf("a complete app still has gaps:\n%s", got)
	}
}
